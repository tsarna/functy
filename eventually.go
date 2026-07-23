package functy

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2/ext/customdecode"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// defaultPollInterval is the pause between condition re-evaluations for
// eventually/never when no explicit interval is given. Small enough that a test
// converges quickly once the awaited state lands, large enough not to spin.
const defaultPollInterval = 10 * time.Millisecond

// eventuallyFunc is the test-scoped `eventually(condition, timeout, interval?)`
// builtin: it re-evaluates the condition until it holds or the timeout elapses,
// failing (like assert) if it never held. The condition is received unevaluated
// (a lazy closure, exactly like assert) so it can be re-evaluated on every poll
// and so its source range and referenced-variable values enrich a timeout
// failure. It is meaningful only over state that changes between evaluations —
// in Vinculum, a `var` capsule mutated by an asynchronous subscription, read via
// `get(var.x)` — since re-evaluating an expression over immutable locals can
// never change its result.
//
// Injected into each test's eval context by RunTests (not part of Stdlib), so it
// is available only inside a test, alongside skip and never.
var eventuallyFunc = function.New(&function.Spec{
	Description: "Poll a condition until it holds or a timeout elapses: eventually(cond, timeout, interval?). Fails (like assert, with the condition's range and operands) if it never held. timeout/interval are duration strings (\"250ms\", \"1s\") or a number of seconds.",
	Params: []function.Parameter{
		{Name: "condition", Type: customdecode.ExpressionClosureType, Description: "The condition to await; re-evaluated each poll until it is true"},
		{Name: "timeout", Type: cty.DynamicPseudoType, Description: "How long to keep polling: a duration string or a number of seconds"},
	},
	VarParam: &function.Parameter{
		Name:        "interval",
		Type:        cty.DynamicPseudoType,
		Description: "Optional poll interval (duration string or seconds); defaults to 10ms",
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 3 {
			return cty.NilType, fmt.Errorf("eventually takes at most 3 arguments (a condition, a timeout, and an optional interval), got %d", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		return pollCondition(args, false, "eventually")
	},
})

// neverFunc is the test-scoped `never(condition, timeout, interval?)` builtin —
// the inverse of eventually. It polls the condition for the whole window and
// succeeds only if it never became true; it fails (like assert) the instant the
// condition holds. Use it to assert an async effect does *not* happen — e.g. a
// message the router should have dropped never reaches a recording sink.
//
// Injected into each test's eval context by RunTests, alongside skip and
// eventually.
var neverFunc = function.New(&function.Spec{
	Description: "Assert a condition stays false for a whole window: never(cond, timeout, interval?). Polls the condition and fails (like assert) the instant it becomes true; succeeds if it never did. timeout/interval are duration strings or a number of seconds.",
	Params: []function.Parameter{
		{Name: "condition", Type: customdecode.ExpressionClosureType, Description: "The condition that must stay false; re-evaluated each poll"},
		{Name: "timeout", Type: cty.DynamicPseudoType, Description: "How long to keep checking: a duration string or a number of seconds"},
	},
	VarParam: &function.Parameter{
		Name:        "interval",
		Type:        cty.DynamicPseudoType,
		Description: "Optional poll interval (duration string or seconds); defaults to 10ms",
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 3 {
			return cty.NilType, fmt.Errorf("never takes at most 3 arguments (a condition, a timeout, and an optional interval), got %d", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		return pollCondition(args, true, "never")
	},
})

// pollCondition implements the shared poll loop for eventually and never. It
// re-evaluates the condition closure every interval until the deadline. For
// eventually (failWhenTrue=false) a true condition is success and the deadline is
// failure; for never (failWhenTrue=true) a true condition is failure and the
// deadline is success. A failure carries the condition's range and referenced-
// variable operands, exactly like a failed assert; a condition that fails to
// evaluate (e.g. a throw from a called function) propagates that error rather
// than reporting a poll failure.
func pollCondition(args []cty.Value, failWhenTrue bool, fnName string) (cty.Value, error) {
	cond := customdecode.ExpressionClosureFromVal(args[0])
	timeout, err := parsePollDuration(args[1], fnName+": timeout")
	if err != nil {
		return cty.NilVal, err
	}
	interval := defaultPollInterval
	if len(args) == 3 {
		interval, err = parsePollDuration(args[2], fnName+": interval")
		if err != nil {
			return cty.NilVal, err
		}
	}
	if interval <= 0 {
		interval = defaultPollInterval
	}

	deadline := time.Now().Add(timeout)
	for {
		cv, diags := cond.Value()
		if diags.HasErrors() {
			// The condition itself failed to evaluate — surface that error
			// (recovering a structured throw), not a poll failure.
			if tv, ok := thrownValueFromDiags(diags); ok {
				return cty.NilVal, &ThrownError{Value: tv}
			}
			return cty.NilVal, diagsToError(fnName+": condition", diags)
		}
		holds, err := pollConditionTrue(cv)
		if err != nil {
			return cty.NilVal, fmt.Errorf("%s: %w", fnName, err)
		}
		if holds {
			if failWhenTrue {
				return cty.NilVal, assertionError(cond.Expression, cond.EvalContext,
					cty.StringVal(fmt.Sprintf("%s: condition became true within %s", fnName, timeout)))
			}
			return cty.True, nil
		}
		if !time.Now().Before(deadline) {
			if failWhenTrue {
				return cty.True, nil
			}
			return cty.NilVal, assertionError(cond.Expression, cond.EvalContext,
				cty.StringVal(fmt.Sprintf("%s: condition did not hold within %s", fnName, timeout)))
		}
		sleep := interval
		if remaining := time.Until(deadline); remaining < sleep {
			sleep = remaining
		}
		time.Sleep(sleep)
	}
}

// pollConditionTrue reports whether a polled condition value counts as satisfied.
// Unlike assert's stricter check, a null or not-yet-known result is treated as
// "not satisfied yet" (keep polling) rather than an error — the common idiom
// `get(var.x) == "foo"` yields null while var.x is still null, and that must poll
// rather than fail. Only a genuinely non-boolean value is an error.
func pollConditionTrue(cv cty.Value) (bool, error) {
	if cv.IsNull() {
		return false, nil
	}
	bv, err := convert.Convert(cv, cty.Bool)
	if err != nil {
		return false, fmt.Errorf("condition must be boolean: %w", err)
	}
	if bv.IsNull() || !bv.IsKnown() {
		return false, nil
	}
	return bv.True(), nil
}

// parsePollDuration reads a timeout/interval argument as a time.Duration. It
// accepts a Go-parseable duration string ("250ms", "1s", "2m") or a number of
// seconds (fractional allowed). It deliberately does not depend on any host
// duration capsule type, keeping these builtins host-agnostic.
func parsePollDuration(v cty.Value, label string) (time.Duration, error) {
	if v.IsNull() {
		return 0, fmt.Errorf("%s must not be null", label)
	}
	switch v.Type() {
	case cty.String:
		d, err := time.ParseDuration(v.AsString())
		if err != nil {
			return 0, fmt.Errorf("%s: invalid duration %q: %w", label, v.AsString(), err)
		}
		return d, nil
	case cty.Number:
		secs, _ := v.AsBigFloat().Float64()
		return time.Duration(secs * float64(time.Second)), nil
	default:
		if s, err := convert.Convert(v, cty.String); err == nil && !s.IsNull() {
			if d, perr := time.ParseDuration(s.AsString()); perr == nil {
				return d, nil
			}
		}
		return 0, fmt.Errorf("%s must be a duration string (e.g. \"1s\") or a number of seconds", label)
	}
}
