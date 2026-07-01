package functy

import (
	"errors"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// TestOutcome is the result of running one `test` block. A test passes when its body
// runs to completion and fails when an error (a failed assert, a throw, or an eval
// error) unwinds out of it; Err carries that failure (nil on pass).
type TestOutcome struct {
	Name     string    // the test's description
	DefRange hcl.Range // the test block's source location
	Err      error     // nil if the test passed
}

// Passed reports whether the test ran to completion without error.
func (o TestOutcome) Passed() bool { return o.Err == nil }

// Diagnostics renders a failed test for the standard hcl diagnostic writer: a thrown
// error (a failed assert or an explicit throw) with its source underline and operand
// detail, or — for any other failure — a plain diagnostic located at the test block.
// It returns nil for a passing test.
func (o TestOutcome) Diagnostics() hcl.Diagnostics {
	if o.Err == nil {
		return nil
	}
	var te *ThrownError
	if errors.As(o.Err, &te) {
		return te.Diagnostics()
	}
	rng := o.DefRange
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  o.Err.Error(),
		Subject:  &rng,
	}}
}

// RunTests builds and runs each test block in source order against the given eval
// context, returning one outcome per test. Each test body is compiled like a niladic
// function (via BuildFunction) and called; a failed assert/throw surfaces as a
// *ThrownError. The eval context should be the same one the compiled functions
// late-bind to, so a test body can call the functions under test and use the baseline
// functions (assert, etc.).
func (r *Result) RunTests(evalCtxFn func() *hcl.EvalContext) []TestOutcome {
	outcomes := make([]TestOutcome, 0, len(r.Tests))
	for _, td := range r.Tests {
		fn := BuildFunction(&FuncDecl{Name: td.Name, Body: td.Body}, evalCtxFn)
		_, err := fn.Call([]cty.Value{})
		outcomes = append(outcomes, TestOutcome{Name: td.Name, DefRange: td.DefRange, Err: err})
	}
	return outcomes
}
