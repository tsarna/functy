package symbols

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

// Can a function reference a const declared in its own namespace by bare name?
func TestBuild_FunctionSeesNamespaceConst(t *testing.T) {
	built, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "foo", Source: "nsconst", Namespace: "foo"}).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}
	fn, ok := built.Functions["symbols::foo::greet"]
	if !ok {
		t.Fatalf("missing symbols::foo::greet, got %v", keys(built.Functions))
	}
	got, err := fn.Call([]cty.Value{cty.StringVal("world")})
	if err != nil {
		t.Fatalf("calling greet: %s", err)
	}
	if got != cty.StringVal("hello world") {
		t.Errorf("greet(\"world\") = %#v, want \"hello world\"", got)
	}
}
