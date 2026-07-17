package functy

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
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

// --- namespace-scoped aliases ---

// A namespaced alias resolves for that namespace's own annotations.
func TestAliasNamespaceScopedResolves(t *testing.T) {
	c := compileNS(t, src("m.cty", `
		namespace foo
		type Id = number
		func f(x: Id) -> number { return x + 1 }
	`))
	// "5" is coerced to number by the pinned Id parameter, proving Id == number.
	if got := call(t, c.Funcs, "foo::f", cty.StringVal("5")); !got.RawEquals(num(6)) {
		t.Errorf("foo::f(\"5\") = %#v, want 6", got)
	}
}

// Two namespaces may each declare the same alias name with different types; each
// namespace's annotations enforce its own.
func TestAliasSameNameDifferentNamespaces(t *testing.T) {
	c := compileNS(t,
		src("foo.cty", "namespace foo\ntype T = number\nfunc f(x: T) { return x }"),
		src("bar.cty", "namespace bar\ntype T = string\nfunc g(x: T) { return x }"),
	)
	if got := call(t, c.Funcs, "foo::f", cty.StringVal("5")); !got.RawEquals(num(5)) {
		t.Errorf("foo::f(\"5\") = %#v, want number 5 (foo's T = number)", got)
	}
	if got := call(t, c.Funcs, "bar::g", num(5)); !got.RawEquals(cty.StringVal("5")) {
		t.Errorf("bar::g(5) = %#v, want string \"5\" (bar's T = string)", got)
	}
}

// An alias declared in one namespace is invisible from another (no global one).
func TestAliasNamespaceIsolation(t *testing.T) {
	sources := []Source{
		{Filename: "foo.cty", Bytes: []byte("namespace foo\ntype Secret = number")},
		{Filename: "bar.cty", Bytes: []byte("namespace bar\nfunc g(x: Secret) { return x }")},
	}
	_, diags := NewParser().ParseAll(sources)
	if !diags.HasErrors() {
		t.Fatal("a foo alias must not be visible from namespace bar")
	}
}

// A namespaced alias may shadow a global alias (own-then-global, own wins) without
// a duplicate error.
func TestAliasNamespaceShadowsGlobalAlias(t *testing.T) {
	c := compileNS(t,
		src("global.cty", "type T = string\nfunc h(x: T) -> string { return x }"),
		src("foo.cty", "namespace foo\ntype T = number\nfunc f(x: T) { return x }"),
	)
	if got := call(t, c.Funcs, "h", num(5)); !got.RawEquals(cty.StringVal("5")) {
		t.Errorf("h(5) = %#v, want string \"5\" (global T = string)", got)
	}
	if got := call(t, c.Funcs, "foo::f", cty.StringVal("5")); !got.RawEquals(num(5)) {
		t.Errorf("foo::f(\"5\") = %#v, want number 5 (foo's T shadows global)", got)
	}
}

// A namespaced function falls back to a global alias when it has none of its own.
func TestAliasNamespaceFallsBackToGlobal(t *testing.T) {
	c := compileNS(t,
		src("global.cty", "type Id = number"),
		src("foo.cty", "namespace foo\nfunc f(x: Id) -> number { return x + 1 }"),
	)
	if got := call(t, c.Funcs, "foo::f", cty.StringVal("5")); !got.RawEquals(num(6)) {
		t.Errorf("foo::f(\"5\") = %#v, want 6 (Id resolves to the global alias)", got)
	}
}

// Shadowing a host-registered type inside a namespace is allowed but warns.
func TestAliasNamespaceShadowsHostTypeWarns(t *testing.T) {
	wty := cty.Capsule("widget", reflect.TypeOf(widget{}))
	p := NewParser().RegisterType("widget", wty)
	res, diags := p.ParseAll([]Source{
		{Filename: "foo.cty", Bytes: []byte("namespace foo\ntype widget = number\nfunc f(x: widget) { return x }")},
	})
	if diags.HasErrors() {
		t.Fatalf("shadowing a host type in a namespace should be allowed: %s", diags.Error())
	}
	var warned bool
	for _, d := range diags {
		if d.Severity == hcl.DiagWarning && strings.Contains(d.Summary, "shadows a host-registered type") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("expected a host-type-shadow warning, got: %s", diags.Error())
	}
	// The alias took effect: it is materialized under namespace foo.
	var found bool
	for i := range res.Types {
		if res.Types[i].Name == "widget" && res.Types[i].Namespace == "foo" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected foo's widget alias in Result.Types, got %+v", res.Types)
	}
}

// --- private aliases ---

// `type _` (the blank identifier) is rejected; `type _spec` (private) is fine and
// still inlines into an exported alias.
func TestAliasBlankIdentifierRejected(t *testing.T) {
	parseErr(t, "type _ = number")
}

func TestAliasPrivateResolvesAndInlines(t *testing.T) {
	res := parse(t, `type _spec = object({ id = string })
type items = list(_spec)`)
	byName := map[string]TypeAlias{}
	for i := range res.Types {
		byName[res.Types[i].Name] = res.Types[i]
	}
	spec, ok := byName["_spec"]
	if !ok {
		t.Fatal("_spec should be materialized in Result.Types")
	}
	if !spec.IsPrivate() {
		t.Error("_spec should report IsPrivate() == true")
	}
	if items := byName["items"]; items.IsPrivate() {
		t.Error("items should not be private")
	} else if !items.Type.Cty().IsListType() {
		t.Errorf("items should resolve to a list type, got %s", items.Type.Cty().FriendlyName())
	}
}

// Namespaced aliases are stamped with their namespace in Result.Types.
func TestAliasResultTypesCarryNamespace(t *testing.T) {
	res, diags := NewParser().ParseAll([]Source{
		src("foo.cty", "namespace foo\ntype Id = string"),
		src("g.cty", "type Gid = number"),
	})
	if diags.HasErrors() {
		t.Fatalf("unexpected errors: %s", diags.Error())
	}
	ns := map[string]string{}
	for i := range res.Types {
		ns[res.Types[i].Name] = res.Types[i].Namespace
	}
	if ns["Id"] != "foo" {
		t.Errorf("Id.Namespace = %q, want foo", ns["Id"])
	}
	if ns["Gid"] != "" {
		t.Errorf("Gid.Namespace = %q, want global", ns["Gid"])
	}
}

// ensure the in-function `type` identifier is still usable (contextual keyword).
func TestTypeUsableAsLocalName(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> string {
    var type = "ok"
    return type
}`)
	wantStr(t, call(t, funcs, "f"), "ok")
}
