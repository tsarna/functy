package main

import (
	"errors"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
)

func testCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "test FILE...",
		Short: "Run the test blocks defined in the source files",
		Long: "Load the given .cty files and run every test \"...\" { ... } block they " +
			"define. A test passes if its body runs to completion and fails if an error " +
			"(a failed assert, a throw, an evaluation error) unwinds out of it. Exits " +
			"non-zero if any test fails.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no source files given")
			}

			out := cmd.OutOrStdout()
			res, ctx, fileMap, diags := loadProgram(args, baselineFunctions(out))
			if diags.HasErrors() {
				writeDiags(cmd.ErrOrStderr(), fileMap, diags)
				return errors.New("compilation failed")
			}

			outcomes := res.RunTests(func() *hcl.EvalContext { return ctx })
			passed, failed := 0, 0
			for _, o := range outcomes {
				if o.Passed() {
					passed++
					fmt.Fprintf(out, "ok   %s\n", o.Name)
					continue
				}
				failed++
				fmt.Fprintf(out, "FAIL %s\n", o.Name)
				writeDiags(out, fileMap, o.Diagnostics())
			}

			fmt.Fprintf(out, "\n%d passed, %d failed\n", passed, failed)
			if failed > 0 {
				return errors.New("tests failed")
			}
			return nil
		},
	}
}
