package repl

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// classify mirrors the REPL's decision: complete (parses), continue (incomplete,
// keep reading), or error (genuine, surface immediately).
func classify(src string) string {
	_, diags := hclsyntax.ParseExpression([]byte(src), "<t>", hcl.Pos{Line: 1, Column: 1})
	if !diags.HasErrors() {
		return "complete"
	}
	if parseIncomplete(diags) {
		return "continue"
	}
	return "error"
}

// TestMultilineClassification pins the empirically-derived classification of
// complete / incomplete / erroneous expression fragments. If an HCL upgrade
// changes diagnostic wording, this fails loudly and the incompleteSummaries map
// / detail substring in multiline.go must be revisited.
func TestMultilineClassification(t *testing.T) {
	cases := []struct {
		src  string
		want string
	}{
		// Complete expressions.
		{"1 + 1", "complete"},
		{`"abc"`, "complete"},
		{"[1, 2]", "complete"},
		{"{a = 1}", "complete"},
		{"foo(1)", "complete"},

		// Incomplete — Unterminated … family.
		{"[1, 2", "continue"},
		{"{a = 1", "continue"},
		{"foo(1", "continue"},
		{`"abc`, "continue"},
		{`"${foo`, "continue"},

		// Incomplete — Missing expression + EOF detail.
		{"1 +", "continue"},
		{"(", "continue"},
		{"{a =", "continue"},
		{"x[", "continue"},
		{"[1] + [", "continue"},

		// Incomplete — nested, still EOF.
		{"[1, {a =", "continue"},
		{"{a = [1,", "continue"},

		// Genuine errors — empty slot.
		{"[1, , 2]", "error"},
		{"{a = , b = 2}", "error"},

		// Genuine errors — stray/invalid token.
		{"]", "error"},
		{"}", "error"},
		{"[, 1]", "error"},

		// Genuine errors — invalid attribute name.
		{"foo.", "error"},
		{"[1, foo.", "error"},

		// Genuine errors — extra characters after expression.
		{"1 2", "error"},
		{"1 + 2 foo", "error"},

		// Genuine errors — invalid expression token (even inside an open bracket).
		{"1 + + 2", "error"},
		{"[1 + + 2", "error"},

		// Genuine errors — requires-a-colon cases (treated as single-line errors).
		{"c ? a", "error"},
		{"[for x in y", "error"},
	}

	for _, tt := range cases {
		if got := classify(tt.src); got != tt.want {
			t.Errorf("classify(%q) = %q, want %q", tt.src, got, tt.want)
		}
	}
}

func TestSplitAssignment(t *testing.T) {
	name, rhs, ok := splitAssignment("x = {\n  a = 1\n}")
	if !ok || name != "x" {
		t.Fatalf("splitAssignment multi-line: name=%q ok=%v", name, ok)
	}
	if rhs != " {\n  a = 1\n}" {
		t.Fatalf("multi-line RHS = %q", rhs)
	}

	// A multi-line non-assignment (object literal) is not an assignment.
	if _, _, ok := splitAssignment("{\n  a = 1\n}"); ok {
		t.Fatal("object literal misclassified as assignment")
	}
}
