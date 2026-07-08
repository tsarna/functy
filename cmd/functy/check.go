package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func checkCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "check [--json] FILE...",
		Short: "Parse and type-check source files without running them",
		Long: "Parse and type-check the given .cty files without running them. Diagnostics " +
			"are printed with source context; the command exits non-zero if any are errors.\n\n" +
			"With --json, emit a single machine-readable JSON report of the diagnostics " +
			"(each with severity, summary, detail, and a 1-based source location) to stderr " +
			"instead of the human-readable output, for editor tooling. Exit status is " +
			"unchanged.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no source files given")
			}
			_, _, fileMap, diags := loadProgram(args, baselineFunctions(io.Discard))

			if jsonOut {
				// The report goes to stderr (not stdout) for consistency with run/test
				// --json, so a consumer parses one stream regardless of verb; errSilent
				// keeps main from appending a "functy: ..." line that would corrupt it.
				writeDiagsJSON(cmd.ErrOrStderr(), diags)
				if diags.HasErrors() {
					return errSilent
				}
				return nil
			}

			writeDiags(cmd.ErrOrStderr(), fileMap, diags)
			if diags.HasErrors() {
				return errors.New("check failed")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "emit a machine-readable JSON diagnostics report instead of human-readable output")
	return c
}
