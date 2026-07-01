// Package functy implements an imperative language whose values are cty values
// and whose expressions are HCL. A functy source file is a sequence of function
// declarations; compiling it yields ordinary cty function.Function values that
// can be added to an *hcl.EvalContext and called from any HCL expression.
//
// The statement grammar (func, var, if/else, for/while, switch, ...) is parsed
// by functy itself, while every embedded expression is handed to HCL
// (hclsyntax.ParseExpression), so operators, templates, and function calls
// behave exactly as they do elsewhere in HCL. Type annotations are resolved by
// functy's own TypeResolver — a superset of the ext/typeexpr grammar that also
// supports host-registered capsule and open (predicate-backed) named types.
package functy

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// Source is a single functy source: its filename (used in diagnostics) and raw
// bytes. ParseSources collects these from files, directories, and embedded
// filesystems.
type Source struct {
	Filename string
	Bytes    []byte
}

// Parser parses functy source into a Result. Options accrue via chained setters;
// the zero value (via NewParser) accepts only function declarations and the
// built-in type grammar.
type Parser struct {
	allowTopLevelVar   bool
	allowTopLevelConst bool

	requireParamTypes    bool
	requireReturnType    bool
	requireDeclaredTypes bool

	resolver *TypeResolver
}

// NewParser returns a Parser with default options.
func NewParser() *Parser { return &Parser{resolver: NewTypeResolver()} }

// Types returns the parser's TypeResolver. Named types registered through the
// Parser are registered on it, so a host can resolve standalone type annotations
// (TypeResolver.ResolveType / ParseType) against the very same named types it uses
// for parsing `.cty` files.
func (p *Parser) Types() *TypeResolver { return p.types() }

// RegisterType registers a named (capsule) type. An annotation naming it is
// enforced by type identity: a value must already be of ty (or null), unless ty
// itself defines a conversion. Hosts use this for their cty capsule and
// rich-object types. Returns the Parser for chaining.
func (p *Parser) RegisterType(name string, ty cty.Type) *Parser {
	p.types().RegisterType(name, ty)
	return p
}

// RegisterOpenType registers a named open type backed by a predicate. An
// annotation naming it requires the value to satisfy pred and otherwise passes it
// through untouched (non-destructive), so extra attributes survive — suitable for
// marker-capsule objects (e.g. a ctx carrying _ctx plus other fields) and
// required-attribute objects (e.g. an error with at least a message). Returns the
// Parser for chaining.
func (p *Parser) RegisterOpenType(name string, pred func(cty.Value) error) *Parser {
	p.types().RegisterOpenType(name, pred)
	return p
}

// RequireParamTypes, when set, requires every function parameter to carry an
// explicit type annotation (`: T`; `any` is allowed but must be written). Off by
// default. A file may additionally request this via a //functy: directive, but a
// file can never relax a host-set requirement. Returns the Parser for chaining.
func (p *Parser) RequireParamTypes(v bool) *Parser {
	p.requireParamTypes = v
	return p
}

// RequireReturnType requires every function to declare a return type (`-> T`).
// See RequireParamTypes for the off-by-default and tighten-only semantics.
func (p *Parser) RequireReturnType(v bool) *Parser {
	p.requireReturnType = v
	return p
}

// RequireDeclaredTypes requires every var/const declaration to carry a type
// (`: T`). See RequireParamTypes for the off-by-default and tighten-only
// semantics.
func (p *Parser) RequireDeclaredTypes(v bool) *Parser {
	p.requireDeclaredTypes = v
	return p
}

// types returns the parser's resolver, creating it if the Parser was constructed
// as a zero value rather than via NewParser.
func (p *Parser) types() *TypeResolver {
	if p.resolver == nil {
		p.resolver = NewTypeResolver()
	}
	return p.resolver
}

// AllowTopLevelVar controls whether a top-level `var` declaration is collected
// into Result.Vars (true) or reported as a parse error (false, the default).
func (p *Parser) AllowTopLevelVar(v bool) *Parser {
	p.allowTopLevelVar = v
	return p
}

// AllowTopLevelConst controls whether a top-level `const` declaration is
// collected into Result.Consts (true) or reported as a parse error (false, the
// default).
func (p *Parser) AllowTopLevelConst(v bool) *Parser {
	p.allowTopLevelConst = v
	return p
}

// Parse parses a single functy source. The returned Result holds the parsed
// declarations even when diagnostics contain errors (best-effort recovery), so
// callers should check diags before using it.
func (p *Parser) Parse(src []byte, filename string) (*Result, hcl.Diagnostics) {
	return p.parseSources([]Source{{Filename: filename, Bytes: src}})
}

// ParseAll parses several sources together and merges their declarations into one
// Result. The sources share one namespace: type aliases declared in any source
// are visible to every source (see parseSources), and per-source function/var/
// const declarations are concatenated in order (duplicate function names across
// sources are detected later by Result.Compile).
func (p *Parser) ParseAll(sources []Source) (*Result, hcl.Diagnostics) {
	return p.parseSources(sources)
}

// lexedSource is a source after lexing, retained for the two-stage parse (collect
// aliases across all sources, then parse each).
type lexedSource struct {
	filename string
	src      []byte
	tokens   []token
}

// parseSources is the shared core of Parse/ParseAll. It lexes every source,
// collects type aliases from all of them, resolves the aliases together
// (order-independent, project-scoped) into one combined type environment, and
// then parses each source against that environment so any source's annotations
// can name any alias.
func (p *Parser) parseSources(sources []Source) (*Result, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	lexed := make([]lexedSource, 0, len(sources))
	for _, s := range sources {
		tokens, ldiags := lex(s.Bytes, s.Filename)
		diags = diags.Extend(ldiags)
		lexed = append(lexed, lexedSource{filename: s.Filename, src: s.Bytes, tokens: tokens})
	}

	// Collect aliases from every source first, then resolve them into a per-parse
	// env (a clone of the parser's registered types so file aliases never leak
	// back into the shared Parser across separate calls).
	env := p.types().env.clone()
	var aliases []aliasDecl
	for _, ls := range lexed {
		ad, cdiags := collectTypeAliases(ls.tokens, ls.src, ls.filename)
		diags = diags.Extend(cdiags)
		aliases = append(aliases, ad...)
	}
	diags = diags.Extend(resolveTypeAliases(aliases, env))

	merged := &Result{}
	for _, a := range aliases {
		if tc, ok := env.named[a.name]; ok {
			merged.Types = append(merged.Types, TypeAlias{Name: a.name, Type: tc, DefRange: a.defRange})
		}
	}

	for _, ls := range lexed {
		// Directives are collected per file (file-scope), drive that file's strict
		// typing, and are passed through to the host via Result.Directives.
		dirs := collectLeadingDirectives(ls.src, ls.filename)
		merged.Directives = append(merged.Directives, dirs...)
		fParam, fRet, fDecl, idiags := interpretFunctyDirectives(dirs)
		diags = diags.Extend(idiags)

		pr := &parser{
			src:                ls.src,
			filename:           ls.filename,
			tokens:             ls.tokens,
			env:                env,
			allowTopLevelVar:   p.allowTopLevelVar,
			allowTopLevelConst: p.allowTopLevelConst,
			strict: strictness{
				paramTypes:    combineReq(p.requireParamTypes, fParam),
				returnType:    combineReq(p.requireReturnType, fRet),
				declaredTypes: combineReq(p.requireDeclaredTypes, fDecl),
			},
		}
		r := pr.parseFile()
		diags = diags.Extend(pr.diags)
		merged.Funcs = append(merged.Funcs, r.Funcs...)
		merged.Tests = append(merged.Tests, r.Tests...)
		merged.Consts = append(merged.Consts, r.Consts...)
		merged.Vars = append(merged.Vars, r.Vars...)
	}
	return merged, diags
}

// Result is the outcome of parsing one or more functy sources. It is a struct
// (rather than a bare map) so additional collected output can be added without
// breaking callers.
type Result struct {
	Funcs  []*FuncDecl // parsed function declarations
	Tests  []*TestDecl // parsed test blocks (not registered as callable functions)
	Consts []Decl      // top-level const declarations (only when enabled)
	Vars   []Decl      // top-level var declarations (only when enabled)
	Types  []TypeAlias // top-level type aliases (project-scoped across all sources)

	// Directives are the directive comments from each source's leading comment
	// block (file-scope), across all sources. functy acts on its own `functy:`
	// namespace; others are passed through for the host.
	Directives []Directive
}

// TypeAlias is a resolved top-level `type Name = <type>` declaration. Aliases are
// project-scoped: every alias collected from the sources parsed together is
// visible to every source (like the function namespace), so a function in one
// file may use a type declared in another.
type TypeAlias struct {
	Name     string
	Type     TypeConstraint
	DefRange hcl.Range
}

// Decl is a collected top-level var/const declaration, returned unevaluated so a
// host can fold it into its own dependency-sorting and evaluation pass.
// Expr.Variables() exposes the references needed for that sort.
type Decl struct {
	Name     string
	Type     TypeConstraint // from an optional `: T`; nil if unannotated
	Expr     hcl.Expression // initializer, lazily evaluated (nil if none)
	DefRange hcl.Range
}
