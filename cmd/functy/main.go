// Command functy is a small CLI for loading and running functy source files. It
// exists for development and experimentation, not production use: a host
// application links the functy library directly and supplies its own richer eval
// context. The CLI's baseline context is the cty standard library plus
// print/println.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// errSilent marks a failure whose diagnostics have already been reported (e.g. as
// a --json diagnostics report), so main exits non-zero without printing a second
// "functy: ..." line that would corrupt the machine-readable output.
var errSilent = errors.New("already reported")

func main() {
	if err := rootCmd().Execute(); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintln(os.Stderr, "functy:", err)
		}
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "functy",
		Short:         "Run and check functy (.cty) source files",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().IntVar(&maxSteps, "max-steps", defaultCLIMaxSteps,
		"max steps per function invocation before a runaway loop is aborted (0 = unbounded)")
	root.AddCommand(runCmd(), evalCmd(), replCmd(), checkCmd(), testCmd(), fmtCmd(), symbolsCmd(), versionCmd())
	return root
}
