package functy

import (
	"fmt"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestCatchStructuralTypeFilter(t *testing.T) {
	// A structural object filter matches an error carrying that attribute, and the
	// raw binding still exposes the error's other attributes (message).
	funcs := compileWith(t, NewParser(), `func f() -> string {
        try {
            throw { message = "boom", code = 422 }
        } catch e: object({ code = number }) {
            return "${e.message}/${e.code}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "boom/422")
}

func TestCatchTypeFilterFallsThrough(t *testing.T) {
	// An error lacking the filtered attribute skips the typed clause (a missing
	// attribute cannot be converted) and reaches the catch-all.
	funcs := compileWith(t, NewParser(), `func f() -> string {
        try {
            throw { message = "x" }
        } catch e: object({ code = number }) {
            return "typed"
        } catch e {
            return "fallthrough"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "fallthrough")
}

func TestCatchGuardFirstMatchWins(t *testing.T) {
	funcs := compileWith(t, NewParser(), `func f(code: number) -> string {
        try {
            throw { message = "http", code = code }
        } catch e if e.code == 404 {
            return "missing"
        } catch e if e.code >= 500 {
            return "server"
        } catch e {
            return "other ${e.code}"
        }
    }`)
	wantStr(t, call(t, funcs, "f", num(404)), "missing")
	wantStr(t, call(t, funcs, "f", num(503)), "server")
	wantStr(t, call(t, funcs, "f", num(418)), "other 418")
}

func TestCatchTypeAndGuard(t *testing.T) {
	// The type filter gates the shape so the guard only runs when `code` exists.
	funcs := compileWith(t, NewParser(), `func f(hasCode: bool) -> string {
        try {
            if hasCode { throw { message = "x", code = 500 } }
            throw { message = "plain" }
        } catch e: object({ code = number }) if e.code >= 500 {
            return "server"
        } catch e {
            return "plain:${e.message}"
        }
    }`)
	wantStr(t, call(t, funcs, "f", cty.True), "server")
	wantStr(t, call(t, funcs, "f", cty.False), "plain:plain")
}

func TestCatchHostOpenType(t *testing.T) {
	// A host-registered open type acts as an error category.
	isTimeout := func(v cty.Value) error {
		ty := v.Type()
		if ty.IsObjectType() && ty.HasAttribute("kind") {
			k := v.GetAttr("kind")
			if !k.IsNull() && k.Type() == cty.String && k.AsString() == "timeout" {
				return nil
			}
		}
		return fmt.Errorf("not a timeout")
	}
	p := NewParser().RegisterOpenType("timeout", isTimeout)
	funcs := compileWith(t, p, `func f(kind: string) -> string {
        try {
            throw { message = "slow", kind = kind }
        } catch e: timeout {
            return "retry"
        } catch e {
            return "fail"
        }
    }`)
	wantStr(t, call(t, funcs, "f", cty.StringVal("timeout")), "retry")
	wantStr(t, call(t, funcs, "f", cty.StringVal("other")), "fail")
}

func TestCatchUnmatchedReRaises(t *testing.T) {
	// An error matching no clause propagates to an enclosing try.
	funcs := compileWith(t, NewParser(), `func f() -> string {
        try {
            try {
                throw { message = "x" }
            } catch e: object({ code = number }) {
                return "inner"
            }
        } catch e {
            return "outer:${e.message}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "outer:x")
}

func TestCatchFinallyRunsWhenUnmatched(t *testing.T) {
	// finally runs even when no clause matches, before the error re-raises.
	funcs := compileWith(t, NewParser(), `func f() -> string {
        var log = ""
        try {
            try {
                throw { message = "x" }
            } catch e: object({ code = number }) {
                log = "${log}caught"
            } finally {
                log = "${log}fin"
            }
        } catch e {
            log = "${log}/outer"
        }
        return log
    }`)
	wantStr(t, call(t, funcs, "f"), "fin/outer")
}

func TestCatchRethrow(t *testing.T) {
	funcs := compileWith(t, NewParser(), `func f() -> string {
        try {
            try {
                throw { message = "boom", code = 500 }
            } catch e if e.code == 404 {
                return "handled"
            } catch e {
                throw e
            }
        } catch e {
            return "rethrown:${e.message}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "rethrown:boom")
}

func TestCatchGuardErrorPropagates(t *testing.T) {
	// A guard that itself errors (here, reading a missing attribute) surfaces an
	// error rather than silently skipping the clause.
	funcs := compileWith(t, NewParser(), `func f() {
        try {
            throw { message = "x" }
        } catch e if e.code == 404 {
            return "missing"
        }
    }`)
	callErr(t, funcs, "f")
}

func TestCatchUnnamedGuarded(t *testing.T) {
	// `catch if cond` is an unnamed, guarded clause (`if` is not taken as a name).
	funcs := compileWith(t, NewParser(), `func f(n: number) -> string {
        var flag = n > 0
        try {
            throw "boom"
        } catch if flag {
            return "guarded"
        } catch {
            return "other"
        }
    }`)
	wantStr(t, call(t, funcs, "f", num(1)), "guarded")
	wantStr(t, call(t, funcs, "f", num(-1)), "other")
}

func TestCatchAllMustBeLast(t *testing.T) {
	parseErr(t, `func f() {
        try {
            throw "x"
        } catch e {
            return "all"
        } catch e: object({ code = number }) {
            return "typed"
        }
    }`)
}

func TestCatchBadTypeFilter(t *testing.T) {
	parseErr(t, `func f() {
        try { throw "x" } catch e: bogustype { return "no" }
    }`)
}
