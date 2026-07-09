package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
)

func checkCmd() *cobra.Command {
	var jsonOut bool
	var filename string
	c := &cobra.Command{
		Use:   "check [--json] [FILE|DIR ... | -]",
		Short: "Parse and type-check source files without running them",
		Long: "Parse and type-check the given .cty files without running them. Directory " +
			"arguments are walked recursively for .cty files; with no arguments, the " +
			"current directory tree is checked (consistent with test and fmt). A single " +
			"'-' reads source from stdin, checking that one buffer — pair it with " +
			"--filename NAME so diagnostics carry a real path (for editors checking an " +
			"unsaved buffer). Diagnostics are printed with source context; the command " +
			"exits non-zero if any are errors.\n\n" +
			"With --json, emit a single machine-readable JSON report of the diagnostics " +
			"(each with severity, summary, detail, and a 1-based source location) to stderr " +
			"instead of the human-readable output, for editor tooling. Exit status is " +
			"unchanged.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var input any
			switch {
			case len(args) == 1 && args[0] == "-":
				src, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return err
				}
				name := filename
				if name == "" {
					name = "<stdin>"
				}
				input = functy.Source{Filename: name, Bytes: src}
			case len(args) == 0:
				// No paths given: check the working directory tree, the same
				// recursive walk a directory argument gets (as test and fmt do).
				input = []string{"."}
			default:
				input = args
			}
			_, _, fileMap, diags := loadProgram(input, baselineFunctions(io.Discard))

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
	c.Flags().StringVar(&filename, "filename", "", "virtual filename for stdin input (used with '-') so diagnostics carry a real path")
	return c
}
