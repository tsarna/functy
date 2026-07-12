package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
	"github.com/tsarna/functy/repl"
)

func replCmd() *cobra.Command {
	var funcName, output string
	c := &cobra.Command{
		Use:   "repl [--func NAME] [FILE...] [-- ARG...]",
		Short: "Load source files and start an interactive REPL",
		Long: "Load the given .cty files into one eval context, run the entry " +
			"function if present, then start an interactive REPL over the loaded " +
			"context. Files are optional: with none, the REPL still exposes the " +
			"baseline context (cty stdlib plus functy's stdlib). Positional " +
			"arguments before -- are source files; arguments after -- are " +
			"evaluated as HCL expressions and passed to the entry function. A " +
			"missing default entry function (main) is silently skipped; an " +
			"explicitly named --func that is absent is an error.",
		RunE: func(cmd *cobra.Command, args []string) error {
			explicitFunc := cmd.Flags().Changed("func")
			return runInteractive(cmd, args, funcName, explicitFunc, output)
		},
	}
	c.Flags().StringVar(&funcName, "func", "main", "entry function to run before the REPL")
	c.Flags().StringVar(&output, "output", "hcl", "format for the entry function's result: json, hcl, or raw")
	return c
}

// runInteractive is shared by "functy repl" and "functy run -i". It loads files
// (zero allowed), optionally runs an entry function, then starts the REPL. The
// entry function is run only if it exists; a missing default main is skipped,
// but a missing explicitly-named --func is an error.
func runInteractive(cmd *cobra.Command, args []string, funcName string, explicitFunc bool, output string) error {
	files, callArgs := splitFilesAndArgs(args, cmd.ArgsLenAtDash())

	baseline := baselineFunctions(cmd.OutOrStdout())
	_, compiled, ctx, fileMap, diags := loadProgram(files, baseline)
	if diags.HasErrors() {
		writeDiags(cmd.ErrOrStderr(), fileMap, diags)
		return errors.New("compilation failed")
	}
	writeDiags(cmd.ErrOrStderr(), fileMap, diags) // warnings, if any

	// An ambiguous name is a real failure even here; only a *missing* entry is
	// tolerated, and only when it wasn't asked for explicitly.
	fn, resolved, err := resolveEntry(compiled, ctx, funcName)
	switch {
	case err == nil:
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
			return fmt.Errorf("calling %q: %w", resolved, err)
		}
		if err := printResult(cmd.OutOrStdout(), result, output); err != nil {
			return err
		}
	case !errors.Is(err, errEntryNotFound):
		return err
	case explicitFunc:
		// An explicitly requested entry function must exist.
		return err
	case len(callArgs) > 0:
		// Args were supplied for an entry that does not exist.
		return fmt.Errorf("%w (arguments were given after --)", err)
	}

	session := repl.New(repl.NewStaticHost(ctx), repl.Options{
		Banner:      "functy — interactive REPL",
		HistoryPath: repl.DefaultHistoryPath("functy"),
	})
	return session.Run()
}
