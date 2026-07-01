package functy

import (
	"errors"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

func TestCrossCallStructuredTypeFilter(t *testing.T) {
	// A structured error thrown by a callee keeps its attributes when caught by a
	// typed clause in the caller (the case that flattened before).
	funcs := compileFuncs(t, `func fetch(status: number) -> string {
        if status >= 400 { throw { message = "http error", code = status } }
        return "ok"
    }
    func describe(status: number) -> string {
        try {
            return fetch(status)
        } catch e: object({ code = number }) {
            return "http ${e.code}: ${e.message}"
        }
    }`)
	wantStr(t, call(t, funcs, "describe", num(503)), "http 503: http error")
}

func TestCrossCallGuard(t *testing.T) {
	funcs := compileFuncs(t, `func fetch(status: number) -> string {
        if status == 0 { throw { message = "refused", kind = "network" } }
        return "ok"
    }
    func describe(status: number) -> string {
        try {
            return fetch(status)
        } catch e if e.kind == "network" {
            return "network: ${e.message}"
        } catch e {
            return "other"
        }
    }`)
	wantStr(t, call(t, funcs, "describe", num(0)), "network: refused")
}

func TestCrossCallMultiLevel(t *testing.T) {
	// Structure survives two boundaries: c throws, b calls c, a calls b.
	funcs := compileFuncs(t, `func c() -> string { throw { message = "deep", code = 418 } }
    func b() -> string { return c() }
    func a() -> string {
        try {
            return b()
        } catch e: object({ code = number }) {
            return "caught ${e.code}: ${e.message}"
        }
    }`)
	wantStr(t, call(t, funcs, "a"), "caught 418: deep")
}

func TestCrossCallNonThrowStillFlattens(t *testing.T) {
	// A callee whose failure is a genuine eval error (not a throw) is still caught
	// as { message, value = null }, so a shape filter does not match it.
	funcs := compileFuncs(t, `func bad() -> number {
        var n: number = "not a number"
        return n
    }
    func f() -> string {
        try {
            return "${bad()}"
        } catch e: object({ code = number }) {
            return "typed"
        } catch e {
            return "flat"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "flat")
}

func TestCrossCallRethrow(t *testing.T) {
	funcs := compileFuncs(t, `func fetch() -> string { throw { message = "boom", code = 500 } }
    func mid() -> string {
        try {
            return fetch()
        } catch e if e.code == 404 {
            return "handled"
        }
    }
    func top() -> string {
        try {
            return mid()
        } catch e: object({ code = number }) {
            return "top ${e.code}: ${e.message}"
        }
    }`)
	// mid's clause does not match (code 500), so the error re-raises structured and
	// top catches it by shape.
	wantStr(t, call(t, funcs, "top"), "top 500: boom")
}

func TestThrownErrorGoHostOptIn(t *testing.T) {
	// A Go host calling a throwing function directly recovers the structured error
	// via errors.As, and Error() renders the message for a generic caller.
	res, diags := NewParser().Parse([]byte(`func f() { throw { message = "boom", code = 422 } }`), "t")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	var ctx *hcl.EvalContext
	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	if cdiags.HasErrors() {
		t.Fatalf("compile: %s", cdiags.Error())
	}
	ctx = &hcl.EvalContext{Functions: funcs, Variables: map[string]cty.Value{}}

	_, err := funcs["f"].Call(nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	var te *ThrownError
	if !errors.As(err, &te) {
		t.Fatalf("expected a *ThrownError, got %T: %v", err, err)
	}
	if te.Value.GetAttr("code").AsBigFloat().Cmp(cty.NumberIntVal(422).AsBigFloat()) != 0 {
		t.Fatalf("recovered value missing code=422: %#v", te.Value)
	}
	if err.Error() != "boom" {
		t.Fatalf("Error() = %q, want %q", err.Error(), "boom")
	}
}
