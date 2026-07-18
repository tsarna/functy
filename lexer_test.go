package functy

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// types returns the non-EOF token types as a slice for compact assertions.
func lexTypes(t *testing.T, src string) []hclsyntax.TokenType {
	t.Helper()
	toks, diags := lex([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected lexer diagnostics: %s", diags.Error())
	}
	var out []hclsyntax.TokenType
	for _, tok := range toks {
		if tok.Type == hclsyntax.TokenEOF {
			continue
		}
		out = append(out, tok.Type)
	}
	return out
}

func TestLexSemicolonNoDiagnostic(t *testing.T) {
	toks, diags := lex([]byte("a = 1; b = 2"), "test")
	if diags.HasErrors() {
		t.Fatalf("semicolon should not produce a diagnostic: %s", diags.Error())
	}
	var semis int
	for _, tok := range toks {
		if tok.Type == hclsyntax.TokenSemicolon {
			semis++
		}
	}
	if semis != 1 {
		t.Fatalf("expected 1 semicolon token, got %d", semis)
	}
}

func TestLexLineCommentBecomesNewline(t *testing.T) {
	got := lexTypes(t, "// comment\nx = 1")
	// The line comment should be replaced by a newline terminator.
	if len(got) == 0 || got[0] != hclsyntax.TokenNewline {
		t.Fatalf("expected leading newline from line comment, got %v", got)
	}
}

func TestLexHashCommentBecomesNewline(t *testing.T) {
	got := lexTypes(t, "# comment\nx = 1")
	if len(got) == 0 || got[0] != hclsyntax.TokenNewline {
		t.Fatalf("expected leading newline from hash comment, got %v", got)
	}
}

func TestLexBlockCommentDropped(t *testing.T) {
	got := lexTypes(t, "x /* mid */ = 1")
	want := []hclsyntax.TokenType{
		hclsyntax.TokenIdent, hclsyntax.TokenEqual, hclsyntax.TokenNumberLit,
	}
	if len(got) != len(want) {
		t.Fatalf("block comment not dropped; got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("token %d = %v, want %v", i, got[i], want[i])
		}
	}
}

func TestContinuesLine(t *testing.T) {
	cont := []hclsyntax.TokenType{
		hclsyntax.TokenPlus, hclsyntax.TokenComma, hclsyntax.TokenDot,
		hclsyntax.TokenOParen, hclsyntax.TokenAnd, hclsyntax.TokenQuestion,
	}
	for _, tt := range cont {
		if !continuesLine(tt) {
			t.Errorf("%v should continue the line", tt)
		}
	}
	noncont := []hclsyntax.TokenType{
		hclsyntax.TokenIdent, hclsyntax.TokenNumberLit, hclsyntax.TokenCParen,
	}
	for _, tt := range noncont {
		if continuesLine(tt) {
			t.Errorf("%v should not continue the line", tt)
		}
	}
}

// TestLexResyncAfterUnterminatedString verifies that an unterminated single-line
// quoted string does not swallow the rest of the file. HCL would otherwise turn
// every following line into TokenQuotedLit content; lexAll resynchronizes at the
// offending newline so later declarations tokenize normally.
func TestLexResyncAfterUnterminatedString(t *testing.T) {
	src := "func a() {\n    return \"oops\n}\nfunc after() {\n}\n"
	toks, diags := lex([]byte(src), "test")
	if !diags.HasErrors() {
		t.Fatal("expected an 'invalid multi-line string' diagnostic")
	}
	// The `func after` declaration on a later line must survive as real tokens,
	// not be absorbed into quoted-literal string content.
	var sawFunc, sawAfter, swallowed bool
	for i, tok := range toks {
		// A quoted-literal carrying later source text (the `}` or the next
		// declaration) is the swallowing symptom; `"oops"` before the break is a
		// legitimate quoted literal and expected.
		if tok.Type == hclsyntax.TokenQuotedLit &&
			(bytes.Contains(tok.Bytes, []byte("func")) || bytes.Contains(tok.Bytes, []byte("}"))) {
			swallowed = true
		}
		if tok.isKeyword("func") && i+1 < len(toks) && toks[i+1].ident() == "after" {
			sawFunc = true
		}
		if tok.ident() == "after" {
			sawAfter = true
		}
	}
	if !sawFunc || !sawAfter {
		t.Fatalf("resync failed: `func after` not recovered as tokens (func=%v after=%v)", sawFunc, sawAfter)
	}
	if swallowed {
		t.Errorf("later source lines were swallowed as quoted-literal string content")
	}
}

// TestLexResyncMultipleUnterminatedStrings verifies the resync recovers across
// several unterminated strings in a row (well under maxResyncs), so the cap that
// bounds pathological input does not disturb ordinary multi-error editing: a `func`
// after three broken strings still tokenizes.
func TestLexResyncMultipleUnterminatedStrings(t *testing.T) {
	src := "a = \"x\nb = \"y\nc = \"z\nfunc after() {\n}\n"
	toks, diags := lex([]byte(src), "test")
	if !diags.HasErrors() {
		t.Fatal("expected 'invalid multi-line string' diagnostics")
	}
	var sawAfter bool
	for _, tok := range toks {
		if tok.ident() == "after" {
			sawAfter = true
		}
		if tok.Type == hclsyntax.TokenQuotedLit && bytes.Contains(tok.Bytes, []byte("func")) {
			t.Errorf("later source swallowed as quoted-literal content")
		}
	}
	if !sawAfter {
		t.Fatal("resync failed: `func after` not recovered after multiple broken strings")
	}
}

// TestLexManyUnterminatedStringsTerminates guards the resync cap: a file with far
// more unterminated strings than maxResyncs must still lex to completion (ending in
// EOF) without the Θ(K²) re-lex blowup — each resync re-lexes the whole remaining
// suffix, so an uncapped run on this input would be pathologically slow.
func TestLexManyUnterminatedStringsTerminates(t *testing.T) {
	src := strings.Repeat("x = \"abc\n", maxResyncs*20)
	toks, diags := lex([]byte(src), "test")
	if !diags.HasErrors() {
		t.Fatal("expected diagnostics for unterminated strings")
	}
	if len(toks) == 0 || toks[len(toks)-1].Type != hclsyntax.TokenEOF {
		t.Fatal("token stream must terminate with EOF")
	}
}
