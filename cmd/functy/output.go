package main

import (
	"fmt"
	"io"

	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// printResult writes a function's return value in the requested format. A null
// value prints nothing, in every format.
func printResult(w io.Writer, v cty.Value, format string) error {
	if v.IsNull() {
		return nil
	}
	switch format {
	case "json":
		fmt.Fprintln(w, string(toJSON(v)))
	case "hcl":
		fmt.Fprintln(w, string(hclwrite.TokensForValue(v).Bytes()))
	case "raw":
		if v.Type() == cty.String && v.IsKnown() {
			fmt.Fprintln(w, v.AsString())
		} else {
			fmt.Fprintln(w, string(toJSON(v)))
		}
	default:
		return fmt.Errorf("unknown output format %q (want json, hcl, or raw)", format)
	}
	return nil
}

// toJSON renders a cty value as JSON using its own type.
func toJSON(v cty.Value) []byte {
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return []byte(fmt.Sprintf("%q", err.Error()))
	}
	return b
}
