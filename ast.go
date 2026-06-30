package functy

import (
	"github.com/hashicorp/hcl/v2"
)

// FuncDecl is a top-level function declaration.
type FuncDecl struct {
	Name     string
	Params   []Param
	RetType  TypeConstraint // nil when no return type is annotated (dynamic)
	Body     []Statement
	DefRange hcl.Range
}

// Param is a single function parameter.
//
// A parameter is required unless it has a Default or is Variadic. A typed
// parameter (Type != cty.NilType) converts its argument to that type. For a
// variadic parameter, Type is the *element* type: `*rest: T` collects the extra
// arguments into a list(T), while an untyped `*rest` collects them into a tuple.
type Param struct {
	Name     string
	Type     TypeConstraint // nil when unannotated (dynamic)
	Default  hcl.Expression // non-nil for an optional parameter
	Variadic bool           // true for the trailing *rest parameter
	DefRange hcl.Range
}

// Statement is implemented by every functy statement node.
type Statement interface {
	srcRange() hcl.Range
}

// VarDecl declares a new local binding in the current scope.
//
// Type is cty.NilType for a dynamic variable. Init is nil for a declaration
// with no initializer, which defaults to null (of the declared type, if any).
type VarDecl struct {
	Name     string
	Type     TypeConstraint // nil when unannotated (dynamic)
	Init     hcl.Expression
	SrcRange hcl.Range
}

// Assign reassigns an existing binding found by walking the scope chain.
type Assign struct {
	Name     string
	Expr     hcl.Expression
	SrcRange hcl.Range
}

// CaptureAssign is the two-target error-capture assignment `val, err = expr`.
// It evaluates Expr exactly once; on success it assigns the value to ValName and
// null to ErrName, and on failure it assigns null to ValName and the caught
// error to ErrName. Either target may be "_" (the blank identifier) to discard
// it. It is statement-level sugar for a try/catch — the function never unwinds.
//
// When Declare is false (the `val, err = expr` operator) both non-blank targets
// must already be declared, like a plain `=`. When Declare is true (the
// `val, err := expr` shorthand) each non-blank target is newly declared (untyped)
// in the current scope, like a pair of `var`s.
type CaptureAssign struct {
	ValName  string // "_" to discard the value
	ErrName  string // "_" to discard the error
	Declare  bool   // true for the `:=` declare-and-capture form
	Expr     hcl.Expression
	ValRange hcl.Range // the value target, for diagnostics
	ErrRange hcl.Range // the error target, for diagnostics
	SrcRange hcl.Range
}

// ExprStmt evaluates an expression purely for its side effects; the value is
// discarded.
type ExprStmt struct {
	Expr     hcl.Expression
	SrcRange hcl.Range
}

// Return exits the enclosing function. Expr is nil for a bare return.
type Return struct {
	Expr     hcl.Expression
	SrcRange hcl.Range
}

// Block is a bare { ... } that introduces a nested lexical scope.
type Block struct {
	Body     []Statement
	SrcRange hcl.Range
}

// IfChain is an if / else-if / else chain. Else is nil when there is no final
// else clause.
type IfChain struct {
	Branches []CondBranch
	Else     []Statement
	SrcRange hcl.Range
}

// CondBranch is one condition-guarded branch of an if chain.
type CondBranch struct {
	Condition hcl.Expression
	Body      []Statement
	SrcRange  hcl.Range
}

// ForKind distinguishes the three surface forms of a for/while loop.
type ForKind int

const (
	// ForCond is `for cond { ... }`, `while cond { ... }`, or the infinite
	// `for { ... }` (Cond nil).
	ForCond ForKind = iota
	// ForClause is the three-clause `for init; cond; post { ... }`.
	ForClause
	// ForRange is `for v in coll { ... }` or `for k, v in coll { ... }`.
	ForRange
)

// For is the unified loop node covering all loop forms.
type For struct {
	Kind ForKind

	// ForCond / ForClause:
	Init Statement      // ForClause init clause (nil otherwise)
	Cond hcl.Expression // condition (nil = always true)
	Post Statement      // ForClause post clause (nil otherwise)

	// ForRange:
	KeyName    string         // first range variable ("" if absent)
	ValName    string         // second range variable ("" if only one is given)
	Collection hcl.Expression // collection being ranged over

	Body     []Statement
	SrcRange hcl.Range
}

// Switch is a switch statement. Subject is nil for the expression-less form,
// whose case values are boolean expressions evaluated like an if/else chain.
// Clauses are in source order; at most one is the default.
type Switch struct {
	Subject  hcl.Expression
	Clauses  []Clause
	SrcRange hcl.Range
}

// Clause is one case or default clause of a switch. For a case clause Values
// holds the match expressions (it runs if any equals the subject, or — in the
// expression-less form — if any is true); the default clause has IsDefault true
// and no Values. A body whose final statement is Fallthrough transfers control to
// the next clause in source order.
type Clause struct {
	Values    []hcl.Expression
	IsDefault bool
	Body      []Statement
	SrcRange  hcl.Range
}

// Throw raises an error whose value is Expr (a string becomes
// { message = <string>, value = null }; an object is used directly).
type Throw struct {
	Expr     hcl.Expression
	SrcRange hcl.Range
}

// Defer schedules Expr to run when the enclosing function exits, in LIFO order.
type Defer struct {
	Expr     hcl.Expression
	SrcRange hcl.Range
}

// Try runs Body, optionally routing a raised error to a catch block and always
// running a finally block. At least one of Catch/Finally is present.
type Try struct {
	Body      []Statement
	HasCatch  bool
	CatchName string // error binding name; "" when omitted (catch { ... })
	Catch     []Statement
	Finally   []Statement
	SrcRange  hcl.Range
}

// Break exits the innermost enclosing loop.
type Break struct{ SrcRange hcl.Range }

// Continue skips to the next iteration of the innermost enclosing loop.
type Continue struct{ SrcRange hcl.Range }

// Fallthrough transfers control to the next clause of the enclosing switch,
// running its body without testing. It is legal only as the final statement of a
// case or default body, and not in the last clause.
type Fallthrough struct{ SrcRange hcl.Range }

func (s *VarDecl) srcRange() hcl.Range       { return s.SrcRange }
func (s *Assign) srcRange() hcl.Range        { return s.SrcRange }
func (s *CaptureAssign) srcRange() hcl.Range { return s.SrcRange }
func (s *ExprStmt) srcRange() hcl.Range      { return s.SrcRange }
func (s *Return) srcRange() hcl.Range        { return s.SrcRange }
func (s *Block) srcRange() hcl.Range         { return s.SrcRange }
func (s *IfChain) srcRange() hcl.Range       { return s.SrcRange }
func (s *For) srcRange() hcl.Range           { return s.SrcRange }
func (s *Switch) srcRange() hcl.Range        { return s.SrcRange }
func (s *Break) srcRange() hcl.Range         { return s.SrcRange }
func (s *Continue) srcRange() hcl.Range      { return s.SrcRange }
func (s *Fallthrough) srcRange() hcl.Range   { return s.SrcRange }
func (s *Throw) srcRange() hcl.Range         { return s.SrcRange }
func (s *Defer) srcRange() hcl.Range         { return s.SrcRange }
func (s *Try) srcRange() hcl.Range           { return s.SrcRange }
