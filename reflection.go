package functy

import (
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
