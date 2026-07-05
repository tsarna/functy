package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
)

func fmtCmd() *cobra.Command {
	var write, list bool
	cmd := &cobra.Command{
		Use:   "fmt [FILE|DIR ...]",
		Short: "Format functy (.cty) source files",
		Long: "Format functy source. With no paths (or \"-\") it reads stdin and writes\n" +
			"the result to stdout. Given files or directories (walked for .cty files),\n" +
			"it prints the formatted source, or with -w rewrites files in place, or with\n" +
			"-l lists the files whose formatting differs.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || (len(args) == 1 && args[0] == "-") {
				return fmtStdin(cmd)
			}
			var failed, differed bool
			for _, path := range args {
				files, err := gatherCty(path)
				if err != nil {
					return err
				}
				for _, file := range files {
					changed, err := fmtFile(cmd, file, write, list)
					if err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "functy fmt:", err)
						failed = true
						continue
					}
					differed = differed || changed
				}
			}
			if failed {
				return errors.New("fmt failed")
			}
			// -l is a query; a difference is not a failure. Rewriting/printing succeed.
			_ = differed
			return nil
		},
	}
	cmd.Flags().BoolVarP(&write, "write", "w", false, "rewrite files in place instead of printing to stdout")
	cmd.Flags().BoolVarP(&list, "list", "l", false, "list files whose formatting differs; do not print or rewrite")
	return cmd
}

// fmtStdin formats stdin to stdout.
func fmtStdin(cmd *cobra.Command) error {
	src, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return err
	}
	out, diags := functy.Format(src, "<stdin>")
	if diags.HasErrors() {
		writeDiags(cmd.ErrOrStderr(), fileMap("<stdin>", src), diags)
		return errors.New("fmt failed")
	}
	_, err = cmd.OutOrStdout().Write(out)
	return err
}

// fmtFile formats one file, returning whether its formatting differs. Behavior
// depends on the flags: default prints to stdout, -w rewrites in place, -l lists.
func fmtFile(cmd *cobra.Command, path string, write, list bool) (bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	out, diags := functy.Format(src, path)
	if diags.HasErrors() {
		writeDiags(cmd.ErrOrStderr(), fileMap(path, src), diags)
		return false, fmt.Errorf("%s: parse errors", path)
	}
	changed := !bytes.Equal(src, out)
	switch {
	case list:
		if changed {
			fmt.Fprintln(cmd.OutOrStdout(), path)
		}
	case write:
		if changed {
			mode := os.FileMode(0o644)
			if info, err := os.Stat(path); err == nil {
				mode = info.Mode().Perm()
			}
			if err := os.WriteFile(path, out, mode); err != nil {
				return changed, err
			}
		}
	default:
		if _, err := cmd.OutOrStdout().Write(out); err != nil {
			return changed, err
		}
	}
	return changed, nil
}

// gatherCty returns the .cty files at path: the file itself, or every .cty file
// under a directory (recursively, skipping dot-directories).
func gatherCty(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	var files []string
	err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != path && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, functy.Extension) {
			files = append(files, p)
		}
		return nil
	})
	return files, err
}

// fileMap wraps source bytes so writeDiags can show source context for parse
// errors without a full parse.
func fileMap(name string, src []byte) map[string]*hcl.File {
	return map[string]*hcl.File{name: {Bytes: src}}
}
