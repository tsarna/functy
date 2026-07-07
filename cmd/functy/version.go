package main

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build information, injected at release time via -ldflags -X. GoReleaser sets
// these automatically; a plain `go build` leaves the defaults below.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print functy version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(),
				"functy %s\n  commit: %s\n  built:  %s\n  go:     %s\n",
				version, commit, date, runtime.Version())
			return nil
		},
	}
}
