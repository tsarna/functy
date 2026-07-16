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

// Two namespaces in one unit each declaring `const greeting` do not collide: each
// namespace's function resolves its own const, and each projects a distinct
// symbols.<label>.greeting. On the flat pre-namespaced evaluator this failed with a
// duplicate-declaration diagnostic.
func TestBuild_NamespaceConstsDoNotCollide(t *testing.T) {
	built, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(
			SymbolsBlock{Label: "foo", Source: "nsconst", Namespace: "foo"},
			SymbolsBlock{Label: "bar", Source: "nsconst", Namespace: "bar"},
		).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	for _, tc := range []struct{ fn, want string }{
		{"symbols::foo::greet", "hello world"},
		{"symbols::bar::greet", "goodbye world"},
	} {
		fn, ok := built.Functions[tc.fn]
		if !ok {
			t.Fatalf("missing %s, got %v", tc.fn, keys(built.Functions))
		}
		got, err := fn.Call([]cty.Value{cty.StringVal("world")})
		if err != nil {
			t.Fatalf("calling %s: %s", tc.fn, err)
		}
		if got != cty.StringVal(tc.want) {
			t.Errorf("%s(\"world\") = %#v, want %q", tc.fn, got, tc.want)
		}
	}

	// Each label projects its own greeting.
	for _, tc := range []struct{ label, want string }{
		{"foo", "hello"},
		{"bar", "goodbye"},
	} {
		obj := built.Symbols.GetAttr(tc.label)
		if got := obj.GetAttr("greeting"); got != cty.StringVal(tc.want) {
			t.Errorf("symbols.%s.greeting = %#v, want %q", tc.label, got, tc.want)
		}
	}
}
