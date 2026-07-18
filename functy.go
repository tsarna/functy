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
	"fmt"
	"strings"

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

	// maxSteps is the Tier-1 execution-limit ceiling (see MaxSteps): the maximum
	// number of steps a single function invocation may take. 0 = unbounded.
	maxSteps int

	resolver *TypeResolver

	// externSources are //functy:extern sources supplied by the *host* (see
	// RegisterExterns), as opposed to extern files among the parsed sources. They
	// are parsed lazily and memoized into hostExterns; see loadHostExterns.
	externSources   []Source
	hostExterns     []*FuncDecl
	hostExternDiags hcl.Diagnostics
	hostExternsRead bool
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
//
// An open type asserts only "this value satisfies pred," never a particular Go
// representation. Because the value passes through unchanged, a host that later
// type-asserts it in its own Go code (a capsule's concrete type, an object's
// attribute types) is trusting pred to have checked exactly that: a predicate
// looser than what the consuming code assumes hands that code an unexpected
// representation and can panic it. Make pred validate everything the value's
// consumers rely on. Use RegisterType instead when a value must be a specific
// capsule type — that is enforced by type identity, not a predicate.
func (p *Parser) RegisterOpenType(name string, pred func(cty.Value) error) *Parser {
	p.types().RegisterOpenType(name, pred)
	return p
}

// RegisterExterns registers a //functy:extern source supplied by the host: the
// bodiless declarations of functions the host itself provides, whose real
// signatures their cty metadata cannot express. They surface on
// Result.HostExterns, feed help(), and are checked for collisions — but they are
// never compiled, and never attributed to the sources being parsed.
//
// The source must carry the //functy:extern directive itself; registration
// verifies it rather than forcing the mode. That keeps one byte string meaning one
// thing however it is loaded: the same file a leaf package embeds is a valid
// standalone `.cty` that `functy fmt`, `functy symbols`, and an editor can open.
//
// The canonical arrangement, which costs the leaf package no dependency on functy
// (`embed` is stdlib, and these bytes are opaque to it):
//
//	//go:embed externs.cty
//	var externsCty []byte
//
//	func Externs() []byte { return externsCty }
//
// and in the host:
//
//	parser.RegisterExterns(pkg.Externs(), pkg.ExternsFilename)
//
// Returns the Parser for chaining. Parsing is deferred to the first Parse/ParseAll,
// so registration order relative to RegisterType does not matter.
func (p *Parser) RegisterExterns(src []byte, filename string) *Parser {
	p.externSources = append(p.externSources, Source{Filename: filename, Bytes: src})
	p.hostExternsRead = false // re-parse: a later RegisterType may change resolution
	return p
}

// ExternSources returns the sources registered with RegisterExterns. A host uses it
// to seed its diagnostic file map, so that a diagnostic pointing into an extern file
// it never read from disk still renders with a source snippet.
func (p *Parser) ExternSources() []Source {
	return append([]Source(nil), p.externSources...)
}

// loadHostExterns parses the registered extern sources, memoizing the result.
//
// Each is parsed against its *own* clone of the host's type environment, and only
// its Externs are kept. Both matter:
//
//   - A separate env means a type alias declared in a user's file cannot silently
//     satisfy a name in a host extern (they are unrelated bodies of source that
//     merely happen to be parsed together).
//   - Keeping only Externs is what makes the fmt guarantee hold. A formatter walks
//     Result.Comments by byte offset into the file it is formatting; a host file's
//     comments merged into that slice would splice foreign text at offsets belonging
//     to the user's source. So Comments, Directives, and Types are dropped here, and
//     the declarations land in Result.HostExterns rather than Result.Externs, which
//     is what fmt renders.
func (p *Parser) loadHostExterns() ([]*FuncDecl, hcl.Diagnostics) {
	if p.hostExternsRead {
		return p.hostExterns, p.hostExternDiags
	}
	p.hostExternsRead = true
	p.hostExterns, p.hostExternDiags = nil, nil

	for _, s := range p.externSources {
		tokens, comments, diags := lexAll(s.Bytes, s.Filename)
		fd, idiags := interpretFunctyDirectives(leadingDirectives(comments, tokens))
		diags = diags.Extend(idiags)

		if !fd.extern {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Registered source is not an extern file",
				Detail: fmt.Sprintf(
					"%s was registered with RegisterExterns but its leading comment block does not carry the //functy:extern directive. A registered extern source must declare itself one.", s.Filename),
				Subject: &hcl.Range{Filename: s.Filename, Start: hcl.InitialPos, End: hcl.InitialPos},
			})
			p.hostExternDiags = p.hostExternDiags.Extend(diags)
			continue
		}

		pr := &parser{
			src:      s.Bytes,
			filename: s.Filename,
			tokens:   tokens,
			env:      p.types().env.clone(),
			extern:   true,
			strict: strictness{
				paramTypes:    combineReq(p.requireParamTypes, fd.paramTypes),
				returnType:    combineReq(p.requireReturnType, fd.returnType),
				declaredTypes: combineReq(p.requireDeclaredTypes, fd.declaredTypes),
			},
		}
		r := pr.parseFile()
		diags = diags.Extend(pr.diags)
		attachDocComments(s.Bytes, r, comments)

		p.hostExterns = append(p.hostExterns, r.Externs...)
		p.hostExternDiags = p.hostExternDiags.Extend(diags)
	}
	return p.hostExterns, p.hostExternDiags
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

// MaxSteps sets the Tier-1 execution limit: the maximum number of steps any single
// function invocation may take before a breach terminates the whole evaluation with
// an (uncatchable) *LimitError. A step is one statement executed plus one per loop
// iteration, counted per invocation — so it bounds a single function's runaway
// `for` / `while`, but not recursion (each nested call starts a fresh count) nor
// work aggregated across many small calls. 0 (the default) means unbounded, leaving
// existing embeddings unchanged. The ceiling is captured immutably at compile time.
// A negative value is a mistake — parsing warns and treats it as unbounded rather
// than silently disabling the limit a host meant to set.
// Returns the Parser for chaining.
func (p *Parser) MaxSteps(v int) *Parser {
	p.maxSteps = v
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
// Result. Type aliases declared in any source are visible to every source (they
// are project-scoped — see parseSources), and per-source function/var/const
// declarations are concatenated in order.
//
// Functions are scoped by the *namespace* of the source they were declared in (a
// source with no `namespace` declaration is in the global namespace). A namespace
// spans files: two sources declaring `namespace foo` share one unit, and each can
// call the other's functions — including its private ones — by their bare names.
// Duplicate function names are detected later by Result.Compile, which keys on the
// qualified name, so two sources in *different* namespaces may each declare `baz`.
func (p *Parser) ParseAll(sources []Source) (*Result, hcl.Diagnostics) {
	return p.parseSources(sources)
}

// lexedSource is a source after lexing, retained for the two-stage parse (collect
// aliases across all sources, then parse each).
type lexedSource struct {
	filename string
	src      []byte
	tokens   []token
	comments []Comment
	dirs     []Directive
	fd       fileDirectives
}

// parseSources is the shared core of Parse/ParseAll. It lexes every source,
// collects type aliases from all of them, resolves the aliases together
// (order-independent, project-scoped) into one combined type environment, and
// then parses each source against that environment so any source's annotations
// can name any alias.
func (p *Parser) parseSources(sources []Source) (*Result, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	// tick treats any non-positive ceiling as unbounded (0 is the documented
	// "unbounded"), so a negative MaxSteps would silently disable the execution
	// limit a host meant to set. Surface it and fall back to unbounded rather than
	// pretending a bound is in force.
	effectiveMaxSteps := p.maxSteps
	if effectiveMaxSteps < 0 {
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagWarning,
			Summary:  "Negative execution-step limit",
			Detail: fmt.Sprintf(
				"MaxSteps is %d; a negative limit is treated as unbounded. Pass a positive value to bound execution, or 0 to make unbounded explicit.",
				p.maxSteps),
		})
		effectiveMaxSteps = 0
	}

	// Lex every source and read its leading directive block. Directives are read
	// here — before aliases are collected — because //functy:extern decides whether
	// a file contributes aliases at all: the parser rejects a `type` declaration in
	// an extern file, so collecting its aliases first would leak them project-wide
	// in spite of that rejection.
	lexed := make([]lexedSource, 0, len(sources))
	for _, s := range sources {
		tokens, comments, ldiags := lexAll(s.Bytes, s.Filename)
		diags = diags.Extend(ldiags)
		dirs := leadingDirectives(comments, tokens)
		fd, idiags := interpretFunctyDirectives(dirs)
		diags = diags.Extend(idiags)
		lexed = append(lexed, lexedSource{
			filename: s.Filename, src: s.Bytes, tokens: tokens,
			comments: comments, dirs: dirs, fd: fd,
		})
	}

	// Collect aliases from every source, stamped with each file's namespace, then
	// resolve them per namespace into per-namespace type environments. globalEnv is
	// a clone of the parser's registered types (so file aliases never leak back into
	// the shared Parser across separate calls); each other namespace's env is a
	// clone of the resolved global env, so a bare type name resolves own-then-global.
	globalEnv := p.types().env.clone()

	// Host-registered type names, captured before any alias is resolved, so a
	// namespaced alias that shadows one can be warned about.
	hostTypes := make(map[string]bool, len(globalEnv.named))
	for name := range globalEnv.named {
		hostTypes[name] = true
	}

	var aliases []aliasDecl
	aliasesByNS := map[string][]aliasDecl{}
	var nsOrder []string
	for _, ls := range lexed {
		if ls.fd.extern {
			continue // extern files declare no aliases; the parser rejects `type` there
		}
		ns := leadingNamespace(ls.tokens)
		ad, cdiags := collectTypeAliases(ls.tokens, ls.src, ls.filename, ns)
		diags = diags.Extend(cdiags)
		aliases = append(aliases, ad...)
		for _, a := range ad {
			if _, seen := aliasesByNS[a.namespace]; !seen {
				nsOrder = append(nsOrder, a.namespace)
			}
			aliasesByNS[a.namespace] = append(aliasesByNS[a.namespace], a)
		}
	}

	// Resolve the global namespace first, then each other namespace into a clone of
	// the resolved global env (own shadows global; global + host types fall back).
	envByNS := map[string]*typeEnv{"": globalEnv}
	diags = diags.Extend(resolveTypeAliases(aliasesByNS[""], globalEnv, "", hostTypes))
	for _, ns := range nsOrder {
		if ns == "" {
			continue
		}
		nsEnv := globalEnv.clone()
		diags = diags.Extend(resolveTypeAliases(aliasesByNS[ns], nsEnv, ns, hostTypes))
		envByNS[ns] = nsEnv
	}

	merged := &Result{maxSteps: effectiveMaxSteps}
	for _, a := range aliases {
		env := envByNS[a.namespace]
		if env == nil {
			env = globalEnv
		}
		if tc, ok := env.named[a.name]; ok {
			merged.Types = append(merged.Types, TypeAlias{Name: a.name, Namespace: a.namespace, Type: tc, TypeSrc: a.rhsSrc, DefRange: a.defRange})
		}
	}

	for _, ls := range lexed {
		// Directives are collected per file (file-scope), drive that file's strict
		// typing, and are passed through to the host via Result.Directives.
		merged.Directives = append(merged.Directives, ls.dirs...)
		merged.Comments = append(merged.Comments, ls.comments...)

		pr := &parser{
			src:                ls.src,
			filename:           ls.filename,
			tokens:             ls.tokens,
			env:                globalEnv,
			envByNS:            envByNS,
			allowTopLevelVar:   p.allowTopLevelVar,
			allowTopLevelConst: p.allowTopLevelConst,
			extern:             ls.fd.extern,
			strict: strictness{
				paramTypes:    combineReq(p.requireParamTypes, ls.fd.paramTypes),
				returnType:    combineReq(p.requireReturnType, ls.fd.returnType),
				declaredTypes: combineReq(p.requireDeclaredTypes, ls.fd.declaredTypes),
			},
		}
		r := pr.parseFile()
		diags = diags.Extend(pr.diags)
		attachDocComments(ls.src, r, ls.comments)
		merged.Funcs = append(merged.Funcs, r.Funcs...)
		merged.Externs = append(merged.Externs, r.Externs...)
		merged.Tests = append(merged.Tests, r.Tests...)
		merged.Consts = append(merged.Consts, r.Consts...)
		merged.Vars = append(merged.Vars, r.Vars...)
		merged.Namespaces = append(merged.Namespaces, r.Namespaces...)
	}
	// Host-supplied externs (RegisterExterns) are declarations *about* the host, not
	// about these sources: they go in their own field, contribute no comments, and
	// are never rendered by fmt. See loadHostExterns.
	hostExterns, hostDiags := p.loadHostExterns()
	merged.HostExterns = hostExterns
	diags = diags.Extend(hostDiags)

	diags = diags.Extend(checkExternNames(merged))
	return merged, diags
}

// checkExternNames validates extern names against each other and against the real
// functy functions. It runs on the *merged* result, so it sees every source parsed
// together, not just one file.
//
// Declaring a name more than once **in one file** is an overload set, and legal: a
// host function whose argument shapes differ per arity is not a function with
// optional parameters, and cannot honestly be described by one signature.
// `parsetime(s)` parses a timestamp, while `parsetime(format, s)` parses a format
// and *then* a timestamp; `timeadd`'s return type is a string or a time depending on
// what it was handed. Each form is its own declaration, carrying its own return type.
//
// Declaring one name across *different files* is an error, not an overload: it is
// what happens when two packages both claim `get`, and silently merging them into
// one set would bury that. An overload set is written adjacently, in one file, by
// one author.
//
// Two forms with the same shape are an error wherever they appear — that is a
// copy-paste, not an overload.
func checkExternNames(r *Result) hcl.Diagnostics {
	var diags hcl.Diagnostics

	funcs := make(map[string]*FuncDecl, len(r.Funcs))
	for _, fn := range r.Funcs {
		funcs[fn.QualifiedName()] = fn
	}

	// One pass across both sets, so a host extern colliding with a file extern (or
	// with another host's) is caught on the same footing as two file externs. File
	// externs go first: they have a source the user can edit, so a diagnostic reads
	// better pointed at the later, host-registered declaration.
	type externSet struct {
		decls  []*FuncDecl
		shapes map[string]*FuncDecl // signature shape -> the form that claimed it
	}
	seen := make(map[string]*externSet, len(r.Externs)+len(r.HostExterns))

	for _, ex := range append(append([]*FuncDecl(nil), r.Externs...), r.HostExterns...) {
		name := ex.QualifiedName()

		set, ok := seen[name]
		if !ok {
			seen[name] = &externSet{decls: []*FuncDecl{ex}, shapes: map[string]*FuncDecl{externShape(ex): ex}}

			if fn, ok := funcs[name]; ok {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Extern duplicates a function",
					Detail: fmt.Sprintf(
						"Extern %q is also declared as a functy function at %s. An extern documents a function the host provides, so it cannot also be defined here.",
						name, fn.DefRange),
					Subject: ex.DefRange.Ptr(),
				})
			}
			continue
		}

		prev := set.decls[0]
		if prev.DefRange.Filename != ex.DefRange.Filename {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate extern",
				Detail: fmt.Sprintf(
					"Extern %q is already declared at %s. Two files declaring one name is a collision, not an overload set — the forms of an overloaded function belong together, in one file.",
					name, prev.DefRange),
				Subject: ex.DefRange.Ptr(),
			})
			continue
		}

		shape := externShape(ex)
		if dup, ok := set.shapes[shape]; ok {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate extern signature",
				Detail: fmt.Sprintf(
					"This form of %q is already declared at %s. The forms of an overload set must differ in their parameters.",
					name, dup.DefRange),
				Subject: ex.DefRange.Ptr(),
			})
			continue
		}
		set.shapes[shape] = ex
		set.decls = append(set.decls, ex)
	}
	return diags
}

// externShape is a key for "these two forms are the same signature", used to reject
// a copy-pasted duplicate inside an overload set.
//
// It is keyed on what distinguishes one *call* from another — the arity, and each
// parameter's type and optionality — and deliberately not on parameter names or
// docs: two forms that differ only in what they call their arguments are the same
// form, and one of them is a mistake.
func externShape(fn *FuncDecl) string {
	var b strings.Builder
	for _, p := range fn.Params {
		if p.Variadic {
			b.WriteByte('*')
		}
		if p.Optional || p.Default != nil {
			b.WriteByte('?')
		}
		if p.Type != nil {
			b.WriteString(p.Type.String())
		} else {
			b.WriteString("any")
		}
		b.WriteByte(',')
	}
	return b.String()
}

// Result is the outcome of parsing one or more functy sources. It is a struct
// (rather than a bare map) so additional collected output can be added without
// breaking callers.
type Result struct {
	Funcs  []*FuncDecl // parsed function declarations
	Tests  []*TestDecl // parsed test blocks (not registered as callable functions)
	Consts []Decl      // top-level const declarations (only when enabled)
	Vars   []Decl      // top-level var declarations (only when enabled)
	Types  []TypeAlias // top-level type aliases (namespace-scoped; see TypeAlias)

	// maxSteps is the Tier-1 execution-limit ceiling stamped from the Parser at
	// parse time and read by CompileUnits when it builds each function. 0 =
	// unbounded, so a hand-assembled Result is unbounded by default.
	maxSteps int

	// Externs are the bodiless declarations from //functy:extern sources, in source
	// order. They declare the signatures of functions the *host* provides, so they
	// are never compiled and never callable — Compile and CompileUnits ignore them
	// entirely. They exist so help(), `functy symbols`, and editor tooling can show
	// a host function's real signature, which its cty metadata cannot express: a cty
	// function fakes optional and defaulted arguments with a trailing VarParam, which
	// erases their names, their defaults, and (for the `f([ctx,] x)` convention) the
	// leading parameter itself.
	//
	// A slice, not a map, and deliberately: two declarations of one name is exactly
	// an overload set, so supporting overloads later is a relaxation of
	// checkExternNames rather than a change to this type.
	Externs []*FuncDecl

	// HostExterns are the externs the *host* registered with Parser.RegisterExterns,
	// as opposed to those declared by the parsed sources (Externs). They describe the
	// host's own functions, so they belong to no source file here.
	//
	// The split is what makes fmt safe, and is why this is a separate field rather
	// than a flag: a tool that renders *a source* — fmt, symbols, an outline — must
	// iterate Externs and ignore HostExterns, and gets that by default, because it
	// cannot reach this field without naming it. Merging the two would let `fmt` on a
	// user's file emit another package's declarations into it.
	//
	// Reflection (help()) reads both.
	HostExterns []*FuncDecl

	// Namespaces holds the `namespace a::b` declaration of each namespaced source
	// parsed together, in parse order (a source without one is in the global
	// namespace and contributes nothing here). The namespace a given declaration
	// belongs to is on the declaration itself (FuncDecl.Namespace and friends);
	// this slice exists so tooling that renders or lists the source — notably fmt,
	// which emits from the AST — can see the declaration itself.
	Namespaces []NamespaceDecl

	// Comments is every comment from every source parsed together, in source
	// order, retained with position. Declaration doc comments are also surfaced on
	// FuncDecl.Doc / Decl.Doc; this is the complete table for tooling that needs
	// all comments (e.g. a formatter).
	Comments []Comment

	// Directives are the directive comments from each source's leading comment
	// block (file-scope), across all sources. functy acts on its own `functy:`
	// namespace; others are passed through for the host.
	Directives []Directive
}

// TypeAlias is a resolved top-level `type Name = <type>` declaration. Aliases are
// namespace-scoped with own-then-global resolution, exactly like functions and
// consts: an alias declared in a namespaced file is visible to that namespace's
// annotations first, then falls back to the global (unnamespaced) aliases and the
// host-registered types. Two files in different namespaces may therefore each
// declare the same name; a `_`-prefixed alias is namespace-local (see IsPrivate).
type TypeAlias struct {
	Name string
	// Namespace is the enclosing namespace of the file the alias appeared in
	// ("" = global). It scopes name resolution (own-then-global) and lets a
	// consumer project an alias under the right namespace surface.
	Namespace string
	Type      TypeConstraint
	TypeSrc   string // source text of the aliased type (right-hand side), for rendering (fmt)
	DefRange  hcl.Range
}

// IsPrivate reports whether the alias is namespace-local (a leading underscore).
// A private alias is still resolved and inlined into other aliases/annotations of
// its namespace (so `type items = list(_spec)` works), but a consumer projecting
// an export surface — e.g. the symbols library — withholds it, mirroring how a
// private function is withheld from the host's function table.
func (t *TypeAlias) IsPrivate() bool { return isPrivateName(t.Name) }

// Decl is a collected top-level var/const declaration, returned unevaluated so a
// host can fold it into its own dependency-sorting and evaluation pass.
// Expr.Variables() exposes the references needed for that sort.
type Decl struct {
	Name string
	// Namespace is the enclosing namespace of the file the declaration appeared
	// in ("" = global). functy attaches it and takes no further position: a global
	// is whatever the host decides it is, so the host may ignore namespaced
	// globals, reject them, or implement them by a mechanism of its own.
	//
	// Note there is no qualified *spelling* for a global: HCL's `::` is a
	// function-call selector, so `foo::bar::x` as a variable reference is a parse
	// error. Namespacing therefore applies to functions; this field is metadata.
	Namespace string
	// Doc is the rendered leading doc-comment block (`//` or `#` lines directly
	// above the declaration, directive lines excluded); "" when there is none.
	Doc      string
	Type     TypeConstraint // from an optional `: T`; nil if unannotated
	TypeSrc  string         // source text of the type annotation, for rendering (fmt)
	Expr     hcl.Expression // initializer, lazily evaluated (nil if none)
	DefRange hcl.Range
}

// IsPrivate reports whether the declaration is namespace-local (a leading
// underscore). As with functions, this is advisory for var/const: functy only
// collects them, so it is the host that decides what to do with a private global.
func (d *Decl) IsPrivate() bool { return isPrivateName(d.Name) }
