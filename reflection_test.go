package functy

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// buildContextWithDoc parses src, compiles its functions against a late-bound
// context, and returns that context with the compiled functions plus doc() merged
// in. It exercises the full chain: FuncDecl.Doc -> compiled function Description ->
// doc() lookup.
func buildContextWithDoc(t *testing.T, src string) *hcl.EvalContext {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	var ctx *hcl.EvalContext
	evalCtxFn := func() *hcl.EvalContext { return ctx }
	funcs, cdiags := res.Compile(evalCtxFn)
	if cdiags.HasErrors() {
		t.Fatalf("compile: %s", cdiags.Error())
	}
	ctx = &hcl.EvalContext{Functions: map[string]function.Function{}}
	for k, v := range funcs {
		ctx.Functions[k] = v
	}
	ctx.Functions["doc"] = DocFunc(evalCtxFn)
	return ctx
}

func TestDocFuncReturnsDescription(t *testing.T) {
	ctx := buildContextWithDoc(t, "// Adds two numbers.\n// Second is optional.\nfunc add(a, b = 0) { return a + b }\n")

	// The compiled function carries the doc as its cty Description.
	if got := ctx.Functions["add"].Description(); got != "Adds two numbers.\nSecond is optional." {
		t.Fatalf("Description = %q", got)
	}

	got, err := ctx.Functions["doc"].Call([]cty.Value{cty.StringVal("add")})
	if err != nil {
		t.Fatalf("doc call: %s", err)
	}
	if got.AsString() != "Adds two numbers.\nSecond is optional." {
		t.Fatalf("doc(\"add\") = %q", got.AsString())
	}
}

func TestDocFuncUnknownAndUndocumented(t *testing.T) {
	ctx := buildContextWithDoc(t, "func bare(a) { return a }\n")

	// Unknown function name -> null (absent).
	got, err := ctx.Functions["doc"].Call([]cty.Value{cty.StringVal("nope")})
	if err != nil {
		t.Fatalf("doc call: %s", err)
	}
	if !got.IsNull() {
		t.Fatalf("doc(\"nope\") = %#v, want null", got)
	}

	// Known but undocumented function -> "" (exists, no docs).
	got, err = ctx.Functions["doc"].Call([]cty.Value{cty.StringVal("bare")})
	if err != nil {
		t.Fatalf("doc call: %s", err)
	}
	if got.IsNull() || got.AsString() != "" {
		t.Fatalf("doc(\"bare\") = %#v, want empty string", got)
	}
}
