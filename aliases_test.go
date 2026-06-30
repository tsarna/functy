package functy

import (
	"reflect"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestAliasBasic(t *testing.T) {
	funcs := compileFuncs(t, `type Pair = object({ a = number, b = number })

func add(p: Pair) -> number {
    return p.a + p.b
}`)
	got := call(t, funcs, "add", cty.ObjectVal(map[string]cty.Value{"a": num(3), "b": num(4)}))
	wantNum(t, got, 7)
}

func TestAliasForwardReference(t *testing.T) {
	// The function is declared before the alias it uses.
	parse(t, `func f(x: Id) -> string { return x }
type Id = string`)
}

func TestAliasToAlias(t *testing.T) {
	// B references A, which is declared after B.
	funcs := compileFuncs(t, `type B = list(A)
type A = number

func sum(xs: B) -> number {
    var t = 0
    for v in xs { t = t + v }
    return t
}`)
	got := call(t, funcs, "sum", cty.ListVal([]cty.Value{num(1), num(2), num(3)}))
	wantNum(t, got, 6)
}

func TestAliasCrossFile(t *testing.T) {
	// An alias declared in one source is usable by a function in another.
	sources := []Source{
		{Filename: "types.cty", Bytes: []byte(`type Id = string`)},
		{Filename: "main.cty", Bytes: []byte(`func tag(x: Id) -> string { return "id:${x}" }`)},
	}
	res, diags := NewParser().ParseAll(sources)
	if diags.HasErrors() {
		t.Fatalf("cross-file alias should resolve: %s", diags.Error())
	}
	if len(res.Types) != 1 || res.Types[0].Name != "Id" {
		t.Fatalf("expected Id in Result.Types, got %+v", res.Types)
	}
}

func TestAliasNestedConcrete(t *testing.T) {
	// A concrete alias is usable inside a collection type.
	funcs := compileFuncs(t, `type Id = string

func first(ids: list(Id)) -> string {
    for v in ids { return v }
    return ""
}`)
	got := call(t, funcs, "first", cty.ListVal([]cty.Value{cty.StringVal("x"), cty.StringVal("y")}))
	wantStr(t, got, "x")
}

func TestAliasResultTypes(t *testing.T) {
	res := parse(t, `type Id = string
type Pair = object({ a = number, b = number })`)
	if len(res.Types) != 2 {
		t.Fatalf("expected 2 aliases in Result.Types, got %d", len(res.Types))
	}
	byName := map[string]TypeAlias{}
	for _, a := range res.Types {
		byName[a.Name] = a
	}
	if byName["Id"].Type.Cty() != cty.String {
		t.Errorf("Id should resolve to string")
	}
	if !byName["Pair"].Type.Cty().IsObjectType() {
		t.Errorf("Pair should resolve to an object type")
	}
}

// --- aliases over host capsule types ---

func TestAliasOverCapsuleLeaf(t *testing.T) {
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	wval := cty.CapsuleVal(wty, &widget{id: "a"})
	p := NewParser().RegisterType("widget", wty)
	funcs := compileWith(t, p, `type W = widget

func id(w: W) { return w }`)
	got, err := funcs["id"].Call([]cty.Value{wval})
	if err != nil {
		t.Fatalf("alias over capsule (leaf) should work: %v", err)
	}
	if !got.RawEquals(wval) {
		t.Fatalf("expected the same widget back")
	}
}

func TestAliasOverCapsuleNestable(t *testing.T) {
	// An alias over a capsule type nests like the capsule itself.
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	w := cty.CapsuleVal(wty, &widget{id: "a"})
	p := NewParser().RegisterType("widget", wty)
	funcs := compileWith(t, p, `type W = widget
func first(ws: list(W)) { for v in ws { return v } return null }`)
	got, err := funcs["first"].Call([]cty.Value{cty.ListVal([]cty.Value{w})})
	if err != nil {
		t.Fatalf("alias over a capsule should nest: %v", err)
	}
	if !got.RawEquals(w) {
		t.Fatalf("expected the widget back")
	}
}

// --- error cases ---

func TestAliasShadowsBuiltin(t *testing.T) {
	parseErr(t, "type string = number")
}

func TestAliasDuplicate(t *testing.T) {
	parseErr(t, "type A = number\ntype A = string")
}

func TestAliasDuplicateCrossFile(t *testing.T) {
	sources := []Source{
		{Filename: "a.cty", Bytes: []byte(`type A = number`)},
		{Filename: "b.cty", Bytes: []byte(`type A = string`)},
	}
	_, diags := NewParser().ParseAll(sources)
	if !diags.HasErrors() {
		t.Fatalf("duplicate alias across files should be an error")
	}
}

func TestAliasCollidesWithRegisteredType(t *testing.T) {
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	p := NewParser().RegisterType("widget", wty)
	_, diags := p.Parse([]byte("type widget = number"), "test")
	if !diags.HasErrors() {
		t.Fatalf("alias colliding with a registered type should be an error")
	}
}

func TestAliasCycle(t *testing.T) {
	parseErr(t, "type A = list(A)")
	parseErr(t, "type A = list(B)\ntype B = list(A)")
}

func TestAliasNullBody(t *testing.T) {
	parseErr(t, "type N = null")
}

func TestAliasUnknownType(t *testing.T) {
	parseErr(t, "type A = nope")
}

// ensure the in-function `type` identifier is still usable (contextual keyword).
func TestTypeUsableAsLocalName(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> string {
    var type = "ok"
    return type
}`)
	wantStr(t, call(t, funcs, "f"), "ok")
}
