package repl

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// incompleteSummaries are hclsyntax parse-diagnostic summaries that always mean
// "ran out of input mid-construct" — i.e. keep reading. Derived empirically by
// probing hclsyntax.ParseExpression across complete, incomplete, and erroneous
// inputs; the classification is pinned by TestMultilineClassification, which
// fails loudly if an HCL upgrade changes the diagnostic wording.
var incompleteSummaries = map[string]bool{
	"Unterminated template string":               true, // "abc  or unclosed heredoc body
	"Unterminated tuple constructor expression":  true, // [1, 2
	"Unterminated object constructor expression": true, // {a = 1
	"Unterminated function call":                 true, // foo(1
	"Unclosed template interpolation sequence":   true, // "${foo
}

// isContinuation reports whether a single diagnostic is a "keep reading" signal.
// The reliable signals are the small fixed family of summaries above plus the
// detail phrase "end of the file" (the EOF-anchored "Missing expression" case:
// trailing operator, open paren/bracket, etc.).
func isContinuation(d *hcl.Diagnostic) bool {
	return incompleteSummaries[d.Summary] ||
		strings.Contains(d.Detail, "end of the file")
}

// parseIncomplete reports whether a failed parse is incomplete (more input
// needed) rather than genuinely erroneous. It is incomplete only if it failed
// AND every error diagnostic is a continuation signal; any genuine error
// anywhere forces immediate surfacing.
func parseIncomplete(diags hcl.Diagnostics) bool {
	if !diags.HasErrors() {
		return false
	}
	for _, d := range diags {
		if d.Severity == hcl.DiagError && !isContinuation(d) {
			return false
		}
	}
	return true
}

// exprSource returns the portion of an input buffer that is parsed as an
// expression to test completeness. For an assignment it is the right-hand side;
// otherwise it is the whole buffer.
func exprSource(buffer string) string {
	if _, rhs, ok := splitAssignment(buffer); ok {
		return rhs
	}
	return buffer
}

// splitAssignment is the multi-line-aware form of parseAssignment: only the
// first physical line may carry "NAME =", and the right-hand side extends across
// any following continuation lines.
func splitAssignment(buffer string) (name, rhs string, ok bool) {
	first := buffer
	rest := ""
	if nl := strings.IndexByte(buffer, '\n'); nl >= 0 {
		first = buffer[:nl]
		rest = buffer[nl:] // keeps the leading newline
	}
	n, r, ok := parseAssignment(first)
	if !ok {
		return "", "", false
	}
	return n, r + rest, true
}

// probeIncomplete parses src purely to classify completeness; it neither
// evaluates nor records diagnostics (the real parse in parseAndEval renders any
// genuine error with the proper <repl:N> source).
func probeIncomplete(src string) bool {
	_, diags := hclsyntax.ParseExpression([]byte(src), "<probe>", hcl.Pos{Line: 1, Column: 1})
	return parseIncomplete(diags)
}

// accumulate reads continuation lines until the buffer parses cleanly or fails
// with a genuine (non-continuation) error. It returns (buffer, true) to be
// dispatched, or ("", false) if the input was discarded (Ctrl-C / EOF at the
// continuation prompt).
func (s *Session) accumulate(first string) (string, bool) {
	buffer := first
	for probeIncomplete(exprSource(buffer)) {
		// The primary prompt is refreshed by the main loop on the next pass.
		s.rl.SetPrompt(s.continuationPrompt())
		line, err := s.rl.Readline()
		if err != nil {
			// Ctrl-C (ErrInterrupt), EOF, or a closed editor: discard the
			// partial buffer and return to the primary prompt.
			return "", false
		}
		buffer += "\n" + line
	}
	return buffer, true
}

// dispatchInput evaluates a complete (possibly multi-line) input as either an
// assignment or a bare expression.
func (s *Session) dispatchInput(buffer string) {
	if name, rhs, ok := splitAssignment(buffer); ok {
		s.setBinding(name, rhs)
		return
	}
	s.evalExpr(buffer)
}
