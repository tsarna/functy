package functy

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// FuncDecl is a top-level function declaration.
//
// An extern (see Result.Externs) is a FuncDecl with Extern true: it declares the
// signature of a function the *host* provides, so it has no Body and a zero
// BodyRange. SigRange is therefore the only end position an extern has, and every
// consumer that would reach for BodyRange.End must use it instead.
type FuncDecl struct {
	Name string
	// Namespace is the enclosing namespace, from the file's `namespace a::b`
	// declaration; "" is the global namespace. See NamespaceDecl.
	Namespace string
	// Doc is the rendered leading doc-comment block (`//` or `#` lines directly
	// above the declaration, directive lines excluded); "" when there is none.
	Doc        string
	Params     []Param
	ParenRange hcl.Range      // the (…) parameter-list span, for fmt
	RetType    TypeConstraint // nil when no return type is annotated (dynamic)
	RetTypeSrc string         // source text of the return-type annotation, for rendering (fmt)
	SigRange   hcl.Range      // spans `func` … the last signature token (`)` or the return type)
	// Extern marks a declaration from a //functy:extern file: a signature only,
	// never compiled and never callable. See Result.Externs.
	Extern    bool
	Body      []Statement
	BodyRange hcl.Range // the `{ ... }` body span, for rendering (fmt); zero for an extern
	DefRange  hcl.Range
}

// QualifiedName is the name the function is registered under with the host:
// its namespace and bare name joined by `::`, or just the bare name in the
// global namespace. Private functions have a qualified name but are never
// handed to the host (see Compiled).
func (f *FuncDecl) QualifiedName() string { return Qualify(f.Namespace, f.Name) }

// IsPrivate reports whether the declaration is namespace-local: visible to the
// other functions of its namespace, never registered with the host.
//
// Privacy is a naming convention (a leading underscore) rather than a keyword,
// so it is a pure function of the name and cannot desync from it. It also makes
// a private name incapable of colliding with a host function, since no host
// function, cty builtin, or add-on package function is `_`-prefixed.
func (f *FuncDecl) IsPrivate() bool { return isPrivateName(f.Name) }

// Qualify joins a namespace ("" = global) and a bare name into the name the
// function is registered under.
//
// HCL parses `a::b::c(x)` natively and resolves it as a single flat map key, so a
// qualified name needs no structure beyond the string itself: nesting is a naming
// convention, not a containment relationship, and there is no parent-namespace
// fallback. Exported so a host that walks Compiled.Units — which is keyed by bare
// name — can render the callable name without reimplementing the join.
func Qualify(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "::" + name
}

func isPrivateName(name string) bool { return strings.HasPrefix(name, "_") }

// TestDecl is a top-level `test "description" { … }` block. Its body is ordinary
// functy statements; the test passes if the body runs to completion and fails if an
// error (a failed assert, a throw, an eval error) unwinds out of it. Tests are not
// registered in the callable function namespace.
type TestDecl struct {
	Name      string      // the test description (a string literal)
	Namespace string      // the namespace of the file the test was declared in ("" = global)
	Body      []Statement // body statements, like a function body
	BodyRange hcl.Range   // the `{ ... }` body span, for rendering (fmt)
	DefRange  hcl.Range   // spans `test` … closing `}`
}

func (t *TestDecl) srcRange() hcl.Range { return t.DefRange }

// SetupDecl is a top-level `test setup { … }` block: shared setup whose statements
// are spliced onto the front of every test in the *same source file* (see
// RunTestsMatching), in the same scope, so its bindings are visible to each test and
// its `defer`s run — function-scoped — at each test's end. `test setup` reuses the
// contextual `test` keyword (the token after `test` is the ident `setup`, not a
// string). A file may declare several; they concatenate in source order.
type SetupDecl struct {
	Namespace string      // the namespace of the file the block was declared in ("" = global)
	Body      []Statement // body statements, spliced ahead of each test's body
	BodyRange hcl.Range   // the `{ ... }` body span, for rendering (fmt)
	DefRange  hcl.Range   // spans `test` … closing `}`; DefRange.Filename groups by file
}

func (s *SetupDecl) srcRange() hcl.Range { return s.DefRange }

// NamespaceDecl is a file's leading `namespace a::b` declaration.
//
// A namespace name is one or more `::`-separated identifiers: `namespace foo` is
// as legitimate as `namespace foo::bar`, and no depth is implied. Nesting is
// purely a naming convention — `foo::bar` is not "inside" `foo` in any sense
// functy or HCL enforces, and code in `foo::bar` gets no special visibility into
// `foo`.
//
// The declaration is modeled in the AST (not merely stamped onto the decls it
// governs) because fmt renders from the AST: an unmodeled top-level item would be
// silently deleted on reformat.
type NamespaceDecl struct {
	Name     string    // "foo" or "foo::bar"
	DefRange hcl.Range // spans `namespace` … the last segment
}

func (n *NamespaceDecl) srcRange() hcl.Range { return n.DefRange }

// Param is a single function parameter.
//
// A parameter is required unless it has a Default, is marked Optional, or is
// Variadic. A typed parameter (Type != cty.NilType) converts its argument to that
// type. For a variadic parameter, Type is the *element* type: `*rest: T` collects
// the extra arguments into a list(T), while an untyped `*rest` collects them into a
// tuple.
type Param struct {
	Name string
	// Doc is the parameter's documentation: a trailing comment on its line
	// (`a: T, // desc`) or a leading `//` / `#` block directly above it (which
	// wins if both are present); "" when there is none. See attachDocComments.
	Doc        string
	Type       TypeConstraint // nil when unannotated (dynamic)
	TypeSrc    string         // source text of the type annotation, for rendering (fmt)
	Default    hcl.Expression // non-nil for a defaulted parameter
	DefaultSrc string         // source text of the default expression, for rendering (help()/fmt)
	// Optional marks `name?`: optional with *no* default, and mutually exclusive
	// with Default. It exists because it is the only way to spell an optional
	// *leading* parameter — the `get([ctx,] thing)` convention that host libraries
	// implement by sniffing args[0], and that cty's parameter list cannot express
	// (an optional cty param may only sit at the tail).
	//
	// Legal only in an extern file, where it is never compiled: BuildFunction
	// classifies a param with no Default as *required*, so an Optional param
	// reaching it would be silently mis-compiled. The compile path guards on
	// FuncDecl.Extern to keep that unreachable.
	Optional  bool
	Variadic  bool // true for the trailing *rest parameter
	DefRange  hcl.Range
	FullRange hcl.Range // spans the whole parameter (name … type/default), for fmt
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
	TypeSrc  string         // source text of the type annotation, for rendering (fmt)
	Init     hcl.Expression
	Short    bool // declared with the `:=` shorthand (always untyped); preserved for fmt
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
	Branches  []CondBranch
	Else      []Statement
	ElseRange hcl.Range // the `else { ... }` body span (zero when there is no else), for fmt
	SrcRange  hcl.Range
}

// CondBranch is one condition-guarded branch of an if chain.
type CondBranch struct {
	Condition hcl.Expression
	Body      []Statement
	BodyRange hcl.Range // the `{ ... }` body span, for fmt
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

	// Label is the loop's label ("" if unlabeled); a labeled break/continue whose
	// target equals this label is consumed by this loop rather than an inner one.
	Label string

	// While is true when a ForCond loop was written with the `while` keyword rather
	// than `for` (the two are synonyms); recorded so fmt can preserve the keyword.
	While bool

	// ForCond / ForClause:
	Init Statement      // ForClause init clause (nil otherwise)
	Cond hcl.Expression // condition (nil = always true)
	Post Statement      // ForClause post clause (nil otherwise)

	// ForRange:
	KeyName    string         // first range variable ("" if absent)
	ValName    string         // second range variable ("" if only one is given)
	Collection hcl.Expression // collection being ranged over

	Body      []Statement
	BodyRange hcl.Range // the `{ ... }` body span, for fmt
	SrcRange  hcl.Range
}

// Switch is a switch statement. Subject is nil for the expression-less form,
// whose case values are boolean expressions evaluated like an if/else chain.
// Clauses are in source order; at most one is the default.
type Switch struct {
	Subject   hcl.Expression
	Clauses   []Clause
	BodyRange hcl.Range // the `{ ... }` body span (open brace to close brace), for fmt
	SrcRange  hcl.Range
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

// Try runs Body, routing a raised error through its catch clauses (first match
// wins) and always running a finally block. At least one of Catches/Finally is
// present.
type Try struct {
	Body         []Statement
	BodyRange    hcl.Range // the `try { ... }` body span, for fmt
	Catches      []CatchClause
	Finally      []Statement
	FinallyRange hcl.Range // the `finally { ... }` body span (zero when absent), for fmt
	SrcRange     hcl.Range
}

// CatchClause is one `catch [name] [: type] [if guard] { ... }` clause. It matches
// a raised error iff its type filter's Coerce succeeds (Type == nil matches any
// shape) and its guard evaluates true (Guard == nil is unconditional); a clause
// with both Type and Guard nil is the catch-all. The bound name receives the raw
// error value (the filter is a gate, not a cast).
type CatchClause struct {
	Name      string         // "" when omitted
	Type      TypeConstraint // nil = no type filter
	TypeSrc   string         // source text of the type filter, for rendering (fmt)
	Guard     hcl.Expression // nil = no guard
	Body      []Statement
	BodyRange hcl.Range // the `{ ... }` body span, for fmt
	SrcRange  hcl.Range
}

// Break exits an enclosing loop: the innermost one, or the loop named by Label.
type Break struct {
	Label    string // "" for the innermost loop
	SrcRange hcl.Range
}

// Continue skips to the next iteration of an enclosing loop: the innermost one,
// or the loop named by Label.
type Continue struct {
	Label    string // "" for the innermost loop
	SrcRange hcl.Range
}

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
