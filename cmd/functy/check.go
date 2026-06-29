package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

func checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check FILE...",
		Short: "Parse and type-check source files without running them",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New("no source files given")
			}
			_, fileMap, diags := loadProgram(args, baselineFunctions(io.Discard))
			writeDiags(cmd.ErrOrStderr(), fileMap, diags)
			if diags.HasErrors() {
				return errors.New("check failed")
			}
			fmt.Fprintln(cmd.OutOrStdout(), "ok")
			return nil
		},
	}
}
