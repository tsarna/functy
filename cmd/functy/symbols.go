package main

import (
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/spf13/cobra"
	"github.com/tsarna/functy"
)

// jsonSymbols is the --json document for the symbols command: every top-level
// declaration and test block, in source order, for an editor outline / test
// discovery.
type jsonSymbols struct {
	Symbols []jsonSymbol `json:"symbols"`
}

// jsonSymbol is one declaration. Kind is func/const/var/type/test. Detail carries
// a function's rendered signature (empty otherwise); Doc is the leading
// doc-comment block (omitted when absent). Range is the full definition span (a
// whole block for func/test), 1-based like the other --json ranges.
type jsonSymbol struct {
	Kind   string     `json:"kind"`
	Name   string     `json:"name"`
	Detail string     `json:"detail,omitempty"`
	Doc    string     `json:"doc,omitempty"`
	Range  *jsonRange `json:"range"`
}

func symbolsCmd() *cobra.Command {
	var filename string
	c := &cobra.Command{
		Use:   "symbols [--filename NAME] [FILE|DIR ... | -]",
		Short: "List top-level declarations and tests as a machine-readable JSON report",
		Long: "Emit every top-level declaration (func, const, var, type) and test block, in " +
			"source order, as a single JSON object on stdout — for editor tooling (an outline " +
			"view, test discovery). Each symbol carries its kind, name, a function's signature " +
			"(detail), any doc comment, and its 1-based source range.\n\n" +
			"Input is handled like check: files and directories (walked recursively), the " +
			"current directory tree with no arguments, or a single '-' to read one buffer from " +
			"stdin (name it with --filename). Parse errors are tolerated — whatever declarations " +
			"the parser recovers are still emitted.",
		RunE: func(cmd *cobra.Command, args []string) error {
			input, err := resolveSourceInput(cmd.InOrStdin(), args, filename)
			if err != nil {
				return err
			}
			// Diagnostics are ignored on purpose: symbols is best-effort, so a file
			// mid-edit still yields whatever top-level declarations parsed.
			res, _, _, _ := loadProgram(input, baselineFunctions(io.Discard))
			writeSymbolsJSON(cmd.OutOrStdout(), res)
			return nil
		},
	}
	c.Flags().StringVar(&filename, "filename", "", "virtual filename for stdin input (used with '-')")
	return c
}

func writeSymbolsJSON(w io.Writer, res *functy.Result) {
	report := jsonSymbols{Symbols: []jsonSymbol{}}
	if res != nil {
		for _, fn := range res.Funcs {
			report.Symbols = append(report.Symbols, jsonSymbol{
				Kind:   "func",
				Name:   fn.Name,
				Detail: funcSignature(fn),
				Doc:    fn.Doc,
				// DefRange is only the header; extend through the body so the range
				// spans the whole block (for outline extent, breadcrumbs, sticky scroll).
				Range: rangeToJSON(hcl.Range{
					Filename: fn.DefRange.Filename,
					Start:    fn.DefRange.Start,
					End:      fn.BodyRange.End,
				}),
			})
		}
		for _, t := range res.Tests {
			report.Symbols = append(report.Symbols, jsonSymbol{
				Kind:  "test",
				Name:  t.Name,
				Range: rangeToJSON(t.DefRange),
			})
		}
		for _, d := range res.Consts {
			report.Symbols = append(report.Symbols, jsonSymbol{
				Kind: "const", Name: d.Name, Doc: d.Doc, Range: rangeToJSON(d.DefRange),
			})
		}
		for _, d := range res.Vars {
			report.Symbols = append(report.Symbols, jsonSymbol{
				Kind: "var", Name: d.Name, Doc: d.Doc, Range: rangeToJSON(d.DefRange),
			})
		}
		for _, ta := range res.Types {
			report.Symbols = append(report.Symbols, jsonSymbol{
				Kind: "type", Name: ta.Name, Range: rangeToJSON(ta.DefRange),
			})
		}
	}

	// Source order: by file, then position.
	sort.SliceStable(report.Symbols, func(i, j int) bool {
		a, b := report.Symbols[i].Range, report.Symbols[j].Range
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(report)
}

// funcSignature renders a function's parameters and return type as they appear in
// source, e.g. "(a: number, b: number = 0, *rest: string) -> number".
func funcSignature(fn *functy.FuncDecl) string {
	var b strings.Builder
	b.WriteByte('(')
	for i, p := range fn.Params {
		if i > 0 {
			b.WriteString(", ")
		}
		if p.Variadic {
			b.WriteByte('*')
		}
		b.WriteString(p.Name)
		if p.TypeSrc != "" {
			b.WriteString(": ")
			b.WriteString(p.TypeSrc)
		}
		if p.DefaultSrc != "" {
			b.WriteString(" = ")
			b.WriteString(p.DefaultSrc)
		}
	}
	b.WriteByte(')')
	if fn.RetTypeSrc != "" {
		b.WriteString(" -> ")
		b.WriteString(fn.RetTypeSrc)
	}
	return b.String()
}
