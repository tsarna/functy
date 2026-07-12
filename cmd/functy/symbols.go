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

// jsonSymbol is one declaration. Kind is namespace/func/const/var/type/test. Detail carries
// a function's rendered signature (empty otherwise); Doc is the leading
// doc-comment block (omitted when absent). Range is the full definition span (a
// whole block for func/test), 1-based like the other --json ranges.
//
// Name stays the *bare* declared name — it is the outline label and the test
// identifier, and a consumer that predates namespaces keeps working unchanged.
// Namespace, Qualified and Private are additive and omitted in the global
// namespace, so an existing client sees exactly what it saw before.
type jsonSymbol struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
	// Namespace is the declaration's namespace; omitted in the global namespace.
	Namespace string `json:"namespace,omitempty"`
	// Qualified is the name a function is callable under (`foo::bar::baz`);
	// omitted in the global namespace, where it equals Name.
	Qualified string `json:"qualified,omitempty"`
	// Private marks a namespace-local (`_`-prefixed) declaration: still listed, so
	// an outline shows the whole file, but never handed to the host.
	Private bool       `json:"private,omitempty"`
	Detail  string     `json:"detail,omitempty"`
	Doc     string     `json:"doc,omitempty"`
	Range   *jsonRange `json:"range"`
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
			res, _, _, _, _ := loadProgram(input, baselineFunctions(io.Discard))
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
		// The namespace declaration itself. Its range is the declaration line, not
		// the file: a client that wants to nest the file's declarations under it
		// (as vscode-functy does) widens the extent itself, rather than having the
		// CLI report a span the declaration does not actually have.
		for i := range res.Namespaces {
			n := res.Namespaces[i]
			symbols = append(symbols, jsonSymbol{
				Kind: "namespace", Name: n.Name, Range: rangeToJSON(n.DefRange),
			})
		}
		for _, fn := range res.Funcs {
			symbols = append(symbols, jsonSymbol{
				Kind:      "func",
				Name:      fn.Name,
				Namespace: fn.Namespace,
				Qualified: qualifiedIfNamespaced(fn.Namespace, fn.Name),
				Private:   fn.IsPrivate(),
				Detail:    funcSignature(fn),
				Doc:       fn.Doc,
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
				Kind:      "test",
				Name:      t.Name,
				Namespace: t.Namespace,
				Range:     rangeToJSON(t.DefRange),
			})
		}
		for _, d := range res.Consts {
			symbols = append(symbols, jsonSymbol{
				Kind: "const", Name: d.Name, Namespace: d.Namespace, Private: d.IsPrivate(),
				Doc: d.Doc, Range: rangeToJSON(d.DefRange),
			})
		}
		for _, d := range res.Vars {
			symbols = append(symbols, jsonSymbol{
				Kind: "var", Name: d.Name, Namespace: d.Namespace, Private: d.IsPrivate(),
				Doc: d.Doc, Range: rangeToJSON(d.DefRange),
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

// qualifiedIfNamespaced returns the callable name for a namespaced declaration,
// and "" in the global namespace — where it would merely repeat Name, and where
// omitting it keeps the JSON byte-identical to what pre-namespace clients saw.
func qualifiedIfNamespaced(namespace, name string) string {
	if namespace == "" {
		return ""
	}
	return functy.Qualify(namespace, name)
}

// writeSymbolsText prints one `file:line: kind name` per symbol — greppable and
// mirroring how the diagnostic text output names locations.
//
// A function is printed under its *qualified* name: that is the name it is
// callable and greppable by, and the `_` prefix already makes a private one
// self-evident in the listing.
func writeSymbolsText(w io.Writer, symbols []jsonSymbol) {
	for _, s := range symbols {
		var display string
		switch s.Kind {
		case "test":
			display = fmt.Sprintf("test %q", s.Name)
		case "func":
			name := s.Name
			if s.Qualified != "" {
				name = s.Qualified
			}
			display = "func " + name + s.Detail
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
