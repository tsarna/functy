package functy

import (
	"reflect"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// compileStd parses and compiles src with the functy stdlib (Stdlib + StdlibExtras)
// plus the test stdlib merged into the eval context.
func compileStd(t *testing.T, src string) map[string]function.Function {
	t.Helper()
	return compileStdWith(t, src, nil)
}

// compileStdWith is compileStd with additional host functions (e.g. a recorder).
func compileStdWith(t *testing.T, src string, extra map[string]function.Function) map[string]function.Function {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test")
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
	for k, v := range extra {
		all[k] = v
	}
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	return funcs
}

func TestCondSelectsBranch(t *testing.T) {
	funcs := compileStd(t, `func f(n: number) -> string {
        return cond(n > 0, "pos", n < 0, "neg", "zero")
    }`)
	wantStr(t, call(t, funcs, "f", num(5)), "pos")
	wantStr(t, call(t, funcs, "f", cty.NumberIntVal(-5)), "neg")
	wantStr(t, call(t, funcs, "f", num(0)), "zero")
}

func TestCondIsLazy(t *testing.T) {
	// The unselected branch (which would raise) is never evaluated.
	funcs := compileStd(t, `func f() -> number {
        return cond(true, 1, error("boom"))
    }`)
	wantNum(t, call(t, funcs, "f"), 1)
}

func TestCondSingleEval(t *testing.T) {
	var calls int
	recorder := function.New(&function.Spec{
		Params: []function.Parameter{},
		Type:   function.StaticReturnType(cty.Number),
		Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
			calls++
			return cty.NumberIntVal(7), nil
		},
	})
	funcs := compileStdWith(t, `func f() -> number {
        return cond(true, record(), 0)
    }`, map[string]function.Function{"record": recorder})
	wantNum(t, call(t, funcs, "f"), 7)
	if calls != 1 {
		t.Fatalf("selected branch evaluated %d times, want 1", calls)
	}
}

func TestCondRequiresOddArgs(t *testing.T) {
	// Even arg count (no trailing else) is an error; cond(c, r, c, r) has no else.
	funcs := compileStd(t, `func f() -> number { return cond(true, 1, false, 2) }`)
	callErr(t, funcs, "f")
}

func TestSwitchDispatch(t *testing.T) {
	funcs := compileStd(t, `func f(n: number) -> string {
        return switch(n, 1, "one", 2, "two", "other")
    }`)
	wantStr(t, call(t, funcs, "f", num(1)), "one")
	wantStr(t, call(t, funcs, "f", num(2)), "two")
	wantStr(t, call(t, funcs, "f", num(9)), "other")
}

func TestSwitchNoMatchNoDefaultErrors(t *testing.T) {
	funcs := compileStd(t, `func f(n: number) -> string {
        return switch(n, 1, "one", 2, "two")
    }`)
	callErr(t, funcs, "f", num(9))
}

func TestTypeof(t *testing.T) {
	funcs := compileStd(t, `func f(x) -> string { return typeof(x) }`)
	wantStr(t, call(t, funcs, "f", cty.StringVal("x")), "string")
	wantStr(t, call(t, funcs, "f", num(1)), "number")
	wantStr(t, call(t, funcs, "f", cty.True), "bool")
	// Structural types render in functy's annotation grammar (lossless, round-trips).
	wantStr(t, call(t, funcs, "f", cty.ListVal([]cty.Value{cty.StringVal("a")})), "list(string)")
	wantStr(t, call(t, funcs, "f", cty.ObjectVal(map[string]cty.Value{
		"a": num(1), "b": cty.StringVal("x"),
	})), "object({ a = number, b = string })")
	wantStr(t, call(t, funcs, "f", cty.TupleVal([]cty.Value{cty.StringVal("a"), num(1)})), "tuple([string, number])")
}

func TestTypeofRoundTrips(t *testing.T) {
	// typeof's output is valid functy type-annotation syntax: it parses back to the
	// same type through the resolver.
	r := NewTypeResolver()
	for _, s := range []string{
		"string", "number", "bool", "any",
		"list(string)", "set(number)", "map(bool)",
		"object({ a = number, b = string })", "tuple([string, number])",
	} {
		tc, diags := r.ParseType([]byte(s), "t")
		if diags.HasErrors() {
			t.Fatalf("ParseType(%q): %s", s, diags.Error())
		}
		if got := typeString(tc.Cty()); got != s {
			t.Fatalf("round-trip: %q -> %q", s, got)
		}
	}
}

type fakeCapsuleVal struct{}

func TestTypeofRichObject(t *testing.T) {
	// A rich object (a cty.Object carrying its capsule under _capsule/_ctx) is named
	// by that capsule, not by its internal structure.
	bytesCap := cty.Capsule("bytes", reflect.TypeOf(fakeCapsuleVal{}))
	ctxCap := cty.Capsule("ctx", reflect.TypeOf(fakeCapsuleVal{}))

	richBytes := cty.ObjectVal(map[string]cty.Value{
		"_capsule":     cty.CapsuleVal(bytesCap, &fakeCapsuleVal{}),
		"content_type": cty.StringVal("text/plain"),
	})
	richCtx := cty.ObjectVal(map[string]cty.Value{
		"_ctx":    cty.CapsuleVal(ctxCap, &fakeCapsuleVal{}),
		"request": cty.StringVal("GET /"),
	})

	tf := compileStd(t, `func f(x) -> string { return typeof(x) }`)
	wantStr(t, call(t, tf, "f", richBytes), "bytes")
	wantStr(t, call(t, tf, "f", richCtx), "ctx")

	kf := compileStd(t, `func g(x) -> string { return typekind(x) }`)
	wantStr(t, call(t, kf, "g", richBytes), "bytes")
	wantStr(t, call(t, kf, "g", richCtx), "ctx")
}

func TestTypekind(t *testing.T) {
	funcs := compileStd(t, `func f(x) -> string { return typekind(x) }`)
	wantStr(t, call(t, funcs, "f", cty.StringVal("x")), "string")
	wantStr(t, call(t, funcs, "f", num(1)), "number")
	// The kind drops element/attribute detail — good for dispatch.
	wantStr(t, call(t, funcs, "f", cty.ListVal([]cty.Value{cty.StringVal("a")})), "list")
	wantStr(t, call(t, funcs, "f", cty.ObjectVal(map[string]cty.Value{"a": num(1)})), "object")
	wantStr(t, call(t, funcs, "f", cty.TupleVal([]cty.Value{num(1)})), "tuple")
}

func TestErrorFuncCaught(t *testing.T) {
	funcs := compileStd(t, `func f() -> string {
        try {
            return error("bad input")
        } catch e {
            return "${e.message}@${e.range.start.line}"
        }
    }`)
	// error("bad input") is on line 3.
	wantStr(t, call(t, funcs, "f"), "bad input@3")
}

func TestErrorFuncObject(t *testing.T) {
	funcs := compileStd(t, `func f() -> number {
        try {
            return error({ message = "nope", code = 409 })
        } catch e {
            return e.code
        }
    }`)
	wantNum(t, call(t, funcs, "f"), 409)
}

func TestErrorFuncCapture(t *testing.T) {
	funcs := compileStd(t, `func f() -> string {
        var v: string
        var err: error
        v, err = error("captured")
        return err.message
    }`)
	wantStr(t, call(t, funcs, "f"), "captured")
}

func TestStructuredErrorSurvivesCondBranch(t *testing.T) {
	// A structured error raised inside a cond result branch keeps its attributes.
	funcs := compileStd(t, `func f() -> number {
        try {
            return cond(true, error({ message = "x", code = 503 }), 0)
        } catch e {
            return e.code
        }
    }`)
	wantNum(t, call(t, funcs, "f"), 503)
}

func TestTryReturnsFirstOk(t *testing.T) {
	funcs := compileStd(t, `func f() -> string {
        return try(error("nope"), "fallback")
    }`)
	wantStr(t, call(t, funcs, "f"), "fallback")
}

func TestCanReportsFailure(t *testing.T) {
	funcs := compileStd(t, `func f() -> string {
        return "${can(error("x"))}/${can("ok")}"
    }`)
	wantStr(t, call(t, funcs, "f"), "false/true")
}
