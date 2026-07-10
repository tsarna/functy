package main

import (
	"encoding/json"
	"fmt"
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
	var jsonOut bool
	c := &cobra.Command{
		Use:   "symbols [--json] [--filename NAME] [FILE|DIR ... | -]",
		Short: "List top-level declarations and tests",
		Long: "List every top-level declaration (func, const, var, type) and test block, in " +
			"source order — a `file:line: kind name` listing by default, or a machine-readable " +
			"JSON object with --json (each symbol carries kind, name, a function's signature, any " +
			"doc comment, and its 1-based range), for editor tooling such as an outline or test " +
			"discovery.\n\n" +
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
			syms := collectSymbols(res)
			if jsonOut {
				writeSymbolsJSON(cmd.OutOrStdout(), syms)
			} else {
				writeSymbolsText(cmd.OutOrStdout(), syms)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&jsonOut, "json", false, "emit a machine-readable JSON object instead of the human-readable listing")
	c.Flags().StringVar(&filename, "filename", "", "virtual filename for stdin input (used with '-')")
	return c
}

// collectSymbols flattens a parse Result into the symbol list, in source order.
func collectSymbols(res *functy.Result) []jsonSymbol {
	symbols := []jsonSymbol{}
	if res != nil {
		for _, fn := range res.Funcs {
			symbols = append(symbols, jsonSymbol{
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
			symbols = append(symbols, jsonSymbol{
				Kind:  "test",
				Name:  t.Name,
				Range: rangeToJSON(t.DefRange),
			})
		}
		for _, d := range res.Consts {
			symbols = append(symbols, jsonSymbol{
				Kind: "const", Name: d.Name, Doc: d.Doc, Range: rangeToJSON(d.DefRange),
			})
		}
		for _, d := range res.Vars {
			symbols = append(symbols, jsonSymbol{
				Kind: "var", Name: d.Name, Doc: d.Doc, Range: rangeToJSON(d.DefRange),
			})
		}
		for _, ta := range res.Types {
			symbols = append(symbols, jsonSymbol{
				Kind: "type", Name: ta.Name, Range: rangeToJSON(ta.DefRange),
			})
		}
	}

	// Source order: by file, then position.
	sort.SliceStable(symbols, func(i, j int) bool {
		a, b := symbols[i].Range, symbols[j].Range
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})

	return symbols
}

func writeSymbolsJSON(w io.Writer, symbols []jsonSymbol) {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(jsonSymbols{Symbols: symbols})
}

// writeSymbolsText prints one `file:line: kind name` per symbol — greppable and
// mirroring how the diagnostic text output names locations.
func writeSymbolsText(w io.Writer, symbols []jsonSymbol) {
	for _, s := range symbols {
		var display string
		switch s.Kind {
		case "test":
			display = fmt.Sprintf("test %q", s.Name)
		case "func":
			display = "func " + s.Name + s.Detail
		default:
			display = s.Kind + " " + s.Name
		}
		fmt.Fprintf(w, "%s:%d: %s\n", s.Range.File, s.Range.Line, display)
	}
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
