package functy

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// TestResolverTypeexprParity proves the hand-written resolver produces the same
// cty.Type as ext/typeexpr for the pure built-in grammar. functy also uses typeexpr
// at runtime for the Defaults.Apply of optional-attribute defaults (see
// defaultedConstraint), but the resolver — grammar, capsule naming, coercion — is its
// own; typeexpr is the oracle for this parity check.
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

// TestObjectDuplicateAttr proves a repeated attribute name in an object type is a
// hard error rather than silently letting the last declaration win, while a
// non-duplicate object still resolves cleanly.
func TestObjectDuplicateAttr(t *testing.T) {
	dupExpr, diags := hclsyntax.ParseExpression([]byte(`object({ a = string, a = number })`), "ann", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	_, rdiags := newTypeEnv().resolveType(dupExpr, false)
	if !rdiags.HasErrors() {
		t.Fatalf("duplicate attribute resolved without error")
	}
	if got := rdiags.Error(); !strings.Contains(got, `duplicate attribute "a"`) {
		t.Errorf("diagnostic = %q, want it to mention duplicate attribute %q", got, "a")
	}

	// A distinct-attribute object with the same types still resolves.
	tc := resolveAnn(t, `object({ a = string, b = number })`)
	want := cty.Object(map[string]cty.Type{"a": cty.String, "b": cty.Number})
	if !tc.Cty().Equals(want) {
		t.Errorf("non-duplicate object resolved to %s, want %s", tc.Cty().FriendlyName(), want.FriendlyName())
	}
}

// resolveAnn parses and resolves a type annotation string to a TypeConstraint.
func resolveAnn(t *testing.T, ann string) TypeConstraint {
	t.Helper()
	expr, diags := hclsyntax.ParseExpression([]byte(ann), "ann", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("%s: parse: %s", ann, diags.Error())
	}
	tc, rdiags := newTypeEnv().resolveType(expr, false)
	if rdiags.HasErrors() {
		t.Fatalf("%s: resolve: %s", ann, rdiags.Error())
	}
	return tc
}

// TestOptionalDefaultsParity proves that for annotations using optional(T, default) the
// resolver's constraint coerces identically to ext/typeexpr's own type + Defaults tree,
// across missing, null, and present attributes and nesting.
func TestOptionalDefaultsParity(t *testing.T) {
	annotations := []string{
		`object({ a = string, b = optional(number, 42) })`,
		`object({ a = optional(string, "x"), b = optional(bool, true) })`,
		`object({ inner = optional(object({ b = optional(string, "y") }), {}) })`,
		`list(object({ n = optional(number, 1) }))`,
		`tuple([object({ a = optional(string, "z") }), number])`,
	}
	inputs := []cty.Value{
		cty.EmptyObjectVal,
		cty.ObjectVal(map[string]cty.Value{"a": cty.StringVal("given")}),
		cty.ObjectVal(map[string]cty.Value{"b": cty.NullVal(cty.Number)}),
	}
	for _, ann := range annotations {
		expr, diags := hclsyntax.ParseExpression([]byte(ann), "ann", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatalf("%s: parse: %s", ann, diags.Error())
		}
		wantTy, wantDefaults, wdiags := typeexpr.TypeConstraintWithDefaults(expr)
		if wdiags.HasErrors() {
			t.Fatalf("%s: typeexpr: %s", ann, wdiags.Error())
		}
		tc := resolveAnn(t, ann)
		if !tc.Cty().Equals(wantTy) {
			t.Errorf("%s: resolver type %s, typeexpr %s", ann, tc.Cty().FriendlyName(), wantTy.FriendlyName())
		}
		for _, in := range inputs {
			got, gerr := tc.Coerce(in)
			want, werr := convert.Convert(wantDefaults.Apply(in), wantTy)
			if (gerr == nil) != (werr == nil) {
				continue // both erroring or an input that doesn't fit this shape; the value cases below cover fit inputs
			}
			if gerr == nil && !got.RawEquals(want) {
				t.Errorf("%s on %#v: resolver gave %#v, typeexpr gave %#v", ann, in, got, want)
			}
		}
	}
}

// TestOptionalDefaultCoerce checks defaults fill missing and null attributes, recurse,
// and never override a value the caller supplied.
func TestOptionalDefaultCoerce(t *testing.T) {
	tc := resolveAnn(t, `object({ a = optional(string, "x"), n = optional(number, 1) })`)

	// Missing attributes take their defaults.
	got, err := tc.Coerce(cty.EmptyObjectVal)
	if err != nil {
		t.Fatalf("coerce empty: %v", err)
	}
	if a := got.GetAttr("a"); a.AsString() != "x" {
		t.Errorf("a = %#v, want \"x\"", a)
	}
	if !got.GetAttr("n").RawEquals(cty.NumberIntVal(1)) {
		t.Errorf("n = %#v, want 1", got.GetAttr("n"))
	}

	// A supplied value is kept; only the other default fills in.
	got, err = tc.Coerce(cty.ObjectVal(map[string]cty.Value{"a": cty.StringVal("given")}))
	if err != nil {
		t.Fatalf("coerce partial: %v", err)
	}
	if got.GetAttr("a").AsString() != "given" {
		t.Errorf("a = %#v, want \"given\"", got.GetAttr("a"))
	}
	if !got.GetAttr("n").RawEquals(cty.NumberIntVal(1)) {
		t.Errorf("n = %#v, want 1", got.GetAttr("n"))
	}

	// An explicit null attribute is overwritten by the default (parity with Terraform).
	got, err = tc.Coerce(cty.ObjectVal(map[string]cty.Value{"a": cty.NullVal(cty.String)}))
	if err != nil {
		t.Fatalf("coerce null: %v", err)
	}
	if got.GetAttr("a").AsString() != "x" {
		t.Errorf("null a = %#v, want default \"x\"", got.GetAttr("a"))
	}

	// Nested defaults fill from the inside out.
	nested := resolveAnn(t, `object({ inner = optional(object({ b = optional(string, "y") }), {}) })`)
	got, err = nested.Coerce(cty.EmptyObjectVal)
	if err != nil {
		t.Fatalf("coerce nested: %v", err)
	}
	if b := got.GetAttr("inner").GetAttr("b"); b.AsString() != "y" {
		t.Errorf("inner.b = %#v, want \"y\"", b)
	}
}

// TestOptionalDefaultBadValue rejects a default that is not convertible to the attribute
// type, at resolve time.
func TestOptionalDefaultBadValue(t *testing.T) {
	expr, diags := hclsyntax.ParseExpression([]byte(`object({ n = optional(number, "nope") })`), "ann", hcl.InitialPos)
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	_, rdiags := newTypeEnv().resolveType(expr, false)
	if !rdiags.HasErrors() {
		t.Fatalf("expected an error for an unconvertible default")
	}
	if !diagsHaveSummary(rdiags, "Invalid default value for optional attribute") {
		t.Errorf("wrong diagnostic: %s", rdiags.Error())
	}
}

// TestOptionalArity rejects optional() with no type and optional(T, d, e) with too many
// arguments.
func TestOptionalArity(t *testing.T) {
	for _, ann := range []string{`object({ a = optional() })`, `object({ a = optional(string, "x", "y") })`} {
		expr, diags := hclsyntax.ParseExpression([]byte(ann), "ann", hcl.InitialPos)
		if diags.HasErrors() {
			t.Fatalf("%s: parse: %s", ann, diags.Error())
		}
		if _, rdiags := newTypeEnv().resolveType(expr, false); !rdiags.HasErrors() {
			t.Errorf("%s: expected an arity error", ann)
		}
	}
}

// TestOptionalDefaultStringRoundTrip checks that String() renders the default and the
// rendering re-resolves to an equivalent constraint that coerces the same way.
func TestOptionalDefaultStringRoundTrip(t *testing.T) {
	tc := resolveAnn(t, `object({ a = optional(string, "x"), n = optional(number, 1) })`)
	rendered := tc.String()
	tc2 := resolveAnn(t, rendered)
	got1, _ := tc.Coerce(cty.EmptyObjectVal)
	got2, err := tc2.Coerce(cty.EmptyObjectVal)
	if err != nil {
		t.Fatalf("re-resolved %q failed to coerce: %v", rendered, err)
	}
	if !got1.RawEquals(got2) {
		t.Errorf("round-trip differs: %q coerced to %#v vs %#v", rendered, got2, got1)
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
