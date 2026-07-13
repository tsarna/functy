package functy

import (
	"strings"
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
	help := HelpFunc(res, nil)

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

// An extern renders like any other declaration, with `?` marking a parameter that
// is optional but has no default — including a *leading* one, which is the shape
// cty cannot express and the reason externs exist.
func TestHelpFuncExtern(t *testing.T) {
	src := "//functy:extern\n\n" +
		"// Read a value from a thing.\n" +
		"func get(\n" +
		"    ctx?: ctx,        // optional context\n" +
		"    thing,            // the thing to read\n" +
		"    fallback = null,  // returned when absent\n" +
		") -> any\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	help := HelpFunc(res, nil)

	got, err := help.Call([]cty.Value{cty.StringVal("get")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	want := "get(ctx?: ctx, thing, fallback = null) -> any\n\n" +
		"Read a value from a thing.\n\n" +
		"Parameters:\n" +
		"  ctx?      optional context\n" +
		"  thing     the thing to read\n" +
		"  fallback  returned when absent"
	if got.AsString() != want {
		t.Fatalf("help(\"get\") =\n%q\nwant\n%q", got.AsString(), want)
	}
}

// The extern must beat the cty-metadata fallback for a name that is also in the
// eval context. That is the entire point: the host function *is* registered, and
// its cty signature is the collapsed required-plus-VarParam shape the extern exists
// to replace. If the context won the lookup, the feature would do nothing.
func TestHelpFuncExternBeatsCtyFallback(t *testing.T) {
	// A host function faking an optional argument with a VarParam — exactly the
	// pattern that cannot be rendered honestly from cty metadata.
	hostGet := function.New(&function.Spec{
		Description: "Host-registered get.",
		Params:      []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam:    &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:        function.StaticReturnType(cty.DynamicPseudoType),
		Impl:        func([]cty.Value, cty.Type) (cty.Value, error) { return cty.NullVal(cty.DynamicPseudoType), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{"get": hostGet}}

	src := "//functy:extern\n\n// The real signature.\nfunc get(ctx?: ctx, thing) -> any\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	help := HelpFunc(res, func() *hcl.EvalContext { return ctx })

	got, err := help.Call([]cty.Value{cty.StringVal("get")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	if !strings.HasPrefix(got.AsString(), "get(ctx?: ctx, thing) -> any") {
		t.Fatalf("the cty fallback shadowed the extern:\n%s", got.AsString())
	}
}

// An extern is by definition absent from the eval context under its own machinery,
// so the no-arg listing has to union the declarations in unconditionally — anything
// weaker drops externs the moment a context exists, which in the CLI is always.
func TestHelpFuncNoArgIncludesExterns(t *testing.T) {
	stub := function.New(&function.Spec{
		Type: function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{"upper": stub}}

	res, diags := NewParser().Parse([]byte("//functy:extern\n\nfunc parsetime(s: string) -> string\n"), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	help := HelpFunc(res, func() *hcl.EvalContext { return ctx })

	got, err := help.Call(nil)
	if err != nil {
		t.Fatalf("help() call: %s", err)
	}
	if got.AsString() != "parsetime\nupper" {
		t.Fatalf("help() = %q, want the extern unioned with the context", got.AsString())
	}
}

func TestHelpFuncNoArgListsFunctions(t *testing.T) {
	// help() with no argument returns the sorted, newline-separated names of every
	// function in the assembled context (host- and functy-defined alike).
	stub := function.New(&function.Spec{
		Type: function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{
		"upper": stub, "add": stub, "cond": stub,
	}}
	help := HelpFunc(nil, func() *hcl.EvalContext { return ctx })

	got, err := help.Call(nil)
	if err != nil {
		t.Fatalf("help() call: %s", err)
	}
	if got.AsString() != "add\ncond\nupper" {
		t.Fatalf("help() = %q, want sorted names", got.AsString())
	}
}
