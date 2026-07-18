package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// ctyOf returns a constraint's cty.Type, or cty.NilType for a dynamic (nil) one.
func ctyOf(tc TypeConstraint) cty.Type {
	if tc == nil {
		return cty.NilType
	}
	return tc.Cty()
}

func parse(t *testing.T, src string) *Result {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected parse errors:\n%s", diags.Error())
	}
	return res
}

func parseErr(t *testing.T, src string) {
	t.Helper()
	_, diags := NewParser().Parse([]byte(src), "test")
	if !diags.HasErrors() {
		t.Fatalf("expected parse errors, got none")
	}
}

func onlyFunc(t *testing.T, src string) *FuncDecl {
	t.Helper()
	res := parse(t, src)
	if len(res.Funcs) != 1 {
		t.Fatalf("expected 1 function, got %d", len(res.Funcs))
	}
	return res.Funcs[0]
}

func TestParseEmptyFunc(t *testing.T) {
	fn := onlyFunc(t, "func main() {}")
	if fn.Name != "main" {
		t.Fatalf("name = %q", fn.Name)
	}
	if len(fn.Params) != 0 || len(fn.Body) != 0 {
		t.Fatalf("expected no params/body")
	}
}

func TestParseParams(t *testing.T) {
	fn := onlyFunc(t, `func f(a, b: number, c = 1, d: string = "x", *rest: bool) { return a }`)
	if len(fn.Params) != 5 {
		t.Fatalf("expected 5 params, got %d", len(fn.Params))
	}
	if ctyOf(fn.Params[1].Type) != cty.Number {
		t.Errorf("b should be number, got %#v", fn.Params[1].Type)
	}
	if fn.Params[2].Default == nil {
		t.Errorf("c should have a default")
	}
	if !fn.Params[4].Variadic {
		t.Errorf("rest should be variadic")
	}
	// For a variadic, Type holds the element type; the builder collects into list(T).
	if ctyOf(fn.Params[4].Type) != cty.Bool {
		t.Errorf("*rest: bool element type should be bool, got %#v", fn.Params[4].Type)
	}
}

func TestParseParamsMultiline(t *testing.T) {
	// A parameter list may span multiple lines, including a trailing comma and
	// comment lines between parameters.
	fn := onlyFunc(t, `func f(
	    a: string,
	    // the toggle
	    b: bool,
	    *rest: number,
	) -> bool {
	    return b
	}`)
	if len(fn.Params) != 3 {
		t.Fatalf("expected 3 params, got %d", len(fn.Params))
	}
	if fn.Params[0].Name != "a" || ctyOf(fn.Params[0].Type) != cty.String {
		t.Errorf("param 0 = %+v", fn.Params[0])
	}
	if fn.Params[1].Name != "b" || ctyOf(fn.Params[1].Type) != cty.Bool {
		t.Errorf("param 1 = %+v", fn.Params[1])
	}
	if !fn.Params[2].Variadic || fn.Params[2].Name != "rest" {
		t.Errorf("param 2 = %+v", fn.Params[2])
	}
	if ctyOf(fn.RetType) != cty.Bool {
		t.Errorf("ret type = %#v", fn.RetType)
	}
}

func TestParseParamsMultilineNoTrailingComma(t *testing.T) {
	// A newline may separate the final parameter from the closing `)`.
	fn := onlyFunc(t, "func f(\n    a: string,\n    b: bool\n) { return a }")
	if len(fn.Params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(fn.Params))
	}
}

func TestParseParamsMultilineEmpty(t *testing.T) {
	// A newline between `(` and `)` is insignificant.
	fn := onlyFunc(t, "func f(\n) { return 1 }")
	if len(fn.Params) != 0 {
		t.Fatalf("expected 0 params, got %d", len(fn.Params))
	}
}

func TestParseMultilineSignature(t *testing.T) {
	// The whole signature may break across lines: after `)`, before `->`, its
	// type, and the `{`.
	fn := onlyFunc(t, "func f(\n    a: number,\n    b: number,\n)\n-> number\n{\n    return a + b\n}")
	if len(fn.Params) != 2 {
		t.Fatalf("expected 2 params, got %d", len(fn.Params))
	}
	if ctyOf(fn.RetType) != cty.Number {
		t.Fatalf("ret type = %#v", fn.RetType)
	}
}

func TestParseReturnType(t *testing.T) {
	fn := onlyFunc(t, "func f() -> object({ q = number, r = number }) { return null }")
	if !ctyOf(fn.RetType).IsObjectType() {
		t.Fatalf("expected object return type, got %#v", fn.RetType)
	}
}

func TestParseVarForms(t *testing.T) {
	fn := onlyFunc(t, `func f() {
        var a = 1
        var b: number = 2
        var c: string
        var d
        a = a + 1
        log_x(a)
    }`)
	if len(fn.Body) != 6 {
		t.Fatalf("expected 6 statements, got %d", len(fn.Body))
	}
	if vd := fn.Body[0].(*VarDecl); vd.Type != nil || vd.Init == nil {
		t.Errorf("a should be dynamic with an initializer")
	}
	if vd := fn.Body[1].(*VarDecl); ctyOf(vd.Type) != cty.Number || vd.Init == nil {
		t.Errorf("b should be typed number with an initializer")
	}
	if vd := fn.Body[2].(*VarDecl); ctyOf(vd.Type) != cty.String || vd.Init != nil {
		t.Errorf("c should be typed string with no init")
	}
	if vd := fn.Body[3].(*VarDecl); vd.Type != nil || vd.Init != nil {
		t.Errorf("d should be dynamic with no init")
	}
	if _, ok := fn.Body[4].(*Assign); !ok {
		t.Errorf("stmt4 should be Assign")
	}
	if _, ok := fn.Body[5].(*ExprStmt); !ok {
		t.Errorf("stmt5 should be ExprStmt")
	}
}

func TestParseIfChain(t *testing.T) {
	fn := onlyFunc(t, `func f(n) {
        if n > 0 {
            return "pos"
        } else if n < 0 {
            return "neg"
        } else {
            return "zero"
        }
    }`)
	chain, ok := fn.Body[0].(*IfChain)
	if !ok {
		t.Fatalf("expected IfChain")
	}
	if len(chain.Branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(chain.Branches))
	}
	if chain.Else == nil {
		t.Fatalf("expected else")
	}
}

func TestParseElseSameLine(t *testing.T) {
	// `} else {` on one line must parse — a headline functy improvement.
	parse(t, "func f(n) { if n > 0 { return 1 } else { return 2 } }")
}

func TestParseForForms(t *testing.T) {
	cases := map[string]ForKind{
		"func f() { for x > 0 { break } }":                   ForCond,
		"func f() { while x > 0 { break } }":                 ForCond,
		"func f() { for { break } }":                         ForCond,
		"func f() { for var i = 0; i < 3; i = i+1 { x() } }": ForClause,
		"func f() { for v in xs { x(v) } }":                  ForRange,
		"func f() { for k, v in m { x(k, v) } }":             ForRange,
	}
	for src, kind := range cases {
		fn := onlyFunc(t, src)
		loop, ok := fn.Body[0].(*For)
		if !ok {
			t.Fatalf("%s: expected For", src)
		}
		if loop.Kind != kind {
			t.Errorf("%s: kind = %v, want %v", src, loop.Kind, kind)
		}
	}
}

func TestParseForRangeNames(t *testing.T) {
	fn := onlyFunc(t, "func f() { for v in xs { x(v) } }")
	loop := fn.Body[0].(*For)
	if loop.KeyName != "" || loop.ValName != "v" {
		t.Fatalf("single var should bind value: key=%q val=%q", loop.KeyName, loop.ValName)
	}
	fn = onlyFunc(t, "func f() { for k, v in m { x(k) } }")
	loop = fn.Body[0].(*For)
	if loop.KeyName != "k" || loop.ValName != "v" {
		t.Fatalf("two vars: key=%q val=%q", loop.KeyName, loop.ValName)
	}
}

func TestParseForRangeObjectLiteralCollection(t *testing.T) {
	// The object literal opens/closes at depth 1; the body brace terminates the
	// collection expression at depth 0.
	fn := onlyFunc(t, "func f() { for k, v in { a = 1 } { x(k, v) } }")
	loop := fn.Body[0].(*For)
	if loop.Kind != ForRange || loop.Collection == nil {
		t.Fatalf("expected range over object literal")
	}
	if len(loop.Body) != 1 {
		t.Fatalf("expected 1 body statement, got %d", len(loop.Body))
	}
}

func TestParseSwitch(t *testing.T) {
	fn := onlyFunc(t, `func f(s) {
        switch s {
        case 200, 201, 204:
            return "ok"
        case 404:
            return "missing"
        default:
            return "error"
        }
    }`)
	sw := fn.Body[0].(*Switch)
	if sw.Subject == nil {
		t.Fatalf("expected subject")
	}
	if len(sw.Clauses) != 3 {
		t.Fatalf("expected 3 clauses, got %d", len(sw.Clauses))
	}
	if len(sw.Clauses[0].Values) != 3 {
		t.Fatalf("first clause should have 3 values, got %d", len(sw.Clauses[0].Values))
	}
	if !sw.Clauses[2].IsDefault {
		t.Fatalf("expected the third clause to be the default")
	}
}

func TestParseSwitchExprless(t *testing.T) {
	fn := onlyFunc(t, `func f(n) {
        switch {
        case n > 0:
            return "pos"
        default:
            return "rest"
        }
    }`)
	sw := fn.Body[0].(*Switch)
	if sw.Subject != nil {
		t.Fatalf("expression-less switch should have nil subject")
	}
}

func TestParseBareBlock(t *testing.T) {
	fn := onlyFunc(t, "func f() { { var t = 1\n use(t) } }")
	if _, ok := fn.Body[0].(*Block); !ok {
		t.Fatalf("expected bare Block, got %T", fn.Body[0])
	}
}

func TestParseSemicolonSeparated(t *testing.T) {
	fn := onlyFunc(t, "func f() { var a = 1; var b = 2; use(a, b) }")
	if len(fn.Body) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(fn.Body))
	}
}

func TestParseLineContinuation(t *testing.T) {
	// A trailing binary operator continues the statement onto the next line.
	fn := onlyFunc(t, "func f() {\n  var x = 1 +\n    2 +\n    3\n  return x\n}")
	if len(fn.Body) != 2 {
		t.Fatalf("expected 2 statements, got %d", len(fn.Body))
	}
}

func TestParseTemplateAcrossExpr(t *testing.T) {
	parse(t, `func greet(name) { return "hello ${name}!" }`)
}

func TestParseMultiSourceConcept(t *testing.T) {
	res := parse(t, "func a() { return 1 }\nfunc b() { return 2 }")
	if len(res.Funcs) != 2 {
		t.Fatalf("expected 2 functions, got %d", len(res.Funcs))
	}
}

// ---- error cases ------------------------------------------------------------

func TestParseBreakOutsideLoop(t *testing.T) { parseErr(t, "func f() { break }") }

func TestParseContinueOutsideLoop(t *testing.T) { parseErr(t, "func f() { continue }") }

func TestParseUnreachable(t *testing.T) {
	parseErr(t, "func f() { return 1\n return 2 }")
}

// An "Unreachable code" diagnostic must underline the whole statement, not just its
// first token — an unreachable `x = compute()` should point at all of it, not only
// the target name (Assign) or the leading token (ExprStmt).
func TestUnreachableRangeSpansFullStatement(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string // the exact source text the diagnostic should underline
	}{
		{"assign", "func f() {\nreturn 1\nx = 2 + 3\n}", "x = 2 + 3"},
		{"exprstmt", "func f() {\nreturn 1\nlog(2 + 3)\n}", "log(2 + 3)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := NewParser().Parse([]byte(tc.src), "test")
			var d *hcl.Diagnostic
			for _, cand := range diags {
				if cand.Summary == "Unreachable code" {
					d = cand
					break
				}
			}
			if d == nil {
				t.Fatalf("no \"Unreachable code\" diagnostic in:\n%s", diags.Error())
			}
			if d.Subject == nil {
				t.Fatal("diagnostic has no Subject range")
			}
			got := tc.src[d.Subject.Start.Byte:d.Subject.End.Byte]
			if got != tc.want {
				t.Errorf("underlined %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseDuplicateVar(t *testing.T) {
	parseErr(t, "func f() { var x = 1\n var x = 2 }")
}

func TestParseRequiredAfterOptional(t *testing.T) {
	parseErr(t, "func f(a = 1, b) { return a }")
}

func TestParseVariadicNotLast(t *testing.T) {
	parseErr(t, "func f(*rest, a) { return a }")
}

func TestParseTopLevelVarRejectedByDefault(t *testing.T) {
	parseErr(t, "var x = 1")
}

func TestParseTopLevelVarAllowed(t *testing.T) {
	res, diags := NewParser().AllowTopLevelVar(true).Parse([]byte("var x: number = 1"), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	if len(res.Vars) != 1 || res.Vars[0].Name != "x" || ctyOf(res.Vars[0].Type) != cty.Number {
		t.Fatalf("var not collected correctly: %+v", res.Vars)
	}
}

func TestParseLocalConstRejected(t *testing.T) {
	parseErr(t, "func f() { const x = 1 }")
}

func TestParseThrowAndDefer(t *testing.T) {
	fn := onlyFunc(t, "func f() { defer cleanup()\n throw \"x\" }")
	if _, ok := fn.Body[0].(*Defer); !ok {
		t.Fatalf("stmt0 should be Defer, got %T", fn.Body[0])
	}
	if _, ok := fn.Body[1].(*Throw); !ok {
		t.Fatalf("stmt1 should be Throw, got %T", fn.Body[1])
	}
}

// hasFunc reports whether a parse Result contains a top-level function of the
// given name — used by the error-recovery tests to prove a declaration after a
// syntax error still parses.
func hasFunc(res *Result, name string) bool {
	for _, fn := range res.Funcs {
		if fn.Name == name {
			return true
		}
	}
	return false
}

// An unexpected panic on the parse path (a functy bug, not a stack overflow) is
// converted into an "Internal parser error" diagnostic and a usable, non-nil
// Result, rather than propagating into and killing the host. The panicInjectHook
// seam forces the panic a real bug would raise.
func TestParsePanicBackstop(t *testing.T) {
	defer func() { panicInjectHook = nil }()
	panicInjectHook = func(stage string) {
		if stage == "parse" {
			panic("injected parser bug")
		}
	}
	res, diags := NewParser().Parse([]byte("func f() { return 1 }"), "test")
	if !hasSummary(diags, "Internal parser error") {
		t.Fatalf("expected a recovered parser-panic diagnostic, got:\n%s", allDiags(diags))
	}
	if res == nil {
		t.Fatal("a recovered panic must still yield a non-nil Result")
	}
}

// A panic while formatting must never corrupt the file: Format returns src
// unchanged (fmt's core invariant) plus an "Internal formatter error" diagnostic.
func TestFormatPanicBackstop(t *testing.T) {
	defer func() { panicInjectHook = nil }()
	panicInjectHook = func(stage string) {
		if stage == "format" {
			panic("injected formatter bug")
		}
	}
	src := []byte("func f() {\n    return 1\n}\n")
	out, diags := NewParser().Format(src, "test")
	if !hasSummary(diags, "Internal formatter error") {
		t.Fatalf("expected a recovered formatter-panic diagnostic, got:\n%s", allDiags(diags))
	}
	if string(out) != string(src) {
		t.Fatalf("formatter must return src unchanged on panic, got:\n%q", out)
	}
}

// Pathologically deep nesting must produce a "Nesting too deep" diagnostic rather
// than overflow Go's stack — an uncatchable crash of the host. Covers both recursion
// vectors: functy's own statement parser (nested blocks) and HCL's expression/type
// parser, which functy hands spans to (nested parens, type constructors, alias RHS).
// The test completing at all proves no stack overflow occurred.
func TestParseNestingDepthGuards(t *testing.T) {
	deepExpr := strings.Repeat("(", maxExprDepth+50) + "1" + strings.Repeat(")", maxExprDepth+50)
	deepType := strings.Repeat("list(", maxExprDepth+50) + "string" + strings.Repeat(")", maxExprDepth+50)
	cases := []struct {
		name string
		src  string
	}{
		{"blocks", "func f() {\n" + strings.Repeat("{\n", maxBlockDepth+50)},
		{"parens", "func f() -> number { return " + deepExpr + " }"},
		{"type constructors", "func f(x: " + deepType + ") -> number { return 0 }"},
		{"alias rhs", "type T = " + deepType + "\nfunc f() -> number { return 0 }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := NewParser().AllowTopLevelVar(true).Parse([]byte(tc.src), "test")
			if !hasSummary(diags, "Nesting too deep") {
				t.Fatalf("expected a \"Nesting too deep\" diagnostic, got:\n%s", allDiags(diags))
			}
		})
	}
}

// A deeply-but-not-pathologically nested file still parses: the caps sit far above
// any realistic nesting, so a legitimate program is never rejected.
func TestParseModerateNestingStillParses(t *testing.T) {
	body := "return 0"
	for i := 0; i < 100; i++ {
		body = "if true {\n" + body + "\n}"
	}
	_, diags := NewParser().Parse([]byte("func f() -> number {\n"+body+"\n}"), "test")
	if diags.HasErrors() {
		t.Fatalf("a 100-deep-but-legit file should parse cleanly, got:\n%s", diags.Error())
	}
}

// A flat file (nesting depth 0) with far more than maxDiagnostics errors must not
// wedge the host: the returned diagnostics are capped with a summary of how many
// were suppressed, so a host rendering them (hcl's writer re-scans the source per
// diagnostic) does O(cap·n) work instead of O(n²). Flat input is the point — the
// depth cap from finding #1 does nothing here, since depth stays 0.
func TestParseDiagnosticFloodIsCapped(t *testing.T) {
	// A stray `)` at statement position is one error each; the body stays at nesting
	// depth 1, so this floods with flat input the depth cap cannot touch.
	src := "func f() {\n" + strings.Repeat(")\n", maxDiagnostics*5) + "}\n"
	_, diags := NewParser().Parse([]byte(src), "test")
	if len(diags) != maxDiagnostics+1 {
		t.Fatalf("got %d diagnostics, want %d (cap + summary)", len(diags), maxDiagnostics+1)
	}
	if !hasSummary(diags, "Too many diagnostics") {
		t.Fatalf("expected a \"Too many diagnostics\" summary, got:\n%s", allDiags(diags))
	}
}

// A file with fewer errors than the cap is returned in full, with no summary.
func TestParseDiagnosticsUnderCapNotTruncated(t *testing.T) {
	src := "func f() {\n" + strings.Repeat(")\n", 5) + "}\n"
	_, diags := NewParser().Parse([]byte(src), "test")
	if len(diags) == 0 {
		t.Fatal("expected diagnostics for malformed input")
	}
	if hasSummary(diags, "Too many diagnostics") {
		t.Fatalf("a small error count must not be capped, got:\n%s", allDiags(diags))
	}
}

// TestParseRecoverUnterminatedBrace verifies brace-aware recovery: an
// unterminated function body must not swallow the declarations after it. A
// bare `func` at statement position (closures are a non-goal) ends the runaway
// body so the following function still parses.
func TestParseRecoverUnterminatedBrace(t *testing.T) {
	src := "func broken() {\n    return 2\n\nfunc after() {\n    return 3\n}\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if !diags.HasErrors() {
		t.Fatal("expected a diagnostic for the unterminated brace")
	}
	if !hasFunc(res, "broken") {
		t.Errorf("the broken function itself should still be recovered")
	}
	if !hasFunc(res, "after") {
		t.Fatalf("recovery failed: 'after' was swallowed by the unterminated body")
	}
	// The runaway body must not have absorbed after's statements as its own.
	for _, fn := range res.Funcs {
		if fn.Name == "broken" && len(fn.Body) != 1 {
			t.Errorf("broken body should hold only its own `return 2`, got %d statements", len(fn.Body))
		}
	}
}

// TestParseRecoverUnterminatedString verifies lexer resync: an unterminated
// quoted string must not swallow the rest of the file into string content.
func TestParseRecoverUnterminatedString(t *testing.T) {
	src := "func broken() -> string {\n    return \"oops\n}\n\nfunc after() {\n    return 3\n}\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if !diags.HasErrors() {
		t.Fatal("expected a diagnostic for the unterminated string")
	}
	if !hasFunc(res, "after") {
		t.Fatalf("recovery failed: 'after' was swallowed by the unterminated string")
	}
}
