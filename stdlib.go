package functy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/customdecode"
	"github.com/hashicorp/hcl/v2/ext/tryfunc"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// Stdlib returns functy's language-level standard library: host-agnostic,
// dependency-free builtins that make HCL expressions more capable than raw HCL.
// A host merges these into its eval context (alongside the cty stdlib and its own
// functions), so they are available anywhere the host evaluates expressions — not
// only inside functy function bodies.
//
//   - typeof(v)                     type in functy's annotation grammar
//   - typekind(v)                   top-level type kind (for dispatch)
//   - cond(c1, r1, …, else)         lazy multi-branch conditional (single-eval)
//   - switch(on, v1, r1, …, def?)   lazy value dispatch (single-eval)
//   - error(v)                      raise an error from an expression
//   - assert(cond, message?)        raise a catchable error when cond is false
//
// The name-colliding, opt-in try/can live in StdlibExtras() instead.
func Stdlib() map[string]function.Function {
	return map[string]function.Function{
		"typeof":   typeOfFunc,
		"typekind": typeKindFunc,
		"cond":     condFunc,
		"switch":   switchFunc,
		"error":    errorFunc,
		"assert":   assertFunc,
	}
}

// StdlibExtras returns the opt-in builtins whose names collide with HCL's stock
// tryfunc:
//
//   - try(e1, e2, …)   first expression that evaluates without error (single-eval)
//   - can(e)           whether an expression evaluates without error
//
// They are kept separate from Stdlib() so a host already exposing a try/can (e.g.
// from hcl/v2/ext/tryfunc, whose try double-evaluates the winning branch) is not
// silently overridden.
func StdlibExtras() map[string]function.Function {
	return map[string]function.Function{
		"try": tryFunc,
		"can": tryfunc.CanFunc,
	}
}

// typeOfFunc returns a value's type in functy's own annotation grammar (e.g.
// list(string), object({ a = string })), so it round-trips through the resolver.
var typeOfFunc = function.New(&function.Spec{
	Description: "Returns the type of a value in functy's annotation grammar (e.g. list(string), object({ a = string }))",
	Params: []function.Parameter{
		// AllowDynamicType so the function accepts a value of any type *and* cty does not
		// poison the return to dynamic — a dynamic argument otherwise hides the static
		// string return in reflected metadata (help/doc). Reading .Type() is safe on a
		// dynamic value (it yields "any").
		{Name: "value", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true, Description: "Any value; its type is what is returned"},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.StringVal(TypeString(args[0].Type())), nil
	},
})

// typeKindFunc returns a value's top-level type kind (string, number, list,
// object, …), dropping element/attribute detail — handy for dispatch.
var typeKindFunc = function.New(&function.Spec{
	Description: "Returns the top-level type kind of a value (string, number, list, object, …), for dispatch",
	Params: []function.Parameter{
		// AllowDynamicType: see typeOfFunc — it keeps the static string return visible in
		// reflected metadata rather than poisoned to dynamic by a dynamic argument.
		{Name: "value", Type: cty.DynamicPseudoType, AllowNull: true, AllowDynamicType: true, Description: "Any value; its top-level type kind is what is returned"},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		return cty.StringVal(typeKind(args[0].Type())), nil
	},
})

// errorFunc raises an error from expression position — the expression-form of the
// throw statement. Its single lazy parameter lets it capture the argument's source
// range; the thrown value is normalized through errorValue and carried by a
// *ThrownError, so it composes with try/catch and `val, err =` and accepts an
// object (error({ message, code })) just like throw.
var errorFunc = function.New(&function.Spec{
	Description: "Raises an error with the given value (string or object), usable in expression position",
	Params: []function.Parameter{
		{Name: "value", Type: customdecode.ExpressionClosureType, Description: "The error to raise: a string message, or an object like { message = ..., code = ... }"},
	},
	Type: function.StaticReturnType(cty.DynamicPseudoType),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		closure := customdecode.ExpressionClosureFromVal(args[0])
		v, diags := closure.Value()
		if diags.HasErrors() {
			return cty.NilVal, diagsToError("error", diags)
		}
		return cty.NilVal, &ThrownError{Value: errorValue(v, closure.Expression.Range())}
	},
})

// assertFunc raises a catchable error when a condition is false — the
// expression-position form of a runtime check. Like error() it composes with
// try/catch and `val, err =`, and carries a source range: the range of the
// *condition* expression, so a surfaced diagnostic underlines exactly what failed.
//
// Usage: assert(cond, message?). The condition is received unevaluated (a lazy
// closure) so assert can capture its range; on success assert returns true. The
// optional message — a string or object, exactly like error()/throw — is itself
// lazy, so it is evaluated only when the assertion fails; without one the error
// message is "assertion failed". A condition that fails to evaluate propagates that
// error (recovering a structured throw) rather than reporting an assertion failure.
var assertFunc = function.New(&function.Spec{
	Description: "Assert that a condition holds: assert(cond, message?). Raises a catchable error (carrying the condition's source range) when cond is false; returns true otherwise. The optional message (string or object, like error()) is evaluated only on failure.",
	Params: []function.Parameter{
		{Name: "condition", Type: customdecode.ExpressionClosureType, Description: "The condition to check; a catchable error is raised when it is false"},
	},
	VarParam: &function.Parameter{
		Name:        "message",
		Type:        customdecode.ExpressionClosureType,
		Description: "Optional failure message (string or object, like error()), evaluated only when the assertion fails",
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) > 2 {
			return cty.NilType, fmt.Errorf("assert takes at most 2 arguments (a condition and an optional message), got %d", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		condClosure := customdecode.ExpressionClosureFromVal(args[0])
		cv, diags := condClosure.Value()
		if diags.HasErrors() {
			// The condition itself failed to evaluate — surface that error
			// (recovering a structured throw), not an assertion failure.
			if tv, ok := thrownValueFromDiags(diags); ok {
				return cty.NilVal, &ThrownError{Value: tv}
			}
			return cty.NilVal, diagsToError("assert: condition", diags)
		}
		bv, err := convert.Convert(cv, cty.Bool)
		if err != nil {
			return cty.NilVal, fmt.Errorf("assert: condition must be boolean: %w", err)
		}
		if bv.IsNull() {
			return cty.NilVal, errors.New("assert: condition is null")
		}
		if !bv.IsKnown() {
			return cty.NilVal, errors.New("assert: condition is not known")
		}
		if bv.True() {
			return cty.True, nil
		}
		// Failed: raise a catchable error carrying the condition's range. The
		// message (string or object) is evaluated only now, on failure.
		msg := cty.StringVal("assertion failed")
		if len(args) == 2 {
			mv, mdiags := customdecode.ExpressionClosureFromVal(args[1]).Value()
			if mdiags.HasErrors() {
				if tv, ok := thrownValueFromDiags(mdiags); ok {
					return cty.NilVal, &ThrownError{Value: tv}
				}
				return cty.NilVal, diagsToError("assert: message", mdiags)
			}
			msg = mv
		}
		return cty.NilVal, assertionError(condClosure.Expression, condClosure.EvalContext, msg)
	},
})

// assertionError builds the catchable error an assertion failure raises: the
// message (string or object, exactly like error()) stamped with the condition's
// source range, enriched pytest-style with the referenced variables' values so a
// caught assertion can report why it failed. The operand enrichment is attached
// independent of a custom message, which stays the headline. Shared by assert and
// the eventually/never poll builtins.
func assertionError(expr hcl.Expression, ctx *hcl.EvalContext, msg cty.Value) *ThrownError {
	ev := errorValue(msg, expr.Range())
	if ops := conditionOperands(expr, ctx); len(ops) > 0 {
		objs := make([]cty.Value, len(ops))
		for i, o := range ops {
			objs[i] = cty.ObjectVal(map[string]cty.Value{
				"name":  cty.StringVal(o.name),
				"value": o.value,
			})
		}
		// Heterogeneous operand objects → Tuple (a List would reject mixed types).
		ev = withAttr(ev, "operands", cty.TupleVal(objs))
		ev = withAttr(ev, "detail", cty.StringVal(renderOperands(ops)))
	}
	return &ThrownError{Value: ev}
}

// condFunc is a lazy, multi-branch conditional.
//
// Usage: cond(c1, r1, c2, r2, ..., else). Conditions are evaluated in order; only
// the result expression paired with the first truthy condition is evaluated. If no
// condition is truthy, the trailing "else" expression is evaluated. Unevaluated
// expressions produce no side effects — unlike HCL's ?: which evaluates both arms.
var condFunc = function.New(&function.Spec{
	Description: "Lazy conditional: cond(c1, r1, c2, r2, ..., else). Evaluates conditions in order; only the selected result expression is evaluated.",
	VarParam: &function.Parameter{
		Name:        "exprs",
		Type:        customdecode.ExpressionClosureType,
		Description: "Alternating condition/result expressions followed by a trailing else: cond(c1, r1, …, else). Only the selected result is evaluated",
	},
	// DynamicPseudoType from Type keeps evaluation single-pass. cty calls Type
	// before Impl; evaluating closures in Type (as upstream tryfunc does) would
	// cause side effects to run twice.
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) < 3 || len(args)%2 == 0 {
			return cty.NilType, fmt.Errorf("cond requires an odd number of arguments >= 3 (got %d)", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		for i := 0; i+1 < len(args); i += 2 {
			cv, diags := customdecode.ExpressionClosureFromVal(args[i]).Value()
			if diags.HasErrors() {
				return cty.NilVal, diagsToError(fmt.Sprintf("cond: condition #%d", i/2+1), diags)
			}
			bv, err := convert.Convert(cv, cty.Bool)
			if err != nil {
				return cty.NilVal, fmt.Errorf("cond: condition #%d: %w", i/2+1, err)
			}
			if bv.IsNull() {
				return cty.NilVal, fmt.Errorf("cond: condition #%d is null", i/2+1)
			}
			if !bv.IsKnown() {
				return cty.NilVal, fmt.Errorf("cond: condition #%d is not known", i/2+1)
			}
			if bv.True() {
				return evalClosure(args[i+1], fmt.Sprintf("cond: result #%d", i/2+1))
			}
		}
		return evalClosure(args[len(args)-1], "cond: else")
	},
})

// switchFunc dispatches on a single value against a series of (match, result)
// pairs, with an optional trailing default.
//
// Usage: switch(on, v1, r1, v2, r2, ..., default?). The trailing default is
// optional; with it the total argument count is even, without it odd.
//
// `on` is evaluated exactly once. Then for each (vN, rN) pair, vN is evaluated and
// compared to `on` for equality; on the first match, rN is evaluated and returned.
// If no vN matches, the default (when present) is evaluated and returned; if no
// default was given, switch() errors. Case values past the matching arm are not
// evaluated, nor are unselected results or the default.
//
// Equality uses cty.Value.RawEquals — exact structural equality. Type mismatches
// (e.g. number 200 vs string "200") count as no match rather than erroring.
//
// Because switch is also a statement keyword, switch() is only usable in expression
// position (x = switch(...), return switch(...), as an argument), not as a bare
// statement.
var switchFunc = function.New(&function.Spec{
	Description: "Switch dispatch: switch(on, v1, r1, v2, r2, ..., default?). Evaluates `on` once, then each vN until a match; returns the matching rN, or the optional default. Errors if nothing matches and no default was given. Each branch is evaluated at most once.",
	VarParam: &function.Parameter{
		Name:        "exprs",
		Type:        customdecode.ExpressionClosureType,
		Description: "The value to match on, then alternating case/result expressions and an optional trailing default: switch(on, v1, r1, …, default?)",
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) < 3 {
			return cty.NilType, fmt.Errorf("switch requires at least 3 arguments (got %d)", len(args))
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		hasDefault := len(args)%2 == 0
		caseEnd := len(args)
		if hasDefault {
			caseEnd = len(args) - 1
		}
		on, err := evalClosure(args[0], "switch: on")
		if err != nil {
			return cty.NilVal, err
		}
		for i := 1; i+1 < caseEnd; i += 2 {
			caseNum := (i + 1) / 2
			v, err := evalClosure(args[i], fmt.Sprintf("switch: case #%d value", caseNum))
			if err != nil {
				return cty.NilVal, err
			}
			if on.RawEquals(v) {
				return evalClosure(args[i+1], fmt.Sprintf("switch: case #%d result", caseNum))
			}
		}
		if hasDefault {
			return evalClosure(args[len(args)-1], "switch: default")
		}
		return cty.NilVal, errors.New("switch: no case matched and no default was given")
	},
})

// tryFunc evaluates each argument in order and returns the first whose evaluation
// produces no diagnostics. Unlike upstream tryfunc.TryFunc, it returns
// cty.DynamicPseudoType from the Type callback so each selected expression is
// evaluated exactly once (upstream's concrete-type inference evaluates the
// successful branch twice, which is unsafe for side-effectful expressions).
var tryFunc = function.New(&function.Spec{
	Description: "Try each expression in order; return the first that evaluates without error. Evaluates each expression at most once (unlike stock HCL try()).",
	VarParam: &function.Parameter{
		Name:        "exprs",
		Type:        customdecode.ExpressionClosureType,
		Description: "Expressions to try in order; the first that evaluates without error is returned",
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		if len(args) == 0 {
			return cty.NilType, errors.New("try requires at least one argument")
		}
		return cty.DynamicPseudoType, nil
	},
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		var accum hcl.Diagnostics
		for _, a := range args {
			v, diags := customdecode.ExpressionClosureFromVal(a).Value()
			if diags.HasErrors() {
				accum = append(accum, diags...)
				continue
			}
			// If the value has unknowns we cannot guarantee that a later
			// known-state evaluation would not fail, so bail out dynamically
			// (same conservative behavior as upstream tryfunc).
			if !v.IsWhollyKnown() {
				return cty.DynamicVal, nil
			}
			return v, nil
		}
		return cty.NilVal, diagsToError("no try expression succeeded", accum)
	},
})

// evalClosure evaluates a lazy-argument closure. When the expression raises a
// functy error (a throw that unwound out of a called function, recoverable from the
// diagnostics), it re-propagates it as a *ThrownError so its structure and range
// survive cond/switch result branches; any other failure becomes a text error.
func evalClosure(v cty.Value, label string) (cty.Value, error) {
	rv, diags := customdecode.ExpressionClosureFromVal(v).Value()
	if diags.HasErrors() {
		if tv, ok := thrownValueFromDiags(diags); ok {
			return cty.NilVal, &ThrownError{Value: tv}
		}
		return cty.NilVal, diagsToError(label, diags)
	}
	return rv, nil
}

func diagsToError(prefix string, diags hcl.Diagnostics) error {
	if len(diags) == 0 {
		return errors.New(prefix)
	}
	var buf strings.Builder
	buf.WriteString(prefix)
	buf.WriteString(":\n")
	for _, d := range diags {
		if d.Subject != nil {
			fmt.Fprintf(&buf, "- %s (at %s)\n  %s\n", d.Summary, d.Subject, d.Detail)
		} else {
			fmt.Fprintf(&buf, "- %s\n  %s\n", d.Summary, d.Detail)
		}
	}
	return errors.New(strings.TrimRight(buf.String(), "\n"))
}
