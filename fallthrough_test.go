package functy

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestFallthroughValueSwitch(t *testing.T) {
	// case 1 falls through into case 2's body; case 3 does not run.
	funcs := compileFuncs(t, `func f(n: number) -> string {
        var out = ""
        switch n {
        case 1:
            out = "${out}a"
            fallthrough
        case 2:
            out = "${out}b"
        case 3:
            out = "${out}c"
        }
        return out
    }`)
	wantStr(t, call(t, funcs, "f", num(1)), "ab") // 1 -> falls into 2
	wantStr(t, call(t, funcs, "f", num(2)), "b")  // 2 alone
	wantStr(t, call(t, funcs, "f", num(3)), "c")  // 3 alone
}

func TestFallthroughChain(t *testing.T) {
	// Consecutive fallthroughs cascade through several clauses.
	funcs := compileFuncs(t, `func f(n: number) -> string {
        var out = ""
        switch n {
        case 1:
            out = "${out}1"
            fallthrough
        case 2:
            out = "${out}2"
            fallthrough
        case 3:
            out = "${out}3"
        }
        return out
    }`)
	wantStr(t, call(t, funcs, "f", num(1)), "123")
	wantStr(t, call(t, funcs, "f", num(2)), "23")
	wantStr(t, call(t, funcs, "f", num(3)), "3")
}

func TestFallthroughIntoDefault(t *testing.T) {
	// A case may fall through into a following default clause.
	funcs := compileFuncs(t, `func f(n: number) -> string {
        var out = ""
        switch n {
        case 1:
            out = "${out}one"
            fallthrough
        default:
            out = "${out}/def"
        }
        return out
    }`)
	wantStr(t, call(t, funcs, "f", num(1)), "one/def")
	wantStr(t, call(t, funcs, "f", num(9)), "/def") // unmatched -> default only
}

func TestFallthroughFromDefaultNotLast(t *testing.T) {
	// default need not be last; it can itself fall through into the next clause.
	funcs := compileFuncs(t, `func f(n: number) -> string {
        var out = ""
        switch n {
        case 1:
            out = "one"
        default:
            out = "def"
            fallthrough
        case 2:
            out = "${out}/two"
        }
        return out
    }`)
	wantStr(t, call(t, funcs, "f", num(9)), "def/two") // no match -> default -> case 2
	wantStr(t, call(t, funcs, "f", num(1)), "one")     // case 1, no fallthrough
	wantStr(t, call(t, funcs, "f", num(2)), "/two")    // case 2 directly
}

func TestFallthroughExprlessSwitch(t *testing.T) {
	funcs := compileFuncs(t, `func f(n: number) -> string {
        var out = ""
        switch {
        case n > 0:
            out = "${out}pos"
            fallthrough
        case n > -100:
            out = "${out}/big"
        }
        return out
    }`)
	wantStr(t, call(t, funcs, "f", num(5)), "pos/big")
	wantStr(t, call(t, funcs, "f", cty.NumberIntVal(-5)), "/big")
}

func TestFallthroughInLastClauseIsError(t *testing.T) {
	parseErr(t, `func f(n: number) {
        switch n {
        case 1:
            fallthrough
        }
    }`)
	// Also when the last clause is the default.
	parseErr(t, `func f(n: number) {
        switch n {
        case 1:
            return
        default:
            fallthrough
        }
    }`)
}

func TestFallthroughNotFinalStatementIsError(t *testing.T) {
	parseErr(t, `func f(n: number) {
        switch n {
        case 1:
            fallthrough
            var x = 1
        case 2:
        }
    }`)
}

func TestFallthroughNestedIsError(t *testing.T) {
	// Go-style: fallthrough may not be nested inside another statement.
	parseErr(t, `func f(n: number) {
        switch n {
        case 1:
            if n > 0 { fallthrough }
        case 2:
        }
    }`)
}

func TestFallthroughOutsideSwitchIsError(t *testing.T) {
	parseErr(t, `func f() { fallthrough }`)
}
