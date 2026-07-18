package functy

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// compileFuncsWithVars is like compileFuncs but seeds the late-bound host eval
// context with the given variables, so a function body can reference a
// host-provided global (e.g. an unknown value) by name.
func compileFuncsWithVars(t *testing.T, src string, vars map[string]cty.Value) map[string]function.Function {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse errors:\n%s", diags.Error())
	}
	var ctx *hcl.EvalContext
	evalCtxFn := func() *hcl.EvalContext { return ctx }
	funcs, diags := res.Compile(evalCtxFn)
	if diags.HasErrors() {
		t.Fatalf("compile errors:\n%s", diags.Error())
	}
	all := testStdlib()
	for k, v := range funcs {
		all[k] = v
	}
	ctx = &hcl.EvalContext{Functions: all, Variables: vars}
	return funcs
}

// callCleanErr asserts that calling name errors but does NOT leak a Go panic
// (cty's Function.Call recovers a panic into an error whose text begins with
// "panic in function implementation"). It returns the error for further checks.
func callCleanErr(t *testing.T, funcs map[string]function.Function, name string, args ...cty.Value) error {
	t.Helper()
	fn, ok := funcs[name]
	if !ok {
		t.Fatalf("function %q not found", name)
	}
	_, err := fn.Call(args)
	if err == nil {
		t.Fatalf("expected error calling %q, got none", name)
	}
	if strings.Contains(err.Error(), "panic in function implementation") {
		t.Fatalf("calling %q leaked a panic instead of a clean diagnostic: %v", name, err)
	}
	return err
}

// TestCondBoolNonBoolean covers every interpreter condition site: a non-boolean
// condition value must yield a clean diagnostic rather than panicking in
// cty.Value.True.
func TestCondBoolNonBoolean(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"if", `func f() -> number { if 5 { return 1 } return 0 }`},
		{"if-string", `func f() -> number { if "x" { return 1 } return 0 }`},
		{"while", `func f() -> number { while "x" { return 1 } return 0 }`},
		{"for-clause", `func f() -> number { for var i = 0; 5; i = i { return 1 } return 0 }`},
		{"switch-exprless", `func f() -> number { switch { case 3: return 1 } return 0 }`},
		{"catch-guard", `func f() -> number {
			try {
				throw "boom"
			} catch e if 5 {
				return 1
			}
			return 0
		}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			funcs := compileFuncs(t, tc.src)
			callCleanErr(t, funcs, "f")
		})
	}
}

// TestCondBoolUnknown covers the same sites (plus the switch subject-match test)
// driven by an unknown condition value: they must return a clean error, never a
// panic. The unknown is injected as a host-provided global.
func TestCondBoolUnknown(t *testing.T) {
	vars := map[string]cty.Value{
		"flag":  cty.UnknownVal(cty.Bool),
		"thing": cty.UnknownVal(cty.Number),
	}
	cases := []struct {
		name string
		src  string
	}{
		{"if", `func f() -> number { if flag { return 1 } return 0 }`},
		{"while", `func f() -> number { while flag { return 1 } return 0 }`},
		{"for-clause", `func f() -> number { for var i = 0; flag; i = i { return 1 } return 0 }`},
		{"switch-exprless", `func f() -> number { switch { case flag: return 1 } return 0 }`},
		{"switch-subject", `func f() -> number { switch thing { case 3: return 1 } return 0 }`},
		{"catch-guard", `func f() -> number {
			try {
				throw "boom"
			} catch e if flag {
				return 1
			}
			return 0
		}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			funcs := compileFuncsWithVars(t, tc.src, vars)
			callCleanErr(t, funcs, "f")
		})
	}
}

// TestCondBoolStdlib covers the stdlib cond()/assert() builtins: a non-boolean
// or unknown condition must produce a clean error, not a panic.
func TestCondBoolStdlib(t *testing.T) {
	// assert and cond both convert.Convert to Bool first, so a non-bool already
	// yields a clean conversion error. The path that reached .True() (and thus
	// panicked) was the unknown Bool, so that is what we exercise here.
	vars := map[string]cty.Value{"flag": cty.UnknownVal(cty.Bool)}

	assertSrc := `func f() -> bool { return assert(flag) }`
	funcs := compileFuncsWithVars(t, assertSrc, vars)
	callCleanErr(t, funcs, "f")

	condSrc := `func f() -> number { return cond(flag, 1, 0) }`
	funcs = compileFuncsWithVars(t, condSrc, vars)
	callCleanErr(t, funcs, "f")
}
