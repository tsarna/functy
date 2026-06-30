package functy

import "testing"

func TestBreakLabelExitsOuter(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        count := 0
        outer: for i in [1, 2, 3] {
            for j in [1, 2, 3] {
                count = count + 1
                if i == 2 && j == 2 { break outer }
            }
        }
        return count
    }`)
	wantNum(t, call(t, funcs, "f"), 5) // 3 (i=1) + 2 (i=2: j=1,2 then break)
}

func TestContinueLabelSkipsToOuter(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        count := 0
        outer: for i in [1, 2, 3] {
            for j in [1, 2, 3] {
                if j == 2 { continue outer }
                count = count + 1
            }
        }
        return count
    }`)
	wantNum(t, call(t, funcs, "f"), 3) // each i counts once (j=1) then jumps out
}

func TestUnlabeledBreakStillInnermost(t *testing.T) {
	// An unlabeled break breaks only the inner loop even when the outer is labeled.
	funcs := compileFuncs(t, `func f() -> number {
        count := 0
        outer: for i in [1, 2] {
            for j in [1, 2, 3] {
                if j == 2 { break }
                count = count + 1
            }
            count = count + 10
        }
        return count
    }`)
	wantNum(t, call(t, funcs, "f"), 22) // each i: 1 (j=1) + 10
}

func TestContinueLabelRunsOuterPost(t *testing.T) {
	// A labeled continue into a three-clause loop runs that loop's post clause.
	funcs := compileFuncs(t, `func f() -> number {
        total := 0
        outer: for i := 0; i < 3; i = i + 1 {
            for k := 0; k < 3; k = k + 1 {
                if k == 1 { continue outer }
                total = total + 1
            }
        }
        return total
    }`)
	wantNum(t, call(t, funcs, "f"), 3) // i advances via post each time
}

func TestBreakLabelWhile(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        i := 0
        seen := 0
        outer: while i < 5 {
            i = i + 1
            for j in [1, 2] {
                seen = seen + 1
                if i == 2 { break outer }
            }
        }
        return seen
    }`)
	wantNum(t, call(t, funcs, "f"), 3) // i=1: j=1,2 (seen=2); i=2: j=1 (seen=3) -> break outer
}

func TestBreakLabelRunsFinally(t *testing.T) {
	// finally blocks between the break and its target loop still run as the
	// labeled break unwinds.
	funcs := compileFuncs(t, `func f() -> string {
        log := ""
        outer: for i in [1, 2] {
            for j in [1, 2] {
                try {
                    if i == 1 && j == 1 { break outer }
                } finally {
                    log = "${log}f"
                }
                log = "${log}b"
            }
        }
        return log
    }`)
	wantStr(t, call(t, funcs, "f"), "f") // finally runs once, then break unwinds out
}

func TestParseLabeledLoop(t *testing.T) {
	fn := onlyFunc(t, `func f() {
        outer: for i in [1] {
            break outer
        }
    }`)
	loop := fn.Body[0].(*For)
	if loop.Label != "outer" {
		t.Fatalf("expected loop label %q, got %q", "outer", loop.Label)
	}
}

func TestLabelErrors(t *testing.T) {
	// Unknown label.
	parseErr(t, `func f() { for i in [1] { break nope } }`)
	parseErr(t, `func f() { for i in [1] { continue nope } }`)
	// Label on a non-loop statement.
	parseErr(t, `func f() { lbl: var x = 1 }`)
	// Duplicate label among enclosing loops.
	parseErr(t, `func f() { a: for i in [1] { a: for j in [1] { break a } } }`)
	// A sibling loop's label is out of scope.
	parseErr(t, `func f() {
        a: for i in [1] { }
        for j in [1] { break a }
    }`)
	// break/continue still require a loop at all.
	parseErr(t, `func f() { break }`)
	parseErr(t, `func f() { continue outer }`)
}
