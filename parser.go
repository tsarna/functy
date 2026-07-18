package functy

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
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
	// env is the type environment this file resolves annotations against. It starts
	// as the global env and is swapped for the file's namespace env once the leading
	// `namespace` declaration is parsed (see parseFile), giving own-then-global type
	// resolution. envByNS maps every namespace ("" = global) to its resolved env.
	env     *typeEnv
	envByNS map[string]*typeEnv

	loops        []string // labels of enclosing loops ("" for an unlabeled loop)
	pendingLabel string   // a label parsed but not yet attached to its loop

	voidReturn         bool // inside a `-> null` function body
	allowTopLevelVar   bool
	allowTopLevelConst bool
	strict             strictness

	// extern marks a //functy:extern file: it holds only bodiless func
	// declarations documenting functions the host provides. A bodiless func is a
	// hard error in every other file — deliberately, so that a half-typed
	// declaration (signature written, brace not yet opened) stays a syntax error
	// rather than silently becoming a valid one.
	extern bool

	// opaqueWarned records the unregistered type names already warned about in this
	// extern file, so each is reported once rather than once per use. An extern
	// names its host's types on nearly every line (`ctx` especially), and a warning
	// per occurrence would bury anything else the file has to say.
	opaqueWarned map[string]bool

	// ns is the file's namespace, from a leading `namespace a::b` declaration
	// ("" = the global namespace). Every declaration parsed from this file is
	// stamped with it; it is the only carrier of that provenance, since the
	// per-file Result is flattened into one merged Result by parseSources.
	ns string
}

// enterLoop pushes a loop onto the enclosing-loop stack, consuming any pending
// label, and returns that label (so it can be stored on the For node). exitLoop
// pops it. inLoop / labelInScope back break/continue validation.
func (p *parser) enterLoop() string {
	label := p.pendingLabel
	p.pendingLabel = ""
	p.loops = append(p.loops, label)
	return label
}

func (p *parser) exitLoop() {
	p.loops = p.loops[:len(p.loops)-1]
}

func (p *parser) inLoop() bool { return len(p.loops) > 0 }

func (p *parser) labelInScope(label string) bool {
	for _, l := range p.loops {
		if l == label {
			return true
		}
	}
	return false
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

// skipNewlines advances past newline tokens only (not semicolons). It is used
// where a construct may span multiple lines without `;` being meaningful — e.g.
// inside a parameter list — so a newline there is insignificant whitespace rather
// than a statement terminator.
func (p *parser) skipNewlines() {
	for p.cur().Type == hclsyntax.TokenNewline {
		p.advance()
	}
}

// ---- Top level --------------------------------------------------------------

func (p *parser) parseFile() *Result {
	result := &Result{}

	// A namespace declaration, if present, must come first: it governs the name
	// every following declaration is registered under, so it cannot be discovered
	// halfway through the file.
	p.skipTerminators()
	if p.atNamespaceKeyword() {
		if nd := p.parseNamespaceDecl(); nd != nil {
			p.ns = nd.Name
			// Resolve this file's annotations against its namespace's env (own
			// aliases first, then global + host types). Absent when the namespace
			// declares no aliases of its own, in which case the global env is right.
			if e, ok := p.envByNS[p.ns]; ok {
				p.env = e
			}
			result.Namespaces = append(result.Namespaces, *nd)
		}
	}

	for {
		p.skipTerminators()
		if p.atEOF() {
			break
		}
		t := p.cur()
		switch {
		case t.isKeyword("func"):
			if fn := p.parseFuncDecl(); fn != nil {
				if p.extern {
					fn.Extern = true
					result.Externs = append(result.Externs, fn)
				} else {
					result.Funcs = append(result.Funcs, fn)
				}
			}
		case t.isKeyword("const"):
			if p.rejectInExtern(t.Range, "const") {
				continue
			}
			p.parseTopLevelDecl(result, true)
		case t.isKeyword("var"):
			if p.rejectInExtern(t.Range, "var") {
				continue
			}
			p.parseTopLevelDecl(result, false)
		case t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "type":
			if p.rejectInExtern(t.Range, "type") {
				continue
			}
			// Type aliases are collected and resolved before this parse (see
			// parseSources); here we just consume the declaration.
			p.skipTypeAlias()
		case t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "test":
			if p.rejectInExtern(t.Range, "test") {
				continue
			}
			if td := p.parseTestDecl(); td != nil {
				result.Tests = append(result.Tests, td)
			}
		case p.atNamespaceKeyword():
			// Misplaced: either a second declaration, or one that follows other
			// declarations. Record it (so an editor outline and fmt still see it)
			// but do NOT adopt it — declarations parsed before this point are
			// already stamped with the old namespace, and adopting it now would
			// leave the file half in one namespace and half in another. The error
			// makes the file un-formattable and un-compilable anyway.
			if nd := p.parseNamespaceDecl(); nd != nil {
				result.Namespaces = append(result.Namespaces, *nd)
				p.errf(nd.DefRange, "Misplaced namespace declaration",
					"A namespace declaration must be the first declaration in the file, and a file may declare only one.")
			}
		default:
			if p.extern {
				p.errf(t.Range, "Expected extern declaration",
					"An extern file (//functy:extern) holds only bodiless function declarations (func name(...) -> T).")
			} else {
				p.errf(t.Range, "Expected function declaration",
					"Top-level functy declarations must be functions (func name(...) { ... }).")
			}
			p.recoverToTopLevel()
		}
	}
	return result
}

// rejectInExtern reports a declaration that an extern file may not contain, and
// reports whether it did so (in which case the caller must not parse it).
//
// An extern file documents the functions a *host* provides, so it holds no functy
// code of its own: var, const, test and type have nothing to describe there. The
// var/const rejection has to fire even when the host set AllowTopLevelVar/Const —
// as the CLI does — so it is checked before parseTopLevelDecl, not inside it.
func (p *parser) rejectInExtern(rng hcl.Range, keyword string) bool {
	if !p.extern {
		return false
	}
	p.errf(rng, "Declaration not allowed in an extern file",
		fmt.Sprintf("An extern file (//functy:extern) may contain only bodiless func declarations; move the %s declaration to a regular functy file.", keyword))
	p.recoverToTopLevel()
	return true
}

// atNamespaceKeyword reports whether the parser is sitting on a `namespace`
// declaration. Like `test` and `type`, `namespace` is a *contextual* keyword —
// special only at top-level declaration position — so it stays usable as an
// ordinary identifier everywhere else (`func namespace()`, `var namespace = 1`,
// a call to `namespace()`), and it is deliberately absent from lexer.go's
// keywords map.
func (p *parser) atNamespaceKeyword() bool {
	t := p.cur()
	return t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "namespace"
}

// parseNamespaceDecl parses `namespace Ident ( "::" Ident )*`.
//
// A namespace name is one or more `::`-separated identifiers — `namespace foo` is
// as valid as `namespace foo::bar`, and no depth is implied, because HCL treats a
// `::` name as one flat lookup key rather than a structure.
func (p *parser) parseNamespaceDecl() *NamespaceDecl {
	kw := p.cur()
	p.advance() // namespace

	var segs []string
	last := kw
	for {
		seg, ok := p.expectIdent("namespace name")
		if !ok {
			p.recoverToTopLevel()
			return nil
		}
		segs = append(segs, seg.text)
		last = seg.tok
		if p.cur().Type != hclsyntax.TokenDoubleColon {
			break
		}
		p.advance() // ::
	}

	defRange := hcl.RangeBetween(kw.Range, last.Range)
	if !isTerminator(p.cur().Type) && !p.atEOF() {
		p.errf(p.cur().Range, "Extra tokens after namespace declaration",
			"A namespace declaration names one namespace: `namespace foo` or `namespace foo::bar`.")
		p.recoverToTopLevel()
		return nil
	}
	return &NamespaceDecl{Name: strings.Join(segs, "::"), DefRange: defRange}
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
		// Also sync on the contextual top-level keywords (idents, not reserved
		// words) so an error in one block doesn't swallow a following
		// test/type/namespace.
		if t.Type == hclsyntax.TokenIdent &&
			(string(t.Bytes) == "test" || string(t.Bytes) == "type" || string(t.Bytes) == "namespace") {
			return
		}
		p.advance()
	}
}

// checkDeclName rejects `_` as a declaration name. A leading underscore marks a
// declaration namespace-local, but a *bare* underscore is the blank identifier
// (see CaptureAssign) and would leave the declaration with an empty base name.
func (p *parser) checkDeclName(name string, rng hcl.Range, kind string) bool {
	if name == "_" {
		p.errf(rng, "Invalid "+kind+" name",
			"`_` is the blank identifier and cannot name a declaration. Use a name like `_helper` for a namespace-local "+kind+".")
		return false
	}
	return true
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
	kindWord := "variable"
	if isConst {
		kindWord = "constant"
	}
	if !p.checkDeclName(name.text, name.tok.Range, kindWord) {
		p.recoverToTopLevel()
		return
	}
	decl := Decl{Name: name.text, Namespace: p.ns, DefRange: hcl.RangeBetween(kw.Range, name.tok.Range)}
	kind := "Variable"
	if isConst {
		kind = "Constant"
	}
	if p.cur().Type == hclsyntax.TokenColon {
		p.advance()
		decl.Type, decl.TypeSrc = p.parseType(false)
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
	if !p.checkDeclName(name.text, name.tok.Range, "function") {
		p.recoverToTopLevel()
		return nil
	}
	if p.extern && isPrivateName(name.text) {
		p.errf(name.tok.Range, "Private extern function",
			"An extern declares a function the host provides, so it is global by definition; a `_`-prefixed (namespace-local) name is not allowed in an extern file.")
		p.recoverToTopLevel()
		return nil
	}
	fn := &FuncDecl{Name: name.text, Namespace: p.ns, DefRange: hcl.RangeBetween(kw.Range, name.tok.Range)}

	oparen := p.cur().Range
	if !p.expect(hclsyntax.TokenOParen, "(") {
		p.recoverToTopLevel()
		return nil
	}
	var cparen hcl.Range
	fn.Params, cparen = p.parseParams()
	fn.ParenRange = hcl.RangeBetween(oparen, cparen)
	sigEnd := cparen

	// The rest of the signature may continue on later lines: newlines before the
	// return-type arrow, its type, and the body brace are insignificant.
	p.skipNewlines()

	// Optional return type: "->" Type
	if p.cur().Type == hclsyntax.TokenMinus && p.peek(1).Type == hclsyntax.TokenGreaterThan {
		p.advance()
		p.advance()
		p.skipNewlines()
		fn.RetType, fn.RetTypeSrc = p.parseType(true)
		// Take the end of the signature before skipping newlines, which would carry
		// it onto the next line. An extern has no body, so this is the only end
		// position it has (see FuncDecl.SigRange).
		sigEnd = p.tokens[p.pos-1].Range
		p.skipNewlines()
	} else if p.strict.returnType.on() {
		p.errf(fn.DefRange, "Missing return type",
			fmt.Sprintf("Function %q must declare a return type (%s); use `-> any` for a dynamic return or `-> null` for void.",
				fn.Name, p.strict.returnType.reason()))
	}
	fn.SigRange = hcl.RangeBetween(kw.Range, sigEnd)

	if p.extern {
		if p.cur().Type == hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Extern function cannot have a body",
				"In an extern file (//functy:extern) a func declaration is a signature only; remove the { ... } body.")
			// Consume the body rather than recovering: recoverToTopLevel resyncs on
			// `var`/`const`/`test`, so a `var` *inside* the body would be mistaken for
			// a top-level declaration and cascade.
			p.parseBlockBody()
		}
		return fn
	}

	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected function body", "A function declaration must be followed by a { ... } body.")
		p.recoverToTopLevel()
		return nil
	}
	// A `-> null` (void) function may only return null; record that for the body.
	prevVoid := p.voidReturn
	_, p.voidReturn = fn.RetType.(nullConstraint)
	fn.Body, fn.BodyRange = p.parseBlockBody()
	p.voidReturn = prevVoid
	return fn
}

// parseTestDecl parses a top-level `test "description" { … }` block. Like `type`,
// `test` is a contextual keyword — special only at top-level declaration position —
// so it stays usable as an ordinary identifier elsewhere. The description must be a
// constant string; the body is parsed exactly like a function body.
func (p *parser) parseTestDecl() *TestDecl {
	kw := p.cur()
	p.advance() // test

	descExpr := p.parseExprStop(stopCond, "test description")
	if descExpr == nil {
		p.recoverToTopLevel()
		return nil
	}
	name := ""
	ok := true
	if dv, diags := descExpr.Value(nil); diags.HasErrors() {
		ok = false
	} else if dv.IsNull() || !dv.IsKnown() || dv.Type() != cty.String {
		ok = false
	} else {
		name = dv.AsString()
	}
	if !ok {
		p.errf(descExpr.Range(), "Invalid test description",
			`A test block's description must be a constant string, e.g. test "adds two numbers" { … }.`)
	}

	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected test body", "A test declaration must be followed by a { ... } body.")
		p.recoverToTopLevel()
		return nil
	}
	body, brange := p.parseBlockBody()
	if !ok {
		return nil // description error already reported; body consumed to avoid cascade
	}
	return &TestDecl{Name: name, Namespace: p.ns, Body: body, BodyRange: brange, DefRange: hcl.RangeBetween(kw.Range, p.tokens[p.pos-1].Range)}
}

func (p *parser) parseParams() ([]Param, hcl.Range) {
	var params []Param
	var closeParen hcl.Range
	sawOptional := false
	sawVariadic := false

	for {
		// A parameter list may span multiple lines: newlines after `(`, after a
		// comma, and before `)` are insignificant.
		p.skipNewlines()
		if p.cur().Type == hclsyntax.TokenCParen {
			closeParen = p.cur().Range
			p.advance()
			break
		}
		if p.atEOF() {
			p.errf(p.cur().Range, "Unterminated parameter list", "Expected ) to close the parameter list.")
			closeParen = p.cur().Range
			break
		}
		// Reaching the body brace means the list was never closed; error and stop
		// (rather than loop forever re-reporting a missing parameter name). The `{`
		// is left for the caller to consume as the function body.
		if p.cur().Type == hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Unterminated parameter list",
				"Expected ) to close the parameter list before the function body.")
			closeParen = p.cur().Range
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
		// DefRange stays name-only: it is what the doc-comment attachment pass keys
		// on, and what a diagnostic about this parameter should point at.
		prm.DefRange = hcl.RangeBetween(start, name.tok.Range)

		// `name?` — optional with no default. The `?` is consumed here, before any
		// type or default is parsed, so no expression scan ever begins on it.
		if p.cur().Type == hclsyntax.TokenQuestion {
			if !p.extern {
				p.errf(p.cur().Range, "Optional parameter marker not allowed here",
					"`name?` (optional with no default) may only be used in an extern file (//functy:extern), where it is never compiled. Give the parameter a default instead: `name = null`.")
			}
			if prm.Variadic {
				p.errf(p.cur().Range, "Variadic parameter cannot be optional",
					"A *rest parameter already accepts zero arguments, so the `?` marker is redundant and not allowed on it.")
			}
			prm.Optional = true
			p.advance()
		}

		if p.cur().Type == hclsyntax.TokenColon {
			p.advance()
			prm.Type, prm.TypeSrc = p.parseType(false)
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
			if prm.Optional {
				p.errf(p.cur().Range, "Optional parameter cannot have a default",
					"`name?` already marks the parameter optional with no default. Write either `name?` or `name = <default>`, not both.")
			}
			p.advance()
			prm.Default = p.parseExprStop(stopArg, "default value")
			if prm.Default != nil {
				if r := prm.Default.Range(); r.Start.Byte >= 0 && r.End.Byte <= len(p.src) && r.Start.Byte <= r.End.Byte {
					prm.DefaultSrc = strings.TrimSpace(string(p.src[r.Start.Byte:r.End.Byte]))
				}
			}
		}

		prm.FullRange = hcl.RangeBetween(start, p.tokens[p.pos-1].Range)

		// Ordering validation.
		if sawVariadic {
			p.errf(prm.DefRange, "Parameter after variadic", "The variadic (*rest) parameter must be last.")
		}
		switch {
		case prm.Variadic:
			sawVariadic = true
		case prm.Default != nil || prm.Optional:
			sawOptional = true
		case sawOptional && !p.extern:
			// Relaxed for externs: an extern transcribes a host function's real
			// shape, which may take optional arguments at the *head* as well as the
			// tail — the `get([ctx,] thing)` convention that `?` exists to spell.
			p.errf(prm.DefRange, "Required parameter after optional",
				"Required parameters must precede optional (defaulted) parameters.")
		}

		params = append(params, prm)

		// Allow a newline between the parameter and its separator (a trailing
		// parameter before `)`, or before its comma).
		p.skipNewlines()
		if p.cur().Type == hclsyntax.TokenComma {
			p.advance()
			continue
		}
		if p.cur().Type == hclsyntax.TokenCParen {
			closeParen = p.cur().Range
			p.advance()
			break
		}
		p.errf(p.cur().Range, "Expected , or )", "Parameters are separated by commas and the list ends with ).")
		p.skipToParamBoundary()
	}
	return params, closeParen
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
func (p *parser) parseBlockBody() ([]Statement, hcl.Range) {
	open := p.cur() // '{'
	p.advance()     // consume '{'
	stmts := p.parseStatements()
	closeTok := p.cur()
	if closeTok.Type == hclsyntax.TokenCBrace {
		p.advance()
	} else {
		p.errf(closeTok.Range, "Expected }", "Unterminated block; expected a closing brace.")
	}
	return stmts, hcl.RangeBetween(open.Range, closeTok.Range)
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
		// Brace-aware recovery: a `func` keyword can never appear at statement
		// position (closures / nested functions are a non-goal — see DESIGN.md),
		// so encountering one means an enclosing block was left unterminated and
		// the following top-level declaration has leaked into this body. Stop here
		// rather than swallowing it; the caller (parseBlockBody) reports the
		// missing `}` and parseFile resynchronizes on the leaked `func`.
		if p.cur().isKeyword("func") {
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
	case t.isKeyword("fallthrough"):
		p.advance()
		p.errf(t.Range, "Misplaced fallthrough",
			"fallthrough may only be used as the final statement of a switch case, "+
				"not nested in another statement or outside a switch.")
		return nil
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
	case t.Type == hclsyntax.TokenIdent && !t.isAnyKeyword() &&
		p.peek(1).Type == hclsyntax.TokenColon && !isWalrus(p.peek(1), p.peek(2)):
		// `label:` preceding a loop. A plain colon after a bare identifier at the
		// start of a statement is only valid as a loop label (`x :=` is the walrus,
		// excluded above).
		return p.parseLabeled(t)
	default:
		return p.parseSimpleStmt(stopStmt)
	}
}

// parseLabeled parses a `label:` prefix and attaches it to the following loop.
// The label is in scope for the loop's body (so a break/continue inside may name
// it) and must be unique among enclosing loops. A label may only precede a loop.
func (p *parser) parseLabeled(nameTok token) Statement {
	label := string(nameTok.Bytes)
	p.advance() // label name
	p.advance() // ':'
	p.skipTerminators()
	if !(p.cur().isKeyword("for") || p.cur().isKeyword("while")) {
		p.errf(nameTok.Range, "Label must precede a loop",
			"A label may only be attached to a for or while loop.")
		return nil
	}
	if p.labelInScope(label) {
		p.errf(nameTok.Range, "Duplicate label",
			fmt.Sprintf("Label %q is already used by an enclosing loop.", label))
	}
	p.pendingLabel = label
	stmt := p.parseStatement() // the for/while consumes pendingLabel via enterLoop
	p.pendingLabel = ""        // defensive: clear if an error path skipped enterLoop
	return stmt
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
		// Span the whole statement (target through value), not just the target name,
		// so a diagnostic subjected here — e.g. "Unreachable code" — underlines the
		// full assignment. p.pos-1 is the last token the value consumed.
		return &Assign{Name: string(name.Bytes), Expr: expr, SrcRange: hcl.RangeBetween(name.Range, p.tokens[p.pos-1].Range)}
	}
	start := t.Range
	expr := p.parseExprStop(stop, "expression")
	if expr == nil {
		return nil
	}
	// Span the whole expression statement, not just its first token, so an
	// "Unreachable code" diagnostic underlines all of it. p.pos-1 is the last token
	// the expression consumed.
	return &ExprStmt{Expr: expr, SrcRange: hcl.RangeBetween(start, p.tokens[p.pos-1].Range)}
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
	vd := &VarDecl{Name: string(name.Bytes), SrcRange: name.Range, Short: true}
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
		vd.Type, vd.TypeSrc = p.parseType(false)
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
	label := p.parseLoopControlLabel("break")
	return &Break{Label: label, SrcRange: kw.Range}
}

func (p *parser) parseContinue() Statement {
	kw := p.cur()
	p.advance()
	label := p.parseLoopControlLabel("continue")
	return &Continue{Label: label, SrcRange: kw.Range}
}

// parseLoopControlLabel parses the optional loop label following a break or
// continue (on the same line — a newline ends the statement) and validates it.
// kw is "break" or "continue" for diagnostics. Returns "" for the unlabeled form.
func (p *parser) parseLoopControlLabel(kw string) string {
	// A label, if present, is an identifier on the same line as the keyword. A
	// newline/';' here is the statement terminator, so the current token being an
	// identifier unambiguously marks the labeled form.
	if t := p.cur(); t.Type == hclsyntax.TokenIdent && !t.isAnyKeyword() {
		p.advance()
		label := string(t.Bytes)
		if !p.labelInScope(label) {
			p.errf(t.Range, "Unknown loop label",
				fmt.Sprintf("No enclosing loop is labeled %q.", label))
		}
		return label
	}
	if !p.inLoop() {
		p.errf(p.tokens[p.pos-1].Range, kw+" outside loop",
			kw+" may only be used inside a for or while loop.")
	}
	return ""
}

func (p *parser) parseBareBlock() Statement {
	body, brange := p.parseBlockBody()
	return &Block{Body: body, SrcRange: brange}
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
	tr.Body, tr.BodyRange = p.parseBlockBody()

	// Zero or more catch clauses, tried in order at runtime. A catch-all (no type
	// filter and no guard) must be last, since a later clause is unreachable.
	sawCatchAll := false
	for p.cur().isKeyword("catch") {
		c, ok := p.parseCatchClause()
		if !ok {
			return tr
		}
		if sawCatchAll {
			p.errf(c.SrcRange, "Unreachable catch clause",
				"A catch-all (a catch with no type filter or guard) must be the last clause.")
		}
		if c.Type == nil && c.Guard == nil {
			sawCatchAll = true
		}
		tr.Catches = append(tr.Catches, c)
	}

	if p.cur().isKeyword("finally") {
		p.advance() // finally
		if p.cur().Type != hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Expected {", "finally must be followed by a { ... } block.")
			return tr
		}
		tr.Finally, tr.FinallyRange = p.parseBlockBody()
	}

	if len(tr.Catches) == 0 && tr.Finally == nil {
		p.errf(start, "Incomplete try", "A try must have a catch and/or a finally block.")
	}
	return tr
}

// parseCatchClause parses one `catch [name] [: type] [if guard] { ... }` clause.
// ok is false (with the parser left at the offending token) when the block is
// missing, so parseTry can stop without looping forever.
func (p *parser) parseCatchClause() (CatchClause, bool) {
	c := CatchClause{SrcRange: p.cur().Range}
	p.advance() // catch

	// Optional binding name. `if` is a keyword, so it is not taken as the name —
	// `catch if cond { ... }` is an unnamed, guarded clause.
	if p.cur().Type == hclsyntax.TokenIdent && !p.cur().isAnyKeyword() {
		c.Name = string(p.cur().Bytes)
		p.advance()
	}
	// Optional type filter.
	if p.cur().Type == hclsyntax.TokenColon {
		p.advance()
		c.Type, c.TypeSrc = p.parseCatchType()
	}
	// Optional guard.
	if p.cur().isKeyword("if") {
		p.advance()
		c.Guard = p.parseExprStop(stopCond, "catch guard")
	}
	if p.cur().Type != hclsyntax.TokenOBrace {
		p.errf(p.cur().Range, "Expected {", "catch must be followed by a { ... } block.")
		return c, false
	}
	c.Body, c.BodyRange = p.parseBlockBody()
	return c, true
}

// parseCatchType parses the type filter after `catch [name] :`. Unlike parseType
// it must also stop at the `if` guard keyword (a stopFunc sees only token types,
// not text), so it scans the span directly and resolves it. Balanced brackets —
// e.g. the braces of object({...}) — are at depth >= 1 and do not stop the scan.
func (p *parser) parseCatchType() (TypeConstraint, string) {
	start := p.cur()
	if isTerminator(start.Type) || start.Type == hclsyntax.TokenOBrace || p.atEOF() {
		p.errf(start.Range, "Expected type", "Expected a type annotation after ':' here.")
		return nil, ""
	}
	depth := 0
	i := p.pos
	for {
		t := p.tokens[i]
		if t.Type == hclsyntax.TokenEOF {
			break
		}
		if depth == 0 {
			if t.Type == hclsyntax.TokenOBrace || isTerminator(t.Type) ||
				t.isKeyword("if") || isCloseBracket(t.Type) {
				break
			}
		}
		if isOpenBracket(t.Type) {
			depth++
		} else if isCloseBracket(t.Type) && depth > 0 {
			depth--
		}
		i++
	}
	endByte := p.tokens[i].Range.Start.Byte
	p.pos = i
	tc := p.resolveTypeSpan(start.Range.Start.Byte, endByte, start.Range.Start)
	return tc, strings.TrimSpace(string(p.src[start.Range.Start.Byte:endByte]))
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
	body, brange := p.parseBlockBody()
	chain.Branches = append(chain.Branches, CondBranch{Condition: cond, Body: body, BodyRange: brange, SrcRange: start})

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
			ebody, ebrange := p.parseBlockBody()
			chain.Branches = append(chain.Branches, CondBranch{Condition: econd, Body: ebody, BodyRange: ebrange, SrcRange: elifStart})
			continue
		}
		if p.cur().Type != hclsyntax.TokenOBrace {
			p.errf(p.cur().Range, "Expected {", "else must be followed by a { ... } block or by if.")
			return chain
		}
		chain.Else, chain.ElseRange = p.parseBlockBody()
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
	label := p.enterLoop()
	body, brange := p.parseBlockBody()
	p.exitLoop()
	return &For{Kind: ForCond, Cond: cond, Body: body, BodyRange: brange, SrcRange: start, Label: label, While: true}
}

func (p *parser) parseFor() Statement {
	start := p.cur().Range
	p.advance() // for

	// Infinite loop: `for { ... }`.
	if p.cur().Type == hclsyntax.TokenOBrace {
		label := p.enterLoop()
		body, brange := p.parseBlockBody()
		p.exitLoop()
		return &For{Kind: ForCond, Body: body, BodyRange: brange, SrcRange: start, Label: label}
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
	label := p.enterLoop()
	body, brange := p.parseBlockBody()
	p.exitLoop()
	return &For{Kind: ForCond, Cond: cond, Body: body, BodyRange: brange, SrcRange: start, Label: label}
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
	loop.Label = p.enterLoop()
	loop.Body, loop.BodyRange = p.parseBlockBody()
	p.exitLoop()
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
	loop.Label = p.enterLoop()
	loop.Body, loop.BodyRange = p.parseBlockBody()
	p.exitLoop()
	return loop
}

func (p *parser) parseSwitch() Statement {
	start := p.cur().Range
	sw := &Switch{SrcRange: start}
	p.advance() // switch

	if p.cur().Type != hclsyntax.TokenOBrace {
		sw.Subject = p.parseExprStop(stopCond, "switch subject")
	}
	obrace := p.cur().Range
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
			sw.Clauses = append(sw.Clauses, Clause{Values: values, Body: body, SrcRange: caseStart})
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
			body := p.parseCaseBody()
			sw.Clauses = append(sw.Clauses, Clause{IsDefault: true, Body: body, SrcRange: dflt})
		default:
			p.errf(p.cur().Range, "Expected case or default",
				"A switch body contains only case and default clauses.")
			p.advance()
		}
	}
	sw.BodyRange = hcl.RangeBetween(obrace, p.cur().Range)
	if p.cur().Type == hclsyntax.TokenCBrace {
		p.advance()
	}
	// fallthrough is legal only as a non-final clause's last statement; the final
	// clause has no following clause to fall into.
	if n := len(sw.Clauses); n > 0 {
		if ft := endingFallthrough(sw.Clauses[n-1].Body); ft != nil {
			p.errf(ft.SrcRange, "fallthrough in final clause",
				"The final clause of a switch cannot fall through; there is no following clause.")
		}
	}
	return sw
}

// endingFallthrough returns the trailing Fallthrough statement of a clause body,
// or nil if the body does not end in one.
func endingFallthrough(body []Statement) *Fallthrough {
	if n := len(body); n > 0 {
		if ft, ok := body[n-1].(*Fallthrough); ok {
			return ft
		}
	}
	return nil
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
		// fallthrough is recognized only here, at the top level of a case body
		// (parseStatement rejects it everywhere else). It must be the body's final
		// statement: the next significant token must end the clause.
		if t.isKeyword("fallthrough") {
			p.advance()
			if terminated {
				p.errf(t.Range, "Unreachable code",
					"This fallthrough follows an unconditional return, break, or continue.")
			}
			p.skipTerminators()
			nt := p.cur()
			if !(nt.Type == hclsyntax.TokenCBrace || nt.isKeyword("case") || nt.isKeyword("default") || p.atEOF()) {
				p.errf(t.Range, "Misplaced fallthrough",
					"fallthrough must be the final statement of a switch case.")
			}
			stmts = append(stmts, &Fallthrough{SrcRange: t.Range})
			terminated = true
			continue
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

// parseType parses and resolves a type annotation to a constraint (nil on error),
// and also returns the annotation's source text (for rendering by fmt; "" on error).
// allowNull permits the `null` void return type, which is invalid elsewhere.
func (p *parser) parseType(allowNull bool) (TypeConstraint, string) {
	if isTerminator(p.cur().Type) || p.atEOF() {
		p.errf(p.cur().Range, "Expected type", "Expected a type annotation here.")
		return nil, ""
	}
	sb, eb, sp := p.scanSpan(stopType)
	if eb <= sb {
		p.errf(p.cur().Range, "Expected type", "Expected a type annotation here.")
		return nil, ""
	}
	// Resolve from the full span (HCL ignores comments), but slice the *source
	// text* only as far as the last real token: scanSpan's end runs to the stopping
	// token, and comments are not tokens, so a comment between the type and the stop
	// would otherwise be captured into the rendered annotation — and then emitted a
	// second time by fmt as a trailing comment. Latent for a function body (whose
	// stop is `{` on the same line); reached every time by an extern, whose signature
	// stops at the newline.
	return p.resolveTypeSpanAllowNull(sb, eb, sp, allowNull), strings.TrimSpace(string(p.src[sb:p.spanTextEnd(sb, eb)]))
}

// spanTextEnd is the end byte for source text captured from a scanSpan: the end of
// the last non-newline token consumed, clamped into [startByte, endByte].
func (p *parser) spanTextEnd(startByte, endByte int) int {
	for i := p.pos - 1; i >= 0; i-- {
		if p.tokens[i].Type == hclsyntax.TokenNewline {
			continue
		}
		end := p.tokens[i].Range.End.Byte
		if end < startByte || end > endByte {
			break
		}
		return end
	}
	return endByte
}

// resolveTypeSpan parses the given source byte span as a type annotation and
// resolves it to a constraint (nil on error). Shared by parseType and
// parseCatchType.
func (p *parser) resolveTypeSpan(startByte, endByte int, startPos hcl.Pos) TypeConstraint {
	return p.resolveTypeSpanAllowNull(startByte, endByte, startPos, false)
}

func (p *parser) resolveTypeSpanAllowNull(startByte, endByte int, startPos hcl.Pos, allowNull bool) TypeConstraint {
	if endByte <= startByte {
		p.errf(hcl.Range{Filename: p.filename, Start: startPos, End: startPos},
			"Expected type", "Expected a type annotation here.")
		return nil
	}
	expr, diags := hclsyntax.ParseExpression(p.src[startByte:endByte], p.filename, startPos)
	p.diags = p.diags.Extend(diags)
	if diags.HasErrors() {
		return nil
	}
	// In an extern file an unregistered named type stands in as an opaque name
	// rather than failing: an extern names its *host's* types, and the reader (the
	// functy CLI, an editor) generally is not that host. See opaqueConstraint.
	tc, rdiags := p.env.resolveTypeOpaque(expr, allowNull, p.extern)
	// resolveTypeOpaque only softens a *bare* unknown name. A host type nested inside a
	// constructor — list(geopoint), object({ p = geopoint }) — still fails there, because
	// a nested position resolves to a concrete cty type an open or unregistered name has
	// none of. An extern documents rather than enforces, so rather than reject the whole
	// declaration, take the annotation opaquely from its source text: it renders as
	// written and enforces nothing, exactly like the bare-name case. A genuinely
	// malformed constructor ("Invalid type") is left to fail.
	if p.extern && rdiags.HasErrors() && allNestedOpaque(rdiags) {
		name := canonicalOpaqueName(p.src[startByte:endByte])
		// Warn only when a nested name is genuinely *unknown* — the composite analogue of
		// a bare unregistered name, where the warning catches a typo. A nested name the
		// host *did* register, which merely cannot enforce inside a collection (an open
		// predicate type has no concrete cty type to nest), is known and correct, so it
		// degrades to opaque rendering silently.
		warn := diagsHaveSummary(rdiags, "Unknown type")
		tc = opaqueConstraint{name: name}
		if warn {
			rdiags = hcl.Diagnostics{opaqueTypeWarning(name, expr.Range())}
		} else {
			rdiags = nil
		}
	}
	if oc, ok := tc.(opaqueConstraint); ok {
		if p.opaqueWarned[oc.name] {
			rdiags = nil // already reported this name once for this file
		} else {
			if p.opaqueWarned == nil {
				p.opaqueWarned = make(map[string]bool)
			}
			p.opaqueWarned[oc.name] = true
		}
	}
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
