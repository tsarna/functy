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

func TestAssertDetailString(t *testing.T) {
	// A failed assertion renders the referenced variables into a detail string.
	funcs := compileStd(t, `func f(n: number) -> string {
        try {
            assert(n > 0)
            return "ok"
        } catch e {
            return e.detail
        }
    }`)
	wantStr(t, call(t, funcs, "f", cty.NumberIntVal(-3)), "n = -3")
}

func TestAssertStructuredOperand(t *testing.T) {
	// The operand carries the raw value, so a catch clause can inspect it.
	funcs := compileStd(t, `func f(n: number) -> string {
        try {
            assert(n > 0)
            return "ok"
        } catch e {
            return "${e.operands[0].name}=${e.operands[0].value}"
        }
    }`)
	wantStr(t, call(t, funcs, "f", cty.NumberIntVal(-3)), "n=-3")
}

func TestAssertOperandRawValue(t *testing.T) {
	// The operand value is a real number (not a string), usable in arithmetic.
	funcs := compileStd(t, `func f(n: number) -> number {
        try {
            assert(n > 0)
            return 0
        } catch e {
            return e.operands[0].value + 1
        }
    }`)
	wantNum(t, call(t, funcs, "f", cty.NumberIntVal(-3)), -2)
}

func TestAssertMultipleOperands(t *testing.T) {
	funcs := compileStd(t, `func f(a: number, b: number) -> string {
        try {
            assert(a > b)
            return "ok"
        } catch e {
            return e.detail
        }
    }`)
	wantStr(t, call(t, funcs, "f", num(1), num(5)), "a = 1, b = 5")
}

func TestAssertDedupesOperand(t *testing.T) {
	// A variable referenced twice appears once.
	funcs := compileStd(t, `func f(n: number) -> number {
        try {
            assert(n > 0 && n < 10)
            return -1
        } catch e {
            return length(e.operands)
        }
    }`)
	wantNum(t, call(t, funcs, "f", cty.NumberIntVal(-3)), 1)
}

func TestAssertOperandsWithCustomMessage(t *testing.T) {
	// A custom message stays the headline; operands are attached alongside.
	funcs := compileStd(t, `func f(n: number) -> string {
        try {
            assert(n > 0, "bad")
            return "ok"
        } catch e {
            return "${e.message}: ${e.detail}"
        }
    }`)
	wantStr(t, call(t, funcs, "f", cty.NumberIntVal(-3)), "bad: n = -3")
}

func TestAssertTraversalName(t *testing.T) {
	// A nested reference renders its dotted path as the operand name.
	funcs := compileStd(t, `func f(o: object({ k = number })) -> string {
        try {
            assert(o.k > 0)
            return "ok"
        } catch e {
            return e.operands[0].name
        }
    }`)
	arg := cty.ObjectVal(map[string]cty.Value{"k": cty.NumberIntVal(-1)})
	wantStr(t, call(t, funcs, "f", arg), "o.k")
}

func TestAssertNoVariablesNoOperands(t *testing.T) {
	// A constant condition references nothing, so no operands/detail are attached.
	funcs := compileStd(t, `func f() -> string {
        try {
            assert(1 > 2)
            return "ok"
        } catch e {
            return "${e.message}/${can(e.detail)}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "assertion failed/false")
}

func TestAssertOperandGatheringIsSideEffectFree(t *testing.T) {
	// A function call in the condition is evaluated once (to test the condition)
	// and never re-run to gather operands: expr.Variables() sees no variables here,
	// so the recorder is called exactly once.
	var calls int
	recorder := function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.Number),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			calls++
			return cty.NumberIntVal(-1), nil
		},
	})
	funcs := compileStdWith(t, `func f() -> string {
        try {
            assert(rec() > 0)
            return "ok"
        } catch e {
            return e.message
        }
    }`, map[string]function.Function{"rec": recorder})
	call(t, funcs, "f")
	if calls != 1 {
		t.Fatalf("condition function evaluated %d times, want 1", calls)
	}
}
