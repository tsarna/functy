package main

import (
	"encoding/json"
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

// jsonVersion is the --json document for the version command: a single object so
// a consumer (e.g. editor tooling) can parse stdout unconditionally.
type jsonVersion struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
	Go      string `json:"go"`
}

func versionCmd() *cobra.Command {
	var jsonOut bool
	c := &cobra.Command{
		Use:   "version",
		Short: "Print functy version information",
		Long: "Print functy version, build metadata, and Go toolchain version.\n\n" +
			"With --json, emit a single machine-readable JSON object (version, commit, " +
			"date, go) instead of the human-readable output, for editor tooling.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(jsonVersion{
					Version: version,
					Commit:  commit,
					Date:    date,
					Go:      runtime.Version(),
				})
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"functy %s\n  commit: %s\n  built:  %s\n  go:     %s\n",
				version, commit, date, runtime.Version())
			return nil
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "emit machine-readable JSON instead of human-readable output")
	return c
}
