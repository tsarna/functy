package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Compile turns the parsed function declarations into cty functions. Each
// function captures evalCtxFn and calls it at invocation time (late binding), so
// a function may call sibling functions and reference host globals that are
// finalized after compilation — enabling recursion and mutual recursion.
//
// Duplicate function names within the result are reported as errors; the host is
// responsible for detecting collisions against its own built-in functions when
// it merges the returned map into its registry.
func (r *Result) Compile(evalCtxFn func() *hcl.EvalContext) (map[string]function.Function, hcl.Diagnostics) {
	funcs := make(map[string]function.Function, len(r.Funcs))
	var diags hcl.Diagnostics
	for _, fn := range r.Funcs {
		if _, exists := funcs[fn.Name]; exists {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate function",
				Detail:   fmt.Sprintf("Function %q is already defined.", fn.Name),
				Subject:  fn.DefRange.Ptr(),
			})
			continue
		}
		funcs[fn.Name] = BuildFunction(fn, evalCtxFn)
	}
	return funcs, diags
}

// BuildFunction builds a single cty function from a parsed declaration.
//
// cty has no native optional parameters, so only the required parameters go in
// Spec.Params; optional parameters and the variadic parameter are collected via
// a VarParam and mapped back to names (applying defaults) inside the Impl.
func BuildFunction(fn *FuncDecl, evalCtxFn func() *hcl.EvalContext) function.Function {
	var required, optional []Param
	var variadic *Param
	for i := range fn.Params {
		p := fn.Params[i]
		switch {
		case p.Variadic:
			variadic = &fn.Params[i]
		case p.Default != nil:
			optional = append(optional, p)
		default:
			required = append(required, p)
		}
	}

	params := make([]function.Parameter, len(required))
	for i, p := range required {
		// Optional/variadic params collapse into VarParam below, so cty can only
		// carry descriptions for the required ones; FuncDecl.Params is the full
		// source of truth (e.g. for help()).
		params[i] = function.Parameter{Name: p.Name, Type: cty.DynamicPseudoType, AllowNull: true, Description: p.Doc}
	}

	spec := &function.Spec{Params: params, Description: fn.Doc}
	if len(optional) > 0 || variadic != nil {
		spec.VarParam = &function.Parameter{Name: "args", Type: cty.DynamicPseudoType, AllowNull: true}
	}

	retType := cty.DynamicPseudoType
	if fn.RetType != nil {
		retType = fn.RetType.Cty()
	}
	spec.Type = function.StaticReturnType(retType)

	numRequired := len(required)
	spec.Impl = func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		parentCtx := evalCtxFn()
		extra := args[numRequired:]

		scope := NewScope(nil)

		// Required parameters.
		for i, p := range required {
			if err := scope.Declare(p.Name, p.Type, args[i]); err != nil {
				return cty.NilVal, argErr(p.Name, err)
			}
		}

		// Optional parameters: extras first, else the evaluated default.
		consumed := 0
		for _, p := range optional {
			var v cty.Value
			if consumed < len(extra) {
				v = extra[consumed]
				consumed++
			} else {
				dv, diags := p.Default.Value(parentCtx)
				if diags.HasErrors() {
					return cty.NilVal, fmt.Errorf("evaluating default for %q: %s", p.Name, diags.Error())
				}
				v = dv
			}
			if err := scope.Declare(p.Name, p.Type, v); err != nil {
				return cty.NilVal, argErr(p.Name, err)
			}
		}

		// Variadic parameter collects whatever extras remain.
		rest := extra[consumed:]
		if variadic != nil {
			v, err := collectVariadic(*variadic, rest)
			if err != nil {
				return cty.NilVal, err
			}
			_ = scope.Declare(variadic.Name, nil, v)
		} else if len(rest) > 0 {
			return cty.NilVal, fmt.Errorf("too many arguments: %q takes %d", fn.Name, len(fn.Params))
		}

		ip := &interp{parentCtx: parentCtx}
		sig, diags := ip.execBlock(fn.Body, scope)

		// Deferred expressions run at function exit (LIFO), after any finally
		// blocks and after the body's outcome is determined. A defer that
		// raises replaces the in-flight outcome.
		if ddiags := ip.runDefers(); ddiags.HasErrors() {
			return cty.NilVal, fmt.Errorf("%s", ddiags.Error())
		}
		if diags.HasErrors() {
			// A `skip` unwinding out (directly or through a called function) is not
			// a failure; re-emit it so a test runner can classify it as skipped.
			if se, ok := skipFromDiags(diags); ok {
				return cty.NilVal, se
			}
			// An uncaught throw from a nested functy call arrives as diagnostics
			// carrying the raw error value; re-emit it so structure survives this
			// boundary too. Any other eval failure stays plain text.
			if v, ok := thrownValueFromDiags(diags); ok {
				return cty.NilVal, &ThrownError{Value: v}
			}
			return cty.NilVal, fmt.Errorf("%s", diags.Error())
		}
		if sig != nil && sig.Kind == SignalError {
			return cty.NilVal, &ThrownError{Value: sig.Value}
		}

		result := cty.NullVal(cty.DynamicPseudoType)
		if sig != nil && sig.Kind == SignalReturn {
			result = sig.Value
		}
		if fn.RetType != nil {
			conv, err := fn.RetType.Coerce(result)
			if err != nil {
				return cty.NilVal, fmt.Errorf("return value of %q: %w", fn.Name, err)
			}
			result = conv
		}
		return result, nil
	}

	return function.New(spec)
}

// collectVariadic gathers the remaining arguments into the variadic parameter's
// value: an untyped *rest yields a tuple; a typed *rest: T yields a list(T) with
// each element converted to T.
func collectVariadic(v Param, rest []cty.Value) (cty.Value, error) {
	if v.Type == nil {
		if len(rest) == 0 {
			return cty.EmptyTupleVal, nil
		}
		vals := make([]cty.Value, len(rest))
		copy(vals, rest)
		return cty.TupleVal(vals), nil
	}
	elemTy := v.Type.Cty()
	if len(rest) == 0 {
		return cty.ListValEmpty(elemTy), nil
	}
	vals := make([]cty.Value, len(rest))
	for i, r := range rest {
		conv, err := v.Type.Coerce(r)
		if err != nil {
			return cty.NilVal, fmt.Errorf("variadic argument %d of %q: %w", i, v.Name, err)
		}
		vals[i] = conv
	}
	return cty.ListVal(vals), nil
}

func argErr(name string, err error) error {
	return fmt.Errorf("argument %q: %w", name, err)
}
