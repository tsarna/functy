package functy

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// interp holds the state for one function invocation: the late-bound host eval
// context and the function's deferred-expression stack.
type interp struct {
	parentCtx *hcl.EvalContext
	defers    []deferred
}

// deferred is a scheduled defer expression together with the scope it was
// registered in; it is evaluated against that scope (as it then exists) at
// function exit.
type deferred struct {
	expr  hcl.Expression
	scope *Scope
}

// execBlock runs a list of statements in scope. It returns a non-nil *Signal if
// a return/break/continue/error propagates out, or nil on normal completion.
//
// The merged *hcl.EvalContext is cached and rebuilt only when a binding visible
// to scope may have changed: after a declaration, an assignment, or any compound
// statement (whose nested body could reassign a variable). A bare expression
// statement, return, break, continue, or throw cannot mutate a binding, so a run
// of such statements reuses one context — avoiding the per-statement variable-map
// rebuild that a naive interpreter performs.
func (ip *interp) execBlock(stmts []Statement, scope *Scope) (*Signal, hcl.Diagnostics) {
	var ctx *hcl.EvalContext
	for _, stmt := range stmts {
		if ctx == nil || scope.dirty {
			ctx = scopeEvalContext(scope, ip.parentCtx)
			scope.dirty = false
		}

		sig, diags := ip.executeStatement(stmt, scope, ctx)
		if diags.HasErrors() {
			return nil, diags
		}
		if sig != nil {
			return sig, nil
		}

		switch stmt.(type) {
		case *ExprStmt, *Return, *Break, *Continue, *Throw, *Defer:
			// These cannot change a binding; the cached context stays valid.
		default:
			// Declarations, assignments, and compound statements may have
			// changed a binding (possibly in an enclosing scope).
			scope.dirty = true
		}
	}
	return nil, nil
}

func (ip *interp) executeStatement(stmt Statement, scope *Scope, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	switch s := stmt.(type) {
	case *VarDecl:
		return nil, execVarDecl(s, scope, ctx)
	case *Assign:
		return nil, execAssign(s, scope, ctx)
	case *CaptureAssign:
		return nil, execCaptureAssign(s, scope, ctx)
	case *ExprStmt:
		_, diags := s.Expr.Value(ctx)
		return nil, diags
	case *Return:
		return execReturn(s, ctx)
	case *Block:
		return ip.execBlock(s.Body, NewScope(scope))
	case *IfChain:
		return ip.execIfChain(s, scope, ctx)
	case *For:
		return ip.execFor(s, scope, ctx)
	case *Switch:
		return ip.execSwitch(s, scope, ctx)
	case *Break:
		return &Signal{Kind: SignalBreak}, nil
	case *Continue:
		return &Signal{Kind: SignalContinue}, nil
	case *Throw:
		return execThrow(s, ctx)
	case *Defer:
		ip.defers = append(ip.defers, deferred{expr: s.Expr, scope: scope})
		return nil, nil
	case *Try:
		return ip.execTry(s, scope)
	default:
		return nil, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Unknown statement",
			Detail:   "Internal error: unrecognized functy statement node.",
			Subject:  stmt.srcRange().Ptr(),
		}}
	}
}

func execVarDecl(s *VarDecl, scope *Scope, ctx *hcl.EvalContext) hcl.Diagnostics {
	var val cty.Value
	if s.Init != nil {
		v, diags := s.Init.Value(ctx)
		if diags.HasErrors() {
			return diags
		}
		val = v
	} else {
		val = nullOf(s.Type)
	}
	if err := scope.Declare(s.Name, s.Type, val); err != nil {
		return userErr(err, s.SrcRange)
	}
	return nil
}

func execAssign(s *Assign, scope *Scope, ctx *hcl.EvalContext) hcl.Diagnostics {
	if !scope.declared(s.Name) {
		return undeclaredErr(s.Name, s.SrcRange)
	}
	v, diags := s.Expr.Value(ctx)
	if diags.HasErrors() {
		return diags
	}
	if err := scope.Set(s.Name, v); err != nil {
		return userErr(err, s.SrcRange)
	}
	return nil
}

// execCaptureAssign runs the `val, err = expr` capture: it evaluates Expr once
// and routes the outcome to the two targets instead of unwinding the function.
// On success val receives the value and err receives null; on failure val
// receives null and err receives the caught error value (the same object shape a
// catch clause binds). A "_" target is skipped. Both non-blank targets must
// already be declared, like a plain assignment.
func execCaptureAssign(s *CaptureAssign, scope *Scope, ctx *hcl.EvalContext) hcl.Diagnostics {
	if s.ValName != "_" && !scope.declared(s.ValName) {
		return undeclaredErr(s.ValName, s.ValRange)
	}
	if s.ErrName != "_" && !scope.declared(s.ErrName) {
		return undeclaredErr(s.ErrName, s.ErrRange)
	}

	val, errVal := cty.NilVal, cty.NilVal
	if v, diags := s.Expr.Value(ctx); diags.HasErrors() {
		// Error path: val stays null, err carries the caught error.
		val = cty.NullVal(cty.DynamicPseudoType)
		errVal = errValueFromDiags(diags)
	} else {
		// Success path: val carries the value, err is null.
		val = v
		errVal = cty.NullVal(cty.DynamicPseudoType)
	}

	if s.ValName != "_" {
		if err := scope.Set(s.ValName, val); err != nil {
			return userErr(err, s.ValRange)
		}
	}
	if s.ErrName != "_" {
		if err := scope.Set(s.ErrName, errVal); err != nil {
			return userErr(err, s.ErrRange)
		}
	}
	return nil
}

func execReturn(s *Return, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	if s.Expr == nil {
		return &Signal{Kind: SignalReturn, Value: cty.NullVal(cty.DynamicPseudoType)}, nil
	}
	v, diags := s.Expr.Value(ctx)
	if diags.HasErrors() {
		return nil, diags
	}
	return &Signal{Kind: SignalReturn, Value: v}, nil
}

func (ip *interp) execIfChain(s *IfChain, scope *Scope, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	for _, branch := range s.Branches {
		cond, diags := branch.Condition.Value(ctx)
		if diags.HasErrors() {
			return nil, diags
		}
		if cond.IsNull() {
			return nil, condNullErr(branch.Condition.Range())
		}
		if cond.True() {
			return ip.execBlock(branch.Body, NewScope(scope))
		}
	}
	if s.Else != nil {
		return ip.execBlock(s.Else, NewScope(scope))
	}
	return nil, nil
}

func (ip *interp) execSwitch(s *Switch, scope *Scope, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	if s.Subject == nil {
		// Expression-less form: each case value is a boolean expression.
		for _, c := range s.Cases {
			for _, ve := range c.Values {
				cond, diags := ve.Value(ctx)
				if diags.HasErrors() {
					return nil, diags
				}
				if !cond.IsNull() && cond.True() {
					return ip.execBlock(c.Body, NewScope(scope))
				}
			}
		}
	} else {
		subj, diags := s.Subject.Value(ctx)
		if diags.HasErrors() {
			return nil, diags
		}
		for _, c := range s.Cases {
			for _, ve := range c.Values {
				cv, vdiags := ve.Value(ctx)
				if vdiags.HasErrors() {
					return nil, vdiags
				}
				eq := subj.Equals(cv)
				if !eq.IsNull() && eq.True() {
					return ip.execBlock(c.Body, NewScope(scope))
				}
			}
		}
	}
	if s.Default != nil {
		return ip.execBlock(s.Default, NewScope(scope))
	}
	return nil, nil
}

func (ip *interp) execFor(s *For, scope *Scope, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	switch s.Kind {
	case ForRange:
		return ip.execForRange(s, scope, ctx)
	case ForClause:
		return ip.execForClause(s, scope)
	default:
		return ip.execForCond(s, scope)
	}
}

// execForCond runs `for cond { ... }` / `while cond { ... }` / `for { ... }`.
func (ip *interp) execForCond(s *For, scope *Scope) (*Signal, hcl.Diagnostics) {
	for {
		if s.Cond != nil {
			cond, diags := s.Cond.Value(scopeEvalContext(scope, ip.parentCtx))
			if diags.HasErrors() {
				return nil, diags
			}
			if cond.IsNull() {
				return nil, condNullErr(s.Cond.Range())
			}
			if !cond.True() {
				return nil, nil
			}
		}
		sig, diags := ip.execBlock(s.Body, NewScope(scope))
		if diags.HasErrors() {
			return nil, diags
		}
		if done, out := loopSignal(sig); done {
			return out, nil
		}
	}
}

// execForClause runs the three-clause `for init; cond; post { ... }`. The init
// declaration lives in a loop scope that persists across iterations; each
// iteration body runs in a fresh child of that loop scope.
func (ip *interp) execForClause(s *For, scope *Scope) (*Signal, hcl.Diagnostics) {
	loopScope := NewScope(scope)
	if s.Init != nil {
		if sig, diags := ip.execBlock([]Statement{s.Init}, loopScope); diags.HasErrors() || sig != nil {
			return sig, diags
		}
	}
	for {
		if s.Cond != nil {
			cond, diags := s.Cond.Value(scopeEvalContext(loopScope, ip.parentCtx))
			if diags.HasErrors() {
				return nil, diags
			}
			if cond.IsNull() {
				return nil, condNullErr(s.Cond.Range())
			}
			if !cond.True() {
				return nil, nil
			}
		}
		sig, diags := ip.execBlock(s.Body, NewScope(loopScope))
		if diags.HasErrors() {
			return nil, diags
		}
		if done, out := loopSignal(sig); done {
			return out, nil
		}
		if s.Post != nil {
			if sig, diags := ip.execBlock([]Statement{s.Post}, loopScope); diags.HasErrors() || sig != nil {
				return sig, diags
			}
		}
	}
}

// execForRange runs `for v in coll` / `for k, v in coll`.
func (ip *interp) execForRange(s *For, scope *Scope, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	coll, diags := s.Collection.Value(ctx)
	if diags.HasErrors() {
		return nil, diags
	}
	if coll.IsNull() {
		return nil, rangeErr("Cannot range over a null value.", s.Collection.Range())
	}

	pairs, perr := rangePairs(coll, s.Collection.Range())
	if perr != nil {
		return nil, perr
	}

	for _, kv := range pairs {
		child := NewScope(scope)
		if s.KeyName != "" {
			_ = child.Declare(s.KeyName, nil, kv.key)
		}
		if s.ValName != "" {
			_ = child.Declare(s.ValName, nil, kv.val)
		}
		sig, diags := ip.execBlock(s.Body, child)
		if diags.HasErrors() {
			return nil, diags
		}
		if done, out := loopSignal(sig); done {
			return out, nil
		}
	}
	return nil, nil
}

// execTry runs a try block, routes a raised error (an explicit throw or a failing
// expression) to the catch block if present, and always runs the finally block.
func (ip *interp) execTry(s *Try, scope *Scope) (*Signal, hcl.Diagnostics) {
	sig, diags := ip.execBlock(s.Body, NewScope(scope))

	errVal, errored := raisedError(sig, diags)
	if errored {
		sig, diags = nil, nil
		if s.HasCatch {
			catchScope := NewScope(scope)
			if s.CatchName != "" {
				_ = catchScope.Declare(s.CatchName, nil, errVal)
			}
			sig, diags = ip.execBlock(s.Catch, catchScope)
			errored = false // handled (the catch itself may raise anew)
		}
	}

	// finally runs unconditionally; an error or non-local exit within finally
	// replaces whatever was in flight.
	if s.Finally != nil {
		fsig, fdiags := ip.execBlock(s.Finally, NewScope(scope))
		if fdiags.HasErrors() {
			return nil, fdiags
		}
		if fsig != nil {
			return fsig, nil
		}
	}

	if errored {
		// Uncaught (no catch clause): re-raise so an enclosing try or the
		// function boundary handles it.
		return &Signal{Kind: SignalError, Value: errVal}, nil
	}
	return sig, diags
}

func execThrow(s *Throw, ctx *hcl.EvalContext) (*Signal, hcl.Diagnostics) {
	v, diags := s.Expr.Value(ctx)
	if diags.HasErrors() {
		return nil, diags
	}
	return &Signal{Kind: SignalError, Value: errorValue(v)}, nil
}

// runDefers evaluates the function's deferred expressions in LIFO order. A defer
// that raises aborts the remaining defers and returns its error.
func (ip *interp) runDefers() hcl.Diagnostics {
	for i := len(ip.defers) - 1; i >= 0; i-- {
		d := ip.defers[i]
		_, diags := d.expr.Value(scopeEvalContext(d.scope, ip.parentCtx))
		if diags.HasErrors() {
			return diags
		}
	}
	return nil
}

type rangeKV struct{ key, val cty.Value }

// rangePairs enumerates a collection into key/value pairs per the range
// semantics: list/tuple yield index+element, set yields a stable counter+element,
// map/object yield key+value.
func rangePairs(coll cty.Value, rng hcl.Range) ([]rangeKV, hcl.Diagnostics) {
	ty := coll.Type()
	var pairs []rangeKV
	switch {
	case ty.IsListType() || ty.IsTupleType():
		for it := coll.ElementIterator(); it.Next(); {
			k, v := it.Element()
			pairs = append(pairs, rangeKV{key: k, val: v})
		}
	case ty.IsSetType():
		i := 0
		for it := coll.ElementIterator(); it.Next(); {
			_, v := it.Element()
			pairs = append(pairs, rangeKV{key: cty.NumberIntVal(int64(i)), val: v})
			i++
		}
	case ty.IsMapType() || ty.IsObjectType():
		for it := coll.ElementIterator(); it.Next(); {
			k, v := it.Element()
			pairs = append(pairs, rangeKV{key: k, val: v})
		}
	default:
		return nil, rangeErr("for ... in requires a list, set, tuple, map, or object value.", rng)
	}
	return pairs, nil
}

// loopSignal interprets a signal produced by a loop body: break ends the loop
// (consumed), continue moves to the next iteration (consumed), and a return or
// error propagates. done reports whether the loop should stop, and out is the
// signal to propagate (nil for break).
func loopSignal(sig *Signal) (done bool, out *Signal) {
	if sig == nil {
		return false, nil
	}
	switch sig.Kind {
	case SignalBreak:
		return true, nil
	case SignalContinue:
		return false, nil
	default: // SignalReturn, SignalError
		return true, sig
	}
}

// scopeEvalContext builds an HCL eval context holding the scope's variables,
// linked as a child of the parent context. HCL resolves a variable or function
// by walking from the child up the parent chain, so host globals and functions
// remain visible without copying them, and a local of the same name as a global
// naturally shadows it. Only the (small) set of locals is materialized here.
func scopeEvalContext(scope *Scope, parentCtx *hcl.EvalContext) *hcl.EvalContext {
	var child *hcl.EvalContext
	if parentCtx != nil {
		child = parentCtx.NewChild()
	} else {
		child = &hcl.EvalContext{}
	}
	child.Variables = scope.ToMap()
	return child
}

func nullOf(tc TypeConstraint) cty.Value {
	if tc == nil {
		return cty.NullVal(cty.DynamicPseudoType)
	}
	return cty.NullVal(tc.Cty())
}

func undeclaredErr(name string, rng hcl.Range) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Assignment to undeclared variable",
		Detail:   name + " is not declared; use \"var " + name + " = ...\" to declare it.",
		Subject:  rng.Ptr(),
	}}
}

func userErr(err error, rng hcl.Range) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Assignment error",
		Detail:   err.Error(),
		Subject:  rng.Ptr(),
	}}
}

func condNullErr(rng hcl.Range) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Null condition",
		Detail:   "A condition expression evaluated to null; it must be true or false.",
		Subject:  rng.Ptr(),
	}}
}

func rangeErr(detail string, rng hcl.Range) hcl.Diagnostics {
	return hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  "Invalid range collection",
		Detail:   detail,
		Subject:  rng.Ptr(),
	}}
}
