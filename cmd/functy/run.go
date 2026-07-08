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
	var interactive, jsonOut bool
	c := &cobra.Command{
		Use:   "run [--func NAME] [--output json|hcl|raw] [--json] [-i] FILE... [-- ARG...]",
		Short: "Load source files and call an entry function",
		Long: "Load the given .cty files into one eval context and invoke an entry " +
			"function (main by default). Positional arguments before -- are source " +
			"files; arguments after -- are evaluated as HCL expressions and passed to " +
			"the entry function.\n\n" +
			"With -i/--interactive, run drops into an interactive REPL after the " +
			"entry function (equivalent to `functy repl`): a missing default main is " +
			"silently skipped, and zero source files are allowed.\n\n" +
			"With --json, any diagnostics (compile, argument, or runtime errors) are " +
			"emitted to stderr as a machine-readable report instead of the " +
			"human-readable text, for editor tooling. The entry function's result and " +
			"the program's own output still go to stdout (unchanged by --json); " +
			"--output controls only the result value's format. Exit status is unchanged.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if interactive {
				return runInteractive(cmd, args, funcName, cmd.Flags().Changed("func"), output)
			}

			files, callArgs := splitFilesAndArgs(args, cmd.ArgsLenAtDash())
			if len(files) == 0 {
				return errors.New("no source files given")
			}

			baseline := baselineFunctions(cmd.OutOrStdout())
			_, ctx, fileMap, diags := loadProgram(files, baseline)
			if diags.HasErrors() {
				return emitRunError(cmd, jsonOut, fileMap, diags, "compilation failed")
			}

			fn, ok := ctx.Functions[funcName]
			if !ok {
				return emitRunPlainError(cmd, jsonOut, fmt.Errorf("entry function %q not found", funcName))
			}

			argVals, adiags := evalArgs(callArgs, ctx)
			if adiags.HasErrors() {
				return emitRunError(cmd, jsonOut, fileMap, adiags, "invalid arguments")
			}

			result, err := fn.Call(argVals)
			if err != nil {
				var te *functy.ThrownError
				if errors.As(err, &te) {
					return emitRunError(cmd, jsonOut, fileMap, te.Diagnostics(), "execution failed")
				}
				return emitRunPlainError(cmd, jsonOut, fmt.Errorf("calling %q: %w", funcName, err))
			}
			return printResult(cmd.OutOrStdout(), result, output)
		},
	}
	c.Flags().StringVar(&funcName, "func", "main", "entry function to call")
	c.Flags().StringVar(&output, "output", "json", "output format: json, hcl, or raw")
	c.Flags().BoolVarP(&interactive, "interactive", "i", false, "after running the entry function, start an interactive REPL (allows zero files; skips a missing default main)")
	c.Flags().BoolVar(&jsonOut, "json", false, "emit diagnostics to stderr as a machine-readable JSON report instead of human-readable text (ignored with -i)")
	return c
}

// emitRunError reports a run failure either as human-readable diagnostics (text to
// stderr) or, when jsonOut, as the machine-readable diagnostics report (also to
// stderr, so the entry function's result and the program's own output on stdout
// are never corrupted). The JSON path returns errSilent so main does not append a
// second "functy: ..." line to the report; the text path returns the descriptive
// sentinel that main prints. Either way the exit status is non-zero.
func emitRunError(cmd *cobra.Command, jsonOut bool, fileMap map[string]*hcl.File, diags hcl.Diagnostics, sentinel string) error {
	if jsonOut {
		writeDiagsJSON(cmd.ErrOrStderr(), diags)
		return errSilent
	}
	writeDiags(cmd.ErrOrStderr(), fileMap, diags)
	return errors.New(sentinel)
}

// emitRunPlainError reports a CLI-level failure that carries no source location (a
// missing entry function, a non-thrown call error). When jsonOut it renders as a
// one-line JSON diagnostic on stderr and returns errSilent; otherwise it returns
// the error unchanged for main to print, preserving run's original text output for
// these cases.
func emitRunPlainError(cmd *cobra.Command, jsonOut bool, err error) error {
	if !jsonOut {
		return err
	}
	writeDiagsJSON(cmd.ErrOrStderr(), hcl.Diagnostics{{
		Severity: hcl.DiagError,
		Summary:  err.Error(),
	}})
	return errSilent
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
