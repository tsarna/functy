package functy

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestErrorTypeAcceptsCaughtError(t *testing.T) {
	// A caught error is error-shaped, so it satisfies `var e: error`.
	funcs := compileFuncs(t, `func f() -> string {
        try {
            throw { message = "boom", code = 422 }
        } catch err {
            var e: error = err
            return e.message
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "boom")
}

func TestErrorTypeRejectsNonError(t *testing.T) {
	// Assigning a non-error value to an error-typed var fails (a catchable error).
	funcs := compileFuncs(t, `func f() {
        var e: error = 42
        return e
    }`)
	callErr(t, funcs, "f")
}

func TestErrorTypeNullDefault(t *testing.T) {
	// A typed error var with no initializer is null; null satisfies the type.
	funcs := compileFuncs(t, `func f() -> bool {
        var e: error
        return e == null
    }`)
	if !call(t, funcs, "f").RawEquals(cty.True) {
		t.Fatalf("an uninitialized error var should be null")
	}
}

func TestErrorTypeParamAndReturn(t *testing.T) {
	funcs := compileFuncs(t, `func msg(e: error) -> string {
        return e.message
    }`)
	errVal := cty.ObjectVal(map[string]cty.Value{
		"message": cty.StringVal("nope"),
		"value":   cty.NullVal(cty.DynamicPseudoType),
	})
	wantStr(t, call(t, funcs, "msg", errVal), "nope")

	// A plain object lacking message is rejected at the call boundary.
	callErr(t, funcs, "msg", cty.ObjectVal(map[string]cty.Value{"note": cty.StringVal("x")}))
}

func TestErrorTypeIsReserved(t *testing.T) {
	parseErr(t, "type error = number")
}

func TestErrorTypeNotNestable(t *testing.T) {
	// error is an open (predicate) type, so it cannot nest (yet), like host types.
	parseErr(t, "func f(es: list(error)) { return es }")
}
