package functy

import (
	"reflect"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestTypeResolverParseTypeBuiltin(t *testing.T) {
	r := NewTypeResolver()
	tc, diags := r.ParseType([]byte("list(string)"), "ann")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	if !tc.Cty().Equals(cty.List(cty.String)) {
		t.Fatalf("Cty() = %s, want list(string)", tc.Cty().FriendlyName())
	}
	// Coerce enforces/converts: a tuple of numbers becomes a list of strings.
	got, err := tc.Coerce(cty.TupleVal([]cty.Value{cty.NumberIntVal(1), cty.NumberIntVal(2)}))
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}
	if got.LengthInt() != 2 || !got.Index(cty.NumberIntVal(0)).RawEquals(cty.StringVal("1")) {
		t.Fatalf("coerce result wrong: %#v", got)
	}
}

func TestTypeResolverParseNumberCoercion(t *testing.T) {
	r := NewTypeResolver()
	tc, diags := r.ParseType([]byte("number"), "ann")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	got, err := tc.Coerce(cty.StringVal("12"))
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}
	if !got.RawEquals(cty.NumberIntVal(12)) {
		t.Fatalf("coerce(\"12\") = %#v, want 12", got)
	}
}

func TestTypeResolverNamedAndNested(t *testing.T) {
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	w := cty.CapsuleVal(wty, &widget{id: "a"})
	r := NewTypeResolver().RegisterType("widget", wty)

	tc, diags := r.ParseType([]byte("widget"), "ann")
	if diags.HasErrors() {
		t.Fatalf("widget: %s", diags.Error())
	}
	if _, err := tc.Coerce(w); err != nil {
		t.Fatalf("a widget should satisfy widget: %v", err)
	}
	if _, err := tc.Coerce(cty.NumberIntVal(1)); err == nil {
		t.Fatalf("a number should not satisfy widget")
	}

	// Capsule types nest.
	lt, diags := r.ParseType([]byte("list(widget)"), "ann")
	if diags.HasErrors() {
		t.Fatalf("list(widget): %s", diags.Error())
	}
	if _, err := lt.Coerce(cty.ListVal([]cty.Value{w})); err != nil {
		t.Fatalf("list(widget) should accept a list of widgets: %v", err)
	}
}

func TestTypeResolverErrorBuiltinAvailable(t *testing.T) {
	r := NewTypeResolver()
	tc, diags := r.ParseType([]byte("error"), "ann")
	if diags.HasErrors() {
		t.Fatalf("error type should be built in: %s", diags.Error())
	}
	errVal := cty.ObjectVal(map[string]cty.Value{"message": cty.StringVal("x")})
	if _, err := tc.Coerce(errVal); err != nil {
		t.Fatalf("error-shaped value should satisfy error: %v", err)
	}
	if _, err := tc.Coerce(cty.NumberIntVal(1)); err == nil {
		t.Fatalf("a number should not satisfy error")
	}
}

func TestTypeResolverNullRejected(t *testing.T) {
	_, diags := NewTypeResolver().ParseType([]byte("null"), "ann")
	if !diags.HasErrors() {
		t.Fatalf("null should not be resolvable as a value type")
	}
}

func TestTypeResolverUnknownType(t *testing.T) {
	_, diags := NewTypeResolver().ParseType([]byte("nope"), "ann")
	if !diags.HasErrors() {
		t.Fatalf("an unknown type should error")
	}
}

func TestParserTypesSharesRegistrations(t *testing.T) {
	// Types registered on the Parser are visible to its TypeResolver, so a host
	// registers once and uses both surfaces.
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	p := NewParser().RegisterType("widget", wty)
	tc, diags := p.Types().ParseType([]byte("widget"), "ann")
	if diags.HasErrors() {
		t.Fatalf("parser-registered type should resolve via Types(): %s", diags.Error())
	}
	if !tc.Cty().Equals(wty) {
		t.Fatalf("Cty() = %s, want widget capsule", tc.Cty().FriendlyName())
	}
}

func TestConvertType(t *testing.T) {
	tc := ConvertType(cty.Number)
	if !tc.Cty().Equals(cty.Number) {
		t.Fatalf("Cty() = %s, want number", tc.Cty().FriendlyName())
	}
	got, err := tc.Coerce(cty.StringVal("5"))
	if err != nil {
		t.Fatalf("coerce: %v", err)
	}
	if !got.RawEquals(cty.NumberIntVal(5)) {
		t.Fatalf("ConvertType(number).Coerce(\"5\") = %#v, want 5", got)
	}
}
