package functy

import (
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// operand is a single variable referenced by a failed assert condition, paired
// with its value — the raw material for the error's operands/detail enrichment.
type operand struct {
	name  string
	value cty.Value
}

// conditionOperands gathers the values of the variables a condition references, for
// pytest-style "why did it fail" reporting. It uses expr.Variables() +
// Traversal.TraverseAbs, which read already-bound variables from the eval context and
// never re-invoke functions — so gathering operands is guaranteed side-effect-free
// (a function call in the condition, e.g. len(xs), contributes only its argument
// variables, not a re-evaluation). References are deduped by rendered name, since a
// variable may appear more than once (n > 0 && n < 10).
func conditionOperands(expr hcl.Expression, ctx *hcl.EvalContext) []operand {
	var ops []operand
	seen := make(map[string]bool)
	for _, tr := range expr.Variables() {
		name := traversalString(tr)
		if seen[name] {
			continue
		}
		seen[name] = true
		v, diags := tr.TraverseAbs(ctx)
		if diags.HasErrors() {
			// The condition already evaluated, so this should not happen; skip
			// defensively rather than mask the assertion with a lookup error.
			continue
		}
		ops = append(ops, operand{name: name, value: v})
	}
	return ops
}

// traversalString renders a traversal in source-like form: n, x.foo, xs[0], m["k"].
// hcl has no built-in for this.
func traversalString(tr hcl.Traversal) string {
	var b strings.Builder
	for i, step := range tr {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			b.WriteString(s.Name)
		case hcl.TraverseAttr:
			b.WriteByte('.')
			b.WriteString(s.Name)
		case hcl.TraverseIndex:
			b.WriteByte('[')
			b.WriteString(renderValue(s.Key))
			b.WriteByte(']')
		default:
			if i > 0 {
				b.WriteByte('.')
			}
			b.WriteString("?")
		}
	}
	return b.String()
}

// renderValue renders a cty value compactly for the human-readable detail string:
// scalars directly (strings quoted), collections/objects as JSON. It is best-effort —
// an unknown or unserializable value degrades to a placeholder rather than erroring.
func renderValue(v cty.Value) string {
	if v.IsNull() {
		return "null"
	}
	if !v.IsWhollyKnown() {
		return "(unknown)"
	}
	switch v.Type() {
	case cty.String:
		return strconv.Quote(v.AsString())
	case cty.Bool:
		if v.True() {
			return "true"
		}
		return "false"
	case cty.Number:
		return v.AsBigFloat().Text('f', -1)
	}
	if b, err := ctyjson.Marshal(v, v.Type()); err == nil {
		return string(b)
	}
	return typeString(v.Type())
}

// renderOperands renders gathered operands as "name = value, name = value" for the
// error's detail attribute.
func renderOperands(ops []operand) string {
	parts := make([]string, len(ops))
	for i, o := range ops {
		parts[i] = o.name + " = " + renderValue(o.value)
	}
	return strings.Join(parts, ", ")
}
