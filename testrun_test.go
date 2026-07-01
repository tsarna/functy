package functy

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// compileAndRunTests parses and compiles src (with the stdlib merged in), then runs
// its test blocks against the assembled eval context.
func compileAndRunTests(t *testing.T, src string) []TestOutcome {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile errors:\n%s", cdiags.Error())
	}
	all := testStdlib()
	for k, v := range Stdlib() {
		all[k] = v
	}
	for k, v := range StdlibExtras() {
		all[k] = v
	}
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	return res.RunTests(func() *hcl.EvalContext { return ctx })
}

func TestParseTestBlocks(t *testing.T) {
	src := `test "one" { }
func f() -> number { return 1 }
test "two" { assert(f() == 1) }`
	res, diags := NewParser().Parse([]byte(src), "t.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	if len(res.Tests) != 2 {
		t.Fatalf("got %d test blocks, want 2", len(res.Tests))
	}
	if res.Tests[0].Name != "one" || res.Tests[1].Name != "two" {
		t.Fatalf("names = %q, %q", res.Tests[0].Name, res.Tests[1].Name)
	}
	// The second test's body has a statement; tests are not registered as functions.
	if len(res.Tests[1].Body) != 1 {
		t.Fatalf("second test body has %d statements, want 1", len(res.Tests[1].Body))
	}
	if len(res.Funcs) != 1 {
		t.Fatalf("got %d funcs, want 1 (tests are not functions)", len(res.Funcs))
	}
}

func TestTestKeywordIsContextual(t *testing.T) {
	// `test` remains usable as an ordinary function name.
	src := `func test(x: number) -> number { return x * 2 }
test "test() still callable" { assert(test(21) == 42) }`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 1 || !outcomes[0].Passed() {
		t.Fatalf("expected 1 passing test, got %+v", outcomes)
	}
}

func TestRunTestsPassAndFail(t *testing.T) {
	src := `func add(a: number, b: number) -> number { return a + b }
test "pass" { assert(add(2, 3) == 5) }
test "fail" { assert(add(2, 3) == 6, "wrong") }`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	if !outcomes[0].Passed() {
		t.Fatalf("test %q should pass, got %v", outcomes[0].Name, outcomes[0].Err)
	}
	if outcomes[1].Passed() {
		t.Fatal("second test should fail")
	}
	var te *ThrownError
	if !errors.As(outcomes[1].Err, &te) {
		t.Fatalf("failed test error should be a *ThrownError, got %T", outcomes[1].Err)
	}
	if got := te.Value.GetAttr("message").AsString(); got != "wrong" {
		t.Fatalf("message = %q, want wrong", got)
	}
}

func TestRunTestsSkip(t *testing.T) {
	src := `test "direct skip" { skip("wip") }
func helper() { skip("from helper") }
test "skip via helper" { helper() }
test "bare skip" { skip() }`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 3 {
		t.Fatalf("got %d outcomes, want 3", len(outcomes))
	}
	for _, o := range outcomes {
		if !o.Skipped {
			t.Fatalf("test %q should be skipped, err=%v", o.Name, o.Err)
		}
		if o.Passed() || o.Failed() {
			t.Fatalf("skipped test %q must be neither passed nor failed", o.Name)
		}
		if o.Diagnostics() != nil {
			t.Fatalf("skipped test %q should have no diagnostics", o.Name)
		}
	}
	if outcomes[0].SkipReason != "wip" {
		t.Fatalf("reason = %q, want wip", outcomes[0].SkipReason)
	}
	if outcomes[1].SkipReason != "from helper" {
		t.Fatalf("reason = %q, want 'from helper' (skip through a call)", outcomes[1].SkipReason)
	}
	if outcomes[2].SkipReason != "" {
		t.Fatalf("bare skip reason = %q, want empty", outcomes[2].SkipReason)
	}
}

func TestRunTestsMatchingFilters(t *testing.T) {
	src := `test "alpha" { assert(true) }
test "beta" { assert(true) }
test "alphabet" { assert(true) }`
	res, diags := NewParser().Parse([]byte(src), "t.cty")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, _ := res.Compile(func() *hcl.EvalContext { return ctx })
	all := testStdlib()
	for k, v := range Stdlib() {
		all[k] = v
	}
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}

	outcomes := res.RunTestsMatching(func() *hcl.EvalContext { return ctx }, func(name string) bool {
		return name == "alpha" || name == "alphabet"
	})
	if len(outcomes) != 2 {
		t.Fatalf("filter should select 2 tests, got %d", len(outcomes))
	}
	if outcomes[0].Name != "alpha" || outcomes[1].Name != "alphabet" {
		t.Fatalf("selected %q, %q", outcomes[0].Name, outcomes[1].Name)
	}
}

func TestRunTestsRecordsDuration(t *testing.T) {
	outcomes := compileAndRunTests(t, `test "t" { assert(true) }`)
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes", len(outcomes))
	}
	if outcomes[0].Duration < 0 {
		t.Fatalf("duration should be non-negative, got %v", outcomes[0].Duration)
	}
}

func TestOutcomeDiagnostics(t *testing.T) {
	rng := hcl.Range{Filename: "t.cty", Start: hcl.Pos{Line: 5, Column: 3, Byte: 40}, End: hcl.Pos{Line: 5, Column: 9, Byte: 46}}

	// A passing outcome yields no diagnostics.
	if d := (TestOutcome{Name: "ok"}).Diagnostics(); d != nil {
		t.Fatalf("passing outcome should give nil diagnostics, got %v", d)
	}

	// A thrown-error failure delegates to the error's own diagnostics (with its
	// message and range).
	thrown := TestOutcome{Name: "x", DefRange: rng, Err: &ThrownError{Value: errorValue(cty.StringVal("boom"), rng)}}
	td := thrown.Diagnostics()
	if len(td) != 1 || td[0].Summary != "boom" || td[0].Subject == nil {
		t.Fatalf("thrown diagnostics = %+v", td)
	}

	// A plain (non-thrown) failure is located at the test block.
	plain := TestOutcome{Name: "y", DefRange: rng, Err: errors.New("kaboom")}
	pd := plain.Diagnostics()
	if len(pd) != 1 || pd[0].Summary != "kaboom" || pd[0].Subject == nil || *pd[0].Subject != rng {
		t.Fatalf("plain diagnostics = %+v", pd)
	}
}
