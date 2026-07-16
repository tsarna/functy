package functy

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// compileLimited parses and compiles src with the given per-invocation step ceiling
// (0 = unbounded), wiring a late-bound eval context that holds the test stdlib, any
// extra host functions, and the compiled functy functions (so recursion and sibling
// calls resolve).
func compileLimited(t *testing.T, src string, maxSteps int, extra map[string]function.Function) map[string]function.Function {
	t.Helper()
	res, diags := NewParser().MaxSteps(maxSteps).Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, diags := res.Compile(func() *hcl.EvalContext { return ctx })
	if diags.HasErrors() {
		t.Fatalf("compile errors:\n%s", diags.Error())
	}
	all := testStdlib()
	for k, v := range extra {
		all[k] = v
	}
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	return funcs
}

// callLimit calls a niladic function and asserts the call breached the execution
// limit, returning the recovered *LimitError.
func callLimit(t *testing.T, funcs map[string]function.Function, name string) *LimitError {
	t.Helper()
	_, err := funcs[name].Call([]cty.Value{})
	var le *LimitError
	if !errors.As(err, &le) {
		t.Fatalf("calling %q: want *LimitError, got %v", name, err)
	}
	return le
}

func TestLimitInfiniteLoops(t *testing.T) {
	for _, src := range []string{
		`func spin() { for {} }`,
		`func spin() { for true {} }`,
		`func spin() { while true {} }`,
	} {
		funcs := compileLimited(t, src, 100, nil)
		le := callLimit(t, funcs, "spin")
		if le.Limit != 100 {
			t.Fatalf("%s: Limit = %d, want 100", src, le.Limit)
		}
		if le.Steps <= le.Limit {
			t.Fatalf("%s: Steps = %d, want > %d", src, le.Steps, le.Limit)
		}
	}
}

func TestLimitEmptyBodyLoopCaught(t *testing.T) {
	// A zero-statement body executes no per-statement ticks; the loop-backedge tick
	// is what catches it.
	funcs := compileLimited(t, `func spin() { for {} }`, 10, nil)
	callLimit(t, funcs, "spin")
}

func TestLimitBoundedLoopCompletes(t *testing.T) {
	src := `func total() -> number {
	var n = 0
	for i in [1, 2, 3, 4, 5] { n = n + i }
	return n
}`
	funcs := compileLimited(t, src, 1000, nil)
	got, err := funcs["total"].Call([]cty.Value{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.RawEquals(cty.NumberIntVal(15)) {
		t.Fatalf("got %#v, want 15", got)
	}
}

func TestLimitUncatchableByTry(t *testing.T) {
	// The guard must not be swallowed by a catch, or `try { while true {} }` would
	// fire the guard, unwind into the catch, and loop again.
	src := `func spin() {
	try {
		for {}
	} catch e {
		return "caught"
	}
}`
	funcs := compileLimited(t, src, 50, nil)
	callLimit(t, funcs, "spin")
}

func TestLimitNotCapturedByValErr(t *testing.T) {
	// A breach inside a called function must propagate through `val, err :=`, not be
	// captured into err.
	src := `func spin() { for {} }
func caller() -> string {
	val, err := spin()
	return "done"
}`
	funcs := compileLimited(t, src, 50, nil)
	callLimit(t, funcs, "caller")
}

func TestLimitSkipsDefers(t *testing.T) {
	// A defer can itself loop, so a breach must not run the frame's defers.
	marked := 0
	mark := function.New(&function.Spec{
		Type: function.StaticReturnType(cty.Number),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) {
			marked++
			return cty.NumberIntVal(1), nil
		},
	})
	src := `func breach() {
	defer mark()
	for {}
}`
	funcs := compileLimited(t, src, 20, map[string]function.Function{"mark": mark})
	callLimit(t, funcs, "breach")
	if marked != 0 {
		t.Fatalf("defer ran %d times after a breach; want 0", marked)
	}
}

func TestLimitRecursionNotCaught(t *testing.T) {
	// Tier-1 limitation: each nested call is a fresh interp whose counter starts at
	// zero, so deep recursion under a per-frame ceiling is not caught. This documents
	// that recursion coverage needs Tier 2.
	src := `func countdown(n: number) -> number {
	if n <= 0 { return 0 }
	return countdown(n - 1)
}`
	funcs := compileLimited(t, src, 50, nil)
	got, err := funcs["countdown"].Call([]cty.Value{cty.NumberIntVal(200)})
	if err != nil {
		t.Fatalf("recursion should not breach a per-frame limit, got: %v", err)
	}
	if !got.RawEquals(cty.NumberIntVal(0)) {
		t.Fatalf("got %#v, want 0", got)
	}
}

func TestLimitUnboundedByDefault(t *testing.T) {
	// maxSteps of 0 (the default) leaves execution unbounded, so a large finite loop
	// runs to completion — existing embeddings are unchanged.
	src := `func count() -> number {
	var n = 0
	for n < 50000 { n = n + 1 }
	return n
}`
	funcs := compileLimited(t, src, 0, nil)
	got, err := funcs["count"].Call([]cty.Value{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.RawEquals(cty.NumberIntVal(50000)) {
		t.Fatalf("got %#v, want 50000", got)
	}
}

func TestLimitBoundsTestBodies(t *testing.T) {
	// A runaway loop in a `test` block surfaces as a (non-skipped) *LimitError rather
	// than hanging.
	src := `test "spins forever" { for {} }`
	res, diags := NewParser().MaxSteps(100).Parse([]byte(src), "test.cty")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile errors:\n%s", cdiags.Error())
	}
	all := testStdlib()
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}

	outcomes := res.RunTests(func() *hcl.EvalContext { return ctx })
	if len(outcomes) != 1 {
		t.Fatalf("got %d outcomes, want 1", len(outcomes))
	}
	o := outcomes[0]
	if !o.Failed() {
		t.Fatalf("outcome should have failed; skipped=%v err=%v", o.Skipped, o.Err)
	}
	var le *LimitError
	if !errors.As(o.Err, &le) {
		t.Fatalf("want *LimitError, got %v", o.Err)
	}
}
