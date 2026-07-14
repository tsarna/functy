package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// externSrc prefixes a source with the directive that makes it an extern file.
func externSrc(body string) string {
	return "//functy:extern\n\n" + body
}

// parseExtern parses an extern file with top-level var/const enabled, so that a
// test proving `var`/`const` are rejected proves the *extern* rule fired rather
// than the (separate) host feature flag.
func parseExtern(t *testing.T, body string) *Result {
	t.Helper()
	res, diags := externParser().Parse([]byte(externSrc(body)), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors:\n%s", diags.Error())
	}
	return res
}

func parseExternErr(t *testing.T, body string) hcl.Diagnostics {
	t.Helper()
	_, diags := externParser().Parse([]byte(externSrc(body)), "test")
	if !diags.HasErrors() {
		t.Fatalf("expected parse errors, got none")
	}
	return diags
}

func externParser() *Parser {
	return NewParser().AllowTopLevelVar(true).AllowTopLevelConst(true)
}

func onlyExtern(t *testing.T, body string) *FuncDecl {
	t.Helper()
	res := parseExtern(t, body)
	if len(res.Externs) != 1 {
		t.Fatalf("expected 1 extern, got %d", len(res.Externs))
	}
	return res.Externs[0]
}

func TestExternFileParses(t *testing.T) {
	res := parseExtern(t, `
func parsetime(format: string, s: string) -> string

func now() -> string
`)
	if len(res.Externs) != 2 {
		t.Fatalf("expected 2 externs, got %d", len(res.Externs))
	}
	if len(res.Funcs) != 0 {
		t.Fatalf("externs must not land in Funcs, got %d", len(res.Funcs))
	}

	fn := res.Externs[0]
	if fn.Name != "parsetime" {
		t.Fatalf("name = %q", fn.Name)
	}
	if !fn.Extern {
		t.Fatal("Extern flag not set")
	}
	if len(fn.Params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(fn.Params))
	}
	if got := ctyOf(fn.RetType); got != cty.String {
		t.Fatalf("return type = %s", got.FriendlyName())
	}
	if fn.Body != nil {
		t.Fatal("an extern must have no body")
	}
	// SigRange is the only end position an extern has; every consumer that would
	// reach for BodyRange.End depends on it (fmt's gap logic, symbols' extent).
	if fn.SigRange.End.Line == 0 {
		t.Fatalf("SigRange not set: %#v", fn.SigRange)
	}
	if fn.SigRange.End.Byte <= fn.DefRange.Start.Byte {
		t.Fatalf("SigRange does not span the declaration: %#v", fn.SigRange)
	}
}

func TestExternDocComments(t *testing.T) {
	fn := onlyExtern(t, `
// Parse a time from a string.
// Panics on malformed input.
func parsetime(
    format: string,   // strftime-style layout
    // The string to parse.
    s: string,
) -> string
`)
	want := "Parse a time from a string.\nPanics on malformed input."
	if fn.Doc != want {
		t.Fatalf("doc = %q, want %q", fn.Doc, want)
	}
	if fn.Params[0].Doc != "strftime-style layout" {
		t.Fatalf("param 0 doc = %q", fn.Params[0].Doc)
	}
	if fn.Params[1].Doc != "The string to parse." {
		t.Fatalf("param 1 doc = %q", fn.Params[1].Doc)
	}
}

// A bodiless func must stay a hard error outside an extern file. This is the whole
// reason extern is a file-level directive rather than a bare bodiless-func syntax:
// a half-typed declaration (signature written, brace not yet opened) has to remain
// a syntax error, not silently become a valid one.
func TestBodilessFuncOutsideExternIsError(t *testing.T) {
	parseErr(t, "func f(a)\n")
}

func TestExternWithBodyIsError(t *testing.T) {
	// The body holds a `var`, which recoverToTopLevel would resync on — so if the
	// body were not consumed, this would cascade into a second, bogus error about a
	// top-level `var` in an extern file.
	diags := parseExternErr(t, "func f(a) {\n    var x = 1\n    return x\n}\n")
	if n := len(diags.Errs()); n != 1 {
		t.Fatalf("expected exactly 1 error (no cascade), got %d:\n%s", n, diags.Error())
	}
	if !strings.Contains(diags[0].Summary, "cannot have a body") {
		t.Fatalf("unexpected error: %s", diags[0].Summary)
	}
}

// An extern file holds only declarations — it documents a host's Go functions, so
// it carries no functy code of its own.
//
// The var/const cases matter most, and are why externParser enables top-level var
// and const: the extern rule has to fire *even when the host allowed them*, as the
// CLI does. Each case is paired with a negative control — the same source in a
// non-extern file — so a passing test cannot be explained by the feature flag being
// off, only by the extern rule.
func TestExternRejectsNonFuncDeclarations(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"var", "var x = 1\n"},
		{"const", "const x = 1\n"},
		{"test", "test \"t\" {\n    return 1\n}\n"},
		{"type", "type Id = string\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Control: legal in a normal file parsed by the very same parser.
			if _, diags := externParser().Parse([]byte(tc.src), "test"); diags.HasErrors() {
				t.Fatalf("precondition failed: %s is not accepted outside an extern file, "+
					"so its rejection inside one proves nothing:\n%s", tc.name, allDiags(diags))
			}

			diags := parseExternErr(t, tc.src)
			if !hasSummary(diags, "not allowed in an extern file") {
				t.Fatalf("unexpected error: %s", allDiags(diags))
			}
		})
	}
}

// A type alias in an extern file must not be registered project-wide. Aliases are
// collected from raw tokens before parsing, so skipping extern files there is the
// only thing that keeps the rejection above from being cosmetic.
func TestExternTypeAliasNotRegistered(t *testing.T) {
	_, diags := externParser().ParseAll([]Source{
		{Filename: "ext", Bytes: []byte(externSrc("type Id = string\n"))},
		{Filename: "real", Bytes: []byte("func f(a: Id) -> Id {\n    return a\n}\n")},
	})
	if !diags.HasErrors() {
		t.Fatal("expected errors")
	}
	// Both the rejection and the now-unknown type must be reported: the alias never
	// reached the shared type environment.
	if !hasSummary(diags, "Unknown type") {
		t.Fatalf("alias leaked out of the extern file:\n%s", allDiags(diags))
	}
}

func hasSummary(diags hcl.Diagnostics, summary string) bool {
	for _, d := range diags {
		if strings.Contains(d.Summary, summary) {
			return true
		}
	}
	return false
}

func allDiags(diags hcl.Diagnostics) string {
	var b strings.Builder
	for _, d := range diags {
		b.WriteString(d.Summary + ": " + d.Detail + "\n")
	}
	return b.String()
}

func TestExternRejectsPrivateName(t *testing.T) {
	diags := parseExternErr(t, "func _helper(a) -> string\n")
	if !strings.Contains(diags[0].Summary, "Private extern") {
		t.Fatalf("unexpected error: %s", diags[0].Summary)
	}
}

func TestOptionalMarker(t *testing.T) {
	fn := onlyExtern(t, "func get(ctx?: string, thing, key?, fallback = null) -> string\n")

	want := []struct {
		name     string
		optional bool
		defaults bool
	}{
		{"ctx", true, false},
		{"thing", false, false},
		{"key", true, false},
		{"fallback", false, true},
	}
	if len(fn.Params) != len(want) {
		t.Fatalf("expected %d params, got %d", len(want), len(fn.Params))
	}
	for i, w := range want {
		p := fn.Params[i]
		if p.Name != w.name || p.Optional != w.optional || (p.Default != nil) != w.defaults {
			t.Errorf("param %d = {name:%q optional:%v default:%v}, want %+v",
				i, p.Name, p.Optional, p.Default != nil, w)
		}
	}
}

func TestOptionalMarkerOutsideExternIsError(t *testing.T) {
	parseErr(t, "func f(a?) {}\n")
}

func TestOptionalMarkerWithDefaultIsError(t *testing.T) {
	diags := parseExternErr(t, "func f(a? = 1) -> string\n")
	if !strings.Contains(diags[0].Summary, "cannot have a default") {
		t.Fatalf("unexpected error: %s", diags[0].Summary)
	}
}

func TestOptionalMarkerOnVariadicIsError(t *testing.T) {
	diags := parseExternErr(t, "func f(*rest?) -> string\n")
	if !strings.Contains(diags[0].Summary, "cannot be optional") {
		t.Fatalf("unexpected error: %s", diags[0].Summary)
	}
}

// An extern transcribes a host function's real shape, which may take optional
// arguments at the head as well as the tail — the `get([ctx,] thing)` convention
// that `?` exists to spell. So the usual required-before-optional rule is relaxed.
func TestExternRelaxesParamOrder(t *testing.T) {
	fn := onlyExtern(t, "func f(a?, b, c = 1, d) -> string\n")
	if len(fn.Params) != 4 {
		t.Fatalf("expected 4 params, got %d", len(fn.Params))
	}
}

func TestNonExternKeepsRequiredAfterOptionalRule(t *testing.T) {
	parseErr(t, "func f(a = 1, b) {}\n")
}

func TestExternVariadicMustStillBeLast(t *testing.T) {
	parseExternErr(t, "func f(*rest, a) -> string\n")

	fn := onlyExtern(t, "func f(a?, *rest: string) -> string\n")
	if !fn.Params[1].Variadic {
		t.Fatal("expected a variadic trailing param")
	}
}

// ---- Overload sets -----------------------------------------------------------

// One name with several *forms* is an overload set: a host function whose argument
// shapes differ per arity is not a function with optional parameters, and one
// signature cannot describe it honestly.
func TestOverloadSetIsLegal(t *testing.T) {
	res := parseExtern(t, `
// Parse a timestamp.
func parsetime(s: string) -> string
func parsetime(format: string, s: string) -> string
func parsetime(format: string, s: string, tz: string) -> string
`)
	if len(res.Externs) != 3 {
		t.Fatalf("expected 3 forms, got %d", len(res.Externs))
	}
	for i, want := range []int{1, 2, 3} {
		if got := len(res.Externs[i].Params); got != want {
			t.Errorf("form %d has %d params, want %d", i, got, want)
		}
	}
}

// Forms may differ by *type* at the same arity — that is `duration(s)` vs
// `duration(n, unit)`, and `timeadd`, whose return type flips with its arguments.
func TestOverloadSetDistinguishedByType(t *testing.T) {
	res := parseExtern(t, `
func timeadd(ts: string, dur: string) -> string
func timeadd(ts: string, dur: number) -> string
`)
	if len(res.Externs) != 2 {
		t.Fatalf("expected 2 forms, got %d", len(res.Externs))
	}
}

// Two forms of the same shape are a copy-paste, not an overload. Names and docs do
// not distinguish a call, so they do not distinguish a form.
func TestDuplicateOverloadShapeIsError(t *testing.T) {
	diags := parseExternErr(t, "func f(a: string) -> string\n\nfunc f(b: string) -> string\n")
	if !hasSummary(diags, "Duplicate extern signature") {
		t.Fatalf("expected a duplicate-shape error, got:\n%s", allDiags(diags))
	}
}

// One name across two *files* is a collision, not an overload — it is what happens
// when two packages both claim `get`. An overload set is written together, by one
// author, in one file.
func TestDuplicateExternAcrossFilesIsError(t *testing.T) {
	_, diags := externParser().ParseAll([]Source{
		{Filename: "a", Bytes: []byte(externSrc("func f(a) -> string\n"))},
		{Filename: "b", Bytes: []byte(externSrc("func f(a, b) -> string\n"))},
	})
	if !hasSummary(diags, "Duplicate extern") {
		t.Fatalf("expected a duplicate error, got:\n%s", allDiags(diags))
	}
}

// help() lists every form, each with its own return type, then the documentation and
// a "Parameters:" section unioned across the forms.
func TestHelpRendersAnOverloadSet(t *testing.T) {
	src := "//functy:extern\n\n" +
		"// Parse a timestamp.\n" +
		"func parsetime(\n" +
		"    s: string,  // the timestamp to parse\n" +
		") -> time\n" +
		"func parsetime(\n" +
		"    format: string,  // a Go reference layout\n" +
		"    s: string,\n" +
		") -> time\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}

	got, err := HelpFunc(res, nil).Call([]cty.Value{cty.StringVal("parsetime")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	want := "parsetime(s: string) -> time\n" +
		"parsetime(format: string, s: string) -> time\n\n" +
		"Parse a timestamp.\n\n" +
		"Parameters:\n" +
		"  s       the timestamp to parse\n" +
		"  format  a Go reference layout"
	if got.AsString() != want {
		t.Fatalf("help(\"parsetime\") =\n%q\nwant\n%q", got.AsString(), want)
	}
}

// An extern documents a function the *host* provides, so it cannot also be a
// function functy itself compiles — that extern would be documenting a lie.
func TestExternDuplicatingAFunctionIsError(t *testing.T) {
	_, diags := externParser().ParseAll([]Source{
		{Filename: "ext", Bytes: []byte(externSrc("func f(a) -> string\n"))},
		{Filename: "real", Bytes: []byte("func f(a) -> string {\n    return a\n}\n")},
	})
	if !hasSummary(diags, "Extern duplicates a function") {
		t.Fatalf("expected a collision error, got:\n%s", allDiags(diags))
	}
}

func TestExternWithNamespace(t *testing.T) {
	res := parseExtern(t, "namespace t\n\nfunc parsetime(s: string) -> string\n")
	if len(res.Externs) != 1 {
		t.Fatalf("expected 1 extern, got %d", len(res.Externs))
	}
	if got := res.Externs[0].QualifiedName(); got != "t::parsetime" {
		t.Fatalf("qualified name = %q", got)
	}
}

func TestExternsAreNotCompiled(t *testing.T) {
	res := parseExtern(t, "func f(a?) -> string\n")
	funcs, diags := res.Compile(nil)
	if diags.HasErrors() {
		t.Fatalf("unexpected compile errors:\n%s", diags.Error())
	}
	if _, ok := funcs["f"]; ok {
		t.Fatal("an extern must not be compiled into a callable function")
	}
}

// An extern names the types of the host whose functions it documents; the reader
// (the CLI, an editor) generally is not that host. An unregistered name therefore
// stands in as an opaque name rather than failing — with a warning, so a typo is
// not silent.
func TestExternUnknownTypeIsOpaqueWithWarning(t *testing.T) {
	res, diags := externParser().Parse([]byte(externSrc("func get(ctx?: ctx, thing) -> any\n")), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags.Error())
	}
	var warned bool
	for _, d := range diags {
		if d.Severity == hcl.DiagWarning && strings.Contains(d.Summary, "Unregistered type") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("expected an unregistered-type warning, got:\n%s", diags.Error())
	}
	if got := res.Externs[0].Params[0].Type.String(); got != "ctx" {
		t.Fatalf("opaque type rendered as %q, want %q", got, "ctx")
	}
}

// The same file against a host that *did* register the type: no warning, and the
// real constraint is used. This is the vinculum shape.
func TestExternRegisteredOpenTypeResolves(t *testing.T) {
	p := externParser().RegisterOpenType("ctx", func(cty.Value) error { return nil })
	res, diags := p.Parse([]byte(externSrc("func get(ctx?: ctx, thing) -> any\n")), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags.Error())
	}
	for _, d := range diags {
		if d.Severity == hcl.DiagWarning {
			t.Fatalf("unexpected warning for a registered type: %s", d.Summary)
		}
	}
	if _, ok := res.Externs[0].Params[0].Type.(predicateConstraint); !ok {
		t.Fatalf("expected the registered open type, got %T", res.Externs[0].Params[0].Type)
	}
}

// An unknown type outside an extern file stays a hard error.
func TestUnknownTypeOutsideExternIsError(t *testing.T) {
	parseErr(t, "func f(a: ctx) {\n    return a\n}\n")
}

// ---- Host-registered externs (Parser.RegisterExterns) ------------------------

// hostExterns is a stand-in for what a leaf package embeds and registers.
const hostExterns = "//functy:extern\n\n" +
	"// A host-provided function.\n" +
	"func hostget(ctx?: ctx, thing, fallback?) -> any\n"

func hostParser() *Parser {
	return externParser().RegisterExterns([]byte(hostExterns), "host/externs.cty")
}

func TestRegisterExterns(t *testing.T) {
	res, diags := hostParser().Parse([]byte("func f(a) {\n    return a\n}\n"), "user.cty")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", allDiags(diags))
	}
	if len(res.HostExterns) != 1 {
		t.Fatalf("expected 1 host extern, got %d", len(res.HostExterns))
	}
	if res.HostExterns[0].Name != "hostget" {
		t.Fatalf("name = %q", res.HostExterns[0].Name)
	}
	if res.HostExterns[0].Doc != "A host-provided function." {
		t.Fatalf("doc = %q — attachDocComments did not run on the host source", res.HostExterns[0].Doc)
	}
	// A host extern belongs to no parsed source, so it must NOT appear in Externs —
	// that is the field fmt renders.
	if len(res.Externs) != 0 {
		t.Fatalf("host externs leaked into Result.Externs: %d", len(res.Externs))
	}
	if len(res.Funcs) != 1 {
		t.Fatalf("expected the user's function, got %d", len(res.Funcs))
	}
}

// THE HAZARD. Parser.Format calls Parse, and fmt renders Result.Externs. If host
// externs were merged into that set, formatting a user's file would splice another
// package's declarations into it.
func TestRegisteredExternsAreNotFormatted(t *testing.T) {
	user := "func f(a) {\n    return a\n}\n" // already canonical
	out, diags := hostParser().Format([]byte(user), "user.cty")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", allDiags(diags))
	}
	if string(out) != user {
		t.Fatalf("fmt changed a canonical file:\n%q\nwant\n%q", out, user)
	}
	if strings.Contains(string(out), "hostget") {
		t.Fatalf("the host's declarations were emitted into the user's file:\n%s", out)
	}
}

// The subtler half of the same hazard: the formatter walks Result.Comments by byte
// offset into the file it is formatting, so a host file's comments in that slice
// would splice foreign text at offsets belonging to the user's source.
func TestRegisteredExternsDoNotLeakComments(t *testing.T) {
	res, diags := hostParser().Parse([]byte("// user comment\nfunc f(a) {\n    return a\n}\n"), "user.cty")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", allDiags(diags))
	}
	for _, c := range res.Comments {
		if c.Range.Filename != "user.cty" {
			t.Fatalf("comment from %q leaked into the parsed source's comments", c.Range.Filename)
		}
	}
	for _, d := range res.Directives {
		if d.Range.Filename != "user.cty" {
			t.Fatalf("directive from %q leaked into the parsed source's directives", d.Range.Filename)
		}
	}
}

// A registered source must declare itself an extern file. One byte string means one
// thing however it is loaded: the file a package embeds is also a valid standalone
// .cty that `functy fmt` and `functy symbols` can open.
func TestRegisteredExternRequiresDirective(t *testing.T) {
	p := externParser().RegisterExterns([]byte("func f(a) -> any\n"), "host/bad.cty")
	_, diags := p.Parse([]byte("func g() {\n    return 1\n}\n"), "user.cty")
	if !hasSummary(diags, "not an extern file") {
		t.Fatalf("expected a missing-directive error, got:\n%s", allDiags(diags))
	}
}

func TestRegisteredExternCollisions(t *testing.T) {
	t.Run("with a user function", func(t *testing.T) {
		_, diags := hostParser().Parse([]byte("func hostget(a) {\n    return a\n}\n"), "user.cty")
		if !hasSummary(diags, "Extern duplicates a function") {
			t.Fatalf("expected a collision error, got:\n%s", allDiags(diags))
		}
	})

	t.Run("with a file extern", func(t *testing.T) {
		_, diags := hostParser().Parse([]byte(externSrc("func hostget(a) -> any\n")), "user.cty")
		if !hasSummary(diags, "Duplicate extern") {
			t.Fatalf("expected a duplicate error, got:\n%s", allDiags(diags))
		}
	})

	t.Run("with another registration", func(t *testing.T) {
		p := hostParser().RegisterExterns([]byte(hostExterns), "other/externs.cty")
		_, diags := p.Parse([]byte("func f() {\n    return 1\n}\n"), "user.cty")
		if !hasSummary(diags, "Duplicate extern") {
			t.Fatalf("expected a duplicate error across two registrations, got:\n%s", allDiags(diags))
		}
	})
}

// The payoff: a host extern beats the cty-metadata fallback for a function that IS
// registered in the eval context. Its cty shape is the collapsed VarParam lie the
// extern exists to replace, so if the context won the lookup the feature would do
// nothing.
func TestHelpFuncHostExternBeatsCtyFallback(t *testing.T) {
	hostFn := function.New(&function.Spec{
		Description: "Host-registered.",
		Params:      []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam:    &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:        function.StaticReturnType(cty.DynamicPseudoType),
		Impl:        func([]cty.Value, cty.Type) (cty.Value, error) { return cty.NullVal(cty.DynamicPseudoType), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{"hostget": hostFn}}

	res, diags := hostParser().Parse([]byte("func f() {\n    return 1\n}\n"), "user.cty")
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", allDiags(diags))
	}
	help := HelpFunc(res, func() *hcl.EvalContext { return ctx })

	got, err := help.Call([]cty.Value{cty.StringVal("hostget")})
	if err != nil {
		t.Fatalf("help call: %s", err)
	}
	if !strings.HasPrefix(got.AsString(), "hostget(ctx?: ctx, thing, fallback?) -> any") {
		t.Fatalf("the cty fallback shadowed the host extern:\n%s", got.AsString())
	}
}
