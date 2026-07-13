package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// compileNS parses and compiles src (or several sources) the way a host does,
// returning the full Compiled so a test can assert on both what the host is handed
// (Funcs, qualified, exported only) and what a namespace's own bodies resolve
// through (Units, bare, privates included).
func compileNS(t *testing.T, sources ...Source) *Compiled {
	t.Helper()
	res, diags := NewParser().ParseAll(sources)
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
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	return compiled
}

func src(name, body string) Source { return Source{Filename: name, Bytes: []byte(body)} }

// compileNSErr expects compilation (not parsing) to fail.
func compileNSErr(t *testing.T, sources ...Source) hcl.Diagnostics {
	t.Helper()
	res, diags := NewParser().ParseAll(sources)
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	_, diags = res.CompileUnits(func() *hcl.EvalContext { return ctx })
	if !diags.HasErrors() {
		t.Fatal("expected compile errors, got none")
	}
	return diags
}

// ---- parsing ----------------------------------------------------------------

func TestNamespaceParsedAndStamped(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"namespace foo\nfunc a() { return 1 }", "foo"},
		{"namespace foo::bar\nfunc a() { return 1 }", "foo::bar"},
		{"namespace a::b::c\nfunc a() { return 1 }", "a::b::c"},
		{"func a() { return 1 }", ""}, // no declaration -> global namespace
	} {
		res := parse(t, tc.src)
		if got := res.Funcs[0].Namespace; got != tc.want {
			t.Errorf("%q: FuncDecl.Namespace = %q, want %q", tc.src, got, tc.want)
		}
		if tc.want == "" {
			if len(res.Namespaces) != 0 {
				t.Errorf("%q: expected no NamespaceDecl, got %d", tc.src, len(res.Namespaces))
			}
			continue
		}
		if len(res.Namespaces) != 1 {
			t.Fatalf("%q: expected 1 NamespaceDecl, got %d", tc.src, len(res.Namespaces))
		}
		if got := res.Namespaces[0].Name; got != tc.want {
			t.Errorf("%q: NamespaceDecl.Name = %q, want %q", tc.src, got, tc.want)
		}
	}
}

func TestNamespaceQualifiedName(t *testing.T) {
	res := parse(t, "namespace foo::bar\nfunc baz() { return 1 }\nfunc _helper() { return 2 }")
	if got := res.Funcs[0].QualifiedName(); got != "foo::bar::baz" {
		t.Errorf("QualifiedName() = %q, want foo::bar::baz", got)
	}
	if res.Funcs[0].IsPrivate() {
		t.Error("baz should not be private")
	}
	if !res.Funcs[1].IsPrivate() {
		t.Error("_helper should be private")
	}
	if got := res.Funcs[1].QualifiedName(); got != "foo::bar::_helper" {
		t.Errorf("private QualifiedName() = %q, want foo::bar::_helper", got)
	}
}

func TestNamespaceParseErrors(t *testing.T) {
	for _, src := range []string{
		"namespace\nfunc a() { return 1 }",          // no name
		"namespace foo::\nfunc a() { return 1 }",    // trailing ::
		"namespace foo bar\nfunc a() { return 1 }",  // extra tokens
		"namespace for\nfunc a() { return 1 }",      // reserved word as a segment
		"namespace foo\nnamespace bar\nfunc a() {}", // two declarations
		"func a() { return 1 }\nnamespace foo",      // not first
		"func _() { return 1 }",                     // blank identifier
	} {
		parseErr(t, src)
	}
}

// A namespace declaration must not steal the identifier: like test/type, it is
// contextual, so `namespace` stays usable as an ordinary name everywhere else.
func TestNamespaceKeywordIsContextual(t *testing.T) {
	funcs := compileFuncs(t, `
		func namespace(n) { return n + 1 }
		func caller() {
			var namespace = 2
			return namespace(namespace)
		}
	`)
	if got := call(t, funcs, "caller"); !got.RawEquals(num(3)) {
		t.Errorf("caller() = %#v, want 3", got)
	}
}

// A misplaced namespace still resyncs the parser, so following declarations parse.
func TestNamespaceRecoversToTopLevel(t *testing.T) {
	res, diags := NewParser().Parse([]byte("func a() { return }\nnamespace foo\nfunc b() { return 2 }"), "t")
	if !diags.HasErrors() {
		t.Fatal("expected a misplaced-namespace error")
	}
	// b must still have been parsed — the outline of a mid-edit file survives.
	var names []string
	for _, fn := range res.Funcs {
		names = append(names, fn.Name)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Errorf("recovered funcs = %v, want [a b]", names)
	}
}

// ---- compile: the export / private split ------------------------------------

func TestNamespaceExportsQualifiedAndWithholdsPrivate(t *testing.T) {
	c := compileNS(t, src("m.cty", `
		namespace foo
		func a() { return _b() }
		func _b() { return 7 }
	`))
	if _, ok := c.Funcs["foo::a"]; !ok {
		t.Fatalf("expected foo::a in the host map, got keys %v", keys(c.Funcs))
	}
	for _, absent := range []string{"a", "_b", "foo::_b"} {
		if _, ok := c.Funcs[absent]; ok {
			t.Errorf("%q must not be in the host map (keys: %v)", absent, keys(c.Funcs))
		}
	}
	// The private one is reachable through the namespace's unit layer, and the
	// exported one calls it by its bare name.
	if _, ok := c.Units["foo"]["_b"]; !ok {
		t.Error("_b should be in the foo unit layer")
	}
	if got := call(t, c.Funcs, "foo::a"); !got.RawEquals(num(7)) {
		t.Errorf("foo::a() = %#v, want 7", got)
	}
}

func TestGlobalNamespacePrivate(t *testing.T) {
	c := compileNS(t, src("m.cty", `
		func a() { return _b() }
		func _b() { return 7 }
	`))
	if got := call(t, c.Funcs, "a"); !got.RawEquals(num(7)) {
		t.Errorf("a() = %#v, want 7", got)
	}
	if _, ok := c.Funcs["_b"]; ok {
		t.Error("_b must not be handed to the host, even in the global namespace")
	}
	if _, ok := c.Units[""]["_b"]; !ok {
		t.Error("_b should be in the global unit layer")
	}
}

// A qualified self-call resolves through the host map, from inside the namespace.
func TestNamespaceQualifiedSelfCall(t *testing.T) {
	c := compileNS(t, src("m.cty", `
		namespace foo
		func a() { return foo::b() }
		func b() { return 5 }
	`))
	if got := call(t, c.Funcs, "foo::a"); !got.RawEquals(num(5)) {
		t.Errorf("foo::a() = %#v, want 5", got)
	}
}

// The unit layer ADDS names; it must not hide the host's library.
func TestNamespaceStillSeesHostFunctions(t *testing.T) {
	c := compileNS(t, src("m.cty", `
		namespace foo
		func shout(s) { return upper(s) }
	`))
	if got := call(t, c.Funcs, "foo::shout", cty.StringVal("hi")); !got.RawEquals(cty.StringVal("HI")) {
		t.Errorf("foo::shout(\"hi\") = %#v, want \"HI\"", got)
	}
}

// Nesting is a naming convention, NOT containment: foo::bar gets no special
// visibility into foo. A bare call to a foo function from foo::bar must fail.
func TestNamespaceHasNoParentFallback(t *testing.T) {
	c := compileNS(t,
		src("parent.cty", "namespace foo\nfunc helper() { return 1 }"),
		src("child.cty", "namespace foo::bar\nfunc a() { return helper() }"),
	)
	callErr(t, c.Funcs, "foo::bar::a")
	// ...but the fully-qualified spelling works, exactly as for any unrelated namespace.
	c2 := compileNS(t,
		src("parent.cty", "namespace foo\nfunc helper() { return 1 }"),
		src("child.cty", "namespace foo::bar\nfunc a() { return foo::helper() }"),
	)
	if got := call(t, c2.Funcs, "foo::bar::a"); !got.RawEquals(num(1)) {
		t.Errorf("foo::bar::a() = %#v, want 1", got)
	}
}

// Default-parameter expressions are evaluated against the same context as the
// body, so they see the unit layer too. (This is the one eval site that bypassed
// scopeEvalContext; it fails before the unitCtxFn wrap.)
func TestNamespaceDefaultParamSeesUnitLayer(t *testing.T) {
	c := compileNS(t, src("m.cty", `
		namespace foo
		func a(x = _d()) { return x }
		func _d() { return 7 }
	`))
	if got := call(t, c.Funcs, "foo::a"); !got.RawEquals(num(7)) {
		t.Errorf("foo::a() = %#v, want 7 (default param should see the unit layer)", got)
	}
}

func TestNamespaceDuplicates(t *testing.T) {
	// Same bare name in the same namespace: a duplicate, even across files.
	diags := compileNSErr(t,
		src("a.cty", "namespace foo\nfunc baz() { return 1 }"),
		src("b.cty", "namespace foo\nfunc baz() { return 2 }"),
	)
	if !strings.Contains(diags.Error(), `"foo::baz"`) {
		t.Errorf("duplicate diagnostic should name the qualified function, got: %s", diags.Error())
	}

	// Same bare name in DIFFERENT namespaces: legal, two distinct functions.
	c := compileNS(t,
		src("a.cty", "namespace foo\nfunc baz() { return 1 }"),
		src("b.cty", "namespace bar\nfunc baz() { return 2 }"),
	)
	if got := call(t, c.Funcs, "foo::baz"); !got.RawEquals(num(1)) {
		t.Errorf("foo::baz() = %#v, want 1", got)
	}
	if got := call(t, c.Funcs, "bar::baz"); !got.RawEquals(num(2)) {
		t.Errorf("bar::baz() = %#v, want 2", got)
	}
}

// ---- multi-file namespaces --------------------------------------------------

// A namespace spans files: siblings see each other's bare names, privates included.
func TestNamespaceSpansFiles(t *testing.T) {
	c := compileNS(t,
		src("a.cty", "namespace foo\nfunc a() { return b() + _shared() }"),
		src("b.cty", "namespace foo\nfunc b() { return 1 }\nfunc _shared() { return 10 }"),
	)
	if got := call(t, c.Funcs, "foo::a"); !got.RawEquals(num(11)) {
		t.Errorf("foo::a() = %#v, want 11", got)
	}
	if _, ok := c.Funcs["foo::_shared"]; ok {
		t.Error("_shared must not be handed to the host")
	}
}

func TestNamespacedAndGlobalFilesTogether(t *testing.T) {
	c := compileNS(t,
		src("a.cty", "namespace foo\nfunc x() { return 1 }"),
		src("b.cty", "func y() { return 2 }"),
	)
	if _, ok := c.Funcs["foo::x"]; !ok {
		t.Errorf("expected foo::x, got %v", keys(c.Funcs))
	}
	if _, ok := c.Funcs["y"]; !ok {
		t.Errorf("expected y, got %v", keys(c.Funcs))
	}
	// A global function cannot reach a namespaced one by its bare name.
	c2 := compileNS(t,
		src("a.cty", "namespace foo\nfunc x() { return 1 }"),
		src("b.cty", "func y() { return x() }"),
	)
	callErr(t, c2.Funcs, "y")
}

// ---- directives --------------------------------------------------------------

// File-scope directives are the comments before the file's first token. A
// namespace declaration *is* that first token, so directives above it still apply
// — and a directive line below it is just an ordinary comment. Both halves matter:
// the first keeps `//functy:` pragmas working in a namespaced file, the second is
// why a directive can never quietly reinterpret the declarations above it.
func TestNamespaceAndFileDirectives(t *testing.T) {
	// Above: takes effect (strict param types are required, so this must fail).
	_, diags := NewParser().Parse([]byte(
		"//functy:require param_types\nnamespace foo\nfunc a(x) { return x }\n"), "t.cty")
	if !diags.HasErrors() {
		t.Error("a directive above the namespace declaration should still take effect")
	}

	// Below: not a file directive, so the untyped param is fine.
	res, diags := NewParser().Parse([]byte(
		"namespace foo\n//functy:require param_types\nfunc a(x) { return x }\n"), "t.cty")
	if diags.HasErrors() {
		t.Errorf("a directive below the namespace declaration is an ordinary comment: %s", diags.Error())
	}
	if len(res.Directives) != 0 {
		t.Errorf("expected no file directives, got %v", res.Directives)
	}
}

// ---- test blocks ------------------------------------------------------------

// A test body belongs to its file's namespace: it calls siblings and privates by
// their bare names, and `skip` (injected one layer up, in the host context) still
// resolves through the chain.
func TestNamespacedTestBlocks(t *testing.T) {
	outcomes := compileAndRunTests(t, `
		namespace foo
		func double(n) { return _twice(n) }
		func _twice(n) { return n * 2 }

		test "sibling and private by bare name" {
			assert(double(4) == 8)
			assert(_twice(3) == 6)
		}

		test "skip still resolves past the unit layer" {
			skip("because")
			assert(false)
		}
	`)
	if len(outcomes) != 2 {
		t.Fatalf("expected 2 outcomes, got %d", len(outcomes))
	}
	if outcomes[0].Err != nil {
		t.Errorf("namespaced test failed: %v", outcomes[0].Err)
	}
	if !outcomes[1].Skipped || outcomes[1].SkipReason != "because" {
		t.Errorf("expected a skip with reason %q, got skipped=%v reason=%q",
			"because", outcomes[1].Skipped, outcomes[1].SkipReason)
	}
}

// ---- reflection --------------------------------------------------------------

func TestNamespaceHelp(t *testing.T) {
	res, diags := NewParser().Parse([]byte(`
		namespace foo::bar
		// Add two numbers.
		func baz(a: number, b: number) -> number { return a + b }
		func _helper() { return 1 }
	`), "t.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	evalCtxFn := func() *hcl.EvalContext { return ctx }
	funcs, _ := res.Compile(evalCtxFn)
	all := testStdlib()
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}

	help := HelpFunc(res, evalCtxFn)
	str := func(args ...cty.Value) string {
		v, err := help.Call(args)
		if err != nil {
			t.Fatalf("help(): %v", err)
		}
		if v.IsNull() {
			return "<null>"
		}
		return v.AsString()
	}

	// The signature head is the name it is callable under.
	if got := str(cty.StringVal("foo::bar::baz")); !strings.HasPrefix(got, "foo::bar::baz(") {
		t.Errorf("help(\"foo::bar::baz\") should lead with the qualified signature, got:\n%s", got)
	}
	// A bare name still resolves when it is unambiguous.
	if got := str(cty.StringVal("baz")); !strings.HasPrefix(got, "foo::bar::baz(") {
		t.Errorf("help(\"baz\") should fall back to the unique declaration, got:\n%s", got)
	}
	// Privates are reachable by name for debugging...
	if got := str(cty.StringVal("foo::bar::_helper")); !strings.HasPrefix(got, "foo::bar::_helper(") {
		t.Errorf("help on a private should render it, got:\n%s", got)
	}
	// ...but never listed, because they are not in the host's map at all.
	list := str()
	if strings.Contains(list, "_helper") {
		t.Errorf("help() must not list private functions, got:\n%s", list)
	}
	if !strings.Contains(list, "foo::bar::baz") {
		t.Errorf("help() should list the qualified name, got:\n%s", list)
	}

	// doc() resolves the qualified name and knows nothing of privates.
	doc := DocFunc(evalCtxFn)
	dv, err := doc.Call([]cty.Value{cty.StringVal("foo::bar::baz")})
	if err != nil {
		t.Fatalf("doc(): %v", err)
	}
	if dv.IsNull() || !strings.Contains(dv.AsString(), "Add two numbers.") {
		t.Errorf("doc(\"foo::bar::baz\") = %#v, want the doc comment", dv)
	}
	dv, err = doc.Call([]cty.Value{cty.StringVal("foo::bar::_helper")})
	if err != nil {
		t.Fatalf("doc(): %v", err)
	}
	if !dv.IsNull() {
		t.Errorf("doc() on a private should be null (it is not host-visible), got %#v", dv)
	}
}

func keys(m map[string]function.Function) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
