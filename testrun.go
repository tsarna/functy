package functy

import (
	"errors"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// TestOutcome is the result of running one `test` block. A test passes when its body
// runs to completion, is skipped when a `skip(...)` call unwinds out of it, and fails
// on any other error (a failed assert, a throw, an eval error). Passed/Skipped/Failed
// are three disjoint states.
type TestOutcome struct {
	Name       string        // the test's description
	DefRange   hcl.Range     // the test block's source location
	Duration   time.Duration // wall-clock time to run the body
	Err        error         // nil if the test passed; the skip or failure otherwise
	Skipped    bool          // true if Err is a skip (not a failure)
	SkipReason string        // the reason passed to skip(), if any
}

// Passed reports whether the test ran to completion without error.
func (o TestOutcome) Passed() bool { return o.Err == nil }

// Failed reports whether the test ended in a real failure (not a skip).
func (o TestOutcome) Failed() bool { return o.Err != nil && !o.Skipped }

// Diagnostics renders a failed test for the standard hcl diagnostic writer: a thrown
// error (a failed assert or an explicit throw) with its source underline and operand
// detail, or — for any other failure — a plain diagnostic located at the test block.
// It returns nil for a passing or skipped test.
func (o TestOutcome) Diagnostics() hcl.Diagnostics {
	if o.Err == nil || o.Skipped {
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

// skipFunc is the test-scoped `skip("reason"?)` builtin: it raises a *SkipError that
// unwinds the test body and marks the test skipped rather than passed or failed. It is
// injected into each test's eval context by RunTests (not part of Stdlib), so `skip`
// is meaningful only inside a test.
var skipFunc = function.New(&function.Spec{
	Description: "Skip the current test, optionally with a reason: skip() or skip(\"why\").",
	VarParam:    &function.Parameter{Name: "reason", Type: cty.String, AllowNull: true},
	Type:        function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		if len(args) > 1 {
			return cty.NilVal, errors.New("skip takes at most one argument (an optional reason)")
		}
		reason := ""
		if len(args) == 1 && !args[0].IsNull() {
			reason = args[0].AsString()
		}
		return cty.NilVal, &SkipError{Reason: reason}
	},
})

// RunTests builds and runs every test block in source order against the given eval
// context. See RunTestsMatching for the details; this runs all tests.
func (r *Result) RunTests(evalCtxFn func() *hcl.EvalContext) []TestOutcome {
	return r.RunTestsMatching(evalCtxFn, nil)
}

// RunTestsMatching runs each test block whose name passes filter (nil runs all), in
// source order, returning one outcome per test that ran. Each test body is compiled
// like a niladic function (via BuildFunction) and called; a failed assert/throw
// surfaces as a *ThrownError and a skip(...) as a *SkipError. The test body sees the
// given eval context (all compiled functions + baseline).
//
// skip must be visible throughout a test's call graph — not just the top-level body
// but any helper functions it calls, which late-bind to this same context — so it is
// injected into the shared context for the duration of the run and restored after,
// keeping `skip` a test-only builtin (absent from `functy run` / `check`).
func (r *Result) RunTestsMatching(evalCtxFn func() *hcl.EvalContext, filter func(name string) bool) []TestOutcome {
	if ctx := evalCtxFn(); ctx != nil {
		if ctx.Functions == nil {
			ctx.Functions = map[string]function.Function{}
		}
		prev, had := ctx.Functions["skip"]
		ctx.Functions["skip"] = skipFunc
		defer func() {
			if had {
				ctx.Functions["skip"] = prev
			} else {
				delete(ctx.Functions, "skip")
			}
		}()
	}

	outcomes := make([]TestOutcome, 0, len(r.Tests))
	for _, td := range r.Tests {
		if filter != nil && !filter(td.Name) {
			continue
		}
		fn := BuildFunction(&FuncDecl{Name: td.Name, Body: td.Body}, evalCtxFn)
		start := time.Now()
		_, err := fn.Call([]cty.Value{})
		o := TestOutcome{Name: td.Name, DefRange: td.DefRange, Duration: time.Since(start), Err: err}
		var se *SkipError
		if errors.As(err, &se) {
			o.Skipped = true
			o.SkipReason = se.Reason
		}
		outcomes = append(outcomes, o)
	}
	return outcomes
}
