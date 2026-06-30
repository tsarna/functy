package functy

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// parser is the recursive-descent statement parser. It walks the functy token
// stream itself and delegates every embedded expression and type annotation to
// HCL by recovering the relevant byte span and calling hclsyntax.ParseExpression
// / typeexpr. See scanSpan for the boundary-finding algorithm.
//
// Token-stream invariant: the lexer always emits a trailing TokenEOF, and the
// position helpers below never advance past it. So cur/peek/advance behave as if
// the stream is followed by an infinite run of EOF tokens — callers can peek and
// advance freely at the end of input without bounds checks and will simply keep
// seeing EOF.
type parser struct {
	src      []byte
	filename string
	tokens   []token
	pos      int
	diags    hcl.Diagnostics
	env      *typeEnv

	loopDepth          int
	voidReturn         bool // inside a `-> null` function body
	allowTopLevelVar   bool
	allowTopLevelConst bool
	strict             strictness
}

// cur returns the current token, or the trailing EOF token if the position is at
// or past the end (see the token-stream invariant on parser).
func (p *parser) cur() token {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return p.tokens[len(p.tokens)-1] // always EOF
}

// peek returns the token n positions ahead, or the trailing EOF token if that
// would be past the end. Peeking off the end yields EOF rather than panicking.
func (p *parser) peek(n int) token {
	i := p.pos + n
	if i < len(p.tokens) {
		return p.tokens[i]
	}
	return p.tokens[len(p.tokens)-1] // always EOF
}

// advance moves to the next token but never past the final EOF token, so the
// parser idles on EOF once it reaches the end of input.
func (p *parser) advance() {
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
}

func (p *parser) atEOF() bool { return p.cur().Type == hclsyntax.TokenEOF }

func (p *parser) errf(rng hcl.Range, summary, detail string) {
	p.diags = p.diags.Append(&hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  rng.Ptr(),
	})
}

func (p *parser) skipTerminators() {
	for isTerminator(p.cur().Type) {
		p.advance()
	}
}

// ---- Top level --------------------------------------------------------------

func (p *parser) parseFile() *Result {
	result := &Result{}
	for {
		p.skipTerminators()
		if p.atEOF() {
			break
		}
		t := p.cur()
		switch {
		case t.isKeyword("func"):
			if fn := p.parseFuncDecl(); fn != nil {
				result.Funcs = append(result.Funcs, fn)
			}
		case t.isKeyword("const"):
			p.parseTopLevelDecl(result, true)
		case t.isKeyword("var"):
			p.parseTopLevelDecl(result, false)
		case t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "type":
			// Type aliases are collected and resolved before this parse (see
			// parseSources); here we just consume the declaration.
			p.skipTypeAlias()
		default:
			p.errf(t.Range, "Expected function declaration",
				"Top-level functy declarations must be functions (func name(...) { ... }).")
			p.recoverToTopLevel()
		}
	}
	return result
}

// skipTypeAlias consumes a top-level `type Name = <type>` declaration. The alias
// was already collected and resolved (see parseSources); diagnostics for malformed
// declarations are produced there, so this only advances the parser past it.
func (p *parser) skipTypeAlias() {
	p.advance() // type
	if p.cur().Type == hclsyntax.TokenIdent && !p.cur().isAnyKeyword() {
		p.advance() // name
	}
	if p.cur().Type != hclsyntax.TokenEqual {
		p.recoverToTopLevel()
		return
	}
	p.advance() // =
	if isTerminator(p.cur().Type) || p.cur().Type == hclsyntax.TokenCBrace || p.atEOF() {
		return
	}
	p.scanSpan(stopStmt) // consume and discard the type annotation
}

// recoverToTopLevel skips to the next plausible top-level keyword so a single
// error does not cascade. It always advances at least one token first, so it
// makes progress even when called while sitting on the keyword that caused the
// error (otherwise parseFile would loop on that token forever).
func (p *parser) recoverToTopLevel() {
	if !p.atEOF() {
		p.advance()
	}
	for !p.atEOF() {
		t := p.cur()
		if t.isKeyword("func") || t.isKeyword("const") || t.isKeyword("var") {
			return
		}
		p.advance()
	}
}

func (p *parser) parseTopLevelDecl(result *Result, isConst bool) {
	kw := p.cur()
	if isConst && !p.allowTopLevelConst {
		p.errf(kw.Range, "Top-level const not allowed",
			"This functy parser does not accept top-level const declarations.")
		p.recoverToTopLevel()
		return
	}
	if !isConst && !p.allowTopLevelVar {
		p.errf(kw.Range, "Top-level var not allowed",
			"This functy parser does not accept top-level var declarations.")
		p.recoverToTopLevel()
		return
	}
	p.advance() // consume keyword
	name, ok := p.expectIdent("declaration name")
	if !ok {
		p.recoverToTopLevel()
		return
	}
	decl := Decl{Name: name.text, DefRange: hcl.RangeBetween(kw.Range, name.tok.Range)}
	kind := "Variable"
	if isConst {
		kind = "Constant"
	}
	if p.cur().Type == hclsyntax.TokenColon {
		p.advance()
		decl.Type = p.parseType(false)
	} else if p.strict.declaredTypes.on() {
		p.errf(decl.DefRange, "Missing "+strings.ToLower(kind)+" type",
			fmt.Sprintf("%s %q must declare a type (%s); use `: any` for an explicitly dynamic declaration.",
				kind, decl.Name, p.strict.declaredTypes.reason()))
	}
	if p.cur().Type == hclsyntax.TokenEqual {
		p.advance()
		decl.Expr = p.parseExprStop(stopStmt, "initializer")
	}
	if isConst {
		result.Consts = append(result.Consts, decl)
	} else {
		result.Vars = append(result.Vars, decl)
	}
}

// ---- Function declarations --------------------------------------------------

func (p *parser) parseFuncDecl() *FuncDecl {
	kw := p.cur()
	p.advance() // func
	name, ok := p.expectIdent("function name")
	if !ok {
		p.recoverToTopLevel()
		return nil
	}
	fn := &FuncDecl{Name: name.text, DefRange: hcl.RangeBetween(kw.Range, name.tok.Range)}

	if !p.expect(hclsyntax.TokenOParen, "(") {
		p.recoverToTopLevel()
		return nil
	}
	fn.Params = p.parseParams()

	// Optional return type: "->" Type
	if p.cur().Type == hclsyntax.TokenMinus && p.peek(1).Type == hclsyntax.TokenGreaterThan {
		p.advance()
		p.advance()
		fn.RetType = p.parseType(true)
	} else if p.strict.returnType.on() {
		p.errf(fn.DefRange, "Missing return type",
			fmt.Sprintf("Function %q must declare a return type (%s); use `-> any` for a dynamic return or `-> null` for void.",
				fn.Name, p.strict.returnType.reason()))
	}

	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected function body", "A function declaration must be followed by a { ... } body.")
		p.recoverToTopLevel()
		return nil
	}
	// A `-> null` (void) function may only return null; record that for the body.
	prevVoid := p.voidReturn
	_, p.voidReturn = fn.RetType.(nullConstraint)
	fn.Body = p.parseBlockBody()
	p.voidReturn = prevVoid
	return fn
}

func (p *parser) parseParams() []Param {
	var params []Param
	sawOptional := false
	sawVariadic := false

	for {
		if p.cur().Type == hclsyntax.TokenCParen {
			p.advance()
			break
		}
		if p.atEOF() {
			p.errf(p.cur().Range, "Unterminated parameter list", "Expected ) to close the parameter list.")
			break
		}

		var prm Param
		start := p.cur().Range
		if p.cur().Type == hclsyntax.TokenStar {
			prm.Variadic = true
			p.advance()
		}
		name, ok := p.expectIdent("parameter name")
		if !ok {
			p.skipToParamBoundary()
			continue
		}
		prm.Name = name.text
		prm.DefRange = hcl.RangeBetween(start, name.tok.Range)

		if p.cur().Type == hclsyntax.TokenColon {
			p.advance()
			prm.Type = p.parseType(false)
		} else if p.strict.paramTypes.on() {
			p.errf(prm.DefRange, "Missing parameter type",
				fmt.Sprintf("Parameter %q must declare a type (%s); use `: any` for an explicitly dynamic parameter.",
					prm.Name, p.strict.paramTypes.reason()))
		}
		if p.cur().Type == hclsyntax.TokenEqual {
			if prm.Variadic {
				p.errf(p.cur().Range, "Variadic parameter cannot have a default",
					"A *rest parameter collects all remaining arguments and cannot declare a default value.")
			}
			p.advance()
			prm.Default = p.parseExprStop(stopArg, "default value")
		}

		// Ordering validation.
		if sawVariadic {
			p.errf(prm.DefRange, "Parameter after variadic", "The variadic (*rest) parameter must be last.")
		}
		if prm.Variadic {
			sawVariadic = true
		} else if prm.Default != nil {
			sawOptional = true
		} else if sawOptional {
			p.errf(prm.DefRange, "Required parameter after optional",
				"Required parameters must precede optional (defaulted) parameters.")
		}

		params = append(params, prm)

		if p.cur().Type == hclsyntax.TokenComma {
			p.advance()
			continue
		}
		if p.cur().Type == hclsyntax.TokenCParen {
			p.advance()
			break
		}
		p.errf(p.cur().Range, "Expected , or )", "Parameters are separated by commas and the list ends with ).")
		p.skipToParamBoundary()
	}
	return params
}

func (p *parser) skipToParamBoundary() {
	for !p.atEOF() {
		t := p.cur().Type
		if t == hclsyntax.TokenComma {
			p.advance()
			return
		}
		if t == hclsyntax.TokenCParen || t == hclsyntax.TokenOBrace {
			return
		}
		p.advance()
	}
}

// ---- Blocks and statements --------------------------------------------------

// parseBlockBody parses a { ... } body. The current token must be the opening
// brace. It returns the body statements and leaves the parser just past the
// closing brace.
func (p *parser) parseBlockBody() []Statement {
	p.advance() // consume '{'
	stmts := p.parseStatements()
	if p.cur().Type == hclsyntax.TokenCBrace {
		p.advance()
	} else {
		p.errf(p.cur().Range, "Expected }", "Unterminated block; expected a closing brace.")
	}
	return stmts
}

// checkShortDecl registers a declare-and-capture (`:=`) target in the scope's
// declared-name set, reporting a duplicate just like a `var`. The blank `_` is
// ignored (it declares nothing).
func (p *parser) checkShortDecl(name string, rng hcl.Range, declared map[string]bool) {
	if name == "_" {
		return
	}
	if declared[name] {
		p.errf(rng, "Duplicate declaration",
			fmt.Sprintf("%q is already declared in this scope.", name))
	}
	declared[name] = true
}

func (p *parser) parseStatements() []Statement {
	var stmts []Statement
	declared := map[string]bool{}
	terminated := false // a return/break/continue has made the rest unreachable

	for {
		p.skipTerminators()
		if p.cur().Type == hclsyntax.TokenCBrace || p.atEOF() {
			break
		}

		startPos := p.pos
		stmt := p.parseStatement()
		if stmt == nil {
			if p.pos == startPos {
				p.advance() // guarantee progress on error
			}
			continue
		}

		if terminated {
			p.errf(stmt.srcRange(), "Unreachable code",
				"This statement can never be reached; it follows an unconditional return, break, or continue.")
		}
		switch s := stmt.(type) {
		case *VarDecl:
			if declared[s.Name] {
				p.errf(s.SrcRange, "Duplicate declaration",
					fmt.Sprintf("%q is already declared in this scope.", s.Name))
			}
			declared[s.Name] = true
		case *CaptureAssign:
			if s.Declare {
				p.checkShortDecl(s.ValName, s.ValRange, declared)
				p.checkShortDecl(s.ErrName, s.ErrRange, declared)
			}
		}
		switch stmt.(type) {
		case *Return, *Break, *Continue:
			terminated = true
		}
		stmts = append(stmts, stmt)
	}
	return stmts
}

func (p *parser) parseStatement() Statement {
	t := p.cur()
	switch {
	case t.isKeyword("var"):
		return p.parseVarDecl(stopStmt)
	case t.isKeyword("const"):
		p.errf(t.Range, "const is not a local declaration",
			"Inside a function body use var; const is only valid at file top level.")
		return nil
	case t.isKeyword("return"):
		return p.parseReturn()
	case t.isKeyword("break"):
		return p.parseBreak()
	case t.isKeyword("continue"):
		return p.parseContinue()
	case t.isKeyword("if"):
		return p.parseIf()
	case t.isKeyword("for"):
		return p.parseFor()
	case t.isKeyword("while"):
		return p.parseWhile()
	case t.isKeyword("switch"):
		return p.parseSwitch()
	case t.Type == hclsyntax.TokenOBrace:
		return p.parseBareBlock()
	case t.isKeyword("throw"):
		return p.parseThrow()
	case t.isKeyword("defer"):
		return p.parseDefer()
	case t.isKeyword("try"):
		return p.parseTry()
	default:
		return p.parseSimpleStmt(stopStmt)
	}
}

// parseSimpleStmt parses a var declaration, assignment, or expression statement,
// using the given stop function for the trailing expression's boundary.
func (p *parser) parseSimpleStmt(stop stopFunc) Statement {
	t := p.cur()
	if t.isKeyword("var") {
		return p.parseVarDecl(stop)
	}
	// Two-target forms: `val, err = expr` and `val, err := expr` (each target a
	// bare identifier or the blank `_`). Detected by a bare identifier followed by
	// a comma; parseCaptureAssign distinguishes the `=` and `:=` operators.
	if t.Type == hclsyntax.TokenIdent && !t.isAnyKeyword() && p.peek(1).Type == hclsyntax.TokenComma {
		return p.parseCaptureAssign(stop)
	}
	// `:=` declaration shorthand: `x := expr`, sugar for `var x = expr` (untyped).
	if t.Type == hclsyntax.TokenIdent && !t.isAnyKeyword() && isWalrus(p.peek(1), p.peek(2)) {
		return p.parseShortVarDecl(t, stop)
	}
	// Assignment: bare identifier (not a keyword) immediately followed by '='.
	if t.Type == hclsyntax.TokenIdent && !t.isAnyKeyword() && p.peek(1).Type == hclsyntax.TokenEqual {
		name := t
		p.advance() // name
		p.advance() // '='
		expr := p.parseExprStop(stop, "value")
		return &Assign{Name: string(name.Bytes), Expr: expr, SrcRange: name.Range}
	}
	start := t.Range
	expr := p.parseExprStop(stop, "expression")
	if expr == nil {
		return nil
	}
	return &ExprStmt{Expr: expr, SrcRange: start}
}

// parseCaptureAssign parses the two-target error-capture forms `val, err = expr`
// (capture into existing variables) and `val, err := expr` (declare-and-capture).
// The caller has verified the first token is a bare identifier followed by a
// comma. Either target may be the blank `_`.
func (p *parser) parseCaptureAssign(stop stopFunc) Statement {
	valTok := p.cur()
	p.advance() // value target
	p.advance() // ','
	errTok, ok := p.expectIdent("error capture target")
	if !ok {
		p.recoverToStatementEnd()
		return nil
	}

	declare := false
	switch {
	case isWalrus(p.cur(), p.peek(1)):
		declare = true
		p.advance() // ':'
		p.advance() // '='
	case p.cur().Type == hclsyntax.TokenEqual:
		p.advance() // '='
	default:
		p.errf(p.cur().Range, "Expected = or :=",
			"A `val, err` capture must be followed by `=` (capture into existing variables) "+
				"or `:=` (declare and capture).")
		p.recoverToStatementEnd()
		return nil
	}

	valName := string(valTok.Bytes)
	errName := errTok.text
	targets := hcl.RangeBetween(valTok.Range, errTok.tok.Range)
	if valName == "_" && errName == "_" {
		op := "="
		if declare {
			op = ":="
		}
		p.errf(targets, "Both capture targets are blank",
			"At least one target of a `val, err "+op+" expr` capture must be a variable; "+
				"to evaluate an expression only for its side effects, use a plain expression statement.")
	}
	expr := p.parseExprStop(stop, "value")
	return &CaptureAssign{
		ValName:  valName,
		ErrName:  errName,
		Declare:  declare,
		Expr:     expr,
		ValRange: valTok.Range,
		ErrRange: errTok.tok.Range,
		SrcRange: targets,
	}
}

// parseShortVarDecl parses the `:=` declaration shorthand `x := expr`, sugar for
// an untyped `var x = expr`. The caller has verified name is a bare identifier
// followed by the `:=` operator. Like `var`, the shorthand cannot annotate a
// type, so it is rejected under strict declared-types.
func (p *parser) parseShortVarDecl(name token, stop stopFunc) Statement {
	p.advance() // name
	p.advance() // ':'
	p.advance() // '='
	vd := &VarDecl{Name: string(name.Bytes), SrcRange: name.Range}
	if p.strict.declaredTypes.on() {
		p.errf(vd.SrcRange, "Missing variable type",
			fmt.Sprintf("Variable %q must declare a type (%s); the `:=` shorthand is untyped, "+
				"so use `var %s: <type> = ...` instead.",
				vd.Name, p.strict.declaredTypes.reason(), vd.Name))
	}
	vd.Init = p.parseExprStop(stop, "initializer")
	return vd
}

func (p *parser) parseVarDecl(stop stopFunc) Statement {
	kw := p.cur()
	p.advance() // var
	name, ok := p.expectIdent("variable name")
	if !ok {
		p.recoverToStatementEnd()
		return nil
	}
	vd := &VarDecl{Name: name.text, SrcRange: hcl.RangeBetween(kw.Range, name.tok.Range)}
	if p.cur().Type == hclsyntax.TokenColon {
		p.advance()
		vd.Type = p.parseType(false)
	} else if p.strict.declaredTypes.on() {
		p.errf(vd.SrcRange, "Missing variable type",
			fmt.Sprintf("Variable %q must declare a type (%s); use `: any` for an explicitly dynamic variable.",
				vd.Name, p.strict.declaredTypes.reason()))
	}
	if p.cur().Type == hclsyntax.TokenEqual {
		p.advance()
		vd.Init = p.parseExprStop(stop, "initializer")
	}
	return vd
}

func (p *parser) parseReturn() Statement {
	kw := p.cur()
	p.advance()
	ret := &Return{SrcRange: kw.Range}
	if isTerminator(p.cur().Type) || p.cur().Type == hclsyntax.TokenCBrace || p.atEOF() {
		return ret // bare return
	}
	ret.Expr = p.parseExprStop(stopStmt, "return value")
	if p.voidReturn && ret.Expr != nil && !isNullLiteral(ret.Expr) {
		p.errf(ret.SrcRange, "Invalid return in void function",
			"A function declared -> null may only return null (a bare `return` or `return null`).")
	}
	return ret
}

// isNullLiteral reports whether an expression is the literal null.
func isNullLiteral(expr hcl.Expression) bool {
	if lit, ok := expr.(*hclsyntax.LiteralValueExpr); ok {
		return lit.Val.IsNull()
	}
	return false
}

func (p *parser) parseBreak() Statement {
	kw := p.cur()
	p.advance()
	if p.loopDepth == 0 {
		p.errf(kw.Range, "break outside loop", "break may only be used inside a for or while loop.")
	}
	return &Break{SrcRange: kw.Range}
}

func (p *parser) parseContinue() Statement {
	kw := p.cur()
	p.advance()
	if p.loopDepth == 0 {
		p.errf(kw.Range, "continue outside loop", "continue may only be used inside a for or while loop.")
	}
	return &Continue{SrcRange: kw.Range}
}

func (p *parser) parseBareBlock() Statement {
	start := p.cur().Range
	body := p.parseBlockBody()
	return &Block{Body: body, SrcRange: start}
}

func (p *parser) parseThrow() Statement {
	kw := p.cur()
	p.advance()
	expr := p.parseExprStop(stopStmt, "thrown value")
	return &Throw{Expr: expr, SrcRange: kw.Range}
}

func (p *parser) parseDefer() Statement {
	kw := p.cur()
	p.advance()
	expr := p.parseExprStop(stopStmt, "deferred expression")
	return &Defer{Expr: expr, SrcRange: kw.Range}
}

func (p *parser) parseTry() Statement {
	start := p.cur().Range
	tr := &Try{SrcRange: start}
	p.advance() // try

	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "try must be followed by a { ... } block.")
		return tr
	}
	tr.Body = p.parseBlockBody()

	if p.cur().isKeyword("catch") {
		p.advance() // catch
		tr.HasCatch = true
		// Optional error binding name before the block.
		if p.cur().Type == hclsyntax.TokenIdent && !p.cur().isAnyKeyword() {
			tr.CatchName = string(p.cur().Bytes)
			p.advance()
		}
		if p.cur().Type != hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Expected {", "catch must be followed by a { ... } block.")
			return tr
		}
		tr.Catch = p.parseBlockBody()
	}

	if p.cur().isKeyword("finally") {
		p.advance() // finally
		if p.cur().Type != hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Expected {", "finally must be followed by a { ... } block.")
			return tr
		}
		tr.Finally = p.parseBlockBody()
	}

	if !tr.HasCatch && tr.Finally == nil {
		p.errf(start, "Incomplete try", "A try must have a catch and/or a finally block.")
	}
	return tr
}

func (p *parser) parseIf() Statement {
	start := p.cur().Range
	chain := &IfChain{SrcRange: start}
	p.advance() // if

	cond := p.parseExprStop(stopCond, "condition")
	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "An if condition must be followed by a { ... } block.")
		return chain
	}
	body := p.parseBlockBody()
	chain.Branches = append(chain.Branches, CondBranch{Condition: cond, Body: body, SrcRange: start})

	for p.cur().isKeyword("else") {
		p.advance() // else
		if p.cur().isKeyword("if") {
			elifStart := p.cur().Range
			p.advance() // if
			econd := p.parseExprStop(stopCond, "condition")
			if p.cur().Type != hclsyntax.TokenOBrace {
				p.errf(p.cur().Range, "Expected {", "An else-if condition must be followed by a { ... } block.")
				return chain
			}
			ebody := p.parseBlockBody()
			chain.Branches = append(chain.Branches, CondBranch{Condition: econd, Body: ebody, SrcRange: elifStart})
			continue
		}
		if p.cur().Type != hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Expected {", "else must be followed by a { ... } block or by if.")
			return chain
		}
		chain.Else = p.parseBlockBody()
		break
	}
	return chain
}

func (p *parser) parseWhile() Statement {
	start := p.cur().Range
	p.advance() // while
	cond := p.parseExprStop(stopCond, "condition")
	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "A while condition must be followed by a { ... } block.")
		return &For{Kind: ForCond, Cond: cond, SrcRange: start}
	}
	p.loopDepth++
	body := p.parseBlockBody()
	p.loopDepth--
	return &For{Kind: ForCond, Cond: cond, Body: body, SrcRange: start}
}

func (p *parser) parseFor() Statement {
	start := p.cur().Range
	p.advance() // for

	// Infinite loop: `for { ... }`.
	if p.cur().Type == hclsyntax.TokenOBrace {
		p.loopDepth++
		body := p.parseBlockBody()
		p.loopDepth--
		return &For{Kind: ForCond, Body: body, SrcRange: start}
	}

	switch p.classifyForHeader() {
	case ForClause:
		return p.parseForClause(start)
	case ForRange:
		return p.parseForRange(start)
	default:
		return p.parseForCond(start)
	}
}

// classifyForHeader looks ahead over the loop header (without consuming) to
// decide which of the three for forms applies: a depth-0 ';' means the
// three-clause form; a depth-0 'in' keyword means the range form; otherwise it
// is a condition loop.
func (p *parser) classifyForHeader() ForKind {
	depth := 0
	prevCompletes := false
	for i := p.pos; i < len(p.tokens); i++ {
		t := p.tokens[i]
		if t.Type == hclsyntax.TokenEOF {
			break
		}
		if depth == 0 {
			if t.Type == hclsyntax.TokenOBrace && prevCompletes {
				break // reached the body
			}
			if t.Type == hclsyntax.TokenSemicolon {
				return ForClause
			}
			if t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "in" {
				return ForRange
			}
		}
		if t.Type == hclsyntax.TokenNewline {
			continue
		}
		if isOpenBracket(t.Type) {
			depth++
		} else if isCloseBracket(t.Type) && depth > 0 {
			depth--
		}
		prevCompletes = completesExpr(t.Type)
	}
	return ForCond
}

func (p *parser) parseForCond(start hcl.Range) Statement {
	cond := p.parseExprStop(stopCond, "condition")
	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "A for condition must be followed by a { ... } block.")
		return &For{Kind: ForCond, Cond: cond, SrcRange: start}
	}
	p.loopDepth++
	body := p.parseBlockBody()
	p.loopDepth--
	return &For{Kind: ForCond, Cond: cond, Body: body, SrcRange: start}
}

func (p *parser) parseForClause(start hcl.Range) Statement {
	loop := &For{Kind: ForClause, SrcRange: start}

	if p.cur().Type != hclsyntax.TokenSemicolon {
		loop.Init = p.parseSimpleStmt(stopSemi)
	}
	if !p.expect(hclsyntax.TokenSemicolon, ";") {
		return loop
	}
	if p.cur().Type != hclsyntax.TokenSemicolon {
		loop.Cond = p.parseExprStop(stopSemi, "condition")
	}
	if !p.expect(hclsyntax.TokenSemicolon, ";") {
		return loop
	}
	if p.cur().Type != hclsyntax.TokenOBrace {
		loop.Post = p.parseSimpleStmt(stopCond)
	}
	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "A for header must be followed by a { ... } block.")
		return loop
	}
	p.loopDepth++
	loop.Body = p.parseBlockBody()
	p.loopDepth--
	return loop
}

func (p *parser) parseForRange(start hcl.Range) Statement {
	loop := &For{Kind: ForRange, SrcRange: start}

	first, ok := p.expectIdent("range variable")
	if !ok {
		return loop
	}
	if p.cur().Type == hclsyntax.TokenComma {
		p.advance()
		second, ok := p.expectIdent("range variable")
		if !ok {
			return loop
		}
		loop.KeyName = first.text
		loop.ValName = second.text
	} else {
		// Single variable binds the value (element).
		loop.ValName = first.text
	}

	if !p.cur().isKeyword("in") {
		p.errf(p.cur().Range, "Expected in", "A range loop is written: for v in collection { ... }.")
		return loop
	}
	p.advance() // in
	loop.Collection = p.parseExprStop(stopCond, "collection")

	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "A range loop header must be followed by a { ... } block.")
		return loop
	}
	p.loopDepth++
	loop.Body = p.parseBlockBody()
	p.loopDepth--
	return loop
}

func (p *parser) parseSwitch() Statement {
	start := p.cur().Range
	sw := &Switch{SrcRange: start}
	p.advance() // switch

	if p.cur().Type != hclsyntax.TokenOBrace {
		sw.Subject = p.parseExprStop(stopCond, "switch subject")
	}
	if !p.expect(hclsyntax.TokenOBrace, "{") {
		return sw
	}

	sawDefault := false
	for {
		p.skipTerminators()
		if p.cur().Type == hclsyntax.TokenCBrace || p.atEOF() {
			break
		}
		switch {
		case p.cur().isKeyword("case"):
			caseStart := p.cur().Range
			p.advance()
			var values []hcl.Expression
			for {
				v := p.parseExprStop(stopCase, "case value")
				if v != nil {
					values = append(values, v)
				}
				if p.cur().Type == hclsyntax.TokenComma {
					p.advance()
					continue
				}
				break
			}
			if !p.expect(hclsyntax.TokenColon, ":") {
				return sw
			}
			body := p.parseCaseBody()
			sw.Cases = append(sw.Cases, Case{Values: values, Body: body, SrcRange: caseStart})
		case p.cur().isKeyword("default"):
			dflt := p.cur().Range
			if sawDefault {
				p.errf(dflt, "Duplicate default", "A switch may have at most one default clause.")
			}
			sawDefault = true
			p.advance()
			if !p.expect(hclsyntax.TokenColon, ":") {
				return sw
			}
			sw.Default = p.parseCaseBody()
		default:
			p.errf(p.cur().Range, "Expected case or default",
				"A switch body contains only case and default clauses.")
			p.advance()
		}
	}
	if p.cur().Type == hclsyntax.TokenCBrace {
		p.advance()
	}
	return sw
}

// parseCaseBody parses statements until the next case, default, or closing brace.
func (p *parser) parseCaseBody() []Statement {
	var stmts []Statement
	terminated := false
	for {
		p.skipTerminators()
		t := p.cur()
		if t.Type == hclsyntax.TokenCBrace || t.isKeyword("case") || t.isKeyword("default") || p.atEOF() {
			break
		}
		startPos := p.pos
		stmt := p.parseStatement()
		if stmt == nil {
			if p.pos == startPos {
				p.advance()
			}
			continue
		}
		if terminated {
			p.errf(stmt.srcRange(), "Unreachable code",
				"This statement follows an unconditional return, break, or continue.")
		}
		switch stmt.(type) {
		case *Return, *Break, *Continue:
			terminated = true
		}
		stmts = append(stmts, stmt)
	}
	return stmts
}

func (p *parser) recoverToStatementEnd() {
	for !p.atEOF() {
		t := p.cur().Type
		if isTerminator(t) || t == hclsyntax.TokenCBrace {
			return
		}
		p.advance()
	}
}

// ---- Expression and type span scanning -------------------------------------

type stopFunc func(t hclsyntax.TokenType, prevCompletes bool) bool

// scanSpan scans from the current position, tracking bracket depth and ternary
// nesting, until stop reports a boundary at depth 0. It returns the byte span of
// the scanned region and the starting source position, and leaves the parser at
// the stopping token. Newlines at depth 0 are continuations unless stop treats
// them as terminators; a ternary ':' is never mistaken for a terminator.
func (p *parser) scanSpan(stop stopFunc) (startByte, endByte int, startPos hcl.Pos) {
	startTok := p.cur()
	startByte = startTok.Range.Start.Byte
	startPos = startTok.Range.Start

	depth := 0
	tern := 0
	prevCompletes := false
	i := p.pos
	for {
		t := p.tokens[i]
		if t.Type == hclsyntax.TokenEOF {
			break
		}
		if depth == 0 {
			if t.Type == hclsyntax.TokenQuestion {
				tern++
			} else if t.Type == hclsyntax.TokenColon && tern > 0 {
				tern--
				prevCompletes = false
				i++
				continue
			} else if stop(t.Type, prevCompletes) {
				break
			}
		}
		if t.Type == hclsyntax.TokenNewline {
			i++ // continuation
			continue
		}
		if isOpenBracket(t.Type) {
			depth++
		} else if isCloseBracket(t.Type) && depth > 0 {
			depth--
		}
		prevCompletes = completesExpr(t.Type)
		i++
	}
	p.pos = i
	// The span is exclusive of the stopping token, except when that token is the
	// terminating newline: a heredoc close marker must be followed by a newline
	// for HCL to recognize it, so include the newline (harmless trailing
	// whitespace for any other expression). The newline token is still left at
	// p.pos for the caller to consume as the terminator.
	stopTok := p.tokens[i]
	if stopTok.Type == hclsyntax.TokenNewline {
		endByte = stopTok.Range.End.Byte
	} else {
		endByte = stopTok.Range.Start.Byte
	}
	return startByte, endByte, startPos
}

func (p *parser) parseExprStop(stop stopFunc, what string) hcl.Expression {
	if isTerminator(p.cur().Type) || p.cur().Type == hclsyntax.TokenCBrace || p.atEOF() {
		p.errf(p.cur().Range, "Expected expression", fmt.Sprintf("Expected an %s expression here.", what))
		return nil
	}
	sb, eb, sp := p.scanSpan(stop)
	if eb <= sb {
		p.errf(p.cur().Range, "Expected expression", fmt.Sprintf("Expected an %s expression here.", what))
		return nil
	}
	expr, diags := hclsyntax.ParseExpression(p.src[sb:eb], p.filename, sp)
	p.diags = p.diags.Extend(diags)
	return expr
}

// parseType parses and resolves a type annotation to a constraint (nil on error).
// allowNull permits the `null` void return type, which is invalid elsewhere.
func (p *parser) parseType(allowNull bool) TypeConstraint {
	if isTerminator(p.cur().Type) || p.atEOF() {
		p.errf(p.cur().Range, "Expected type", "Expected a type annotation here.")
		return nil
	}
	sb, eb, sp := p.scanSpan(stopType)
	if eb <= sb {
		p.errf(p.cur().Range, "Expected type", "Expected a type annotation here.")
		return nil
	}
	expr, diags := hclsyntax.ParseExpression(p.src[sb:eb], p.filename, sp)
	p.diags = p.diags.Extend(diags)
	if diags.HasErrors() {
		return nil
	}
	tc, rdiags := p.env.resolveType(expr, allowNull)
	p.diags = p.diags.Extend(rdiags)
	return tc
}

// ---- Stop functions for each scanning context ------------------------------

func stopStmt(t hclsyntax.TokenType, prevCompletes bool) bool {
	switch t {
	case hclsyntax.TokenSemicolon:
		return true
	case hclsyntax.TokenNewline:
		return prevCompletes
	case hclsyntax.TokenOBrace:
		return prevCompletes
	default:
		return isCloseBracket(t)
	}
}

func stopCond(t hclsyntax.TokenType, prevCompletes bool) bool {
	switch t {
	case hclsyntax.TokenOBrace:
		return prevCompletes
	case hclsyntax.TokenSemicolon:
		return true
	default:
		return isCloseBracket(t)
	}
}

func stopArg(t hclsyntax.TokenType, prevCompletes bool) bool {
	if t == hclsyntax.TokenComma {
		return true
	}
	return isCloseBracket(t)
}

func stopType(t hclsyntax.TokenType, prevCompletes bool) bool {
	switch t {
	case hclsyntax.TokenEqual, hclsyntax.TokenComma, hclsyntax.TokenSemicolon:
		return true
	case hclsyntax.TokenNewline, hclsyntax.TokenOBrace:
		return prevCompletes
	default:
		return isCloseBracket(t)
	}
}

func stopSemi(t hclsyntax.TokenType, prevCompletes bool) bool {
	if t == hclsyntax.TokenSemicolon {
		return true
	}
	return isCloseBracket(t)
}

func stopCase(t hclsyntax.TokenType, prevCompletes bool) bool {
	switch t {
	case hclsyntax.TokenComma, hclsyntax.TokenColon:
		return true
	default:
		return isCloseBracket(t)
	}
}

func completesExpr(t hclsyntax.TokenType) bool {
	switch t {
	case hclsyntax.TokenIdent, hclsyntax.TokenNumberLit,
		hclsyntax.TokenCQuote, hclsyntax.TokenCParen,
		hclsyntax.TokenCBrack, hclsyntax.TokenCBrace,
		hclsyntax.TokenCHeredoc:
		return true
	}
	return false
}

// ---- Small token helpers ----------------------------------------------------

type identTok struct {
	text string
	tok  token
}

func (p *parser) expectIdent(what string) (identTok, bool) {
	t := p.cur()
	if t.Type == hclsyntax.TokenIdent && !t.isAnyKeyword() {
		p.advance()
		return identTok{text: string(t.Bytes), tok: t}, true
	}
	p.errf(t.Range, "Expected "+what, fmt.Sprintf("Expected an identifier (%s) here.", what))
	return identTok{}, false
}

func (p *parser) expect(tt hclsyntax.TokenType, lexeme string) bool {
	if p.cur().Type == tt {
		p.advance()
		return true
	}
	p.errf(p.cur().Range, "Expected "+lexeme, fmt.Sprintf("Expected %q here.", lexeme))
	return false
}
