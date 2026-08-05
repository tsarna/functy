package functy

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// DocFunc returns the context-aware builtin doc(name): given a function's name as
// a string, it returns that function's description — the doc comment captured on a
// functy declaration (FuncDecl.Doc, wired into the compiled function's cty
// Description) or whatever Description a host function carries. It is tri-state:
//
//   - null   — no such function (absent from the context)
//   - ""     — the function exists but is undocumented
//   - "text" — the function's description
//
// Distinguishing "absent" (null) from "undocumented" ("") lets a caller catch a
// mistyped name without doc having to throw: absence is a normal reflection answer,
// so a caller who wants strictness opts in (e.g. assert(doc(x) != null)).
//
// It is not part of Stdlib() because it needs a handle to the assembled eval
// context. evalCtxFn is the same late-binding closure passed to Result.Compile: at
// call time it yields the merged context whose Functions map holds every function
// (host- and functy-defined) in one flat map, which doc looks the name up in. A
// host merges the result under the name "doc":
//
//	ctx.Functions["doc"] = functy.DocFunc(evalCtxFn)
//
// (The richer help(name) — assembling a function's full calling convention and
// per-argument docs — is left for later; doc is the primitive it will build on.)
func DocFunc(evalCtxFn func() *hcl.EvalContext) function.Function {
	return function.New(&function.Spec{
		Description: `Return a function's documentation by name: doc("f"). Returns the function's description, "" if it exists but is undocumented, or null if there is no such function.`,
		Params: []function.Parameter{
			{Name: "name", Type: cty.String, Description: "The name of the function to document"},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			absent := cty.NullVal(cty.String)
			if !args[0].IsKnown() {
				return cty.UnknownVal(cty.String), nil
			}
			if args[0].IsNull() || evalCtxFn == nil {
				return absent, nil
			}
			ctx := evalCtxFn()
			if ctx == nil {
				return absent, nil
			}
			if fn, ok := ctx.Functions[args[0].AsString()]; ok {
				// Known: its description ("" when the function is undocumented).
				return cty.StringVal(fn.Description()), nil
			}
			return absent, nil // no such function
		},
	})
}

// HelpFunc returns the context-aware builtin help(name): a human-readable summary
// of a function — its signature (calling convention), description, and per-parameter
// docs — as a single string, or null if there is no such function.
//
// Called with no argument, help() instead returns the sorted, newline-separated
// names of every available function (a directory to explore with help(name)),
// drawn from the assembled eval context so it spans host- and functy-defined
// functions alike.
//
// res (the parse Result) supplies the functy declarations, which help renders from
// directly: functy's optional and variadic parameters collapse into a single
// VarParam in the cty calling convention, so the declaration is the only accurate
// source of the real signature. Result.Externs is consulted the same way, and is
// the *point* of that set — an extern declares what a host function's cty metadata
// cannot express. evalCtxFn provides a best-effort fallback for anything neither
// declares, rendered from its cty metadata (parameter names, types, descriptions).
// A host wires both reflection builtins in:
//
//	ctx.Functions["doc"]  = functy.DocFunc(evalCtxFn)
//	ctx.Functions["help"] = functy.HelpFunc(result, evalCtxFn)
//
// Note: a non-functy Go builtin that emulates optional/defaulted parameters through
// its VarParam cannot be rendered with its intended signature — that structure is
// not recoverable from cty — so the fallback shows the raw required-plus-variadic
// shape. Declaring an extern for it is how that is fixed.
func HelpFunc(res *Result, evalCtxFn func() *hcl.EvalContext) function.Function {
	return function.New(&function.Spec{
		Description: `Return a human-readable help summary for a function by name: help("f"). Includes its signature, description, and per-parameter docs; null if there is no such function. Called with no argument, help() lists the names of all available functions.`,
		Params:      []function.Parameter{},
		VarParam:    &function.Parameter{Name: "name", Type: cty.String, Description: "The name of the function to describe; omit to list all available function names"},
		Type:        function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			if len(args) == 0 {
				return cty.StringVal(strings.Join(res.FuncNames(evalCtxFn), "\n")), nil
			}
			if !args[0].IsKnown() {
				return cty.UnknownVal(cty.String), nil
			}
			if args[0].IsNull() {
				return cty.NullVal(cty.String), nil
			}
			name := args[0].AsString()
			if fn := res.LookupFuncDecls(name); len(fn) > 0 {
				return cty.StringVal(RenderFuncHelp(fn)), nil
			}
			if evalCtxFn != nil {
				if ctx := evalCtxFn(); ctx != nil {
					if f, ok := ctx.Functions[name]; ok {
						return cty.StringVal(RenderCtyHelp(name, f)), nil
					}
				}
			}
			// Last: a bare name that is unambiguous across the namespaces. Ordered
			// after the eval-context lookup so it can never shadow a host function.
			if fn := res.LookupBareFuncDecls(name); len(fn) > 0 {
				return cty.StringVal(RenderFuncHelp(fn)), nil
			}
			return cty.NullVal(cty.String), nil // no such function
		},
	})
}

// indexDecls keys declarations by qualified name, and — for a last-resort fallback
// so help("baz") still works in the common single-namespace project — by bare name.
// A bare name declared in more than one namespace is ambiguous and dropped rather
// than guessed at.
//
// Private functions are included: they are not host-visible, but help() is a
// developer tool and `help("foo::_helper")` is exactly what you want when debugging
// one.
// Each entry is a *set* of declarations, because one extern name may carry several
// signatures (an overload set). For an ordinary function the set has one member.
func indexDecls(decls []*FuncDecl) (byName, byBare map[string][]*FuncDecl) {
	byName = make(map[string][]*FuncDecl, len(decls))
	for _, fn := range decls {
		q := fn.QualifiedName()
		byName[q] = append(byName[q], fn)
	}

	// A bare name is usable only when it resolves to exactly one *qualified* name.
	// Several overloads of that one name are not ambiguity — they are the point —
	// so the check counts distinct namespaces, not declarations.
	qualifiedNames := make(map[string]map[string]bool, len(decls))
	for _, fn := range decls {
		if qualifiedNames[fn.Name] == nil {
			qualifiedNames[fn.Name] = make(map[string]bool)
		}
		qualifiedNames[fn.Name][fn.QualifiedName()] = true
	}
	byBare = make(map[string][]*FuncDecl, len(decls))
	for _, fn := range decls {
		if len(qualifiedNames[fn.Name]) != 1 {
			continue // declared in more than one namespace: ambiguous, not guessed at
		}
		byBare[fn.Name] = append(byBare[fn.Name], fn)
	}
	return byName, byBare
}

// decls returns the two declaration sets reflection searches, in the order it
// searches them.
//
// Both extern sets are equally real to it: one was declared by the parsed
// sources, the other registered by the host (RegisterExterns). They are separate
// fields only so that tools which render *a source* cannot emit the host's
// declarations into it, so they are returned as one search set. File externs go
// first, so that on a duplicate — which checkExternNames has already reported as
// an error — the one with an editable source is what a reader sees first.
func (r *Result) decls() (funcs, externs []*FuncDecl) {
	if r == nil {
		return nil, nil
	}
	return r.Funcs, append(append([]*FuncDecl(nil), r.Externs...), r.HostExterns...)
}

// LookupFuncDecls returns the declarations of the function named by its
// qualified name, or nil when none declares it.
//
// The result is a *set* because one name may carry several signatures — an
// overload set (see checkExternNames). Pass it to RenderFuncHelp for the text
// help() produces, or read the declarations directly to render them your own way.
//
// Externs are searched after the parsed functions but, deliberately, before any
// eval context: an extern names a function that is *in* the context, and the
// whole reason it exists is that the cty metadata there renders the collapsed
// VarParam shape instead of the real signature. A host reproducing help()'s
// precedence therefore does:
//
//	if d := res.LookupFuncDecls(n); len(d) > 0 { … RenderFuncHelp(d) }
//	if f, ok := ctx.Functions[n]; ok        { … RenderCtyHelp(n, f) }
//	if d := res.LookupBareFuncDecls(n); len(d) > 0 { … RenderFuncHelp(d) }
func (r *Result) LookupFuncDecls(name string) []*FuncDecl {
	funcs, externs := r.decls()
	for _, set := range []([]*FuncDecl){funcs, externs} {
		byName, _ := indexDecls(set)
		if d, ok := byName[name]; ok {
			return d
		}
	}
	return nil
}

// LookupBareFuncDecls returns the declarations of a function named without its
// namespace — help("baz") for `ns::baz` — when that bare name is unambiguous.
//
// A bare name declared in more than one namespace resolves to nothing rather
// than to a guess; BareNameCandidates says which ones it could have meant, so a
// caller can report the ambiguity instead of a bare absence.
//
// Ordered after the eval-context lookup by every caller, so it can never shadow
// a host function.
func (r *Result) LookupBareFuncDecls(name string) []*FuncDecl {
	funcs, externs := r.decls()
	for _, set := range []([]*FuncDecl){funcs, externs} {
		_, byBare := indexDecls(set)
		if d, ok := byBare[name]; ok {
			return d
		}
	}
	return nil
}

// BareNameCandidates returns the qualified names a bare name could refer to,
// sorted.
//
// One name is what LookupBareFuncDecls resolves; more than one is why it
// resolves nothing, and is the difference between "ambiguous" and "no such
// function" — which a caller cannot otherwise tell apart, since both come back
// empty. None means the bare name is not declared anywhere.
//
// Private declarations are included: they are not host-visible, but reflection is
// a developer tool and knowing that `foo` is ambiguous with a private `ns::foo`
// is exactly what explains the answer.
func (r *Result) BareNameCandidates(name string) []string {
	funcs, externs := r.decls()
	seen := make(map[string]bool)
	for _, set := range []([]*FuncDecl){funcs, externs} {
		for _, fn := range set {
			if fn.Name == name {
				seen[fn.QualifiedName()] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// FuncNames returns the sorted names of every available function: the directory
// the no-argument help() lists.
//
// The assembled eval context (which holds host- and functy-defined functions in
// one flat map) is unioned with the declarations. The union is unconditional, and
// must be: an extern is by definition *not* in the eval context (it names a
// function the host registered under its own machinery, or one reflection simply
// cannot see), so anything short of a union would drop externs the moment a
// context exists — which, in a CLI, is always.
//
// evalCtxFn may be nil, which yields the declared names alone.
//
// Private functions never appear. From the eval context that is structural rather
// than filtered — they were never put in the host's map — and the declarations are
// filtered here to match.
func (r *Result) FuncNames(evalCtxFn func() *hcl.EvalContext) []string {
	names := make(map[string]struct{})
	if evalCtxFn != nil {
		if ctx := evalCtxFn(); ctx != nil {
			for n := range ctx.Functions {
				names[n] = struct{}{}
			}
		}
	}
	funcs, externs := r.decls()
	for _, set := range []([]*FuncDecl){funcs, externs} {
		byName, _ := indexDecls(set)
		for n, decls := range byName {
			// An overload set's members share a name, so privacy is a property of
			// the name and any member answers for all of them.
			if len(decls) == 0 || decls[0].IsPrivate() {
				continue
			}
			names[n] = struct{}{}
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	return sorted
}

// paramDoc is one entry of a rendered "Parameters:" section.
type paramDoc struct{ name, doc string }

// RenderFuncHelp renders help for a functy function from its declaration: an exact
// signature, the function's doc, and a per-parameter section for documented params.
//
// fns is a *set*, because an extern name may carry several signatures — an overload
// set (see checkExternNames). A host function whose argument shapes differ per arity
// is not a function with optional parameters, and rendering it as one would lie:
// parsetime(s) reads a timestamp, while parsetime(format, s) reads a format and then
// a timestamp. So every form is listed, each with its own return type — which is also
// how a form whose return type depends on its arguments (timeadd) becomes sayable at
// all.
//
// The forms are rendered together rather than as separate blocks: the signatures
// first, then the documentation, then one "Parameters:" section unioned across the
// forms (first occurrence of a name wins). Documenting the family once, above the
// first form, is the expected style; if several forms carry distinct docs, each is
// kept, in order.
func RenderFuncHelp(fns []*FuncDecl) string {
	if len(fns) == 0 {
		return ""
	}

	var b strings.Builder
	var docs []paramDoc
	seenParam := make(map[string]bool)
	var seenDoc []string

	for i, fn := range fns {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(RenderFuncSignature(fn))

		for _, p := range fn.Params {
			name := p.DisplayName()
			if p.Doc == "" || seenParam[name] {
				continue
			}
			seenParam[name] = true
			docs = append(docs, paramDoc{name, p.Doc})
		}
		if fn.Doc != "" && !slices.Contains(seenDoc, fn.Doc) {
			seenDoc = append(seenDoc, fn.Doc)
		}
	}

	if len(seenDoc) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(seenDoc, "\n\n"))
	}
	b.WriteString(renderParamDocs(docs))
	return b.String()
}

// RenderFuncSignature renders one form of a declaration: the name it is callable
// under, its parameters, and its return type — the line that heads help()'s
// output for it.
//
// Exported alongside RenderFuncHelp for a host that lays a function out itself:
// a page wants the signature and the parameters as separate elements, so that
// the parameter list can become a table and re-wrap, which the single rendered
// block cannot.
func RenderFuncSignature(fn *FuncDecl) string {
	var b strings.Builder
	b.WriteString(fn.QualifiedName())
	b.WriteByte('(')
	for i, p := range fn.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(renderParam(p))
	}
	b.WriteByte(')')
	if fn.RetType != nil {
		b.WriteString(" -> ")
		b.WriteString(fn.RetType.String())
	}
	return b.String()
}

// DisplayName is the parameter's name carrying whatever marker its surface
// syntax puts *on the name* — the variadic star, or the optional-without-default
// `?`. Both the signature line and the aligned "Parameters:" column key on it, so
// they stay consistent with each other, and a host rendering its own parameter
// table keys on it for the same reason.
func (p Param) DisplayName() string {
	if p.Variadic {
		return "*" + p.Name
	}
	if p.Optional {
		return p.Name + "?"
	}
	return p.Name
}

// renderParam renders one parameter in functy signature syntax:
// [*]name[?][: T][ = default].
func renderParam(p Param) string {
	var b strings.Builder
	b.WriteString(p.DisplayName())
	if p.Type != nil {
		b.WriteString(": ")
		b.WriteString(p.Type.String())
	}
	if p.Default != nil {
		b.WriteString(" = ")
		if p.DefaultSrc != "" {
			b.WriteString(p.DefaultSrc)
		} else {
			b.WriteString("…")
		}
	}
	return b.String()
}

// RenderCtyHelp renders best-effort help for a non-functy function from its cty
// metadata: the parameter names/types cty exposes, plus descriptions.
func RenderCtyHelp(name string, f function.Function) string {
	var b strings.Builder
	b.WriteString(RenderCtySignature(name, f))

	var docs []paramDoc
	for i, p := range f.Params() {
		if p.Description != "" {
			docs = append(docs, paramDoc{ctyParamName(p, i), p.Description})
		}
	}
	if vp := f.VarParam(); vp != nil && vp.Description != "" {
		docs = append(docs, paramDoc{"*" + ctyVarParamName(*vp), vp.Description})
	}

	if d := f.Description(); d != "" {
		b.WriteString("\n\n")
		b.WriteString(d)
	}
	b.WriteString(renderParamDocs(docs))
	return b.String()
}

// RenderCtySignature renders a non-functy function's calling convention from its
// cty metadata: the parameter names and types cty exposes, and the return type
// where one can be computed.
//
// The sibling of RenderFuncSignature, for the functions that have no declaration
// — which is most of a host's. A host laying a function out itself needs the
// signature separately from the parameter documentation, and cannot reproduce
// this: the return type has to be asked for with a speculative call that may
// panic, and the rule for when a type annotation is noise rather than
// information lives here.
func RenderCtySignature(name string, f function.Function) string {
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('(')
	first := true
	sep := func() {
		if !first {
			b.WriteString(", ")
		}
		first = false
	}
	for i, p := range f.Params() {
		sep()
		b.WriteString(ctyParamString(ctyParamName(p, i), p.Type))
	}
	if vp := f.VarParam(); vp != nil {
		sep()
		b.WriteString("*")
		b.WriteString(ctyParamString(ctyVarParamName(*vp), vp.Type))
	}
	b.WriteByte(')')
	if ret, ok := ctyReturnType(f); ok {
		b.WriteString(" -> ")
		b.WriteString(TypeString(ret))
	}
	return b.String()
}

// ctyParamName is a positional parameter's name, or a synthesized one when cty
// carries none.
func ctyParamName(p function.Parameter, i int) string {
	if p.Name == "" {
		return fmt.Sprintf("arg%d", i+1)
	}
	return p.Name
}

// ctyVarParamName is the variadic parameter's name, without its star.
func ctyVarParamName(p function.Parameter) string {
	if p.Name == "" {
		return "args"
	}
	return p.Name
}

// ctyReturnType asks a cty function what it returns when called with exactly its
// declared parameter types and no variadic arguments — which for the common case (a
// static return type) is simply what it always returns.
//
// It is best-effort by nature: a function whose return type is computed from its
// *arguments* cannot answer without them, and reports back dynamic or an error. That
// is not a failure to report — a function whose result type depends on what it is
// handed is exactly the kind that needs an extern, where each form states its own
// return type.
func ctyReturnType(f function.Function) (cty.Type, bool) {
	params := f.Params()
	argTypes := make([]cty.Type, len(params))
	for i, p := range params {
		argTypes[i] = p.Type
	}
	ret, err := safeReturnType(f, argTypes)
	if err != nil || ret == cty.NilType || ret == cty.DynamicPseudoType {
		return cty.NilType, false
	}
	return ret, true
}

// safeReturnType calls f.ReturnType with panic recovery. A host function's Type
// callback is arbitrary host code, so a buggy one can panic; help() reflecting over
// it must not crash. A panic is treated exactly like a reported error or a dynamic
// result — "no static return type" — so the signature simply renders without one.
func safeReturnType(f function.Function, argTypes []cty.Type) (ret cty.Type, err error) {
	defer func() {
		if r := recover(); r != nil {
			ret, err = cty.NilType, fmt.Errorf("panic in return-type callback: %v", r)
		}
	}()
	return f.ReturnType(argTypes)
}

// ctyParamString renders a cty parameter as name[: type], omitting the type when it
// is dynamic (any). The type is rendered in functy's own grammar — object({ … }),
// list(string) — the same round-trippable syntax a declaration uses, rather than cty's
// prose FriendlyName, which flattens every object to bare "object" and so hides the
// attributes that are often the whole point of a structural return.
func ctyParamString(name string, ty cty.Type) string {
	if ty == cty.NilType || ty == cty.DynamicPseudoType {
		return name
	}
	return name + ": " + TypeString(ty)
}

// renderParamDocs renders the aligned "Parameters:" section, or "" when no
// parameter is documented. Multi-line docs have their continuation lines indented
// to the description column.
func renderParamDocs(docs []paramDoc) string {
	if len(docs) == 0 {
		return ""
	}
	width := 0
	for _, d := range docs {
		if len(d.name) > width {
			width = len(d.name)
		}
	}
	pad := strings.Repeat(" ", 2+width+2)
	var b strings.Builder
	b.WriteString("\n\nParameters:")
	for _, d := range docs {
		b.WriteString("\n  ")
		b.WriteString(d.name)
		b.WriteString(strings.Repeat(" ", width-len(d.name)))
		b.WriteString("  ")
		b.WriteString(strings.ReplaceAll(d.doc, "\n", "\n"+pad))
	}
	return b.String()
}
