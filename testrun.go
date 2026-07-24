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

// testBuiltins returns the builtins that are meaningful only inside a test and so
// are injected into each test's eval context by RunTests (not part of Stdlib):
// skip, plus the eventually/never polling assertions. Rebuilt per call because it
// is layered into a fresh private child context each time (see RunTestsMatching),
// keeping it off the caller's shared Functions map.
func testBuiltins() map[string]function.Function {
	return map[string]function.Function{
		"skip":       skipFunc,
		"eventually": eventuallyFunc,
		"never":      neverFunc,
	}
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
// but any helper functions it calls, which late-bind to this same context. Rather
// than write `skip` into the caller's shared Functions map (a data race against any
// concurrent evaluation using the same context, since compiled functions late-bind
// to it), it is layered into a private child context that becomes the parent of
// every test body. The caller's context is never mutated, keeping `skip` a test-only
// builtin (absent from `functy run` / `check`).
func (r *Result) RunTestsMatching(evalCtxFn func() *hcl.EvalContext, filter func(name string) bool) []TestOutcome {
	// Wrap evalCtxFn so it hands back a child context carrying only `skip`, chained to
	// the caller's context. HCL walks the chain, so everything else — baseline
	// functions, global vars — still resolves from the parent, and `skip` never
	// touches the caller's map. Late-bound: rebuilt per call, like unitCtxFn.
	skipCtxFn := evalCtxFn
	if evalCtxFn != nil {
		skipCtxFn = func() *hcl.EvalContext {
			parent := evalCtxFn()
			if parent == nil {
				return &hcl.EvalContext{Functions: testBuiltins()}
			}
			child := parent.NewChild()
			child.Functions = testBuiltins()
			return child
		}
	}

	// A test body belongs to its file's namespace, so it must resolve that
	// namespace's functions — including its private ones — by their bare names,
	// exactly as a function in the same file would. Build the unit layers against
	// *this* evalCtxFn rather than reusing any from an earlier Compile: RunTests is
	// handed its own context function, and binding test bodies to a compile-time
	// closure instead would be a silent, hard-to-see bug. Diagnostics are dropped
	// here — duplicates are reported by the Compile every caller already runs.
	//
	// `skip` still resolves: skipCtxFn's child sits between the caller's context and
	// each unit layer, and HCL walks the whole chain. (A namespace may itself declare
	// `func skip()`, which shadows it for that namespace's tests — a consequence of
	// local-wins, documented in doc/language.md.)
	compiled, _ := r.CompileUnits(skipCtxFn)

	// `test setup` blocks are shared setup spliced onto the front of every test in the
	// *same file*, in source order. Group their bodies by filename here so each test
	// prepends only its own file's setup (a file has one namespace, so the spliced
	// statements resolve names in the test's namespace as if written inline).
	setupByFile := map[string][]Statement{}
	for _, sd := range r.Setups {
		f := sd.DefRange.Filename
		setupByFile[f] = append(setupByFile[f], sd.Body...)
	}

	outcomes := make([]TestOutcome, 0, len(r.Tests))
	for _, td := range r.Tests {
		if filter != nil && !filter(td.Name) {
			continue
		}
		bodyCtxFn := skipCtxFn
		if table, ok := compiled.Units[td.Namespace]; ok {
			bodyCtxFn = unitCtxFn(skipCtxFn, table, compiled.Vars, td.Namespace)
		}
		// Splice this file's setup ahead of the test body (same scope): its bindings
		// are visible to the test, and its function-scoped `defer`s run at the test's
		// end (LIFO, so after the test's own defers). A failed assert / skip / throw in
		// setup fails / skips the test, since it is now part of the test function.
		body := td.Body
		if pre := setupByFile[td.DefRange.Filename]; len(pre) > 0 {
			body = append(append(make([]Statement, 0, len(pre)+len(td.Body)), pre...), td.Body...)
		}
		// Bound test bodies by the same ceiling as normal functions, so a runaway
		// loop in a test surfaces as a (non-skipped) *LimitError rather than hanging.
		fn := BuildFunction(&FuncDecl{Name: td.Name, Namespace: td.Namespace, Body: body}, bodyCtxFn, r.maxSteps)
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
