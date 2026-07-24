package functy

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
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

// --- test setup blocks -------------------------------------------------------

func TestParseTestSetupBlock(t *testing.T) {
	src := `test setup { var a = 1 }
test "t" { assert(a == 1) }
test setup { var b = 2 }`
	res, diags := NewParser().Parse([]byte(src), "t.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	if len(res.Setups) != 2 {
		t.Fatalf("got %d setup blocks, want 2", len(res.Setups))
	}
	if len(res.Tests) != 1 {
		t.Fatalf("got %d tests, want 1 (setup blocks are not tests)", len(res.Tests))
	}
}

// `test setup` reuses the `test` keyword; both must stay contextual — a `setup`
// function and a `test setup` block coexist, and `test` remains a callable name.
func TestTestSetupKeywordIsContextual(t *testing.T) {
	src := `func setup() -> number { return 7 }
test setup { var s = setup() }
test "setup() the function still callable" { assert(s == 7) }`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 1 || !outcomes[0].Passed() {
		t.Fatalf("expected 1 passing test, got %+v", outcomes)
	}
}

func TestReturnInSetupIsRejected(t *testing.T) {
	// Even nested inside an if — every return in the block belongs to the test
	// function, since functy has no nested functions.
	_, diags := NewParser().Parse([]byte("test setup {\n    if true { return }\n}\n"), "t.cty")
	if !diags.HasErrors() {
		t.Fatal("expected a parse error for return in a test setup block")
	}
	if !hasSummary(diags, "Return not allowed in a test setup block") {
		t.Fatalf("unexpected diagnostics:\n%s", diags.Error())
	}
}

// A setup block's bindings are visible in every test, and the block re-runs fresh
// per test (a host counter incremented in setup advances test-to-test).
func TestSetupBindingVisibleAndFreshPerTest(t *testing.T) {
	src := `test setup { var db = next() }
test "first" { assert(db == 1) }
test "second" { assert(db == 2) }`
	outcomes := runTestsWithEnv(t, src, map[string]function.Function{"next": counterFunc()}, nil)
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	for _, o := range outcomes {
		if !o.Passed() {
			t.Fatalf("test %q should pass: %v", o.Name, o.Err)
		}
	}
}

// A setup `defer` is function-scoped, so it runs at the test's end — after the
// test's own defers (LIFO) — and it runs even when the test fails.
func TestSetupTeardownRunsPerTestLIFO(t *testing.T) {
	rec, log := recorder()
	src := `test setup {
    record(1)
    defer record(2)
}
test "passes" {
    record(3)
    defer record(4)
    assert(true)
}
test "fails" {
    record(5)
    defer record(6)
    assert(false)
}`
	outcomes := runTestsWithEnv(t, src, map[string]function.Function{"record": rec}, nil)
	if len(outcomes) != 2 {
		t.Fatalf("got %d outcomes, want 2", len(outcomes))
	}
	if !outcomes[0].Passed() || !outcomes[1].Failed() {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	// Test 1: setup body (1), test body (3), defers LIFO — test's (4) then setup's (2).
	// Test 2 re-runs setup fresh (1), test body (5), fails at assert — defers on unwind
	// LIFO: test's (6) then setup's (2). The failing test still runs the shared setup
	// teardown, and setup re-runs per test (the leading 1 appears twice).
	want := []int64{1, 3, 4, 2, 1, 5, 6, 2}
	if !equalInt64(*log, want) {
		t.Fatalf("record order = %v, want %v", *log, want)
	}
}

func TestSetupSkipSkipsTest(t *testing.T) {
	src := `test setup { skip("no db") }
test "gated" { assert(false) }`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(outcomes))
	}
	if !outcomes[0].Skipped {
		t.Fatalf("test should be skipped, err=%v", outcomes[0].Err)
	}
	if outcomes[0].SkipReason != "no db" {
		t.Fatalf("skip reason = %q, want %q", outcomes[0].SkipReason, "no db")
	}
}

// Setup is per-file: a `test setup` in one file applies only to tests in that file,
// not to tests of the same namespace declared in another file.
func TestSetupIsPerFile(t *testing.T) {
	res, diags := NewParser().ParseAll([]Source{
		{Filename: "a.cty", Bytes: []byte("test setup { var db = 1 }\ntest \"a sees db\" { assert(db == 1) }\n")},
		{Filename: "b.cty", Bytes: []byte("test \"b does not see db\" { assert(db == 1) }\n")},
	})
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
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	outcomes := res.RunTests(func() *hcl.EvalContext { return ctx })

	byName := map[string]TestOutcome{}
	for _, o := range outcomes {
		byName[o.Name] = o
	}
	if a, ok := byName["a sees db"]; !ok || !a.Passed() {
		t.Fatalf("test in file a should pass (sees its own setup): %+v", a)
	}
	if b, ok := byName["b does not see db"]; !ok || !b.Failed() {
		t.Fatalf("test in file b should fail (must not see file a's setup): %+v", b)
	}
}

// counterFunc returns a niladic host function yielding 1, 2, 3, … on successive
// calls — a stand-in for per-test-fresh state established in setup.
func counterFunc() function.Function {
	n := int64(0)
	return function.New(&function.Spec{
		Type: function.StaticReturnType(cty.Number),
		Impl: func(_ []cty.Value, _ cty.Type) (cty.Value, error) {
			n++
			return cty.NumberIntVal(n), nil
		},
	})
}

// recorder returns a host function record(n) that appends n to a shared log, plus a
// pointer to that log — for asserting the order in which setup/test/defer code runs.
func recorder() (function.Function, *[]int64) {
	var log []int64
	f := function.New(&function.Spec{
		Params: []function.Parameter{{Name: "n", Type: cty.Number}},
		Type:   function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			n, _ := args[0].AsBigFloat().Int64()
			log = append(log, n)
			return cty.NullVal(cty.DynamicPseudoType), nil
		},
	})
	return f, &log
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- soft assertions (expect) ------------------------------------------------

// Every expect runs; each failure is recorded and the test fails reporting all of
// them, rather than stopping at the first like assert.
func TestExpectRecordsAllFailures(t *testing.T) {
	src := `test "several checks" {
    expect(1 == 2, "one")
    expect(2 == 2)
    expect(3 == 4, "three")
}`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(outcomes))
	}
	o := outcomes[0]
	if o.Passed() || !o.Failed() || o.Skipped {
		t.Fatalf("test should fail (not pass/skip): %+v", o)
	}
	if len(o.SoftFailures) != 2 {
		t.Fatalf("got %d soft failures, want 2 (the passing expect records nothing)", len(o.SoftFailures))
	}
	if got := o.SoftFailures[0].Value.GetAttr("message").AsString(); got != "one" {
		t.Fatalf("first failure message = %q, want one", got)
	}
	// Both failures render in Diagnostics (two error diagnostics).
	if n := len(o.Diagnostics()); n != 2 {
		t.Fatalf("Diagnostics() has %d entries, want 2", n)
	}
}

func TestExpectAllPassIsAPass(t *testing.T) {
	src := `test "all good" {
    expect(1 == 1)
    expect(2 == 2)
}`
	outcomes := compileAndRunTests(t, src)
	if len(outcomes) != 1 || !outcomes[0].Passed() {
		t.Fatalf("expected 1 passing test, got %+v", outcomes)
	}
}

// A failed expect enriches its recorded error with the condition's operands, exactly
// like assert.
func TestExpectCarriesOperandDetail(t *testing.T) {
	src := `test "detail" {
    var n = -3
    expect(n > 0, "must be positive")
}`
	outcomes := compileAndRunTests(t, src)
	sf := outcomes[0].SoftFailures
	if len(sf) != 1 {
		t.Fatalf("got %d soft failures, want 1", len(sf))
	}
	detail := sf[0].Value.GetAttr("detail")
	if detail.IsNull() || detail.AsString() != "n = -3" {
		t.Fatalf("operand detail = %#v, want \"n = -3\"", detail)
	}
}

// A recorded soft failure wins over a later skip: the test is failed, not skipped.
func TestExpectFailureBeatsLaterSkip(t *testing.T) {
	src := `test "soft then skip" {
    expect(false, "boom")
    skip("later")
}`
	outcomes := compileAndRunTests(t, src)
	o := outcomes[0]
	if o.Skipped {
		t.Fatalf("a test with a recorded failure must not be skipped: %+v", o)
	}
	if !o.Failed() {
		t.Fatalf("test should be failed: %+v", o)
	}
}

// A soft failure followed by a hard assert failure reports both.
func TestExpectThenHardFailureReportsBoth(t *testing.T) {
	src := `test "soft then hard" {
    expect(false, "soft")
    assert(false, "hard")
}`
	outcomes := compileAndRunTests(t, src)
	o := outcomes[0]
	if !o.Failed() || len(o.SoftFailures) != 1 || o.Err == nil {
		t.Fatalf("want one soft failure plus a hard error: %+v", o)
	}
	if n := len(o.Diagnostics()); n != 2 {
		t.Fatalf("Diagnostics() has %d entries, want 2 (soft + hard)", n)
	}
}

// expect is test-only: it is injected by the runner, not present in a plain
// (non-test) eval context, so `functy run`-style compilation cannot call it.
func TestExpectIsTestOnly(t *testing.T) {
	if _, ok := Stdlib()["expect"]; ok {
		t.Fatal("expect must not be in Stdlib (it is test-only)")
	}
	src := `func f() -> bool { return expect(true) }`
	res, diags := NewParser().Parse([]byte(src), "t.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile errors:\n%s", cdiags.Error())
	}
	all := map[string]function.Function{}
	for k, v := range Stdlib() {
		all[k] = v
	}
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	if _, err := funcs["f"].Call(nil); err == nil {
		t.Fatal("expected calling expect outside a test to fail (unknown function)")
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
