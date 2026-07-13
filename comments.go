package functy

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// Comment is a single comment captured from a functy source, retained with its
// position so tooling can attach it to the construct it decorates. The statement
// parser never sees comments — the lexer keeps the token stream comment-free — so
// this side table is the sole record that a comment existed. It is the foundation
// for doc-comment metadata (below) and, in future, a source formatter.
type Comment struct {
	// Text is the comment's source text with any trailing CR/LF trimmed. It keeps
	// the leading marker (`//`, `#`, or `/*` … `*/`).
	Text string
	// Line is true for a `//` or `#` single-line comment, false for a `/* */`
	// block comment.
	Line bool
	// Range is the comment's full source span. For a line comment it includes the
	// terminating newline (as HCL's lexer reports it).
	Range hcl.Range
}

// leadingDirectives derives a source's leading directive block from its comment
// table and adapted token stream: every directive comment (`//<ns>:<name>`)
// appearing before the first real (non-newline) token. This replaces a dedicated
// second lex pass — the comment table already carries what that pass recovered.
func leadingDirectives(comments []Comment, tokens []token) []Directive {
	// boundary is the byte offset of the first real token; a comment at or past it
	// is not in the leading block. Newlines (blank lines, and the newlines that
	// line comments were rewritten to) do not end the leading block.
	boundary := -1
	for _, t := range tokens {
		if t.Type == hclsyntax.TokenNewline {
			continue
		}
		boundary = t.Range.Start.Byte
		break
	}

	var out []Directive
	for _, c := range comments {
		if boundary >= 0 && c.Range.Start.Byte >= boundary {
			break
		}
		if d, ok := parseDirectiveComment([]byte(c.Text), c.Range); ok {
			out = append(out, d)
		}
	}
	return out
}

// attachDocComments sets the Doc field of each declaration in r from the leading
// comment block above it (see docComment). src is the source the declarations and
// comments came from. Func declarations are *FuncDecl (mutated in place); Consts
// and Vars are []Decl (mutated by index), so this must run before they are copied
// into a merged Result.
func attachDocComments(src []byte, r *Result, comments []Comment) {
	for _, fn := range r.Funcs {
		attachFuncDoc(src, fn, comments)
	}
	// Externs carry documentation for exactly the same reason a func does — more so,
	// since documenting the host's functions is their whole purpose.
	for _, fn := range r.Externs {
		attachFuncDoc(src, fn, comments)
	}
	for i := range r.Consts {
		r.Consts[i].Doc = docComment(src, r.Consts[i].DefRange, comments)
	}
	for i := range r.Vars {
		r.Vars[i].Doc = docComment(src, r.Vars[i].DefRange, comments)
	}
}

// attachFuncDoc sets a function declaration's Doc and its parameters' Docs.
func attachFuncDoc(src []byte, fn *FuncDecl, comments []Comment) {
	fn.Doc = docComment(src, fn.DefRange, comments)
	for i := range fn.Params {
		// A leading block above the parameter wins (it supports multi-line
		// descriptions); otherwise a trailing comment on the parameter's line.
		if lead := docComment(src, fn.Params[i].DefRange, comments); lead != "" {
			fn.Params[i].Doc = lead
		} else {
			fn.Params[i].Doc = trailingParamDoc(src, fn.Params, i, comments)
		}
	}
}

// trailingParamDoc returns the cleaned text of a comment trailing parameter
// params[i] on its own line (`a: T, // desc`), or "" if there is none. The
// parameter must both start its own line (preceded only by whitespace) and be the
// last parameter starting on that line. This confines trailing docs to the
// multi-line layout (one parameter per line): a single-line list (`f(a, b) // x`)
// misattributes nothing, and a comment on the closing-`)` line attaches to no
// parameter.
func trailingParamDoc(src []byte, params []Param, i int, comments []Comment) string {
	start := params[i].DefRange.Start
	if !precededOnlyByWhitespace(src, start.Byte) {
		return "" // parameter shares its line's start with other tokens
	}
	if i+1 < len(params) && params[i+1].DefRange.Start.Line == start.Line {
		return "" // another parameter follows on the same line
	}
	for _, c := range comments {
		if !c.Line || c.Range.Start.Line != start.Line {
			continue
		}
		if precededOnlyByWhitespace(src, c.Range.Start.Byte) {
			continue // a whole-line comment, not a trailing one
		}
		return cleanDocLine(c.Text)
	}
	return ""
}

// docComment returns the rendered documentation for a declaration whose
// definition begins at defRange. It is the maximal run of whole-line single-line
// comments (`//` or `#`) on consecutive lines immediately above the declaration,
// with no blank line breaking the run, and with directive lines (`//<ns>:<name>`)
// excluded from the prose (they carry no human documentation). Each line's marker
// and one following space are stripped; lines are joined with "\n". Block
// comments never form documentation. Returns "" when there is no such block.
func docComment(src []byte, defRange hcl.Range, comments []Comment) string {
	// Index whole-line comments above the declaration by their starting line.
	byLine := make(map[int]Comment)
	for _, c := range comments {
		if !c.Line || c.Range.Start.Line >= defRange.Start.Line {
			continue
		}
		if !precededOnlyByWhitespace(src, c.Range.Start.Byte) {
			continue // a trailing comment after code is not a lead comment
		}
		byLine[c.Range.Start.Line] = c
	}

	// Walk upward from the line just above the declaration; a missing line (blank
	// or code) ends the block.
	var rev []string
	for ln := defRange.Start.Line - 1; ; ln-- {
		c, ok := byLine[ln]
		if !ok {
			break
		}
		if _, isDir := parseDirectiveComment([]byte(c.Text), c.Range); isDir {
			continue // directive line: part of the block, but not documentation
		}
		rev = append(rev, cleanDocLine(c.Text))
	}
	if len(rev) == 0 {
		return ""
	}

	// rev is bottom-to-top; reverse into source order.
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return strings.Join(rev, "\n")
}

// precededOnlyByWhitespace reports whether the bytes from the start of the line
// containing startByte up to startByte are all whitespace — i.e. the token at
// startByte is the first non-whitespace on its line (a whole-line comment rather
// than a trailing one).
func precededOnlyByWhitespace(src []byte, startByte int) bool {
	i := startByte
	for i > 0 && src[i-1] != '\n' {
		i--
	}
	for ; i < startByte; i++ {
		switch src[i] {
		case ' ', '\t', '\r':
		default:
			return false
		}
	}
	return true
}

// cleanDocLine strips a single-line comment's marker (`//`, `///`, `#`, `##`, …)
// and one following space, and trims trailing whitespace, yielding the prose of
// one documentation line.
func cleanDocLine(text string) string {
	s := text
	switch {
	case strings.HasPrefix(s, "//"):
		s = strings.TrimLeft(s, "/")
	case strings.HasPrefix(s, "#"):
		s = strings.TrimLeft(s, "#")
	}
	s = strings.TrimPrefix(s, " ")
	return strings.TrimRight(s, " \t")
}
