package functy

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// testStdlib is a small set of cty stdlib functions used by the end-to-end
// tests, standing in for the host's function library.
func testStdlib() map[string]function.Function {
	return map[string]function.Function{
		"merge":  stdlib.MergeFunc,
		"upper":  stdlib.UpperFunc,
		"lower":  stdlib.LowerFunc,
		"length": stdlib.LengthFunc,
		"max":    stdlib.MaxFunc,
		"min":    stdlib.MinFunc,
	}
}

// compileFuncs parses and compiles src, wiring a late-bound eval context that
// contains the stdlib plus the compiled functy functions (so recursion and
// sibling calls work).
func compileFuncs(t *testing.T, src string) map[string]function.Function {
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
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}
	return funcs
}

func call(t *testing.T, funcs map[string]function.Function, name string, args ...cty.Value) cty.Value {
	t.Helper()
	fn, ok := funcs[name]
	if !ok {
		t.Fatalf("function %q not found", name)
	}
	v, err := fn.Call(args)
	if err != nil {
		t.Fatalf("calling %q: %v", name, err)
	}
	return v
}

func callErr(t *testing.T, funcs map[string]function.Function, name string, args ...cty.Value) {
	t.Helper()
	fn := funcs[name]
	if _, err := fn.Call(args); err == nil {
		t.Fatalf("expected error calling %q, got none", name)
	}
}

func num(i int) cty.Value { return cty.NumberIntVal(int64(i)) }

func wantNum(t *testing.T, got cty.Value, want int) {
	t.Helper()
	if !got.RawEquals(cty.NumberIntVal(int64(want))) {
		t.Fatalf("got %#v, want %d", got, want)
	}
}

func wantStr(t *testing.T, got cty.Value, want string) {
	t.Helper()
	if !got.RawEquals(cty.StringVal(want)) {
		t.Fatalf("got %#v, want %q", got, want)
	}
}

func TestAdd(t *testing.T) {
	funcs := compileFuncs(t, "func add(a: number, b: number) -> number { return a + b }")
	wantNum(t, call(t, funcs, "add", num(2), num(3)), 5)
}

func TestGreetDefaultAndTemplate(t *testing.T) {
	funcs := compileFuncs(t, `func greet(name: string = "world") -> string { return "hello ${name}" }`)
	wantStr(t, call(t, funcs, "greet"), "hello world")
	wantStr(t, call(t, funcs, "greet", cty.StringVal("alice")), "hello alice")
}

func TestClassify(t *testing.T) {
	funcs := compileFuncs(t, `func classify(n: number) -> string {
        if n > 0 {
            return "positive"
        } else if n < 0 {
            return "negative"
        } else {
            return "zero"
        }
    }`)
	wantStr(t, call(t, funcs, "classify", num(5)), "positive")
	wantStr(t, call(t, funcs, "classify", num(-5)), "negative")
	wantStr(t, call(t, funcs, "classify", num(0)), "zero")
}

func TestSumToThreeClauseFor(t *testing.T) {
	funcs := compileFuncs(t, `func sum_to(n: number) -> number {
        var total = 0
        for var i = 1; i <= n; i = i + 1 {
            total = total + i
        }
        return total
    }`)
	wantNum(t, call(t, funcs, "sum_to", num(5)), 15)
}

func TestSumListRange(t *testing.T) {
	funcs := compileFuncs(t, `func sum_list(items: list(number)) -> number {
        var total = 0
        for v in items {
            total = total + v
        }
        return total
    }`)
	got := call(t, funcs, "sum_list", cty.ListVal([]cty.Value{num(1), num(2), num(3), num(4)}))
	wantNum(t, got, 10)
}

func TestRangeKeyValue(t *testing.T) {
	funcs := compileFuncs(t, `func sum_keys(m) -> number {
        var total = 0
        for k, v in m {
            total = total + v
        }
        return total
    }`)
	m := cty.MapVal(map[string]cty.Value{"a": num(10), "b": num(20)})
	wantNum(t, call(t, funcs, "sum_keys", m), 30)
}

// Ranging over a list binds the element index as the key.
func TestRangeOverListUsesIndexKey(t *testing.T) {
	funcs := compileFuncs(t, `func sum_idx(items: list(number)) -> number {
        var total = 0
        for i, v in items { total = total + i }
        return total
    }`)
	items := cty.ListVal([]cty.Value{num(5), num(6), num(7), num(8)})
	wantNum(t, call(t, funcs, "sum_idx", items), 6) // indices 0+1+2+3
}

// A set has no key, so ranging over one binds a stable running counter as the key
// while the value is each element — regardless of the (deterministic) iteration
// order.
func TestRangeOverSetUsesCounterKey(t *testing.T) {
	funcs := compileFuncs(t, `func sum_keys(s: set(number)) -> number {
        var total = 0
        for i, v in s { total = total + i }
        return total
    }
    func sum_vals(s: set(number)) -> number {
        var total = 0
        for i, v in s { total = total + v }
        return total
    }`)
	s := cty.SetVal([]cty.Value{num(10), num(20), num(30)})
	wantNum(t, call(t, funcs, "sum_keys", s), 3)  // counter keys 0+1+2
	wantNum(t, call(t, funcs, "sum_vals", s), 60) // 10+20+30
}

func TestWhileBreakContinue(t *testing.T) {
	funcs := compileFuncs(t, `func count_evens(n: number) -> number {
        var c = 0
        var i = 0
        while i < n {
            i = i + 1
            if i % 2 == 1 { continue }
            c = c + 1
        }
        return c
    }`)
	wantNum(t, call(t, funcs, "count_evens", num(10)), 5)
}

func TestSwitch(t *testing.T) {
	funcs := compileFuncs(t, `func name_of(code: number) -> string {
        switch code {
        case 200, 201, 204:
            return "ok"
        case 404:
            return "missing"
        default:
            return "error"
        }
    }`)
	wantStr(t, call(t, funcs, "name_of", num(201)), "ok")
	wantStr(t, call(t, funcs, "name_of", num(404)), "missing")
	wantStr(t, call(t, funcs, "name_of", num(500)), "error")
}

func TestRecursion(t *testing.T) {
	funcs := compileFuncs(t, `func fact(n: number) -> number {
        if n <= 1 { return 1 }
        return n * fact(n - 1)
    }`)
	wantNum(t, call(t, funcs, "fact", num(5)), 120)
}

func TestVariadicTyped(t *testing.T) {
	funcs := compileFuncs(t, `func sum(*nums: number) -> number {
        var t = 0
        for n in nums { t = t + n }
        return t
    }`)
	wantNum(t, call(t, funcs, "sum", num(1), num(2), num(3), num(4)), 10)
	wantNum(t, call(t, funcs, "sum"), 0)
}

func TestTypedVarConversion(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        var c: number = 0
        c = "12"
        c = c + 1
        return c
    }`)
	wantNum(t, call(t, funcs, "f"), 13)
}

func TestTypedVarConversionFailure(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> number {
        var c: number = 0
        c = "nope"
        return c
    }`)
	callErr(t, funcs, "f")
}

func TestTypedNullDefault(t *testing.T) {
	funcs := compileFuncs(t, `func f() -> bool {
        var x: number
        return x == null
    }`)
	if !call(t, funcs, "f").RawEquals(cty.True) {
		t.Fatalf("expected typed var with no initializer to be null")
	}
}

func TestReturnTypeConversion(t *testing.T) {
	// "12" is converted to the declared number return type.
	funcs := compileFuncs(t, `func f() -> number { return "12" }`)
	wantNum(t, call(t, funcs, "f"), 12)
}

func TestFallOffReturnsNull(t *testing.T) {
	funcs := compileFuncs(t, `func f(x) { var y = x }`)
	if !call(t, funcs, "f", num(1)).IsNull() {
		t.Fatalf("falling off the end should return null")
	}
}

func TestAssignUndeclared(t *testing.T) {
	funcs := compileFuncs(t, `func f() { x = 1 }`)
	callErr(t, funcs, "f")
}

func TestBlockScope(t *testing.T) {
	// A var inside a bare block does not leak out.
	funcs := compileFuncs(t, `func f() -> number {
        var x = 1
        {
            var x = 99
            x = x + 1
        }
        return x
    }`)
	wantNum(t, call(t, funcs, "f"), 1)
}

func TestHeredocExpression(t *testing.T) {
	// A heredoc is an ordinary HCL expression; its interior newlines must not be
	// mistaken for statement terminators, and the closing marker must survive the
	// expression-span extraction.
	src := "func doc(name: string) -> string {\n" +
		"    var body = <<-EOT\n" +
		"        Hello, ${name}!\n" +
		"        Welcome.\n" +
		"        EOT\n" +
		"    return body\n" +
		"}"
	funcs := compileFuncs(t, src)
	got := call(t, funcs, "doc", cty.StringVal("Ada"))
	wantStr(t, got, "Hello, Ada!\nWelcome.\n")
}

func TestSiblingScopeRedeclaration(t *testing.T) {
	// The same name may be declared in separate (sibling) scopes: each if branch
	// and each loop iteration is its own scope, so `var hit` below is legal in
	// both branches and re-declared fresh on every iteration.
	funcs := compileFuncs(t, `func count_positive(items: list(number)) -> number {
        var total = 0
        for v in items {
            if v > 0 {
                var hit = 1
                total = total + hit
            } else {
                var hit = 0
                total = total + hit
            }
        }
        return total
    }`)
	got := call(t, funcs, "count_positive", cty.ListVal([]cty.Value{num(1), num(-2), num(3), num(-4), num(5)}))
	wantNum(t, got, 3)
}

func TestIndexBy(t *testing.T) {
	funcs := compileFuncs(t, `func index_by(items, key: string) {
        var out = {}
        for item in items {
            out = merge(out, { (item[key]) = item })
        }
        return out
    }`)
	items := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("a"), "n": num(1)}),
		cty.ObjectVal(map[string]cty.Value{"id": cty.StringVal("b"), "n": num(2)}),
	})
	got := call(t, funcs, "index_by", items, cty.StringVal("id"))
	if got.LengthInt() != 2 {
		t.Fatalf("expected 2 entries, got %d", got.LengthInt())
	}
	if !got.GetAttr("a").GetAttr("n").RawEquals(num(1)) {
		t.Fatalf("unexpected index_by result: %#v", got)
	}
}
