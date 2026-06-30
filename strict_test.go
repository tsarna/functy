package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
)

func parseWith(t *testing.T, p *Parser, src string) hcl.Diagnostics {
	t.Helper()
	_, diags := p.Parse([]byte(src), "test")
	return diags
}

func TestStrictOffByDefault(t *testing.T) {
	// An entirely unannotated program parses clean with no flags or pragmas.
	parse(t, `func f(a, b) { var x = a }
func g() { return 1 }`)
}

func TestRequireParamTypes(t *testing.T) {
	p := NewParser().RequireParamTypes(true)
	if !parseWith(t, p, "func f(a) { return a }").HasErrors() {
		t.Fatalf("expected error for untyped param")
	}
	// Annotated (incl. explicit `any`) passes.
	if d := parseWith(t, p, "func f(a: number) { return a }"); d.HasErrors() {
		t.Fatalf("typed param should pass: %s", d.Error())
	}
	if d := parseWith(t, p, "func f(a: any) { return a }"); d.HasErrors() {
		t.Fatalf("`: any` should satisfy RequireParamTypes: %s", d.Error())
	}
	// Variadic must be typed too.
	if !parseWith(t, p, "func f(*rest) { return rest }").HasErrors() {
		t.Fatalf("expected error for untyped variadic")
	}
	if d := parseWith(t, p, "func f(*rest: number) { return rest }"); d.HasErrors() {
		t.Fatalf("typed variadic should pass: %s", d.Error())
	}
}

func TestRequireReturnType(t *testing.T) {
	p := NewParser().RequireReturnType(true)
	if !parseWith(t, p, "func f() { return 1 }").HasErrors() {
		t.Fatalf("expected error for missing return type")
	}
	if d := parseWith(t, p, "func f() -> number { return 1 }"); d.HasErrors() {
		t.Fatalf("typed return should pass: %s", d.Error())
	}
	if d := parseWith(t, p, "func f() -> any { return 1 }"); d.HasErrors() {
		t.Fatalf("`-> any` should satisfy RequireReturnType: %s", d.Error())
	}
	if d := parseWith(t, p, "func f() -> null { }"); d.HasErrors() {
		t.Fatalf("`-> null` should satisfy RequireReturnType: %s", d.Error())
	}
}

func TestRequireDeclaredTypes(t *testing.T) {
	p := NewParser().RequireDeclaredTypes(true)
	if !parseWith(t, p, "func f() { var x = 1 }").HasErrors() {
		t.Fatalf("expected error for untyped var")
	}
	if d := parseWith(t, p, "func f() { var x: number = 1 }"); d.HasErrors() {
		t.Fatalf("typed var should pass: %s", d.Error())
	}
	// Top-level const/var too.
	pc := NewParser().AllowTopLevelConst(true).RequireDeclaredTypes(true)
	if !parseWith(t, pc, "const k = 1").HasErrors() {
		t.Fatalf("expected error for untyped top-level const")
	}
	if d := parseWith(t, pc, "const k: number = 1"); d.HasErrors() {
		t.Fatalf("typed const should pass: %s", d.Error())
	}
}

func TestStrictDoesNotAffectOtherDeclKinds(t *testing.T) {
	// RequireParamTypes shouldn't force var/return annotations.
	p := NewParser().RequireParamTypes(true)
	if d := parseWith(t, p, "func f(a: number) { var x = a\n return x }"); d.HasErrors() {
		t.Fatalf("only params should be required: %s", d.Error())
	}
}

func TestPragmaStrict(t *testing.T) {
	src := `//functy:strict

func f(a) { return a }`
	if !parseWith(t, NewParser(), src).HasErrors() {
		t.Fatalf("//functy:strict should require the param type")
	}
}

func TestPragmaRequireSpecific(t *testing.T) {
	// Only return_type is required here, so an untyped param is fine but a missing
	// return type errors.
	src := `//functy:require return_type
func f(a) { return a }`
	if !parseWith(t, NewParser(), src).HasErrors() {
		t.Fatalf("//functy:require return_type should require a return type")
	}
	ok := `//functy:require return_type
func f(a) -> any { return a }`
	if d := parseWith(t, NewParser(), ok); d.HasErrors() {
		t.Fatalf("untyped param should be allowed when only return_type is required: %s", d.Error())
	}
}

func TestPragmaTightenOnly(t *testing.T) {
	// Host requires nothing; a file directive adds a requirement.
	if !parseWith(t, NewParser(), "//functy:require param_types\nfunc f(a) { return a }").HasErrors() {
		t.Fatalf("file directive should add the param requirement")
	}
	// Host requires it; file is silent — still enforced.
	if !parseWith(t, NewParser().RequireParamTypes(true), "func f(a) { return a }").HasErrors() {
		t.Fatalf("host flag should enforce even without a directive")
	}
}

func TestStrictSourceInMessage(t *testing.T) {
	hostDiags := parseWith(t, NewParser().RequireParamTypes(true), "func f(a) { return a }")
	if !strings.Contains(hostDiags.Error(), "host") {
		t.Fatalf("host-sourced message should mention the host: %s", hostDiags.Error())
	}
	fileDiags := parseWith(t, NewParser(), "//functy:require param_types\nfunc f(a) { return a }")
	if !strings.Contains(fileDiags.Error(), "directive") {
		t.Fatalf("file-sourced message should mention a directive: %s", fileDiags.Error())
	}
}

func TestDirectiveSpaceIsProse(t *testing.T) {
	// A space after // makes it prose, not a directive — strict not enabled.
	src := `// functy:strict
func f(a) { return a }`
	if d := parseWith(t, NewParser(), src); d.HasErrors() {
		t.Fatalf("`// functy:strict` (space) must not be a directive: %s", d.Error())
	}
}

func TestResultDirectivesCollected(t *testing.T) {
	src := `//functy:strict
//vinculum:cache 5m
//myapp:route /x y
func f(a: number) -> number { return a }`
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	if len(res.Directives) != 3 {
		t.Fatalf("expected 3 directives, got %d: %+v", len(res.Directives), res.Directives)
	}
	byNS := map[string]Directive{}
	for _, d := range res.Directives {
		byNS[d.Namespace] = d
	}
	if byNS["vinculum"].Name != "cache" || byNS["vinculum"].Args != "5m" {
		t.Errorf("vinculum directive parsed wrong: %+v", byNS["vinculum"])
	}
	if byNS["myapp"].Name != "route" || byNS["myapp"].Args != "/x y" {
		t.Errorf("myapp directive parsed wrong: %+v", byNS["myapp"])
	}
}

func TestDirectivesOnlyFromLeadingBlock(t *testing.T) {
	// A directive after the first declaration is not in the leading block.
	src := `func f(a: number) -> number { return a }
//myapp:late x`
	res, _ := NewParser().Parse([]byte(src), "test")
	if len(res.Directives) != 0 {
		t.Fatalf("expected no leading directives, got %+v", res.Directives)
	}
}

func TestUnknownFunctyDirectiveWarns(t *testing.T) {
	res, diags := NewParser().Parse([]byte("//functy:bogus\nfunc f(a: number) -> number { return a }"), "test")
	_ = res
	if diags.HasErrors() {
		t.Fatalf("unknown functy directive should be a warning, not an error: %s", diags.Error())
	}
	var warned bool
	for _, d := range diags {
		if d.Severity == hcl.DiagWarning && strings.Contains(d.Summary, "Unknown functy directive") {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("expected a warning for the unknown functy directive")
	}
}
