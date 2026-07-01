package functy

import (
	"errors"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// ThrownError is the Go error a functy function returns at its cty.Function
// boundary when an uncaught throw unwinds out of it. It carries the raw error
// value so a functy catch (or a Go host, via errors.As) recovers the full error
// object; Error() renders the message for any other caller.
type ThrownError struct{ Value cty.Value }

func (e *ThrownError) Error() string { return errorMessage(e.Value) }

// Diagnostics renders the thrown error as hcl.Diagnostics so a host can print it with
// source context (an underline under the failing expression) using the standard hcl
// diagnostic writer. It delegates to ErrorDiagnostics on the carried value.
func (e *ThrownError) Diagnostics() hcl.Diagnostics { return ErrorDiagnostics(e.Value) }

// SkipError is the Go error a functy function returns at its cty.Function boundary
// when a `skip` call unwinds out of it. It is distinct from ThrownError (a skip is
// not a failure): a test runner classifies it as skipped rather than failed. Reason
// is the optional message passed to skip().
type SkipError struct{ Reason string }

func (e *SkipError) Error() string {
	if e.Reason == "" {
		return "test skipped"
	}
	return "test skipped: " + e.Reason
}

// skipFromDiags recovers a *SkipError raised by a `skip` call that unwound out of a
// called function, mirroring thrownValueFromDiags: HCL stashes the underlying call
// error on a diagnostic's Extra, and it is returned when it is a *SkipError.
func skipFromDiags(diags hcl.Diagnostics) (*SkipError, bool) {
	for _, d := range diags {
		if fce, ok := hcl.DiagnosticExtra[hclsyntax.FunctionCallDiagExtra](d); ok {
			var se *SkipError
			if errors.As(fce.FunctionCallError(), &se) {
				return se, true
			}
		}
	}
	return nil, false
}

// thrownValueFromDiags recovers the original functy error value from a set of
// diagnostics produced by evaluating a call to a functy function that threw. HCL
// stashes the underlying call error on a diagnostic's Extra (exposed via
// hclsyntax.FunctionCallDiagExtra); when that error is a *ThrownError, its raw
// value is returned so structure survives the call boundary. ok is false for any
// other failure (a genuine eval error, a host function error), which the caller
// then wraps as plain diagnostic text.
func thrownValueFromDiags(diags hcl.Diagnostics) (cty.Value, bool) {
	for _, d := range diags {
		if fce, ok := hcl.DiagnosticExtra[hclsyntax.FunctionCallDiagExtra](d); ok {
			var te *ThrownError
			if errors.As(fce.FunctionCallError(), &te) {
				return te.Value, true
			}
		}
	}
	return cty.NilVal, false
}

// rangeToCty renders a source range as a functy value: { filename, start, end }
// with start and end each { line, column, byte }. It is the shape of an error's
// `range` attribute — the location where the error was raised.
func rangeToCty(rng hcl.Range) cty.Value {
	pos := func(p hcl.Pos) cty.Value {
		return cty.ObjectVal(map[string]cty.Value{
			"line":   cty.NumberIntVal(int64(p.Line)),
			"column": cty.NumberIntVal(int64(p.Column)),
			"byte":   cty.NumberIntVal(int64(p.Byte)),
		})
	}
	return cty.ObjectVal(map[string]cty.Value{
		"filename": cty.StringVal(rng.Filename),
		"start":    pos(rng.Start),
		"end":      pos(rng.End),
	})
}

// ctyToRange is the inverse of rangeToCty: it reconstructs an hcl.Range from an
// error's `range` attribute so a thrown error can be rendered as a source-underlining
// diagnostic. It is defensive — ok is false for a null value, a non-object, or any
// missing/ill-typed field — so a malformed or absent range degrades to "no underline"
// rather than a panic.
func ctyToRange(v cty.Value) (hcl.Range, bool) {
	if v.IsNull() || !v.Type().IsObjectType() {
		return hcl.Range{}, false
	}
	ty := v.Type()
	if !ty.HasAttribute("filename") || !ty.HasAttribute("start") || !ty.HasAttribute("end") {
		return hcl.Range{}, false
	}
	fn := v.GetAttr("filename")
	if fn.IsNull() || fn.Type() != cty.String {
		return hcl.Range{}, false
	}
	start, ok := ctyToPos(v.GetAttr("start"))
	if !ok {
		return hcl.Range{}, false
	}
	end, ok := ctyToPos(v.GetAttr("end"))
	if !ok {
		return hcl.Range{}, false
	}
	return hcl.Range{Filename: fn.AsString(), Start: start, End: end}, true
}

// ctyToPos reconstructs an hcl.Pos from a { line, column, byte } object.
func ctyToPos(v cty.Value) (hcl.Pos, bool) {
	if v.IsNull() || !v.Type().IsObjectType() {
		return hcl.Pos{}, false
	}
	line, ok := attrInt(v, "line")
	if !ok {
		return hcl.Pos{}, false
	}
	col, ok := attrInt(v, "column")
	if !ok {
		return hcl.Pos{}, false
	}
	b, ok := attrInt(v, "byte")
	if !ok {
		return hcl.Pos{}, false
	}
	return hcl.Pos{Line: line, Column: col, Byte: b}, true
}

// attrInt extracts a named integer attribute from an object value.
func attrInt(obj cty.Value, name string) (int, bool) {
	if !obj.Type().HasAttribute(name) {
		return 0, false
	}
	a := obj.GetAttr(name)
	if a.IsNull() || a.Type() != cty.Number {
		return 0, false
	}
	i, _ := a.AsBigFloat().Int64()
	return int(i), true
}

// ErrorDiagnostics renders a functy error value as an hcl.Diagnostic so a host can
// print it with source context (an underline under the failing expression) via the
// standard hcl diagnostic writer. The message becomes the summary, the `detail`
// attribute (e.g. an assert's operand values) becomes the diagnostic detail, and the
// `range` attribute becomes the Subject — omitted (no underline) when absent or
// malformed. It takes an error *value* so the same formatter serves the host boundary
// (via ThrownError.Diagnostics) and any consumer that catches errors as values.
func ErrorDiagnostics(errVal cty.Value) hcl.Diagnostics {
	diag := &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  errorMessage(errVal),
	}
	if errVal.Type().IsObjectType() {
		if errVal.Type().HasAttribute("detail") {
			if d := errVal.GetAttr("detail"); !d.IsNull() && d.Type() == cty.String {
				diag.Detail = d.AsString()
			}
		}
		if errVal.Type().HasAttribute("range") {
			if rng, ok := ctyToRange(errVal.GetAttr("range")); ok {
				diag.Subject = &rng
			}
		}
	}
	return hcl.Diagnostics{diag}
}

// withAttr returns a copy of an object value with one attribute added or replaced.
func withAttr(obj cty.Value, name string, val cty.Value) cty.Value {
	m := obj.AsValueMap()
	if m == nil {
		m = make(map[string]cty.Value, 1)
	}
	m[name] = val
	return cty.ObjectVal(m)
}

// errorValue converts a thrown value into a functy error value: an object with at
// least .message (string) and .range (the raise site). Throwing a string yields
// { message = <string> }; throwing an object uses it directly; throwing any other
// value (a number, bool, list, …) wraps it as { message = "error", value = <it> }
// so the raw payload is recoverable via .value — the only case an error carries a
// `value`. The raise site is stamped as `range`, except on an object that already
// carries one (so a rethrown error keeps its original site).
func errorValue(v cty.Value, rng hcl.Range) cty.Value {
	r := rangeToCty(rng)
	switch {
	case v.IsNull():
		return cty.ObjectVal(map[string]cty.Value{
			"message": cty.StringVal("error"),
			"range":   r,
		})
	case v.Type() == cty.String:
		return cty.ObjectVal(map[string]cty.Value{
			"message": v,
			"range":   r,
		})
	case v.Type().IsObjectType():
		if v.Type().HasAttribute("range") {
			return v
		}
		return withAttr(v, "range", r)
	default:
		return cty.ObjectVal(map[string]cty.Value{
			"message": cty.StringVal("error"),
			"value":   v,
			"range":   r,
		})
	}
}

// errValueFromDiags wraps an expression-evaluation failure as a functy error
// value so try/catch can handle it like an explicit throw. The `range` is taken
// from the first diagnostic with a source subject, or null when none is available.
func errValueFromDiags(diags hcl.Diagnostics) cty.Value {
	return cty.ObjectVal(map[string]cty.Value{
		"message": cty.StringVal(diags.Error()),
		"range":   diagRange(diags),
	})
}

// diagRange returns the range of the first diagnostic carrying a source subject,
// as a functy range value, or null when none is available.
func diagRange(diags hcl.Diagnostics) cty.Value {
	for _, d := range diags {
		if d.Subject != nil {
			return rangeToCty(*d.Subject)
		}
	}
	return cty.NullVal(cty.DynamicPseudoType)
}

// errorFromDiags turns a set of diagnostics into a functy error value: it recovers
// the original structured error when the failure was a throw that unwound out of a
// called functy function (preserving its attributes and range), otherwise it wraps
// the diagnostic text. Shared by the try/catch and `val, err =` capture paths.
func errorFromDiags(diags hcl.Diagnostics) cty.Value {
	if v, ok := thrownValueFromDiags(diags); ok {
		return v
	}
	return errValueFromDiags(diags)
}

// raisedError reports whether the outcome of a try body is an error, and if so
// returns the error value. Both an explicit throw (SignalError) and an ordinary
// expression-evaluation failure (diagnostics) count as raised errors.
func raisedError(sig *Signal, diags hcl.Diagnostics) (cty.Value, bool) {
	if diags.HasErrors() {
		return errorFromDiags(diags), true
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
