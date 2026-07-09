package main

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/spf13/cobra"
)

func evalCmd() *cobra.Command {
	var output string
	var jsonOut bool
	c := &cobra.Command{
		Use:   "eval [--output json|hcl|raw] [--json] EXPR [FILE...]",
		Short: "Evaluate an expression in the context of loaded source files",
		Long: "Evaluate a single HCL expression against the context built from the given " +
			".cty files (their functions, consts, and types). The first argument is the " +
			"expression; any remaining arguments are source files to load (zero is allowed, " +
			"leaving just the baseline context).\n\n" +
			"The result is printed to stdout in the --output format. With --json, any " +
			"diagnostics (compile or evaluation errors) go to stderr as a machine-readable " +
			"report instead of human-readable text, for editor tooling; the exit status is " +
			"non-zero on error either way.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			exprSrc := args[0]
			files := args[1:]

			baseline := baselineFunctions(cmd.OutOrStdout())
			_, ctx, fileMap, diags := loadProgram(files, baseline)
			if diags.HasErrors() {
				return emitRunError(cmd, jsonOut, fileMap, diags, "compilation failed")
			}

			expr, pdiags := hclsyntax.ParseExpression([]byte(exprSrc), "<expr>", hcl.InitialPos)
			if pdiags.HasErrors() {
				return emitRunError(cmd, jsonOut, fileMap, pdiags, "invalid expression")
			}

			val, vdiags := expr.Value(ctx)
			if vdiags.HasErrors() {
				return emitRunError(cmd, jsonOut, fileMap, vdiags, "evaluation failed")
			}

			return printResult(cmd.OutOrStdout(), val, output)
		},
	}
	c.Flags().StringVar(&output, "output", "hcl", "output format: json, hcl, or raw")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit diagnostics to stderr as a machine-readable JSON report instead of human-readable text")
	return c
}
