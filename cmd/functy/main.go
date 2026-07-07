// Command functy is a small CLI for loading and running functy source files. It
// exists for development and experimentation, not production use: a host
// application links the functy library directly and supplies its own richer eval
// context. The CLI's baseline context is the cty standard library plus
// print/println.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "functy:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "functy",
		Short:         "Run and check functy (.cty) source files",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(runCmd(), replCmd(), checkCmd(), testCmd(), fmtCmd())
	return root
}
