package symbols

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// baseFuncs is the host (stand-in for OpenTofu's) function registry the fixtures
// call by bare name: functy's language stdlib plus the go-cty builtins the
// libraries use.
func baseFuncs() map[string]function.Function {
	f := map[string]function.Function{
		"upper":  stdlib.UpperFunc,
		"length": stdlib.LengthFunc,
	}
	for k, v := range functy.Stdlib() {
		f[k] = v
	}
	return f
}

func hasSummary(diags hcl.Diagnostics, summary string) bool {
	for _, d := range diags {
		if d.Summary == summary {
			return true
		}
	}
	return false
}

func TestBuild_GlobalLibrary(t *testing.T) {
	built, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "lib", Source: "lib"}).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	// Functions: exported ones are keyed symbols::lib::name; private ones withheld.
	if _, ok := built.Functions["symbols::lib::non_empty"]; !ok {
		t.Errorf("expected symbols::lib::non_empty in Functions, got keys %v", keys(built.Functions))
	}
	if _, ok := built.Functions["symbols::lib::shout"]; !ok {
		t.Errorf("expected symbols::lib::shout in Functions")
	}
	if _, ok := built.Functions["symbols::lib::_helper"]; ok {
		t.Errorf("private function _helper must not be exported")
	}

	// The function is executable: call it through cty.
	got, err := built.Functions["symbols::lib::non_empty"].Call([]cty.Value{
		cty.ListVal([]cty.Value{cty.StringVal("a")}),
	})
	if err != nil {
		t.Fatalf("calling non_empty: %s", err)
	}
	if got != cty.True {
		t.Errorf("non_empty([\"a\"]) = %#v, want true", got)
	}

	// Symbols value: symbols.lib.<const>, private const withheld.
	lib := built.Symbols.GetAttr("lib")
	if diff := lib.GetAttr("default_items"); !diff.RawEquals(
		cty.ListVal([]cty.Value{cty.StringVal("foo"), cty.StringVal("bar"), cty.StringVal("baz")})) {
		t.Errorf("symbols.lib.default_items = %#v", diff)
	}
	// greeting = shout("hi") — a const that calls a library function.
	if g := lib.GetAttr("greeting"); g != cty.StringVal("HI") {
		t.Errorf("symbols.lib.greeting = %#v, want \"HI\"", g)
	}
	if lib.Type().HasAttribute("_secret") {
		t.Errorf("private const _secret must not be exported")
	}

	// Types: exported alias resolves; private alias absent.
	if ty, ok := built.Type("lib", "items"); !ok || !ty.Equals(cty.List(cty.String)) {
		t.Errorf("Type(lib, items) = %#v, %v; want list(string)", ty, ok)
	}
	if _, ok := built.Type("lib", "_spec"); ok {
		t.Errorf("private type _spec must not be exported")
	}
	if _, ok := built.Type("lib", "nope"); ok {
		t.Errorf("Type(lib, nope) should not resolve")
	}
}

func TestBuild_NamespaceProjection(t *testing.T) {
	built, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "net", Source: "nslib", Namespace: "acme::net"}).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}

	// The functy namespace is replaced by the label: acme::net::mask -> symbols::net::mask.
	if _, ok := built.Functions["symbols::net::mask"]; !ok {
		t.Errorf("expected symbols::net::mask, got %v", keys(built.Functions))
	}
	net := built.Symbols.GetAttr("net")
	if c := net.GetAttr("default_cidr"); c != cty.StringVal("10.0.0.0/8") {
		t.Errorf("symbols.net.default_cidr = %#v", c)
	}
}

func TestBuild_WrongNamespaceIsEmpty(t *testing.T) {
	// Binding the global surface of a namespaced library yields nothing.
	built, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "net", Source: "nslib"}).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}
	if len(built.Functions) != 0 {
		t.Errorf("expected no functions for the global surface, got %v", keys(built.Functions))
	}
	if attrs := built.Symbols.GetAttr("net").Type().AttributeTypes(); len(attrs) != 0 {
		t.Errorf("expected no consts for the global surface, got %v", attrs)
	}
}

func TestBuild_DuplicateLabel(t *testing.T) {
	_, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(
			SymbolsBlock{Label: "lib", Source: "lib"},
			SymbolsBlock{Label: "lib", Source: "lib"},
		).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected a duplicate-label error")
	}
	if !hasSummary(diags, "Duplicate symbols label") {
		t.Errorf("expected Duplicate symbols label, got %s", diags)
	}
}

func TestBuild_MissingSource(t *testing.T) {
	_, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "lib", Source: "does-not-exist"}).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected an error for a missing source")
	}
}

func TestBuild_TopLevelVarRejected(t *testing.T) {
	_, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "bad", Source: "badvar"}).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected a parse error for a top-level var")
	}
}

func TestBuild_SharedSourceTwoLabels(t *testing.T) {
	// Two labels on the same source: the unit is parsed once (cached by path) and
	// projected under each label.
	built, diags := NewBuilder().
		WithBaseDir("testdata").
		WithBaseFunctions(baseFuncs()).
		WithBlocks(
			SymbolsBlock{Label: "a", Source: "lib"},
			SymbolsBlock{Label: "b", Source: "lib"},
		).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}
	if _, ok := built.Functions["symbols::a::non_empty"]; !ok {
		t.Errorf("missing symbols::a::non_empty")
	}
	if _, ok := built.Functions["symbols::b::non_empty"]; !ok {
		t.Errorf("missing symbols::b::non_empty")
	}
	if _, ok := built.Type("a", "items"); !ok {
		t.Errorf("missing Type(a, items)")
	}
	if _, ok := built.Type("b", "items"); !ok {
		t.Errorf("missing Type(b, items)")
	}
}

func keys(m map[string]function.Function) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
