package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// baselineFunctions returns the eval-context functions available to functy code
// run by the CLI: the cty standard library plus a couple of CLI conveniences
// (print/println). A real host embeds the library and supplies its own richer
// set instead; these exist only so the standalone tool is useful for
// experimentation.
func baselineFunctions(out io.Writer) map[string]function.Function {
	funcs := map[string]function.Function{
		// strings
		"upper":      stdlib.UpperFunc,
		"lower":      stdlib.LowerFunc,
		"title":      stdlib.TitleFunc,
		"trimspace":  stdlib.TrimSpaceFunc,
		"trim":       stdlib.TrimFunc,
		"trimprefix": stdlib.TrimPrefixFunc,
		"trimsuffix": stdlib.TrimSuffixFunc,
		"chomp":      stdlib.ChompFunc,
		"indent":     stdlib.IndentFunc,
		"join":       stdlib.JoinFunc,
		"split":      stdlib.SplitFunc,
		"replace":    stdlib.ReplaceFunc,
		"substr":     stdlib.SubstrFunc,
		"strrev":     stdlib.ReverseFunc,
		"format":     stdlib.FormatFunc,
		"formatlist": stdlib.FormatListFunc,
		"regex":      stdlib.RegexFunc,
		"regexall":   stdlib.RegexAllFunc,

		// collections
		"concat":   stdlib.ConcatFunc,
		"contains": stdlib.ContainsFunc,
		"distinct": stdlib.DistinctFunc,
		"element":  stdlib.ElementFunc,
		"flatten":  stdlib.FlattenFunc,
		"keys":     stdlib.KeysFunc,
		"values":   stdlib.ValuesFunc,
		"length":   stdlib.LengthFunc,
		"lookup":   stdlib.LookupFunc,
		"merge":    stdlib.MergeFunc,
		"reverse":  stdlib.ReverseListFunc,
		"slice":    stdlib.SliceFunc,
		"sort":     stdlib.SortFunc,
		"range":    stdlib.RangeFunc,
		"zipmap":   stdlib.ZipmapFunc,
		"setunion": stdlib.SetUnionFunc,
		"compact":  stdlib.CompactFunc,
		"coalesce": stdlib.CoalesceFunc,

		// numbers
		"abs":      stdlib.AbsoluteFunc,
		"ceil":     stdlib.CeilFunc,
		"floor":    stdlib.FloorFunc,
		"log":      stdlib.LogFunc,
		"pow":      stdlib.PowFunc,
		"signum":   stdlib.SignumFunc,
		"parseint": stdlib.ParseIntFunc,
		"max":      stdlib.MaxFunc,
		"min":      stdlib.MinFunc,

		// encoding
		"jsonencode": stdlib.JSONEncodeFunc,
		"jsondecode": stdlib.JSONDecodeFunc,
		"csvdecode":  stdlib.CSVDecodeFunc,

		// CLI conveniences
		"print":   printFunc(out, false),
		"println": printFunc(out, true),
	}

	// functy's own standard library: the expression power-up kit (typeof, cond,
	// switch, error) plus the opt-in try/can.
	for name, fn := range functy.Stdlib() {
		funcs[name] = fn
	}
	for name, fn := range functy.StdlibExtras() {
		funcs[name] = fn
	}
	return funcs
}

// printFunc builds a print/println function that writes its arguments to out and
// returns null. println appends a trailing newline; print does not.
func printFunc(out io.Writer, newline bool) function.Function {
	return function.New(&function.Spec{
		VarParam: &function.Parameter{Name: "args", Type: cty.DynamicPseudoType, AllowNull: true},
		Type:     function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			parts := make([]string, len(args))
			for i, a := range args {
				parts[i] = displayValue(a)
			}
			fmt.Fprint(out, strings.Join(parts, " "))
			if newline {
				fmt.Fprintln(out)
			}
			return cty.NullVal(cty.DynamicPseudoType), nil
		},
	})
}

// displayValue renders a cty value for print/println: strings unquoted, other
// values in their JSON form.
func displayValue(v cty.Value) string {
	if v.IsNull() {
		return "null"
	}
	if v.Type() == cty.String && v.IsKnown() {
		return v.AsString()
	}
	return string(toJSON(v))
}
