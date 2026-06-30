package functy

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestShortVarDecl(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        x := 40
        return x + 2
    }`)
	wantNum(t, call(t, funcs, "f"), 42)
}

func TestShortVarThenReassign(t *testing.T) {
	// `:=` declares; a later `=` reassigns the same binding.
	funcs := compileFuncs(t, `func f() -> number {
        x := 1
        x = 2
        return x
    }`)
	wantNum(t, call(t, funcs, "f"), 2)
}

func TestShortVarInForClause(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        sum := 0
        for i := 0; i < 4; i = i + 1 {
            sum = sum + i
        }
        return sum
    }`)
	wantNum(t, call(t, funcs, "f"), 6) // 0+1+2+3
}

func TestShortVarShadowsInNestedScope(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        x := 1
        {
            x := 10
            x = x + 1
        }
        return x
    }`)
	wantNum(t, call(t, funcs, "f"), 1) // inner shadow does not leak
}

func TestShortVarDuplicateIsError(t *testing.T) {
	parseErr(t, `func f() { x := 1; x := 2 }`)
	// Mixed with var: a `:=` that re-declares an existing var is also a duplicate.
	parseErr(t, `func f() { var x = 1; x := 2 }`)
}

func TestShortVarUntypedUnderStrict(t *testing.T) {
	// `:=` cannot annotate a type, so it is rejected under strict declared-types.
	p := NewParser().RequireDeclaredTypes(true)
	if !parseWith(t, p, "func f() { x := 1 }").HasErrors() {
		t.Fatal("expected `:=` to be rejected under RequireDeclaredTypes")
	}
}

func TestShortCaptureSuccess(t *testing.T) {
	// `v, err := expr` declares both and captures.
	funcs := compileFuncs(t, pickSrc+`func f() -> number {
        v, err := pick(false)
        if err != null { return -1 }
        return v
    }`)
	wantNum(t, call(t, funcs, "f"), 42)
}

func TestShortCaptureError(t *testing.T) {
	funcs := compileFuncs(t, pickSrc+`func f() -> bool {
        v, err := pick(true)
        return v == null && err != null
    }`)
	if !call(t, funcs, "f").True() {
		t.Fatal("expected v null and err set on failure")
	}
}

func TestShortCaptureDiscard(t *testing.T) {
	// Blank targets are allowed in the declare form too.
	funcs := compileFuncs(t, pickSrc+`func f(fail: bool) -> bool {
        _, err := pick(fail)
        return err != null
    }`)
	if !call(t, funcs, "f", cty.True).True() {
		t.Fatal("expected err set on failure")
	}
	if call(t, funcs, "f", cty.False).True() {
		t.Fatal("expected err null on success")
	}
}

func TestShortCaptureErrPinnedToErrorType(t *testing.T) {
	// The `:=` form pins the error target to the built-in `error` type, so a later
	// reassignment of a non-error value to it fails (like `var err: error`).
	funcs := compileFuncs(t, pickSrc+`func f() {
        v, err := pick(false)
        err = "not an error"
    }`)
	callErr(t, funcs, "f")

	// The value target stays dynamic: reassigning a different type is fine.
	funcs = compileFuncs(t, pickSrc+`func f() -> string {
        v, err := pick(false)
        v = "now a string"
        return v
    }`)
	wantStr(t, call(t, funcs, "f"), "now a string")
}

func TestShortCaptureDuplicateIsError(t *testing.T) {
	parseErr(t, pickSrc+`func f() {
        v, err := pick(false)
        v, err := pick(false)
    }`)
}

func TestShortCaptureBothBlankIsError(t *testing.T) {
	parseErr(t, pickSrc+`func f() { _, _ := pick(false) }`)
}

func TestParseShortForms(t *testing.T) {
	parse(t, `func f() {
        a := 1
        b, c := work()
        _, d := work()
        e, _ := work()
    }`)
}
