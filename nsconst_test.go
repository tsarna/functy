package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// compileNSConsts parses sources with top-level const enabled, compiles them, and
// evaluates their consts per namespace under the own+global policy (the way a host
// does), returning the full Compiled so a test can call functions and inspect the
// per-namespace variable tables (Compiled.Vars).
func compileNSConsts(t *testing.T, sources ...Source) *Compiled {
	t.Helper()
	res, diags := NewParser().AllowTopLevelConst(true).ParseAll(sources)
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	compiled, diags := res.CompileUnits(func() *hcl.EvalContext { return ctx })
	if diags.HasErrors() {
		t.Fatalf("compile errors:\n%s", diags.Error())
	}
	all := testStdlib()
	for k, v := range compiled.Funcs {
		all[k] = v
	}
	// The global variable scope is the host's to create (Compiled.Vars starts empty);
	// point the shared context at it so global consts land there and namespaced
	// bodies see them, exactly as cmd/functy and symbols do.
	if compiled.Vars[""] == nil {
		compiled.Vars[""] = map[string]cty.Value{}
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: compiled.Vars[""]}
	if diags := EvalNamespacedDecls(res.Consts, ctx, compiled); diags.HasErrors() {
		t.Fatalf("EvalNamespacedDecls errors:\n%s", diags.Error())
	}
	return compiled
}

// Two namespaces may each declare `const greeting`; each namespace's body resolves
// its own by bare name, and the two consts land in distinct Compiled.Vars tables.
// On the flat pre-namespaced evaluator this collided as a duplicate declaration.
func TestNamespaceConstNoCollision(t *testing.T) {
	c := compileNSConsts(t,
		src("foo.cty", `
			namespace foo
			const greeting = "hello"
			func greet(who: string) -> string { return "${greeting} ${who}" }
		`),
		src("bar.cty", `
			namespace bar
			const greeting = "goodbye"
			func greet(who: string) -> string { return "${greeting} ${who}" }
		`),
	)

	if got := call(t, c.Funcs, "foo::greet", cty.StringVal("world")); !got.RawEquals(cty.StringVal("hello world")) {
		t.Errorf("foo::greet = %#v, want \"hello world\"", got)
	}
	if got := call(t, c.Funcs, "bar::greet", cty.StringVal("world")); !got.RawEquals(cty.StringVal("goodbye world")) {
		t.Errorf("bar::greet = %#v, want \"goodbye world\"", got)
	}

	if got := c.Vars["foo"]["greeting"]; !got.RawEquals(cty.StringVal("hello")) {
		t.Errorf("Vars[foo].greeting = %#v, want \"hello\"", got)
	}
	if got := c.Vars["bar"]["greeting"]; !got.RawEquals(cty.StringVal("goodbye")) {
		t.Errorf("Vars[bar].greeting = %#v, want \"goodbye\"", got)
	}
}

// A namespaced body sees a global (unnamespaced) const by bare name (own+global).
func TestNamespaceConstSeesGlobalConst(t *testing.T) {
	c := compileNSConsts(t,
		src("g.cty", `const tld = "com"`),
		src("foo.cty", `
			namespace foo
			func host(name: string) -> string { return "${name}.${tld}" }
		`),
	)
	if got := call(t, c.Funcs, "foo::host", cty.StringVal("example")); !got.RawEquals(cty.StringVal("example.com")) {
		t.Errorf("foo::host = %#v, want \"example.com\"", got)
	}
}

// A namespace const shadows a global const of the same name inside the namespace's
// bodies; the global body still sees the global value.
func TestNamespaceConstShadowsGlobal(t *testing.T) {
	c := compileNSConsts(t,
		src("g.cty", `
			const label = "global"
			func here() -> string { return label }
		`),
		src("foo.cty", `
			namespace foo
			const label = "foo-local"
			func here() -> string { return label }
		`),
	)
	if got := call(t, c.Funcs, "here"); !got.RawEquals(cty.StringVal("global")) {
		t.Errorf("here() = %#v, want \"global\"", got)
	}
	if got := call(t, c.Funcs, "foo::here"); !got.RawEquals(cty.StringVal("foo-local")) {
		t.Errorf("foo::here() = %#v, want \"foo-local\" (namespace const should shadow global)", got)
	}
}

// A name declared twice within one namespace is still a duplicate, even across files.
func TestNamespaceConstDuplicateWithinNamespace(t *testing.T) {
	res, diags := NewParser().AllowTopLevelConst(true).ParseAll([]Source{
		src("a.cty", "namespace foo\nconst x = 1"),
		src("b.cty", "namespace foo\nconst x = 2"),
	})
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	compiled, diags := res.CompileUnits(func() *hcl.EvalContext { return ctx })
	if diags.HasErrors() {
		t.Fatalf("compile errors:\n%s", diags.Error())
	}
	ctx = &hcl.EvalContext{Variables: compiled.Vars[""]}
	diags = EvalNamespacedDecls(res.Consts, ctx, compiled)
	if !diags.HasErrors() || !strings.Contains(diags.Error(), "more than once") {
		t.Fatalf("expected duplicate-declaration error within namespace, got: %s", diags.Error())
	}
}
