package functy

import (
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
