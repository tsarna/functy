package functy

import (
	"fmt"
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
			{Name: "name", Type: cty.String},
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
// funcs (typically Result.Funcs) supplies the functy declarations, which help
// renders from directly: functy's optional and variadic parameters collapse into a
// single VarParam in the cty calling convention, so the declaration is the only
// accurate source of the real signature. evalCtxFn provides a best-effort fallback
// for non-functy functions, rendered from their cty metadata (parameter names,
// types, and descriptions). A host wires both reflection builtins in:
//
//	ctx.Functions["doc"]  = functy.DocFunc(evalCtxFn)
//	ctx.Functions["help"] = functy.HelpFunc(result.Funcs, evalCtxFn)
//
// Note: a non-functy Go builtin that emulates optional/defaulted parameters through
// its VarParam cannot be rendered with its intended signature — that structure is
// not recoverable from cty — so the fallback shows the raw required-plus-variadic
// shape.
func HelpFunc(funcs []*FuncDecl, evalCtxFn func() *hcl.EvalContext) function.Function {
	byName := make(map[string]*FuncDecl, len(funcs))
	for _, fn := range funcs {
		byName[fn.Name] = fn
	}
	return function.New(&function.Spec{
		Description: `Return a human-readable help summary for a function by name: help("f"). Includes its signature, description, and per-parameter docs; null if there is no such function.`,
		Params: []function.Parameter{
			{Name: "name", Type: cty.String},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			if !args[0].IsKnown() {
				return cty.UnknownVal(cty.String), nil
			}
			if args[0].IsNull() {
				return cty.NullVal(cty.String), nil
			}
			name := args[0].AsString()
			if fn, ok := byName[name]; ok {
				return cty.StringVal(renderFuncHelp(fn)), nil
			}
			if evalCtxFn != nil {
				if ctx := evalCtxFn(); ctx != nil {
					if f, ok := ctx.Functions[name]; ok {
						return cty.StringVal(renderCtyHelp(name, f)), nil
					}
				}
			}
			return cty.NullVal(cty.String), nil // no such function
		},
	})
}

// paramDoc is one entry of a rendered "Parameters:" section.
type paramDoc struct{ name, doc string }

// renderFuncHelp renders help for a functy function from its declaration: an exact
// signature, the function's doc, and a per-parameter section for documented params.
func renderFuncHelp(fn *FuncDecl) string {
	var b strings.Builder
	b.WriteString(fn.Name)
	b.WriteByte('(')
	var docs []paramDoc
	for i, p := range fn.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(renderParam(p))
		if p.Doc != "" {
			docs = append(docs, paramDoc{paramDisplayName(p), p.Doc})
		}
	}
	b.WriteByte(')')
	if fn.RetType != nil {
		b.WriteString(" -> ")
		b.WriteString(fn.RetType.String())
	}
	if fn.Doc != "" {
		b.WriteString("\n\n")
		b.WriteString(fn.Doc)
	}
	b.WriteString(renderParamDocs(docs))
	return b.String()
}

// paramDisplayName is the parameter's name as it appears in a signature (a
// variadic parameter carries its leading `*`).
func paramDisplayName(p Param) string {
	if p.Variadic {
		return "*" + p.Name
	}
	return p.Name
}

// renderParam renders one parameter in functy signature syntax:
// [*]name[: T][ = default].
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
	if d := f.Description(); d != "" {
		b.WriteString("\n\n")
		b.WriteString(d)
	}
	b.WriteString(renderParamDocs(docs))
	return b.String()
}

// ctyParamString renders a cty parameter as name[: type], omitting the type when it
// is dynamic (any).
func ctyParamString(name string, ty cty.Type) string {
	if ty == cty.NilType || ty == cty.DynamicPseudoType {
		return name
	}
	return name + ": " + ty.FriendlyName()
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
