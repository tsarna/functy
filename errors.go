package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// errorValue converts a thrown value into a functy error value: an object with
// at least .message (string) and .value. Throwing a string yields
// { message = <string>, value = null }; throwing an object uses it directly; any
// other value is wrapped with a generic message and the value preserved.
func errorValue(v cty.Value) cty.Value {
	switch {
	case v.IsNull():
		return cty.ObjectVal(map[string]cty.Value{
			"message": cty.StringVal("error"),
			"value":   cty.NullVal(cty.DynamicPseudoType),
		})
	case v.Type() == cty.String:
		return cty.ObjectVal(map[string]cty.Value{
			"message": v,
			"value":   cty.NullVal(cty.DynamicPseudoType),
		})
	case v.Type().IsObjectType():
		return v
	default:
		return cty.ObjectVal(map[string]cty.Value{
			"message": cty.StringVal("error"),
			"value":   v,
		})
	}
}

// errValueFromDiags wraps an expression-evaluation failure as a functy error
// value so try/catch can handle it like an explicit throw.
func errValueFromDiags(diags hcl.Diagnostics) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"message": cty.StringVal(diags.Error()),
		"value":   cty.NullVal(cty.DynamicPseudoType),
	})
}

// raisedError reports whether the outcome of a try body is an error, and if so
// returns the error value. Both an explicit throw (SignalError) and an ordinary
// expression-evaluation failure (diagnostics) count as raised errors.
func raisedError(sig *Signal, diags hcl.Diagnostics) (cty.Value, bool) {
	if diags.HasErrors() {
		return errValueFromDiags(diags), true
	}
	if sig != nil && sig.Kind == SignalError {
		return sig.Value, true
	}
	return cty.NilVal, false
}

// errorConstraint is the built-in `error` open type as a TypeConstraint: the
// shape throw raises and catch binds (an object with at least a string message),
// or null. It is the single source of truth shared by the resolver (the `error`
// annotation) and the `val, err := expr` form, which pins its error target to it.
func errorConstraint() TypeConstraint {
	return predicateConstraint{name: "error", pred: errorTypePredicate}
}

// errorTypePredicate backs the built-in `error` type: an error value is an object
// with at least a string `message` attribute (it may also carry `value` and other
// attributes — the check is open and non-destructive). This is the shape that
// throw raises and catch binds.
func errorTypePredicate(v cty.Value) error {
	ty := v.Type()
	if !ty.IsObjectType() || !ty.HasAttribute("message") {
		return fmt.Errorf("an error must be an object with a message attribute")
	}
	if mt := ty.AttributeType("message"); mt != cty.String && mt != cty.DynamicPseudoType {
		return fmt.Errorf("the error message must be a string")
	}
	return nil
}

// errorMessage extracts a human-readable message from a functy error value for
// surfacing as a Go error at the function boundary.
func errorMessage(errVal cty.Value) string {
	if errVal.Type().IsObjectType() && errVal.Type().HasAttribute("message") {
		m := errVal.GetAttr("message")
		if !m.IsNull() && m.Type() == cty.String {
			return m.AsString()
		}
	}
	return "functy error"
}
