package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
)

func testCmd() *cobra.Command {
	var run string
	var verbose, jsonOut bool
	c := &cobra.Command{
		Use:   "test [--run PATTERN] [-v] [--json] [FILE...]",
		Short: "Run the test blocks defined in the source files",
		Long: "Load the given .cty files and run every test \"...\" { ... } block they " +
			"define. A test passes if its body runs to completion, is skipped if it calls " +
			"skip(...), and fails if any other error (a failed assert, a throw, an " +
			"evaluation error) unwinds out of it. Exits non-zero if any test fails.\n\n" +
			"With no FILE arguments, discovers .cty files in the current directory " +
			"(recursively, skipping dot-directories) — equivalent to `functy test .`.\n\n" +
			"With --json, emit a single machine-readable JSON report (one entry per test " +
			"plus a summary) instead of the human-readable output, for CI and editor " +
			"tooling. Exit status is unchanged.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				// No paths given: discover .cty files in the working directory tree,
				// the same recursive walk a directory argument gets.
				args = []string{"."}
			}

			var filter func(string) bool
			if run != "" {
				re, err := regexp.Compile(run)
				if err != nil {
					return fmt.Errorf("invalid --run pattern: %w", err)
				}
				filter = re.MatchString
			}

			out := cmd.OutOrStdout()
			res, ctx, fileMap, diags := loadProgram(args, baselineFunctions(out))
			if diags.HasErrors() {
				if jsonOut {
					// A compile failure produces no test outcomes; still emit a
					// well-formed report so consumers can parse it unconditionally.
					writeTestJSON(out, nil, 0)
					return errors.New("compilation failed")
				}
				writeDiags(cmd.ErrOrStderr(), fileMap, diags)
				return errors.New("compilation failed")
			}

			outcomes := res.RunTestsMatching(func() *hcl.EvalContext { return ctx }, filter)
			deselected := len(res.Tests) - len(outcomes)

			if jsonOut {
				if writeTestJSON(out, outcomes, deselected) > 0 {
					return errors.New("tests failed")
				}
				return nil
			}

			passed, failed, skipped := 0, 0, 0
			for _, o := range outcomes {
				switch {
				case o.Skipped:
					skipped++
					if verbose {
						fmt.Fprintf(out, "SKIP %s%s (%s)\n", o.Name, skipReason(o.SkipReason), fmtDur(o.Duration))
					}
				case o.Failed():
					failed++
					fmt.Fprintf(out, "FAIL %s (%s)\n", o.Name, fmtDur(o.Duration))
					writeDiags(out, fileMap, o.Diagnostics())
				default:
					passed++
					if verbose {
						fmt.Fprintf(out, "ok   %s (%s)\n", o.Name, fmtDur(o.Duration))
					}
				}
			}

			fmt.Fprintf(out, "\n%d passed, %d failed, %d skipped%s\n",
				passed, failed, skipped, deselectedNote(deselected))
			if failed > 0 {
				return errors.New("tests failed")
			}
			return nil
		},
	}
	c.Flags().StringVar(&run, "run", "", "run only tests whose description matches this regular expression")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "list every test (ok/SKIP/FAIL), not just failures")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit a machine-readable JSON report instead of human-readable output")
	return c
}

// fmtDur renders a test duration compactly (rounded to the microsecond).
func fmtDur(d time.Duration) string {
	if d < time.Microsecond {
		return "<1µs"
	}
	return d.Round(time.Microsecond).String()
}

func skipReason(r string) string {
	if r == "" {
		return ""
	}
	return ": " + r
}

func deselectedNote(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" (%d deselected by --run)", n)
}

// jsonReport is the top-level --json document: one entry per test that ran, plus
// aggregate counts. It is a single self-contained object (not a stream) so a
// consumer can parse the whole thing at once.
type jsonReport struct {
	Tests   []jsonTest  `json:"tests"`
	Summary jsonSummary `json:"summary"`
}

type jsonSummary struct {
	Passed     int `json:"passed"`
	Failed     int `json:"failed"`
	Skipped    int `json:"skipped"`
	Deselected int `json:"deselected"`
}

type jsonTest struct {
	Name       string       `json:"name"`
	Status     string       `json:"status"` // "passed", "failed", or "skipped"
	DurationMs float64      `json:"duration_ms"`
	Location   *jsonRange   `json:"location,omitempty"`    // the test block's source location
	SkipReason string       `json:"skip_reason,omitempty"` // set only when status is "skipped"
	Failure    *jsonFailure `json:"failure,omitempty"`     // set only when status is "failed"
}

// jsonFailure describes why a test failed, mirroring the first error diagnostic:
// the assert/throw message, its operand detail, and the source range to underline.
type jsonFailure struct {
	Message  string     `json:"message"`
	Detail   string     `json:"detail,omitempty"`
	Location *jsonRange `json:"location,omitempty"`
}

// jsonRange is an hcl.Range flattened for JSON: 1-based start and end line/column.
type jsonRange struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Column    int    `json:"column"`
	EndLine   int    `json:"end_line"`
	EndColumn int    `json:"end_column"`
}

func rangeToJSON(r hcl.Range) *jsonRange {
	return &jsonRange{
		File:      r.Filename,
		Line:      r.Start.Line,
		Column:    r.Start.Column,
		EndLine:   r.End.Line,
		EndColumn: r.End.Column,
	}
}

// writeTestJSON emits the --json report for the given outcomes and returns the
// number of failed tests (so the caller can set the exit status). Passing, failing,
// and skipped tests are all included regardless of -v; -v only affects the
// human-readable output.
func writeTestJSON(w io.Writer, outcomes []functy.TestOutcome, deselected int) (failed int) {
	rep := jsonReport{
		Tests:   make([]jsonTest, 0, len(outcomes)),
		Summary: jsonSummary{Deselected: deselected},
	}
	for _, o := range outcomes {
		jt := jsonTest{
			Name:       o.Name,
			DurationMs: float64(o.Duration.Nanoseconds()) / 1e6,
			Location:   rangeToJSON(o.DefRange),
		}
		switch {
		case o.Skipped:
			jt.Status = "skipped"
			jt.SkipReason = o.SkipReason
			rep.Summary.Skipped++
		case o.Failed():
			jt.Status = "failed"
			jt.Failure = failureToJSON(o)
			rep.Summary.Failed++
			failed++
		default:
			jt.Status = "passed"
			rep.Summary.Passed++
		}
		rep.Tests = append(rep.Tests, jt)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// json.Encoder.Encode only errors on an unmarshalable value (none here) or a
	// broken writer; nothing actionable to do with either at this point.
	_ = enc.Encode(rep)
	return failed
}

// failureToJSON renders a failed outcome's first error diagnostic as a jsonFailure.
func failureToJSON(o functy.TestOutcome) *jsonFailure {
	for _, d := range o.Diagnostics() {
		if d.Severity != hcl.DiagError {
			continue
		}
		f := &jsonFailure{Message: d.Summary, Detail: d.Detail}
		if d.Subject != nil {
			f.Location = rangeToJSON(*d.Subject)
		}
		return f
	}
	// A failed outcome always yields at least one error diagnostic, but fall back to
	// the raw error rather than emitting a failure with no message.
	msg := "test failed"
	if o.Err != nil {
		msg = o.Err.Error()
	}
	return &jsonFailure{Message: msg, Location: rangeToJSON(o.DefRange)}
}
