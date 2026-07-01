package functy

import "testing"

func TestThrowCarriesRange(t *testing.T) {
	// throw "boom" is on line 3 of this source; the parse filename is "test".
	funcs := compileFuncs(t, `func f() -> string {
        try {
            throw "boom"
        } catch e {
            return "${e.message}@${e.range.filename}:${e.range.start.line}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "boom@test:3")
}

func TestObjectThrowGetsRange(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> string {
        try {
            throw { message = "bad", code = 422 }
        } catch e {
            return "${e.code}/${e.range == null ? "none" : "have"}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "422/have")
}

func TestRethrowPreservesRange(t *testing.T) {
	// inner()'s throw is on line 2; a rethrow must keep that origin, not restamp
	// to the rethrow site.
	funcs := compileFuncs(t, `func inner() -> string {
        throw "origin"
    }
    func f() -> string {
        try {
            try {
                return inner()
            } catch e {
                throw e
            }
        } catch e {
            return "${e.range.filename}:${e.range.start.line}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "test:2")
}

func TestEvalFailureCarriesRange(t *testing.T) {
	// A type-conversion failure is caught with a non-null range.
	funcs := compileFuncs(t, `func f() -> string {
        try {
            var n: number = "not a number"
            return "unreached"
        } catch e {
            return e.range == null ? "null" : "have"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "have")
}

func TestCrossCallRangeSurvives(t *testing.T) {
	// callee()'s throw is on line 2; the range points at the callee across the
	// call boundary.
	funcs := compileFuncs(t, `func callee() -> string {
        throw { message = "deep", code = 500 }
    }
    func f() -> string {
        try {
            return callee()
        } catch e {
            return "${e.code}@${e.range.start.line}"
        }
    }`)
	wantStr(t, call(t, funcs, "f"), "500@2")
}

func TestCaptureAssignPreservesStructure(t *testing.T) {
	// `val, err = callee()` recovers the structured error (with range) like
	// try/catch, rather than flattening it — callee throws on line 2.
	funcs := compileFuncs(t, `func callee() -> string {
        throw { message = "boom", code = 409 }
    }
    func f() -> string {
        var v: string
        var err: error
        v, err = callee()
        return "${err.code}@${err.range.start.line}"
    }`)
	wantStr(t, call(t, funcs, "f"), "409@2")
}
