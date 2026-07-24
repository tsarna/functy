package functy

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/customdecode"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// softSink accumulates the failures recorded by soft assertions (expect) during one
// test. The runner installs a fresh sink per test (as a capsule value in the test's
// eval context) and inspects it after the test body returns; see RunTestsMatching.
type softSink struct {
	failures []*ThrownError
}

// softSinkCapsuleType carries a *softSink through the eval context. It is never
// compared or converted — only stashed by the runner and read back by expect — so a
// plain capsule (no ops) suffices.
var softSinkCapsuleType = cty.Capsule("test sink", reflect.TypeOf(softSink{}))

// testSinkVar is the reserved eval-context variable under which the per-test soft
// failure sink is installed. The `$` prefix marks it internal (no valid functy
// identifier begins with `$`, so user code can neither name nor collide with it).
const testSinkVar = "$test"

// sinkVars returns the eval-context variables carrying the per-test soft failure sink,
// or nil when there is no sink (so HCL adds nothing). The runner layers this onto the
// same child that carries the test-only builtins.
func sinkVars(s *softSink) map[string]cty.Value {
	if s == nil {
		return nil
	}
	return map[string]cty.Value{testSinkVar: cty.CapsuleVal(softSinkCapsuleType, s)}
}

// sinkFromCtx walks the eval-context parent chain for the per-test soft failure sink,
// returning nil when there is none (i.e. not running under the test runner).
func sinkFromCtx(ctx *hcl.EvalContext) *softSink {
	for c := ctx; c != nil; c = c.Parent() {
		if v, ok := c.Variables[testSinkVar]; ok && !v.IsNull() {
			if s, ok := v.EncapsulatedValue().(*softSink); ok {
				return s
			}
		}
	}
	return nil
}

// expectFunc is the test-only soft assertion: the non-fatal twin of assert (the gtest
// EXPECT_* vs ASSERT_* distinction). On a false condition it records the failure — the
// same enriched error value assert would raise — into the current test's sink and
// returns false, so the test keeps running and every failure is reported at the end;
// on success it returns true. It is injected into each test's eval context by
// RunTests (see testBuiltins), so — like skip/eventually/never — it exists only while
// running tests.
//
// Usage: expect(cond, message?). The condition and optional message are lazy closures,
// exactly like assert, so the condition's source range and operand values enrich the
// recorded failure and the message is evaluated only on failure. A condition that
// fails to evaluate propagates that error (recovering a structured throw) as a hard
// failure, matching assert.
var expectFunc = function.New(&function.Spec{
	Description: "Soft assertion (test-only): expect(cond, message?). Like assert, but on failure it records the failure and returns false instead of aborting, so a test reports every failed expectation. The optional message (string or object, like error()) is evaluated only on failure.",
	Params: []function.Parameter{
		{Name: "condition", Type: customdecode.ExpressionClosureType, Description: "The condition to check; a failure is recorded (not raised) when it is false"},
	},
	VarParam: &function.Parameter{
		Name:        "message",
		Type:        customdecode.ExpressionClosureType,
		Description: "Optional failure message (string or object, like error()), evaluated only when the expectation fails",
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 2 {
			return cty.NilType, fmt.Errorf("expect takes at most 2 arguments (a condition and an optional message), got %d", len(args))
		}
		return cty.Bool, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		condClosure := customdecode.ExpressionClosureFromVal(args[0])
		cv, diags := condClosure.Value()
		if diags.HasErrors() {
			// The condition itself failed to evaluate — a hard failure, surfaced
			// exactly as assert does (recovering a structured throw).
			if tv, ok := thrownValueFromDiags(diags); ok {
				return cty.NilVal, &ThrownError{Value: tv}
			}
			return cty.NilVal, diagsToError("expect: condition", diags)
		}
		bv, err := convert.Convert(cv, cty.Bool)
		if err != nil {
			return cty.NilVal, fmt.Errorf("expect: condition must be boolean: %w", err)
		}
		if bv.IsNull() {
			return cty.NilVal, errors.New("expect: condition is null")
		}
		if !bv.IsKnown() {
			return cty.NilVal, errors.New("expect: condition is not known")
		}
		if bv.True() {
			return cty.True, nil
		}

		// Failed: build the same enriched error value assert would raise. The message
		// (string or object) is evaluated only now, on failure.
		msg := cty.StringVal("expectation failed")
		if len(args) == 2 {
			mv, mdiags := customdecode.ExpressionClosureFromVal(args[1]).Value()
			if mdiags.HasErrors() {
				if tv, ok := thrownValueFromDiags(mdiags); ok {
					return cty.NilVal, &ThrownError{Value: tv}
				}
				return cty.NilVal, diagsToError("expect: message", mdiags)
			}
			msg = mv
		}
		failure := assertionError(condClosure.Expression, condClosure.EvalContext, msg)

		// Record and continue. Outside a test there is no sink (expect is test-only, so
		// this should not happen); fail hard rather than silently discard the failure.
		sink := sinkFromCtx(condClosure.EvalContext)
		if sink == nil {
			return cty.NilVal, failure
		}
		sink.failures = append(sink.failures, failure)
		return cty.False, nil
	},
})
