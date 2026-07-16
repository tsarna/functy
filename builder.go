package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// Compiled is the outcome of compiling a Result: the functions to hand the host,
// and the per-namespace name-resolution layers functy's own bodies resolve
// against.
type Compiled struct {
	// Funcs is the map for the host's eval context. It holds only the *exported*
	// functions — private (`_`-prefixed) ones are absent — keyed by their
	// *qualified* name (`foo::bar::baz`, or the bare name in the global
	// namespace).
	Funcs map[string]function.Function

	// Units maps a namespace ("" = global) to that namespace's own functions by
	// their *bare* names, private ones included. This mirrors the layer a
	// namespace's functions resolve their siblings through, and it is what makes a
	// private function callable from inside its namespace while remaining invisible
	// to the host. Exposed because tooling needs it: to reach a private function by
	// name (`functy run --func _helper`), and to detect a bare name that shadows a
	// host function (see the note on unitCtxFn).
	//
	// It is a *snapshot*, not the live table the compiled functions read: those are
	// consulted on every call, from whatever goroutine is calling, so handing the
	// same maps out would let a host's write race a running function.
	Units map[string]map[string]function.Function
}

// Compile turns the parsed function declarations into cty functions for the host.
//
// It returns the exported functions, keyed by qualified name; private functions
// are compiled but withheld. Use CompileUnits when the namespace-local layers are
// needed too.
func (r *Result) Compile(evalCtxFn func() *hcl.EvalContext) (map[string]function.Function, hcl.Diagnostics) {
	compiled, diags := r.CompileUnits(evalCtxFn)
	return compiled.Funcs, diags
}

// CompileUnits turns the parsed function declarations into cty functions. Each
// function captures evalCtxFn and calls it at invocation time (late binding), so
// a function may call sibling functions and reference host globals that are
// finalized after compilation — enabling recursion and mutual recursion.
//
// Functions are scoped by namespace. A namespace's functions see each other by
// their bare names through a unit layer (see unitCtxFn) and are handed to the host
// under their qualified names; `_`-prefixed functions are never handed over at all.
//
// Duplicate function names within a namespace are reported as errors. Two
// different namespaces may each declare the same bare name — their qualified names
// differ, so they are distinct functions. The host remains responsible for
// detecting collisions against its own built-in functions when it merges Funcs
// into its registry.
//
// Result.Externs is deliberately not compiled: an extern declares a function the
// *host* provides, so there is no body to compile and nothing to register.
func (r *Result) CompileUnits(evalCtxFn func() *hcl.EvalContext) (*Compiled, hcl.Diagnostics) {
	var diags hcl.Diagnostics

	// Allocate every namespace's table (and the context function closing over it)
	// before building anything, so each function can be built against a layer that
	// will hold all of its siblings. The tables are populated below; that is safe
	// because a built function only reads its table when it is *called*, which
	// cannot happen before this function returns.
	units := make(map[string]map[string]function.Function)
	ctxFns := make(map[string]func() *hcl.EvalContext)
	for _, fn := range r.Funcs {
		if _, ok := units[fn.Namespace]; !ok {
			table := make(map[string]function.Function)
			units[fn.Namespace] = table
			ctxFns[fn.Namespace] = unitCtxFn(evalCtxFn, table)
		}
	}

	exported := make(map[string]function.Function, len(r.Funcs))
	seen := make(map[string]bool, len(r.Funcs))
	for _, fn := range r.Funcs {
		qualified := fn.QualifiedName()
		// The parser routes externs into Result.Externs, so this is unreachable from
		// a parsed Result — it defends the invariant against a hand-assembled one.
		// It matters because BuildFunction treats a parameter with no default as
		// *required*, so an extern's `name?` parameter would compile to the wrong
		// signature rather than failing.
		if fn.Extern {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Extern function cannot be compiled",
				Detail:   fmt.Sprintf("Extern %q declares a function the host provides; it has no body. It belongs in Result.Externs, not Result.Funcs.", qualified),
				Subject:  fn.DefRange.Ptr(),
			})
			continue
		}
		if seen[qualified] {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate function",
				Detail:   fmt.Sprintf("Function %q is already defined.", qualified),
				Subject:  fn.DefRange.Ptr(),
			})
			continue
		}
		seen[qualified] = true

		f := BuildFunction(fn, ctxFns[fn.Namespace], r.maxSteps)
		units[fn.Namespace][fn.Name] = f
		if !fn.IsPrivate() {
			exported[qualified] = f
		}
	}

	// Hand out a copy: `units` is read by every compiled function on every call, so
	// sharing it would let a caller's write race a running function.
	snapshot := make(map[string]map[string]function.Function, len(units))
	for ns, table := range units {
		copied := make(map[string]function.Function, len(table))
		for name, f := range table {
			copied[name] = f
		}
		snapshot[ns] = copied
	}
	return &Compiled{Funcs: exported, Units: snapshot}, diags
}

// unitCtxFn wraps a late-bound host eval context in a child layer carrying one
// namespace's own functions under their bare names, private ones included.
//
// HCL resolves a call by walking the *entire* context chain, checking each non-nil
// Functions map, so this layer ADDS the namespace's names without hiding the
// host's library: a sibling call resolves here, everything else falls through to
// the host. It is also the only place a private function is reachable, which is
// what lets it be callable from its namespace yet absent from the host's map.
//
// Two consequences worth naming:
//
//   - Local wins. A namespace's bare name shadows a host function of the same name
//     *inside that namespace's bodies*. This cannot be diagnosed here — evalCtxFn is
//     late-bound and the host's function set may not exist yet — so a host that wants
//     to warn should compare Compiled.Units against its own registry (cmd/functy does).
//     Note a private name cannot collide this way: no host function is `_`-prefixed.
//
//   - The child is rebuilt per call, because evalCtxFn is late-bound and may return a
//     different context each time. That is one small allocation (NewChild is just
//     &EvalContext{parent}) against an interpreter that already builds a context per
//     statement group. Memoizing on the parent pointer would be safe but buys nothing
//     and adds a concurrency surface; don't.
func unitCtxFn(evalCtxFn func() *hcl.EvalContext, table map[string]function.Function) func() *hcl.EvalContext {
	return func() *hcl.EvalContext {
		var parent *hcl.EvalContext
		if evalCtxFn != nil {
			parent = evalCtxFn()
		}
		child := &hcl.EvalContext{}
		if parent != nil {
			child = parent.NewChild()
		}
		child.Functions = table
		return child
	}
}

// BuildFunction builds a single cty function from a parsed declaration.
//
// cty has no native optional parameters, so only the required parameters go in
// Spec.Params; optional parameters and the variadic parameter are collected via
// a VarParam and mapped back to names (applying defaults) inside the Impl.
// maxSteps is the Tier-1 execution-limit ceiling captured immutably into the Impl
// closure: every invocation builds a fresh interp seeded with it, so the step
// counter is per-frame and needs no shared state. 0 means unbounded.
func BuildFunction(fn *FuncDecl, evalCtxFn func() *hcl.EvalContext, maxSteps int) function.Function {
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

		ip := &interp{parentCtx: parentCtx, maxSteps: maxSteps}
		sig, diags := ip.execBlock(fn.Body, scope)

		// An execution-limit breach is uncatchable and terminates the whole
		// evaluation: re-emit it as a Go error so it crosses this boundary (arriving
		// at the caller as a FunctionCallDiagExtra, like a propagated skip), and
		// skip the defers — a defer can itself loop, so running it would defeat the
		// bound on post-breach cost.
		if le, ok := limitFromDiags(diags); ok {
			return cty.NilVal, le
		}

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
