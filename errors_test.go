package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

func TestParseTryCatchFinally(t *testing.T) {
	parse(t, `func f(ctx) {
        try {
            var r = work()
        } catch err {
            log(err.message)
        } finally {
            cleanup()
        }
    }`)
}

func TestParseTryRequiresCatchOrFinally(t *testing.T) {
	parseErr(t, "func f() { try { work() } }")
}

func TestThrowCaught(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> string {
        try {
            throw "boom"
        } catch err {
            return err.message
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "boom")
}

func TestThrowObjectCaught(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        try {
            throw { message = "bad", code = 422 }
        } catch err {
            return err.code
        }
    }`)
	wantNum(t, call(t, funcs, "f"), 422)
}

func TestThrowBarePayloadValue(t *testing.T) {
	// Throwing a non-string/non-object value wraps it so .value recovers it.
	funcs := compileFuncs(t, `func f() -> number {
        try { throw 42 } catch e { return e.value }
    }`)
	wantNum(t, call(t, funcs, "f"), 42)
}

func TestThrowStringHasNoValue(t *testing.T) {
	// A string throw carries no value attribute; accessing it is an error.
	funcs := compileFuncs(t, `func f() -> number {
        try { throw "x" } catch e { return e.value }
    }`)
	callErr(t, funcs, "f")
}

func TestUncaughtPropagates(t *testing.T) {
	funcs := compileFuncs(t, `func f() { throw "nope" }`)
	fn := funcs["f"]
	_, err := fn.Call(nil)
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected error mentioning 'nope', got %v", err)
	}
}

func TestExpressionErrorIsCatchable(t *testing.T) {
	// A failing type conversion inside try transfers to catch.
	funcs := compileFuncs(t, `func f() -> string {
        try {
            var n: number = "not a number"
            return "unreached"
        } catch err {
            return "caught"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "caught")
}

func TestFinallyRunsOnNormalAndError(t *testing.T) {
	// finally appends to an out-of-scope variable in both paths via a counter.
	src := `func f(fail: bool) -> number {
        var count = 0
        try {
            if fail { throw "x" }
        } catch err {
            count = count + 10
        } finally {
            count = count + 1
        }
        return count
    }`
	funcs := compileFuncs(t, src)
	wantNum(t, call(t, funcs, "f", cty.False), 1) // finally only
	wantNum(t, call(t, funcs, "f", cty.True), 11) // catch + finally
}

func TestCatchOptionalBinding(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> string {
        try {
            throw "x"
        } catch {
            return "handled"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "handled")
}

func TestDeferLIFOOrder(t *testing.T) {
	// A host "record" function appends its argument to a Go slice, letting us
	// observe execution order: body first, then defers in LIFO order.
	var order []string
	record := function.New(&function.Spec{
		Params: []function.Parameter{{Name: "s", Type: cty.String}},
		Type:   function.StaticReturnType(cty.DynamicPseudoType),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			order = append(order, args[0].AsString())
			return cty.NullVal(cty.DynamicPseudoType), nil
		},
	})

	src := `func f() {
        defer record("first")
        defer record("second")
        record("body")
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
	want := []string{"body", "second", "first"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("defer order = %v, want %v", order, want)
	}
}

func TestDeferRunsOnReturn(t *testing.T) {
	// A defer evaluating a known function runs without disturbing the return value.
	funcs := compileFuncs(t, `func f() -> number {
        defer length([1, 2, 3])
        return 7
    }`)
	wantNum(t, call(t, funcs, "f"), 7)
}

func TestFinallyOverridesReturn(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        try {
            return 1
        } finally {
            return 2
        }
    }`)
	wantNum(t, call(t, funcs, "f"), 2)
}
