package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// pickSrc defines a helper functy function that throws when asked, so the
// capture-assignment tests can exercise both the success and error paths.
const pickSrc = `func pick(fail: bool) -> number {
    if fail { throw "kaboom" }
    return 42
}
`

func TestCaptureSuccess(t *testing.T) {
	funcs := compileFuncs(t, pickSrc+`func f() -> number {
        var val: number
        var err: error
        val, err = pick(false)
        if err != null { return -1 }
        return val
    }`)
	wantNum(t, call(t, funcs, "f"), 42)
}

func TestCaptureError(t *testing.T) {
	// On failure: val is null, err carries the caught error (with .message).
	funcs := compileFuncs(t, pickSrc+`func f() -> string {
        var val: number
        var err: error
        val, err = pick(true)
        if val != null { return "val not null" }
        return err.message
    }`)
	got := call(t, funcs, "f")
	if got.IsNull() || got.Type() != cty.String || !strings.Contains(got.AsString(), "kaboom") {
		t.Fatalf("expected captured message containing 'kaboom', got %#v", got)
	}
}

func TestCaptureDiscardValue(t *testing.T) {
	// `_, err = expr` evaluates for side effects / error only.
	funcs := compileFuncs(t, pickSrc+`func f(fail: bool) -> bool {
        var err: error
        _, err = pick(fail)
        return err != null
    }`)
	if !call(t, funcs, "f", cty.True).True() {
		t.Fatal("expected err captured on failure")
	}
	if call(t, funcs, "f", cty.False).True() {
		t.Fatal("expected err null on success")
	}
}

func TestCaptureDiscardError(t *testing.T) {
	// `val, _ = expr` is Go's best-effort form: val is set on success, null on
	// failure, and the error is swallowed (no unwind).
	funcs := compileFuncs(t, pickSrc+`func f(fail: bool) -> number {
        var val: number = 7
        val, _ = pick(fail)
        if val == null { return -1 }
        return val
    }`)
	wantNum(t, call(t, funcs, "f", cty.False), 42)
	wantNum(t, call(t, funcs, "f", cty.True), -1) // val nulled, error swallowed
}

func TestCaptureTypedValueCoerces(t *testing.T) {
	// A successful value is coerced through the value target's declared type.
	funcs := compileFuncs(t, `func f() -> number {
        var val: number
        var err: error
        val, err = "5"
        return val
    }`)
	wantNum(t, call(t, funcs, "f"), 5)
}

func TestCaptureBothBlankIsParseError(t *testing.T) {
	parseErr(t, pickSrc+`func f() { _, _ = pick(false) }`)
}

func TestCaptureUndeclaredTarget(t *testing.T) {
	// Both non-blank targets must already be declared, like a plain assignment.
	funcs := compileFuncs(t, pickSrc+`func f() {
        var err: error
        val, err = pick(false)
    }`)
	callErr(t, funcs, "f")
}

func TestParseCaptureAssign(t *testing.T) {
	parse(t, `func f() {
        var val
        var err
        val, err = work()
        _, err = work()
        val, _ = work()
    }`)
}

// TestCaptureSingleEvaluation proves the right-hand side runs exactly once.
func TestCaptureSingleEvaluation(t *testing.T) {
	var calls int
	record := function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.Number),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			calls++
			return cty.NumberIntVal(1), nil
		},
	})

	src := `func f() -> number {
        var val: number
        var err: error
        val, err = record()
        return val
    }`
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile: %s", cdiags.Error())
	}
	all := map[string]function.Function{"record": record}
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}

	if _, err := funcs["f"].Call(nil); err != nil {
		t.Fatalf("call: %v", err)
	}
	if calls != 1 {
		t.Fatalf("rhs evaluated %d times, want 1", calls)
	}
}
