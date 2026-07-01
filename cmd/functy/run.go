package main

import (
	"errors"
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
)

func runCmd() *cobra.Command {
	var funcName, output string
	c := &cobra.Command{
		Use:   "run [--func NAME] [--output json|hcl|raw] FILE... [-- ARG...]",
		Short: "Load source files and call an entry function",
		Long: "Load the given .cty files into one eval context and invoke an entry " +
			"function (main by default). Positional arguments before -- are source " +
			"files; arguments after -- are evaluated as HCL expressions and passed to " +
			"the entry function.",
		RunE: func(cmd *cobra.Command, args []string) error {
			files, callArgs := splitFilesAndArgs(args, cmd.ArgsLenAtDash())
			if len(files) == 0 {
				return errors.New("no source files given")
			}

			baseline := baselineFunctions(cmd.OutOrStdout())
			_, ctx, fileMap, diags := loadProgram(files, baseline)
			if diags.HasErrors() {
				writeDiags(cmd.ErrOrStderr(), fileMap, diags)
				return errors.New("compilation failed")
			}

			fn, ok := ctx.Functions[funcName]
			if !ok {
				return fmt.Errorf("entry function %q not found", funcName)
			}

			argVals, adiags := evalArgs(callArgs, ctx)
			if adiags.HasErrors() {
				writeDiags(cmd.ErrOrStderr(), fileMap, adiags)
				return errors.New("invalid arguments")
			}

			result, err := fn.Call(argVals)
			if err != nil {
				var te *functy.ThrownError
				if errors.As(err, &te) {
					writeDiags(cmd.ErrOrStderr(), fileMap, te.Diagnostics())
					return errors.New("execution failed")
				}
				return fmt.Errorf("calling %q: %w", funcName, err)
			}
			return printResult(cmd.OutOrStdout(), result, output)
		},
	}
	c.Flags().StringVar(&funcName, "func", "main", "entry function to call")
	c.Flags().StringVar(&output, "output", "json", "output format: json, hcl, or raw")
	return c
}

// splitFilesAndArgs divides positionals into source files and call arguments at
// the -- separator. dash is cobra's ArgsLenAtDash: the count of positionals
// before --, or -1 when no -- was given.
func splitFilesAndArgs(args []string, dash int) (files, callArgs []string) {
	if dash < 0 {
		return args, nil
	}
	return args[:dash], args[dash:]
}

// evalArgs evaluates each argument string as an HCL expression in the given
// context, yielding the cty values to pass to the entry function.
func evalArgs(srcs []string, ctx *hcl.EvalContext) ([]cty.Value, hcl.Diagnostics) {
	var vals []cty.Value
	var diags hcl.Diagnostics
	for i, src := range srcs {
		expr, pdiags := hclsyntax.ParseExpression([]byte(src), fmt.Sprintf("<arg %d>", i+1), hcl.InitialPos)
		diags = diags.Extend(pdiags)
		if pdiags.HasErrors() {
			continue
		}
		v, vdiags := expr.Value(ctx)
		diags = diags.Extend(vdiags)
		vals = append(vals, v)
	}
	return vals, diags
}
