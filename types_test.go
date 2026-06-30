package functy

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// TestResolverTypeexprParity proves the hand-written resolver produces the same
// cty.Type as ext/typeexpr for the pure built-in grammar. typeexpr is used here
// only as a test oracle — functy does not depend on it at runtime.
func TestResolverTypeexprParity(t *testing.T) {
	annotations := []string{
		"string", "bool", "number", "any",
		"list(string)", "set(number)", "map(bool)",
		"list(map(string))",
		"tuple([string, number, bool])",
		"object({ a = string, b = number })",
		"object({ a = string, b = optional(number) })",
		"object({ items = list(object({ id = string })) })",
	}
	env := newTypeEnv()
	for _, ann := range annotations {
		expr, diags := hclsyntax.ParseExpression([]byte(ann), "ann", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatalf("%s: parse: %s", ann, diags.Error())
		}
		want, wdiags := typeexpr.TypeConstraint(expr)
		if wdiags.HasErrors() {
			t.Fatalf("%s: typeexpr: %s", ann, wdiags.Error())
		}
		tc, rdiags := env.resolveType(expr, false)
		if rdiags.HasErrors() {
			t.Fatalf("%s: resolver: %s", ann, rdiags.Error())
		}
		if !tc.Cty().Equals(want) {
			t.Errorf("%s: resolver gave %s, typeexpr gave %s", ann, tc.Cty().FriendlyName(), want.FriendlyName())
		}
	}
}

// compileWith parses and compiles src with a preconfigured parser (e.g. one with
// registered named types), wiring a late-bound context with the stdlib.
func compileWith(t *testing.T, p *Parser, src string) map[string]function.Function {
	t.Helper()
	res, diags := p.Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile errors:\n%s", cdiags.Error())
	}
	all := testStdlib()
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	return funcs
}

type widget struct{ id string }

// TestIdentityNamedType registers a capsule type and checks identity enforcement.
func TestIdentityNamedType(t *testing.T) {
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	wval := cty.CapsuleVal(wty, &widget{id: "a"})

	p := NewParser().RegisterType("widget", wty)
	funcs := compileWith(t, p, `func id(w: widget) { return w }`)

	// A widget passes through by identity.
	got, err := funcs["id"].Call([]cty.Value{wval})
	if err != nil {
		t.Fatalf("widget arg: %v", err)
	}
	if !got.RawEquals(wval) {
		t.Fatalf("expected the same widget back, got %#v", got)
	}

	// null is accepted.
	if _, err := funcs["id"].Call([]cty.Value{cty.NullVal(wty)}); err != nil {
		t.Fatalf("null widget: %v", err)
	}

	// A value of a different type is rejected.
	if _, err := funcs["id"].Call([]cty.Value{cty.NumberIntVal(1)}); err == nil {
		t.Fatalf("expected a type error passing a number where a widget is required")
	}
}

// TestPredicateOpenType registers an open type (object with at least a string
// message) and checks non-destructive pass-through.
func TestPredicateOpenType(t *testing.T) {
	pred := func(v cty.Value) error {
		ty := v.Type()
		if !ty.IsObjectType() || !ty.HasAttribute("message") {
			return fmt.Errorf("expected an object with a message attribute")
		}
		if v.GetAttr("message").Type() != cty.String {
			return fmt.Errorf("message must be a string")
		}
		return nil
	}
	p := NewParser().RegisterOpenType("rec", pred)
	funcs := compileWith(t, p, `func message(e: rec) -> string { return e.message }`)

	// An object with message plus extra attributes passes; extras are preserved
	// (the value flows in untouched, so the body can read message).
	val := cty.ObjectVal(map[string]cty.Value{
		"message": cty.StringVal("hi"),
		"code":    cty.NumberIntVal(42),
	})
	got, err := funcs["message"].Call([]cty.Value{val})
	if err != nil {
		t.Fatalf("open-type arg: %v", err)
	}
	wantStr(t, got, "hi")

	// An object missing message is rejected.
	bad := cty.ObjectVal(map[string]cty.Value{"note": cty.StringVal("x")})
	if _, err := funcs["message"].Call([]cty.Value{bad}); err == nil {
		t.Fatalf("expected rejection of an object without a message")
	}
}

func TestNullReturnType(t *testing.T) {
	// A void function returns null on an explicit `return null`, a bare `return`,
	// and fall-through.
	funcs := compileFuncs(t, `func a() -> null { return null }
func b() -> null { return }
func c() -> null { var x = 1 }`)
	for _, name := range []string{"a", "b", "c"} {
		if !call(t, funcs, name).IsNull() {
			t.Fatalf("%s: a -> null function must return null", name)
		}
	}
}

func TestVoidReturnRejectsValue(t *testing.T) {
	// Returning a non-null value from a -> null function is a compile-time error.
	parseErr(t, `func f() -> null { return 5 }`)
}

func TestNullNotAllowedAsDeclType(t *testing.T) {
	parseErr(t, "func f() { var x: null = 1 }")
	parseErr(t, "func f(a: null) { return a }")
}

func TestUnknownTypeIsError(t *testing.T) {
	parseErr(t, "func f(a: widget) { return a }") // widget not registered on a default parser
}

func TestNestedCapsuleType(t *testing.T) {
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	w1 := cty.CapsuleVal(wty, &widget{id: "a"})
	w2 := cty.CapsuleVal(wty, &widget{id: "b"})
	p := NewParser().RegisterType("widget", wty)

	// A capsule type composes inside collections and structural types, enforced
	// element-wise by identity.
	funcs := compileWith(t, p, `func ids(ws: list(widget)) { return ws }
func pick(o: object({ w = widget })) { return o.w }`)

	got, err := funcs["ids"].Call([]cty.Value{cty.ListVal([]cty.Value{w1, w2})})
	if err != nil {
		t.Fatalf("list(widget) should be accepted: %v", err)
	}
	if got.LengthInt() != 2 {
		t.Fatalf("expected 2 widgets, got %d", got.LengthInt())
	}

	// A list element of the wrong type is rejected.
	bad := cty.TupleVal([]cty.Value{w1, cty.NumberIntVal(1)})
	if _, err := funcs["ids"].Call([]cty.Value{bad}); err == nil {
		t.Fatalf("a non-widget element should be rejected")
	}

	// Nested in an object attribute.
	o := cty.ObjectVal(map[string]cty.Value{"w": w1})
	gw, err := funcs["pick"].Call([]cty.Value{o})
	if err != nil {
		t.Fatalf("object({ w = widget }) should be accepted: %v", err)
	}
	if !gw.RawEquals(w1) {
		t.Fatalf("expected the widget back")
	}
}
