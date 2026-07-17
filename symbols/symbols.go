// Package symbols is a prototype of the interface OpenTofu would embed to consume
// functy `.cty` files as symbol libraries (types + values + functions), per the
// in-draft Symbol Libraries RFC. See ../OPENTOFU-SYMBOLS.md for the design record.
//
// A host binds one or more libraries with SymbolsBlock values — the Go shape of a
// `symbols "label" { source; namespace }` block — and calls Build to get the three
// symbol-table artifacts:
//
//   - Functions: value-plane cty.Functions, keyed symbols::label::name
//   - Symbols:   a cty.Value object: { label = { const = value } }
//   - Type(label, name): the type-plane resolver for symbols::label::types(name)
//
// The three land directly on OpenTofu's own reference grammar: functions in the
// `::` call space, values in the `.` traversal space, and types on the typeexpr
// special form. functy is the parser; nothing here is serialized.
package symbols

import (
	"fmt"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// SymbolsBlock mirrors what OpenTofu decodes a `symbols "label" { ... }` binding
// block into. The hcl tags are what OpenTofu's gohcl decode would use; they are
// carried here so the struct could double as the decode target in a real integration.
type SymbolsBlock struct {
	// Label is the consumer-chosen local name (the block label), unique across the
	// config. It replaces the functy namespace in every output key: a function
	// bound under label "lib" is callable as symbols::lib::name regardless of which
	// functy namespace it came from.
	Label string `hcl:"name,label"`

	// Source is a module-style source address resolved against the builder's base
	// directory. It names a *directory* — a functy "unit", all its `.cty` files
	// parsed together — not a single file.
	Source string `hcl:"source"`

	// Namespace selects which functy namespace within the unit to bind; "" (omitted)
	// binds the global, unnamespaced surface. One block per namespace.
	Namespace string `hcl:"namespace,optional"`

	DefRange hcl.Range `hcl:",def_range"`
}

// Builder assembles a symbol table from a set of binding blocks. It is configured
// with a fluent chain so future knobs can be added without breaking callers:
//
//	built, diags := symbols.NewBuilder().
//	    WithBaseDir(moduleDir).
//	    WithBaseFunctions(hostFuncs).
//	    WithBlocks(blocks...).
//	    Build()
type Builder struct {
	blocks    []SymbolsBlock
	baseDir   string
	baseFuncs map[string]function.Function
	maxSteps  int
}

// NewBuilder returns an empty Builder. maxSteps defaults to 0 (unbounded); a host
// evaluating libraries at plan time should set a ceiling with WithMaxSteps.
func NewBuilder() *Builder { return &Builder{} }

// WithBlocks appends binding blocks. May be called more than once.
func (b *Builder) WithBlocks(blocks ...SymbolsBlock) *Builder {
	b.blocks = append(b.blocks, blocks...)
	return b
}

// WithBaseDir sets the directory each block's Source is resolved against.
func (b *Builder) WithBaseDir(dir string) *Builder { b.baseDir = dir; return b }

// WithBaseFunctions supplies the host's function registry (OpenTofu's builtins).
// functy library functions late-bind to it — they call these by bare name, and so
// do const initializers — so anything a library references must be present. A
// pure library that calls nothing may leave it nil.
func (b *Builder) WithBaseFunctions(funcs map[string]function.Function) *Builder {
	b.baseFuncs = funcs
	return b
}

// WithMaxSteps sets the per-invocation execution-limit ceiling passed to functy
// (0 = unbounded). A plan-time host wants a finite ceiling so a runaway loop or
// unbounded recursion in a library aborts instead of wedging the process.
func (b *Builder) WithMaxSteps(n int) *Builder { b.maxSteps = n; return b }

// Built is the symbol table handed to the host. Functions and Symbols drop
// straight into an hcl.EvalContext's Functions and Variables["symbols"]; Type is
// the resolver the host's typeexpr layer calls for symbols::label::types(name).
type Built struct {
	// Functions holds every exported library function, keyed by its host-facing
	// name, symbols::label::name. These are executable cty.Functions — signature
	// and implementation in one — so there is no separate "impl".
	Functions map[string]function.Function

	// Symbols is an object { label = { constName = value } }: each label's exported
	// consts, pre-evaluated into self-contained values. Only `const` crosses; `var`
	// is rejected at parse. Goes into OpenTofu's ctx.Variables["symbols"].
	Symbols cty.Value

	// Files maps filename to parsed bytes, for rendering diagnostics with source
	// snippets (hcl.NewDiagnosticTextWriter).
	Files map[string]*hcl.File

	// types is the type-plane lookup [label][typeName] backing Type(). Unexported
	// because a cty.Type is not a cty.Value and must not leak into the value planes
	// above; the host reaches it only through Type(), mirroring how the typeexpr
	// special form symbols::label::types(name) is the only way to name it.
	types map[string]map[string]cty.Type
}

// Type resolves symbols::label::types(name) to its cty.Type. A type constraint is
// a cty.Type, not a cty.Value, so it cannot be a member of Functions (cty
// functions return values); the host's typeexpr evaluator calls this instead.
func (bt *Built) Type(label, name string) (cty.Type, bool) {
	byName, ok := bt.types[label]
	if !ok {
		return cty.NilType, false
	}
	ty, ok := byName[name]
	return ty, ok
}

// unit is one parsed source directory: its parse result and compiled functions.
// The per-namespace consts are evaluated into compiled.Vars by parseUnit, which is
// where projectConsts reads them. Cached per resolved source path so two blocks
// pointing at the same source with different namespaces parse it once.
type unit struct {
	res      *functy.Result
	compiled *functy.Compiled
}

// Build parses each block's source once, projects the requested namespace under
// the block's label, and returns the assembled symbol table plus any diagnostics.
func (b *Builder) Build() (*Built, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	built := &Built{
		Functions: map[string]function.Function{},
		Files:     map[string]*hcl.File{},
		types:     map[string]map[string]cty.Type{},
	}
	labelObjs := map[string]cty.Value{}

	seenLabel := map[string]bool{}
	units := map[string]*unit{}

	for _, blk := range b.blocks {
		if seenLabel[blk.Label] {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate symbols label",
				Detail:   fmt.Sprintf("A symbols block labeled %q is already declared; labels must be unique.", blk.Label),
				Subject:  blk.DefRange.Ptr(),
			})
			continue
		}
		seenLabel[blk.Label] = true

		path := blk.Source
		if b.baseDir != "" {
			path = filepath.Join(b.baseDir, blk.Source)
		}

		u, ok := units[path]
		if !ok {
			var udiags hcl.Diagnostics
			u, udiags = b.parseUnit(path, built.Files)
			diags = diags.Extend(udiags)
			units[path] = u // cache even on error, keyed by path, so we don't re-report
		}
		if u == nil {
			continue
		}

		built.Functions = mergeFunctions(built.Functions, projectFunctions(blk, u), &diags)
		labelObjs[blk.Label] = projectConsts(blk, u)
		built.types[blk.Label] = projectTypes(blk, u)
	}

	built.Symbols = cty.ObjectVal(labelObjs)
	return built, diags
}

// parseUnit parses and compiles one source directory, evaluating its consts into
// a fresh eval context. Top-level const is enabled and top-level var rejected: a
// var in a shared, plan-time library is meaningless (evaluated once, never
// mutated), so it is a parse error rather than a silently dropped declaration.
func (b *Builder) parseUnit(path string, files map[string]*hcl.File) (*unit, hcl.Diagnostics) {
	sources, diags := functy.ParseSources(path)
	for _, s := range sources {
		files[s.Filename] = &hcl.File{Bytes: s.Bytes}
	}
	if diags.HasErrors() {
		return nil, diags
	}

	var ctx *hcl.EvalContext
	evalCtxFn := func() *hcl.EvalContext { return ctx }

	res, pdiags := functy.NewParser().
		MaxSteps(b.maxSteps).
		AllowTopLevelConst(true).
		AllowTopLevelVar(false).
		ParseAll(sources)
	diags = diags.Extend(pdiags)

	compiled, cdiags := res.CompileUnits(evalCtxFn)
	diags = diags.Extend(cdiags)

	// The context library functions late-bind to, and where consts are evaluated:
	// host builtins plus this unit's exported functions under their functy-
	// qualified names (siblings inside a namespace still resolve by bare name
	// through functy's unit layer; this is for consts and for cross-namespace
	// qualified calls, matching cmd/functy's behavior).
	funcs := make(map[string]function.Function, len(b.baseFuncs)+len(compiled.Funcs))
	for k, v := range b.baseFuncs {
		funcs[k] = v
	}
	for k, v := range compiled.Funcs {
		funcs[k] = v
	}

	// Point the shared context's Variables at the global (unnamespaced) var table so
	// global consts are both projected under the empty-namespace label and visible
	// to every namespace's bodies (which late-bind to this context as their parent).
	// EvalNamespacedDecls then fills each namespace's own table in compiled.Vars,
	// so two namespaces may each declare `const greeting` without colliding.
	if compiled.Vars[""] == nil {
		compiled.Vars[""] = map[string]cty.Value{}
	}
	ctx = &hcl.EvalContext{Functions: funcs, Variables: compiled.Vars[""]}

	diags = diags.Extend(functy.EvalNamespacedDecls(res.Consts, ctx, compiled))

	return &unit{res: res, compiled: compiled}, diags
}

// projectFunctions selects the unit's exported functions in the block's namespace
// and re-keys them symbols::label::name. Private (`_`-prefixed) functions are
// withheld, exactly as they are from functy's own host map.
func projectFunctions(blk SymbolsBlock, u *unit) map[string]function.Function {
	out := map[string]function.Function{}
	table := u.compiled.Units[blk.Namespace]
	for _, fn := range u.res.Funcs {
		if fn.Namespace != blk.Namespace || fn.IsPrivate() {
			continue
		}
		if f, ok := table[fn.Name]; ok {
			out[hostFuncKey(blk.Label, fn.Name)] = f
		}
	}
	return out
}

// projectConsts buckets the unit's exported consts in the block's namespace into
// one flat object under the block's label. Consts were evaluated per namespace into
// u.compiled.Vars by parseUnit; here they are only selected and collected. `var`
// never crosses (it is rejected at parse), and a `const x` never collides with a
// `func x` — values live in the `.` space, functions in the `::` space.
//
// Consts are namespace-scoped: each namespace's consts live in their own
// u.compiled.Vars[namespace] table, so two namespaces in one unit may both declare
// `const greeting` and each projects into its own symbols.<label> object.
func projectConsts(blk SymbolsBlock, u *unit) cty.Value {
	fields := map[string]cty.Value{}
	table := u.compiled.Vars[blk.Namespace]
	for i := range u.res.Consts {
		d := u.res.Consts[i]
		if d.Namespace != blk.Namespace || d.IsPrivate() {
			continue
		}
		if v, ok := table[d.Name]; ok {
			fields[d.Name] = v
		}
	}
	return cty.ObjectVal(fields)
}

// projectTypes exposes the block's namespace's exported type aliases as a
// [typeName]cty.Type lookup backing Built.Type(label, name).
//
// Types are namespace-scoped, mirroring projectFunctions/projectConsts: only
// aliases whose namespace matches the block's cross, so two namespaces in one unit
// may both declare `type cidr = …` and each projects under its own label. Private
// (`_`-prefixed) aliases are withheld via IsPrivate() — they are still resolved and
// inlined into exported aliases (e.g. `type items = list(_spec)` crosses as a
// concrete `list(object(...))`), just not named on the export surface.
func projectTypes(blk SymbolsBlock, u *unit) map[string]cty.Type {
	out := map[string]cty.Type{}
	for _, a := range u.res.Types {
		if a.Namespace != blk.Namespace || a.IsPrivate() {
			continue
		}
		out[a.Name] = a.Type.Cty()
	}
	return out
}

// mergeFunctions folds src into dst, reporting a diagnostic on any key collision
// (a label's namespace cannot produce duplicate keys, so a collision means two
// blocks share a label — already caught — or a future bug; report rather than
// silently overwrite).
func mergeFunctions(dst, src map[string]function.Function, diags *hcl.Diagnostics) map[string]function.Function {
	for k, v := range src {
		if _, exists := dst[k]; exists {
			*diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate symbol function",
				Detail:   fmt.Sprintf("Function %q is produced by more than one symbols block.", k),
			})
			continue
		}
		dst[k] = v
	}
	return dst
}

// hostFuncKey builds the host-facing function key symbols::label::name.
func hostFuncKey(label, name string) string {
	return fmt.Sprintf("symbols::%s::%s", label, name)
}
