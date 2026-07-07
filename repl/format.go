package repl

import (
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// maxInlineWidth bounds the single-line form of a collection of scalars before
// it is broken onto multiple lines.
const maxInlineWidth = 72

// formatValue renders a cty.Value in HCL style — the way the value would be
// written in source — so REPL output is round-trippable. Top-level nulls are
// handled by the caller (echo rules), not here.
func formatValue(v cty.Value) string {
	return render(v, 0)
}

func render(v cty.Value, indent int) string {
	if v.IsNull() {
		return "null"
	}
	if !v.IsKnown() {
		return "(unknown)"
	}

	t := v.Type()
	// Capsule values and rich objects (objects carrying a _capsule attribute)
	// have no literal HCL form; print a concise typed summary instead.
	if t.IsCapsuleType() || (t.IsObjectType() && t.HasAttribute("_capsule")) {
		return renderCapsule(v)
	}

	switch {
	case t == cty.String:
		return quoteHCL(v.AsString())
	case t == cty.Number:
		return v.AsBigFloat().Text('f', -1)
	case t == cty.Bool:
		if v.True() {
			return "true"
		}
		return "false"
	case t.IsTupleType() || t.IsListType() || t.IsSetType():
		return renderSeq(v, indent)
	case t.IsObjectType() || t.IsMapType():
		return renderMapping(v, indent)
	default:
		return v.GoString()
	}
}

// renderCapsule prints a capsule (or rich object) using the underlying capsule's
// CapsuleOps.GoString summary (e.g. eventbus(0x...), <bytes 1024>).
func renderCapsule(v cty.Value) string {
	if v.Type().IsObjectType() && v.Type().HasAttribute("_capsule") {
		return v.GetAttr("_capsule").GoString()
	}
	return v.GoString()
}

func renderSeq(v cty.Value, indent int) string {
	if v.LengthInt() == 0 {
		return "[]"
	}

	var parts []string
	allScalar := true
	for it := v.ElementIterator(); it.Next(); {
		_, ev := it.Element()
		parts = append(parts, render(ev, indent+1))
		if !isScalar(ev) {
			allScalar = false
		}
	}

	inline := "[" + strings.Join(parts, ", ") + "]"
	if allScalar && len(inline) <= maxInlineWidth {
		return inline
	}

	ind := strings.Repeat("  ", indent)
	indChild := strings.Repeat("  ", indent+1)
	var b strings.Builder
	b.WriteString("[\n")
	for _, p := range parts {
		b.WriteString(indChild)
		b.WriteString(p)
		b.WriteString(",\n")
	}
	b.WriteString(ind)
	b.WriteString("]")
	return b.String()
}

func renderMapping(v cty.Value, indent int) string {
	m := v.AsValueMap()
	if len(m) == 0 {
		return "{}"
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type entry struct{ key, val string }
	entries := make([]entry, 0, len(keys))
	maxKey := 0
	allScalar := true
	for _, k := range keys {
		key := k
		if !hclsyntax.ValidIdentifier(k) {
			key = quoteHCL(k)
		}
		entries = append(entries, entry{key, render(m[k], indent+1)})
		if len(key) > maxKey {
			maxKey = len(key)
		}
		if !isScalar(m[k]) {
			allScalar = false
		}
	}

	if allScalar {
		parts := make([]string, len(entries))
		for i, e := range entries {
			parts[i] = e.key + " = " + e.val
		}
		inline := "{ " + strings.Join(parts, ", ") + " }"
		if len(inline) <= maxInlineWidth {
			return inline
		}
	}

	ind := strings.Repeat("  ", indent)
	indChild := strings.Repeat("  ", indent+1)
	var b strings.Builder
	b.WriteString("{\n")
	for _, e := range entries {
		b.WriteString(indChild)
		b.WriteString(e.key)
		b.WriteString(strings.Repeat(" ", maxKey-len(e.key)))
		b.WriteString(" = ")
		b.WriteString(e.val)
		b.WriteString("\n")
	}
	b.WriteString(ind)
	b.WriteString("}")
	return b.String()
}

// isScalar reports whether a value renders to a single token, so a collection of
// such values can collapse onto one line. Collections (objects/maps/lists/sets/
// tuples) are not scalar; primitives, null/unknown, and capsules are.
func isScalar(v cty.Value) bool {
	if v.IsNull() || !v.IsKnown() {
		return true
	}
	t := v.Type()
	if t.IsCapsuleType() || (t.IsObjectType() && t.HasAttribute("_capsule")) {
		return true
	}
	return t == cty.String || t == cty.Number || t == cty.Bool
}

// quoteHCL renders s as an HCL string literal, escaping the template-start
// sequences ${ and %{ so the printed value round-trips without interpolation.
func quoteHCL(s string) string {
	q := strconv.Quote(s)
	q = strings.ReplaceAll(q, "${", "$${")
	q = strings.ReplaceAll(q, "%{", "%%{")
	return q
}
