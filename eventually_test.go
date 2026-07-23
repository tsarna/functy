package functy

import (
	"errors"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// runTestsWithEnv parses and compiles src, then runs its test blocks against an
// eval context carrying the stdlib plus the given extra functions and variables.
// The extra functions stand in for host state that changes between polls (what an
// asynchronous writer would mutate in a real runtime).
func runTestsWithEnv(t *testing.T, src string, funcs map[string]function.Function, vars map[string]cty.Value) []TestOutcome {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	compiled, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile errors:\n%s", cdiags.Error())
	}
	all := testStdlib()
	for k, v := range Stdlib() {
		all[k] = v
	}
	for k, v := range compiled {
		all[k] = v
	}
	for k, v := range funcs {
		all[k] = v
	}
	if vars == nil {
		vars = map[string]cty.Value{}
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: vars}
	return res.RunTests(func() *hcl.EvalContext { return ctx })
}

// trueAfter returns a niladic host function returning false for its first
// (threshold-1) calls and true thereafter — a stand-in for state an async writer
// flips to true after a short delay, observed by re-evaluation.
func trueAfter(threshold int) function.Function {
	calls := 0
	return function.New(&function.Spec{
		Type: function.StaticReturnType(cty.Bool),
		Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
			calls++
			return cty.BoolVal(calls >= threshold), nil
		},
	})
}

func TestEventuallySucceedsWhenConditionBecomesTrue(t *testing.T) {
	outcomes := runTestsWithEnv(t,
		`test "converges" { eventually(ready(), "2s") }`,
		map[string]function.Function{"ready": trueAfter(3)}, nil)
	if len(outcomes) != 1 || !outcomes[0].Passed() {
		t.Fatalf("expected 1 passing test, got %+v (err=%v)", outcomes, outcomes[0].Err)
	}
}

func TestEventuallyFailsOnTimeout(t *testing.T) {
	outcomes := runTestsWithEnv(t,
		`test "never converges" { eventually(false, "20ms") }`, nil, nil)
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes", len(outcomes))
	}
	if !outcomes[0].Failed() {
		t.Fatalf("expected failure, got %+v", outcomes[0])
	}
	var te *ThrownError
	if !errors.As(outcomes[0].Err, &te) {
		t.Fatalf("failure should be *ThrownError, got %T", outcomes[0].Err)
	}
	if msg := te.Value.GetAttr("message").AsString(); !strings.Contains(msg, "did not hold") {
		t.Fatalf("message = %q, want it to mention 'did not hold'", msg)
	}
	// The failure carries a source range so it renders with an underline.
	if d := outcomes[0].Diagnostics(); len(d) != 1 || d[0].Subject == nil {
		t.Fatalf("expected a diagnostic with a subject range, got %+v", d)
	}
}

func TestEventuallyTimeoutRendersOperands(t *testing.T) {
	// A condition over a (static, here) variable: on timeout the failure enriches
	// with the referenced variable's value, exactly like a failed assert.
	outcomes := runTestsWithEnv(t,
		`test "operands" { eventually(flag == true, "20ms") }`, nil,
		map[string]cty.Value{"flag": cty.False})
	if len(outcomes) != 1 || !outcomes[0].Failed() {
		t.Fatalf("expected failure, got %+v", outcomes)
	}
	var te *ThrownError
	if !errors.As(outcomes[0].Err, &te) {
		t.Fatalf("failure should be *ThrownError, got %T", outcomes[0].Err)
	}
	if !te.Value.Type().HasAttribute("detail") {
		t.Fatalf("expected operand detail on the error, got %#v", te.Value)
	}
	if detail := te.Value.GetAttr("detail").AsString(); !strings.Contains(detail, "flag = false") {
		t.Fatalf("detail = %q, want it to mention 'flag = false'", detail)
	}
}

func TestEventuallyPollsNullCondition(t *testing.T) {
	// `x == "foo"` yields null while x is null; that must poll (and here time out
	// as an ordinary assertion failure), not raise a "condition must be boolean"
	// error. This is the get(var.x) idiom's shape.
	outcomes := runTestsWithEnv(t,
		`test "null polls" { eventually(x == "foo", "20ms") }`, nil,
		map[string]cty.Value{"x": cty.NullVal(cty.String)})
	if len(outcomes) != 1 || !outcomes[0].Failed() {
		t.Fatalf("expected failure, got %+v", outcomes)
	}
	var te *ThrownError
	if !errors.As(outcomes[0].Err, &te) {
		t.Fatalf("null condition should time out as a *ThrownError, got %T: %v", outcomes[0].Err, outcomes[0].Err)
	}
	if msg := te.Value.GetAttr("message").AsString(); !strings.Contains(msg, "did not hold") {
		t.Fatalf("message = %q, want a timeout failure", msg)
	}
}

func TestNeverSucceedsWhenConditionStaysFalse(t *testing.T) {
	outcomes := runTestsWithEnv(t,
		`test "stays false" { never(false, "20ms") }`, nil, nil)
	if len(outcomes) != 1 || !outcomes[0].Passed() {
		t.Fatalf("expected 1 passing test, got %+v (err=%v)", outcomes, outcomes[0].Err)
	}
}

func TestNeverFailsWhenConditionBecomesTrue(t *testing.T) {
	outcomes := runTestsWithEnv(t,
		`test "becomes true" { never(oops(), "2s") }`,
		map[string]function.Function{"oops": trueAfter(3)}, nil)
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes", len(outcomes))
	}
	if !outcomes[0].Failed() {
		t.Fatalf("expected failure, got %+v", outcomes[0])
	}
	var te *ThrownError
	if !errors.As(outcomes[0].Err, &te) {
		t.Fatalf("failure should be *ThrownError, got %T", outcomes[0].Err)
	}
	if msg := te.Value.GetAttr("message").AsString(); !strings.Contains(msg, "became true") {
		t.Fatalf("message = %q, want it to mention 'became true'", msg)
	}
}

func TestPollDurationAcceptsNumberSeconds(t *testing.T) {
	// A bare number is interpreted as seconds; 0 means a single check, so a
	// never-true condition times out immediately without hanging the test.
	outcomes := runTestsWithEnv(t, `test "num" { eventually(false, 0) }`, nil, nil)
	if len(outcomes) != 1 || !outcomes[0].Failed() {
		t.Fatalf("expected a timeout failure, got %+v", outcomes)
	}
}

func TestPollRejectsBadDuration(t *testing.T) {
	outcomes := runTestsWithEnv(t, `test "bad" { eventually(true, "not-a-duration") }`, nil, nil)
	if len(outcomes) != 1 || !outcomes[0].Failed() {
		t.Fatalf("expected failure on bad duration, got %+v", outcomes)
	}
	// A malformed duration is a plain error (not a thrown assertion).
	var te *ThrownError
	if errors.As(outcomes[0].Err, &te) {
		t.Fatalf("bad duration should be a plain error, not a *ThrownError")
	}
}

func TestPollBuiltinsAbsentFromStdlib(t *testing.T) {
	// eventually/never are test-only: not present in the plain stdlib surface.
	if _, ok := Stdlib()["eventually"]; ok {
		t.Fatal("eventually must not be in Stdlib()")
	}
	if _, ok := Stdlib()["never"]; ok {
		t.Fatal("never must not be in Stdlib()")
	}
}
