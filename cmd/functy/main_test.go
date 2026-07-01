package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execCLI runs the CLI with the given args, capturing stdout and stderr.
func execCLI(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetArgs(args)
	err = c.Execute()
	return out.String(), errb.String(), err
}

// writeCty writes a .cty file into a temp dir and returns its path.
func writeCty(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunEntryMain(t *testing.T) {
	path := writeCty(t, "m.cty", "func main() -> number { return 6 * 7 }")
	out, _, err := execCLI(t, "run", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("got %q, want 42", out)
	}
}

func TestRunFuncWithArgs(t *testing.T) {
	path := writeCty(t, "m.cty", "func add(a: number, b: number) -> number { return a + b }")
	out, _, err := execCLI(t, "run", "--func", "add", path, "--", "2", "3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "5" {
		t.Fatalf("got %q, want 5", out)
	}
}

func TestRunRawString(t *testing.T) {
	path := writeCty(t, "g.cty", `func greet(name: string = "world") -> string { return "hello ${name}" }`)
	out, _, err := execCLI(t, "run", "--func", "greet", "--output", "raw", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "hello world" {
		t.Fatalf("got %q, want 'hello world'", out)
	}

	out, _, err = execCLI(t, "run", "--func", "greet", "--output", "raw", path, "--", `"alice"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "hello alice" {
		t.Fatalf("got %q, want 'hello alice'", out)
	}
}

func TestRunArgTypeConversion(t *testing.T) {
	// A quoted string argument is converted to the declared number parameter.
	path := writeCty(t, "m.cty", "func id(n: number) -> number { return n }")
	out, _, err := execCLI(t, "run", "--func", "id", path, "--", `"12"`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "12" {
		t.Fatalf("got %q, want 12", out)
	}
}

func TestRunTopLevelConstsOutOfOrder(t *testing.T) {
	src := `const tau: number = pi * 2
const pi = 3
func main() -> number { return tau }`
	path := writeCty(t, "c.cty", src)
	out, _, err := execCLI(t, "run", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "6" {
		t.Fatalf("got %q, want 6", out)
	}
}

func TestRunNullPrintsNothing(t *testing.T) {
	path := writeCty(t, "m.cty", "func main() { var x = 1 }")
	out, _, err := execCLI(t, "run", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("null return should print nothing, got %q", out)
	}
}

func TestCheckValid(t *testing.T) {
	path := writeCty(t, "ok.cty", "func main() -> number { return 1 }")
	out, _, err := execCLI(t, "check", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("got %q, want ok", out)
	}
}

func TestCheckInvalid(t *testing.T) {
	path := writeCty(t, "bad.cty", "func main() { break }")
	_, errOut, err := execCLI(t, "check", path)
	if err == nil {
		t.Fatalf("expected check to fail")
	}
	if !strings.Contains(errOut, "break") {
		t.Fatalf("expected a diagnostic mentioning break, got: %s", errOut)
	}
}

func TestRunUncaughtAssertRendersDiagnostic(t *testing.T) {
	// An uncaught assertion surfaces as a source-located diagnostic: the message,
	// the failing source line, and the captured operand values (its detail).
	src := `func main(n: number) -> number {
    assert(n > 0, "must be positive")
    return n
}`
	path := writeCty(t, "a.cty", src)
	_, errOut, err := execCLI(t, "run", path, "--", "-3")
	if err == nil {
		t.Fatalf("expected an error for the failed assertion")
	}
	for _, want := range []string{"must be positive", "line 2", "assert(n > 0", "n = -3"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr missing %q; got:\n%s", want, errOut)
		}
	}
}

func TestCLITestPass(t *testing.T) {
	// A passing run exits 0 and reports the summary; quiet (default) does not list
	// the passing test itself.
	src := `func add(a: number, b: number) -> number { return a + b }
test "sums" { assert(add(2, 3) == 5) }`
	path := writeCty(t, "t.cty", src)
	out, _, err := execCLI(t, "test", path)
	if err != nil {
		t.Fatalf("unexpected error: %v (out: %s)", err, out)
	}
	if !strings.Contains(out, "1 passed, 0 failed, 0 skipped") {
		t.Fatalf("stdout missing summary; got:\n%s", out)
	}
	if strings.Contains(out, "ok   sums") {
		t.Fatalf("quiet output should not list the passing test; got:\n%s", out)
	}
}

func TestCLITestFailRendersDetail(t *testing.T) {
	// A failing assertion in a test reports FAIL, the message, the operand detail,
	// and a non-zero exit.
	src := `test "positivity" {
    var n = -3
    assert(n > 0, "must be positive")
}`
	path := writeCty(t, "t.cty", src)
	out, _, err := execCLI(t, "test", path)
	if err == nil {
		t.Fatalf("expected a non-zero exit for a failing test; out:\n%s", out)
	}
	for _, want := range []string{"FAIL positivity", "must be positive", "n = -3", "0 passed, 1 failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout missing %q; got:\n%s", want, out)
		}
	}
}

func TestCLITestQuietHidesPasses(t *testing.T) {
	// Quiet (default) prints only failures, not passing tests.
	src := `test "passes" { assert(true) }
test "fails" { var n = -3
    assert(n > 0, "must be positive") }`
	path := writeCty(t, "t.cty", src)
	out, _, err := execCLI(t, "test", path)
	if err == nil {
		t.Fatalf("expected failure; out:\n%s", out)
	}
	if strings.Contains(out, "ok   passes") {
		t.Fatalf("quiet output should not list passing tests; got:\n%s", out)
	}
	for _, want := range []string{"FAIL fails", "n = -3", "1 passed, 1 failed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q; got:\n%s", want, out)
		}
	}
}

func TestCLITestVerboseListsAll(t *testing.T) {
	src := `test "passes" { assert(true) }
test "skipped" { skip("later") }`
	path := writeCty(t, "t.cty", src)
	out, _, err := execCLI(t, "test", "-v", path)
	if err != nil {
		t.Fatalf("unexpected error: %v (out: %s)", err, out)
	}
	for _, want := range []string{"ok   passes", "SKIP skipped: later", "1 passed, 0 failed, 1 skipped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q; got:\n%s", want, out)
		}
	}
}

func TestCLITestSkipDoesNotFail(t *testing.T) {
	// A skipped test is not a failure: exit 0.
	path := writeCty(t, "t.cty", `test "wip" { skip("todo") }`)
	out, _, err := execCLI(t, "test", path)
	if err != nil {
		t.Fatalf("a skipped test should not fail the run: %v (out: %s)", err, out)
	}
	if !strings.Contains(out, "0 passed, 0 failed, 1 skipped") {
		t.Fatalf("missing skip summary; got:\n%s", out)
	}
}

func TestCLITestRunFilter(t *testing.T) {
	src := `test "keep me" { assert(true) }
test "drop me" { assert(true) }`
	path := writeCty(t, "t.cty", src)
	out, _, err := execCLI(t, "test", "-v", "--run", "keep", path)
	if err != nil {
		t.Fatalf("unexpected error: %v (out: %s)", err, out)
	}
	for _, want := range []string{"ok   keep me", "1 passed, 0 failed, 0 skipped", "1 deselected by --run"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q; got:\n%s", want, out)
		}
	}
	if strings.Contains(out, "drop me") {
		t.Fatalf("deselected test should not run; got:\n%s", out)
	}
}

func TestCLITestBadRunPattern(t *testing.T) {
	path := writeCty(t, "t.cty", `test "t" { assert(true) }`)
	_, _, err := execCLI(t, "test", "--run", "[", path)
	if err == nil {
		t.Fatal("expected an error for an invalid --run pattern")
	}
}

func TestCLIRunIgnoresTests(t *testing.T) {
	// `functy run` does not execute test blocks.
	src := `func main() -> number { return 7 }
test "would fail if run" { assert(false, "should not execute") }`
	path := writeCty(t, "t.cty", src)
	out, _, err := execCLI(t, "run", "--output", "raw", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "7" {
		t.Fatalf("got %q, want 7 (tests must not run)", out)
	}
}

func TestRunMissingEntry(t *testing.T) {
	path := writeCty(t, "m.cty", "func other() { return 1 }")
	_, _, err := execCLI(t, "run", path)
	if err == nil {
		t.Fatalf("expected error for missing main")
	}
}
