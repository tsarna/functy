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

func TestHelpFuncFunctyFunction(t *testing.T) {
	src := "// Adds two numbers.\n" +
		"func add(\n" +
		"    a: number,      // the first addend\n" +
		"    b: number = 0,  // the second addend\n" +
		") -> number { return a + b }\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	help := HelpFunc(res.Funcs, nil)

	got, err := help.Call([]cty.Value{cty.StringVal("add")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	want := "add(a: number, b: number = 0) -> number\n\n" +
		"Adds two numbers.\n\n" +
		"Parameters:\n" +
		"  a  the first addend\n" +
		"  b  the second addend"
	if got.AsString() != want {
		t.Fatalf("help(\"add\") =\n%q\nwant\n%q", got.AsString(), want)
	}
}

func TestHelpFuncCtyFallback(t *testing.T) {
	greet := function.New(&function.Spec{
		Description: "A host greeting.",
		Params: []function.Parameter{
			{Name: "who", Type: cty.String, Description: "who to greet"},
		},
		Type: function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{"greet": greet}}
	help := HelpFunc(nil, func() *hcl.EvalContext { return ctx })

	got, err := help.Call([]cty.Value{cty.StringVal("greet")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	want := "greet(who: string)\n\nA host greeting.\n\nParameters:\n  who  who to greet"
	if got.AsString() != want {
		t.Fatalf("help(\"greet\") =\n%q\nwant\n%q", got.AsString(), want)
	}
}

func TestHelpFuncUnknown(t *testing.T) {
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{}}
	help := HelpFunc(nil, func() *hcl.EvalContext { return ctx })
	got, err := help.Call([]cty.Value{cty.StringVal("nope")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	if !got.IsNull() {
		t.Fatalf("help(\"nope\") = %#v, want null", got)
	}
}
