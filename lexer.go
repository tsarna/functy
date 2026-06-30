package functy

import (
	"bytes"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// token is a single lexical token in a functy source file. It wraps the
// underlying HCL token type, the exact source bytes, and the source range so
// the parser can both classify the token and recover the byte span of an
// embedded HCL expression.
type token struct {
	Type  hclsyntax.TokenType
	Bytes []byte
	Range hcl.Range
}

// keywords are the reserved words of the functy statement grammar. Type names
// (string, number, list, ...) are deliberately absent: they are contextual,
// recognized only in type-annotation position by the parser.
var keywords = map[string]bool{
	"func": true, "var": true, "const": true, "return": true,
	"if": true, "else": true, "for": true, "while": true, "in": true,
	"break": true, "continue": true, "switch": true, "case": true, "default": true,
	"try": true, "catch": true, "finally": true, "defer": true, "throw": true,
	"true": true, "false": true, "null": true,
}

// lex tokenizes functy source. It runs the HCL native-syntax lexer (which
// correctly handles strings, ${}/%{} templates, heredocs, and comments) and
// then adapts the stream for functy:
//
//   - block comments are dropped;
//   - line comments are replaced by a newline token, since the comment consumes
//     the line's terminating newline;
//   - the spurious "invalid character" diagnostic HCL emits for ';' is dropped,
//     because functy uses ';' as an explicit statement terminator (HCL still
//     produces a TokenSemicolon for it).
//
// All other lexer diagnostics are returned to the caller.
func lex(src []byte, filename string) ([]token, hcl.Diagnostics) {
	raw, diags := hclsyntax.LexConfig(src, filename, hcl.InitialPos)
	diags = dropSemicolonDiags(diags)

	out := make([]token, 0, len(raw))
	for _, t := range raw {
		switch t.Type {
		case hclsyntax.TokenComment:
			if isLineComment(t.Bytes) && bytes.HasSuffix(t.Bytes, []byte("\n")) {
				// A line comment ends the line; preserve that as a newline so
				// statement termination still happens.
				out = append(out, token{
					Type:  hclsyntax.TokenNewline,
					Bytes: []byte("\n"),
					Range: t.Range,
				})
			}
			// Block comments (and an EOF-terminated line comment) are dropped.
		default:
			out = append(out, token{Type: t.Type, Bytes: t.Bytes, Range: t.Range})
		}
	}
	return out, diags
}

// isLineComment reports whether a comment token is a // or # line comment
// (as opposed to a /* */ block comment).
func isLineComment(b []byte) bool {
	return bytes.HasPrefix(b, []byte("//")) || bytes.HasPrefix(b, []byte("#"))
}

// dropSemicolonDiags removes the "invalid character" diagnostic HCL's lexer
// emits for each ';'. functy treats ';' as a statement terminator, so the
// diagnostic is noise; the token itself is still emitted as TokenSemicolon.
func dropSemicolonDiags(diags hcl.Diagnostics) hcl.Diagnostics {
	if len(diags) == 0 {
		return diags
	}
	kept := make(hcl.Diagnostics, 0, len(diags))
	for _, d := range diags {
		if d.Summary == "Invalid character" && d.Subject != nil &&
			d.Subject.End.Byte-d.Subject.Start.Byte == 1 {
			// Heuristic: a single-character "invalid character" diagnostic whose
			// detail mentions ';'. HCL's message names the ';' explicitly.
			if bytes.Contains([]byte(d.Detail), []byte(`";"`)) {
				continue
			}
		}
		kept = append(kept, d)
	}
	return kept
}

// isKeyword reports whether an identifier token is a reserved keyword.
func (t token) isKeyword(kw string) bool {
	return t.Type == hclsyntax.TokenIdent && string(t.Bytes) == kw
}

// isAnyKeyword reports whether the token is any reserved keyword.
func (t token) isAnyKeyword() bool {
	return t.Type == hclsyntax.TokenIdent && keywords[string(t.Bytes)]
}

// ident returns the identifier text, or "" if the token is not an identifier.
func (t token) ident() string {
	if t.Type == hclsyntax.TokenIdent {
		return string(t.Bytes)
	}
	return ""
}

// continuesLine reports whether a newline immediately following a token of this
// type is a line continuation rather than a statement terminator. A newline is
// a continuation when the preceding token cannot legally end a statement: a
// binary/unary operator, a separator (',' '.' '?' ':' '='), or an opening
// bracket / template-interpolation introducer.
func continuesLine(t hclsyntax.TokenType) bool {
	switch t {
	case hclsyntax.TokenPlus, hclsyntax.TokenMinus, hclsyntax.TokenStar,
		hclsyntax.TokenSlash, hclsyntax.TokenPercent,
		hclsyntax.TokenEqualOp, hclsyntax.TokenNotEqual,
		hclsyntax.TokenLessThan, hclsyntax.TokenGreaterThan,
		hclsyntax.TokenLessThanEq, hclsyntax.TokenGreaterThanEq,
		hclsyntax.TokenAnd, hclsyntax.TokenOr, hclsyntax.TokenBang,
		hclsyntax.TokenQuestion, hclsyntax.TokenColon,
		hclsyntax.TokenDot, hclsyntax.TokenComma, hclsyntax.TokenEqual,
		hclsyntax.TokenOParen, hclsyntax.TokenOBrack, hclsyntax.TokenOBrace,
		hclsyntax.TokenTemplateInterp, hclsyntax.TokenTemplateControl,
		hclsyntax.TokenEllipsis:
		return true
	default:
		return false
	}
}

// isOpenBracket / isCloseBracket classify the three bracket pairs used for
// depth tracking when scanning expression spans. Template braces inside strings
// and heredocs are distinct token types and are intentionally not counted.
func isOpenBracket(t hclsyntax.TokenType) bool {
	return t == hclsyntax.TokenOParen || t == hclsyntax.TokenOBrack || t == hclsyntax.TokenOBrace
}

func isCloseBracket(t hclsyntax.TokenType) bool {
	return t == hclsyntax.TokenCParen || t == hclsyntax.TokenCBrack || t == hclsyntax.TokenCBrace
}

// isTerminator reports whether a token ends a statement (a newline or ';').
func isTerminator(t hclsyntax.TokenType) bool {
	return t == hclsyntax.TokenNewline || t == hclsyntax.TokenSemicolon
}

// isWalrus reports whether two consecutive tokens form the `:=` operator: a colon
// immediately followed (no intervening space) by an equals. HCL has no `:=`
// token, so it lexes as adjacent ':' and '=' tokens; requiring adjacency keeps a
// spaced `x : = ...` from being misread as the shorthand.
func isWalrus(colon, eq token) bool {
	return colon.Type == hclsyntax.TokenColon && eq.Type == hclsyntax.TokenEqual &&
		colon.Range.End.Byte == eq.Range.Start.Byte
}
