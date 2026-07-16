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
	var funcs, externs []*FuncDecl
	if res != nil {
		funcs = res.Funcs
		// Both extern sets are equally real to reflection: one was declared by the
		// parsed sources, the other registered by the host (RegisterExterns). They are
		// separate fields only so that tools which render *a source* can't emit the
		// host's declarations into it. File externs go first, so that on a duplicate
		// — which checkExternNames has already reported as an error — the one with an
		// editable source wins the index.
		externs = append(append([]*FuncDecl(nil), res.Externs...), res.HostExterns...)
	}
	funcByName, funcByBare := indexDecls(funcs)
	externByName, externByBare := indexDecls(externs)

	return function.New(&function.Spec{
		Description: `Return a human-readable help summary for a function by name: help("f"). Includes its signature, description, and per-parameter docs; null if there is no such function. Called with no argument, help() lists the names of all available functions.`,
		Params:      []function.Parameter{},
		VarParam:    &function.Parameter{Name: "name", Type: cty.String, Description: "The name of the function to describe; omit to list all available function names"},
		Type:        function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			if len(args) == 0 {
				return cty.StringVal(renderFuncList(funcByName, externByName, evalCtxFn)), nil
			}
			if !args[0].IsKnown() {
				return cty.UnknownVal(cty.String), nil
			}
			if args[0].IsNull() {
				return cty.NullVal(cty.String), nil
			}
			name := args[0].AsString()
			if fn, ok := funcByName[name]; ok {
				return cty.StringVal(renderFuncHelp(fn)), nil
			}
			// Externs are consulted *before* the eval context, because an extern names
			// a function that is in the context: the whole reason it exists is that the
			// cty metadata there renders the collapsed VarParam shape instead of the
			// real signature. Letting the context win would defeat the feature.
			if fn, ok := externByName[name]; ok {
				return cty.StringVal(renderFuncHelp(fn)), nil
			}
			if evalCtxFn != nil {
				if ctx := evalCtxFn(); ctx != nil {
					if f, ok := ctx.Functions[name]; ok {
						return cty.StringVal(renderCtyHelp(name, f)), nil
					}
				}
			}
			// Last: a bare name that is unambiguous across the namespaces. Ordered
			// after the eval-context lookup so it can never shadow a host function.
			if fn, ok := funcByBare[name]; ok {
				return cty.StringVal(renderFuncHelp(fn)), nil
			}
			if fn, ok := externByBare[name]; ok {
				return cty.StringVal(renderFuncHelp(fn)), nil
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

// renderFuncList returns the sorted, newline-separated names of every available
// function, for the no-argument help() form: the assembled eval context (which
// holds host- and functy-defined functions in one flat map), unioned with the
// declarations.
//
// The union is unconditional, and must be: an extern is by definition *not* in the
// eval context (it names a function the host registered under its own machinery, or
// one help simply cannot see), so anything short of a union would drop externs the
// moment a context exists — which, in the CLI, is always.
//
// Private functions never appear. From the eval context that is structural rather
// than filtered — they were never put in the host's map — and the declarations are
// filtered below to match.
func renderFuncList(funcByName, externByName map[string][]*FuncDecl, evalCtxFn func() *hcl.EvalContext) string {
	names := make(map[string]struct{})
	if evalCtxFn != nil {
		if ctx := evalCtxFn(); ctx != nil {
			for n := range ctx.Functions {
				names[n] = struct{}{}
			}
		}
	}
	for _, decls := range []map[string][]*FuncDecl{funcByName, externByName} {
		for n, set := range decls {
			// An overload set's members share a name, so privacy is a property of the
			// name and any member answers for all of them.
			if len(set) == 0 || set[0].IsPrivate() {
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
	return strings.Join(sorted, "\n")
}

// paramDoc is one entry of a rendered "Parameters:" section.
type paramDoc struct{ name, doc string }

// renderFuncHelp renders help for a functy function from its declaration: an exact
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
func renderFuncHelp(fns []*FuncDecl) string {
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
		b.WriteString(renderSignature(fn))

		for _, p := range fn.Params {
			name := paramDisplayName(p)
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

// renderSignature renders one form: the name it is callable under, its parameters,
// and its return type.
func renderSignature(fn *FuncDecl) string {
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

// paramDisplayName is the parameter's name carrying whatever marker its surface
// syntax puts *on the name* — the variadic star, or the optional-without-default
// `?`. Both the signature line and the aligned "Parameters:" column key on it, so
// they stay consistent with each other.
func paramDisplayName(p Param) string {
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
	b.WriteString(paramDisplayName(p))
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

// renderCtyHelp renders best-effort help for a non-functy function from its cty
// metadata: the parameter names/types cty exposes, plus descriptions.
func renderCtyHelp(name string, f function.Function) string {
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
	var docs []paramDoc
	for i, p := range f.Params() {
		sep()
		pn := p.Name
		if pn == "" {
			pn = fmt.Sprintf("arg%d", i+1)
		}
		b.WriteString(ctyParamString(pn, p.Type))
		if p.Description != "" {
			docs = append(docs, paramDoc{pn, p.Description})
		}
	}
	if vp := f.VarParam(); vp != nil {
		sep()
		vn := vp.Name
		if vn == "" {
			vn = "args"
		}
		b.WriteString("*")
		b.WriteString(ctyParamString(vn, vp.Type))
		if vp.Description != "" {
			docs = append(docs, paramDoc{"*" + vn, vp.Description})
		}
	}
	b.WriteByte(')')
	if ret, ok := ctyReturnType(f); ok {
		b.WriteString(" -> ")
		b.WriteString(typeString(ret))
	}
	if d := f.Description(); d != "" {
		b.WriteString("\n\n")
		b.WriteString(d)
	}
	b.WriteString(renderParamDocs(docs))
	return b.String()
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
	ret, err := f.ReturnType(argTypes)
	if err != nil || ret == cty.NilType || ret == cty.DynamicPseudoType {
		return cty.NilType, false
	}
	return ret, true
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
	return name + ": " + typeString(ty)
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
