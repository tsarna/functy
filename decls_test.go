package functy

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// evalConsts parses top-level const/var declarations from src and evaluates them
// with EvalDecls, returning the resulting variable table and any diagnostics.
func evalConsts(t *testing.T, src string) (map[string]cty.Value, hcl.Diagnostics) {
	t.Helper()
	res, diags := NewParser().AllowTopLevelConst(true).AllowTopLevelVar(true).Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	ctx := &hcl.EvalContext{Functions: testStdlib(), Variables: map[string]cty.Value{}}
	decls := append(append([]Decl{}, res.Consts...), res.Vars...)
	return ctx.Variables, EvalDecls(decls, ctx)
}

// A reverse-ordered dependency chain (c0 = c1+1; …; cN = 0) resolves to correct
// values regardless of source order — and now in O(n+e) rather than the former
// O(n²), so a size that used to take seconds is instant.
func TestEvalDeclsReverseChain(t *testing.T) {
	const n = 5000
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "const c%d = c%d + 1\n", i, i+1)
	}
	fmt.Fprintf(&b, "const c%d = 0\n", n)

	vars, diags := evalConsts(t, b.String())
	if diags.HasErrors() {
		t.Fatalf("unexpected errors:\n%s", diags.Error())
	}
	// c_i = n - i (c_n = 0, each earlier one is +1).
	for _, i := range []int{0, 1, n / 2, n - 1, n} {
		want := n - i
		got := vars[fmt.Sprintf("c%d", i)]
		if got.IsNull() || !got.RawEquals(cty.NumberIntVal(int64(want))) {
			t.Fatalf("c%d = %#v, want %d", i, got, want)
		}
	}
}

// A dependency cycle is reported (and not silently resolved), and does not hang.
func TestEvalDeclsCycleReported(t *testing.T) {
	_, diags := evalConsts(t, "const a = b + 1\nconst b = a + 1\n")
	if !hasSummary(diags, "Unresolved declaration") {
		t.Fatalf("expected a cycle to be reported, got:\n%s", allDiags(diags))
	}
}

// A declaration depending on a cyclic one is itself unresolved.
func TestEvalDeclsDependentOnCycleUnresolved(t *testing.T) {
	// a<->b cycle; c depends on b.
	_, diags := evalConsts(t, "const a = b\nconst b = a\nconst c = b + 1\n")
	n := 0
	for _, d := range diags {
		if d.Summary == "Unresolved declaration" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("expected all 3 of a, b, c unresolved, got %d:\n%s", n, allDiags(diags))
	}
}

// A duplicate name is still reported after the switch to topological ordering.
func TestEvalDeclsDuplicateReported(t *testing.T) {
	_, diags := evalConsts(t, "const a = 1\nconst a = 2\n")
	if !hasSummary(diags, "Duplicate declaration") {
		t.Fatalf("expected a duplicate to be reported, got:\n%s", allDiags(diags))
	}
}

// A self-referential const is evaluated (and errors on the undefined self-reference)
// rather than being reported as a cycle — the pre-existing behavior, preserved.
func TestEvalDeclsSelfReferenceIsNotACycle(t *testing.T) {
	_, diags := evalConsts(t, "const a = a + 1\n")
	if hasSummary(diags, "Unresolved declaration") {
		t.Fatalf("a self-reference should evaluate-and-error, not be a cycle:\n%s", allDiags(diags))
	}
	if !diags.HasErrors() {
		t.Fatal("expected an evaluation error for the undefined self-reference")
	}
}
