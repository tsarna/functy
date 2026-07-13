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

	// Collect aliases from every source first, then resolve them into a per-parse
	// env (a clone of the parser's registered types so file aliases never leak
	// back into the shared Parser across separate calls).
	env := p.types().env.clone()
	var aliases []aliasDecl
	for _, ls := range lexed {
		if ls.fd.extern {
			continue // extern files declare no aliases; the parser rejects `type` there
		}
		ad, cdiags := collectTypeAliases(ls.tokens, ls.src, ls.filename)
		diags = diags.Extend(cdiags)
		aliases = append(aliases, ad...)
	}
	diags = diags.Extend(resolveTypeAliases(aliases, env))

	merged := &Result{}
	for _, a := range aliases {
		if tc, ok := env.named[a.name]; ok {
			merged.Types = append(merged.Types, TypeAlias{Name: a.name, Type: tc, TypeSrc: a.rhsSrc, DefRange: a.defRange})
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
			env:                env,
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
	diags = diags.Extend(checkExternNames(merged))
	return merged, diags
}

// checkExternNames rejects an extern name that is declared twice, or that is also
// declared as a real functy function. It runs on the *merged* result so it catches
// collisions across sources parsed together, not just within one file.
//
// Duplicates are an error only because overload sets are not implemented yet: an
// overloaded host function (parsetime(s) / parsetime(format, s)) is exactly a name
// with several signatures, and Result.Externs is a slice precisely so allowing that
// later is a relaxation of this check rather than a change of shape.
func checkExternNames(r *Result) hcl.Diagnostics {
	var diags hcl.Diagnostics

	funcs := make(map[string]*FuncDecl, len(r.Funcs))
	for _, fn := range r.Funcs {
		funcs[fn.QualifiedName()] = fn
	}

	seen := make(map[string]*FuncDecl, len(r.Externs))
	for _, ex := range r.Externs {
		name := ex.QualifiedName()
		if prev, ok := seen[name]; ok {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate extern",
				Detail: fmt.Sprintf(
					"Extern %q is already declared at %s. Declaring one name with several signatures (an overload set) is not supported yet.",
					name, prev.DefRange),
				Subject: ex.DefRange.Ptr(),
			})
			continue
		}
		seen[name] = ex

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
	}
	return diags
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
// project-scoped: every alias collected from the sources parsed together is
// visible to every source (like the function namespace), so a function in one
// file may use a type declared in another.
type TypeAlias struct {
	Name     string
	Type     TypeConstraint
	TypeSrc  string // source text of the aliased type (right-hand side), for rendering (fmt)
	DefRange hcl.Range
}

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
