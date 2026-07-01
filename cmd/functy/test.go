package main

import (
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
)

func testCmd() *cobra.Command {
	var run string
	var verbose bool
	c := &cobra.Command{
		Use:   "test [--run PATTERN] [-v] FILE...",
		Short: "Run the test blocks defined in the source files",
		Long: "Load the given .cty files and run every test \"...\" { ... } block they " +
			"define. A test passes if its body runs to completion, is skipped if it calls " +
			"skip(...), and fails if any other error (a failed assert, a throw, an " +
			"evaluation error) unwinds out of it. Exits non-zero if any test fails.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no source files given")
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
				writeDiags(cmd.ErrOrStderr(), fileMap, diags)
				return errors.New("compilation failed")
			}

			outcomes := res.RunTestsMatching(func() *hcl.EvalContext { return ctx }, filter)
			deselected := len(res.Tests) - len(outcomes)

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
