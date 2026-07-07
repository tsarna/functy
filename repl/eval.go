package repl

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"golang.org/x/term"
)

// evalExpr parses src as a single HCL expression, evaluates it, and applies the
// echo + result-binding rules (non-null → printed and numbered into _ / _N;
// top-level null → nothing; error → diagnostics, not numbered).
func (s *Session) evalExpr(src string) {
	val, ok := s.parseAndEval(src)
	if !ok {
		return
	}
	s.echoAndNumber(val)
}

// parseAndEval parses src as a single HCL expression and evaluates it against
// the host's context plus the session bindings. It renders any diagnostics
// (parse, eval, or warnings) and returns ok=false if there were errors. Shared
// by bare expressions and by assignment / :set RHS evaluation.
func (s *Session) parseAndEval(src string) (cty.Value, bool) {
	s.inputCounter++
	filename := fmt.Sprintf("<repl:%d>", s.inputCounter)

	expr, diags := hclsyntax.ParseExpression([]byte(src), filename, hcl.Pos{Line: 1, Column: 1})
	// Record the source so diagnostics can render caret-underlined snippets.
	s.files[filename] = &hcl.File{Bytes: []byte(src)}
	if diags.HasErrors() {
		s.printDiags(diags)
		return cty.NilVal, false
	}

	val, evalDiags := s.evaluate(src, expr)
	if evalDiags.HasErrors() {
		s.printDiags(evalDiags)
		return cty.NilVal, false
	}
	// Surface non-error diagnostics (warnings) without blocking the result.
	if len(evalDiags) > 0 {
		s.printDiags(evalDiags)
	}
	return val, true
}

// evaluate builds the per-eval context (host parent → session-bindings child)
// and evaluates expr. The host's finish func (e.g. a trace span end) runs when
// evaluation completes.
func (s *Session) evaluate(src string, expr hcl.Expression) (cty.Value, hcl.Diagnostics) {
	parent, finish, err := s.host.EvalContext(s.baseCtx, src)
	if finish != nil {
		defer finish()
	}
	if err != nil {
		return cty.NilVal, hcl.Diagnostics{{
			Severity: hcl.DiagError,
			Summary:  "Failed to build evaluation context",
			Detail:   err.Error(),
		}}
	}

	child := parent.NewChild()
	child.Variables = s.bindings // includes "_" and the numbered "_N"

	return expr.Value(child)
}

// echoAndNumber applies the result-numbering + echo rules to a successful value:
// a non-null value is printed HCL-style and numbered into "_" and "_N"; a
// top-level null produces no output and does not advance the counter or rebind
// "_". (Named assignment bindings are recorded by the caller first; this only
// handles the shared echo/numbering.)
func (s *Session) echoAndNumber(val cty.Value) {
	if val.IsNull() {
		return
	}
	s.resultCounter++
	name := fmt.Sprintf("_%d", s.resultCounter)
	s.bindings[name] = val
	s.bindings["_"] = val
	fmt.Fprintln(s.out, formatValue(val))
}

// printDiags renders diagnostics with caret-underlined source snippets against
// the session's accumulated files, to stderr (kept separate from the stdout
// result stream so power users can redirect with 2>logfile).
func (s *Session) printDiags(diags hcl.Diagnostics) {
	width := 80
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		width = w
	}
	wr := hcl.NewDiagnosticTextWriter(s.errOut, s.files, uint(width), true)
	_ = wr.WriteDiagnostics(diags)
}
