package functy

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func TestAssertPasses(t *testing.T) {
	// A true condition returns true and does not raise.
	funcs := compileStd(t, `func f(n: number) -> bool {
        return assert(n > 0)
    }`)
	got := call(t, funcs, "f", num(5))
	if !got.RawEquals(cty.True) {
		t.Fatalf("got %#v, want true", got)
	}
}

func TestAssertFailsDefaultMessage(t *testing.T) {
	funcs := compileStd(t, `func f(n: number) -> string {
        try {
            assert(n > 0)
            return "ok"
        } catch e {
            return e.message
        }
    }`)
	wantStr(t, call(t, funcs, "f", num(-1)), "assertion failed")
}

func TestAssertFailsCustomMessage(t *testing.T) {
	funcs := compileStd(t, `func f(n: number) -> string {
        try {
            assert(n > 0, "n must be positive")
            return "ok"
        } catch e {
            return e.message
        }
    }`)
	wantStr(t, call(t, funcs, "f", num(-1)), "n must be positive")
}

func TestAssertObjectMessage(t *testing.T) {
	// An object message is used directly, like error()/throw — so extra
	// attributes survive to the catch site.
	funcs := compileStd(t, `func f(n: number) -> number {
        try {
            assert(n > 0, { message = "bad", code = 422 })
            return 0
        } catch e {
            return e.code
        }
    }`)
	wantNum(t, call(t, funcs, "f", num(-1)), 422)
}

func TestAssertCarriesConditionRange(t *testing.T) {
	// The error's range points at the condition expression (line 3 here).
	funcs := compileStd(t, `func f(n: number) -> number {
        try {
            assert(n > 0)
            return 0
        } catch e {
            return e.range.start.line
        }
    }`)
	wantNum(t, call(t, funcs, "f", num(-1)), 3)
}

func TestAssertCapture(t *testing.T) {
	// assert composes with val, err = capture.
	funcs := compileStd(t, `func f(n: number) -> string {
        var _ok
        var err: error
        _ok, err = assert(n > 0, "nope")
        return err.message
    }`)
	wantStr(t, call(t, funcs, "f", num(-1)), "nope")
}

func TestAssertMessageIsLazy(t *testing.T) {
	// The message is evaluated only on failure: a passing assertion must not
	// evaluate it, so a message that would raise is harmless.
	var calls int
	recorder := function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.String),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			calls++
			return cty.StringVal("msg"), nil
		},
	})
	funcs := compileStdWith(t, `func f(n: number) -> bool {
        return assert(n > 0, record())
    }`, map[string]function.Function{"record": recorder})
	call(t, funcs, "f", num(5))
	if calls != 0 {
		t.Fatalf("message evaluated %d times on success, want 0", calls)
	}
}

func TestAssertConditionErrorPropagates(t *testing.T) {
	// A structured error raised while evaluating the condition surfaces with its
	// attributes intact — it is not masked as an assertion failure.
	funcs := compileStd(t, `func f() -> number {
        try {
            assert(error({ message = "boom", code = 503 }) == null)
            return 0
        } catch e {
            return e.code
        }
    }`)
	wantNum(t, call(t, funcs, "f"), 503)
}

func TestAssertTooManyArgs(t *testing.T) {
	funcs := compileStd(t, `func f() -> bool { return assert(true, "a", "b") }`)
	callErr(t, funcs, "f")
}
