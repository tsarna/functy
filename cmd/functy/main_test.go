package main

import (
	"bytes"
	"encoding/json"
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

func TestCheckDirectory(t *testing.T) {
	// A directory argument is walked recursively; the files are checked as one
	// merged program (so main.cty may reference lib.cty's function).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "lib.cty"),
		[]byte("func add(a: number, b: number) -> number { return a + b }"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.cty"),
		[]byte("func main() -> number { return add(1, 2) }"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := execCLI(t, "check", dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "ok" {
		t.Fatalf("got %q, want ok", out)
	}
}

func TestCheckStdinFilename(t *testing.T) {
	// `check -` reads the buffer from stdin; --filename names it so the diagnostic
	// location carries a real path (an editor checking an unsaved buffer).
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader("func main() { break }"))
	c.SetArgs([]string{"check", "--json", "-", "--filename", "buf.cty"})
	if err := c.Execute(); err == nil {
		t.Fatal("expected check to fail")
	}
	var rep struct {
		Diagnostics []struct {
			Location *struct {
				File string `json:"file"`
			} `json:"location"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(errb.Bytes(), &rep); err != nil {
		t.Fatalf("stderr is not valid JSON: %v\n%s", err, errb.String())
	}
	if len(rep.Diagnostics) != 1 || rep.Diagnostics[0].Location == nil ||
		rep.Diagnostics[0].Location.File != "buf.cty" {
		t.Fatalf("expected one diagnostic at buf.cty, got %+v", rep.Diagnostics)
	}
}

func TestRunHelpAndDocWired(t *testing.T) {
	// The standalone CLI wires the reflection builtins into the run context:
	// help(name) renders a functy function's signature, help() lists all functions,
	// and doc(name) returns a host function's description.
	src := `// Add two numbers.
func add(a: number, b: number) -> number { return a + b }
func main() -> string { return help("add") }`
	path := writeCty(t, "lib.cty", src)
	out, _, err := execCLI(t, "run", "--output", "raw", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "add(a: number, b: number) -> number") || !strings.Contains(out, "Add two numbers.") {
		t.Fatalf("help(\"add\") did not render the signature/doc; got:\n%s", out)
	}

	listPath := writeCty(t, "list.cty", `func main() -> string { return help() }`)
	out, _, err = execCLI(t, "run", "--output", "raw", listPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The listing includes the reflection builtins themselves and the cty stdlib.
	for _, want := range []string{"help", "doc", "upper"} {
		if !strings.Contains(out, want) {
			t.Fatalf("help() listing missing %q; got:\n%s", want, out)
		}
	}
}

func TestRunHelpIsReservedName(t *testing.T) {
	// Wiring help/doc into the baseline makes them reserved: a user function may
	// not shadow them.
	path := writeCty(t, "clash.cty", "func help() -> number { return 1 }\nfunc main() -> number { return 1 }")
	_, errOut, err := execCLI(t, "run", path)
	if err == nil {
		t.Fatalf("expected a reserved-name error")
	}
	if !strings.Contains(errOut, "reserved") {
		t.Fatalf("expected a reserved-name diagnostic; got:\n%s", errOut)
	}
}

func TestCheckJSONValid(t *testing.T) {
	// A clean file emits a well-formed report (empty diagnostics array) to stderr and
	// exits zero; stdout carries no "ok" or anything else in --json mode.
	path := writeCty(t, "ok.cty", "func main() -> number { return 1 }")
	out, errOut, err := execCLI(t, "check", "--json", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--json stdout should be empty, got:\n%s", out)
	}
	var rep struct {
		Diagnostics []any `json:"diagnostics"`
	}
	if uerr := json.Unmarshal([]byte(errOut), &rep); uerr != nil {
		t.Fatalf("stderr is not valid JSON: %v\n%s", uerr, errOut)
	}
	if len(rep.Diagnostics) != 0 {
		t.Fatalf("expected no diagnostics for a valid file, got %d", len(rep.Diagnostics))
	}
}

func TestCheckJSONInvalid(t *testing.T) {
	// An error diagnostic is rendered structurally to stderr (severity, summary,
	// detail, 1-based location) and the command still exits non-zero.
	path := writeCty(t, "bad.cty", "func main() { break }")
	out, errOut, err := execCLI(t, "check", "--json", path)
	if err == nil {
		t.Fatalf("expected check to fail; stderr:\n%s", errOut)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--json stdout should be empty, got:\n%s", out)
	}
	var rep struct {
		Diagnostics []struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
			Detail   string `json:"detail"`
			Location *struct {
				File      string `json:"file"`
				Line      int    `json:"line"`
				Column    int    `json:"column"`
				EndLine   int    `json:"end_line"`
				EndColumn int    `json:"end_column"`
			} `json:"location"`
		} `json:"diagnostics"`
	}
	if uerr := json.Unmarshal([]byte(errOut), &rep); uerr != nil {
		t.Fatalf("stderr is not valid JSON: %v\n%s", uerr, errOut)
	}
	if len(rep.Diagnostics) != 1 {
		t.Fatalf("expected exactly one diagnostic, got %d:\n%s", len(rep.Diagnostics), errOut)
	}
	d := rep.Diagnostics[0]
	if d.Severity != "error" {
		t.Fatalf("severity = %q, want error", d.Severity)
	}
	if !strings.Contains(d.Summary, "break") {
		t.Fatalf("summary %q should mention break", d.Summary)
	}
	if d.Detail == "" {
		t.Fatalf("expected a detail for the break diagnostic")
	}
	if d.Location == nil {
		t.Fatalf("expected a source location for the diagnostic")
	}
	if d.Location.Line != 1 || d.Location.Column < 1 || d.Location.EndColumn <= d.Location.Column {
		t.Fatalf("unexpected 1-based location: %+v", *d.Location)
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

func TestRunJSONCompileErrorOnStderr(t *testing.T) {
	// A compile failure under --json emits a structural report on stderr (not the
	// text writer), with severity, summary, and a 1-based location; stdout stays
	// empty so a consumer never confuses program output with the report.
	path := writeCty(t, "bad.cty", "func main() { break }")
	out, errOut, err := execCLI(t, "run", "--json", path)
	if err == nil {
		t.Fatalf("expected a non-zero exit; stderr:\n%s", errOut)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("stdout should be empty on a compile error, got:\n%s", out)
	}
	var rep struct {
		Diagnostics []struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
			Location *struct {
				Line int `json:"line"`
			} `json:"location"`
		} `json:"diagnostics"`
	}
	if uerr := json.Unmarshal([]byte(errOut), &rep); uerr != nil {
		t.Fatalf("stderr is not valid JSON: %v\n%s", uerr, errOut)
	}
	if len(rep.Diagnostics) != 1 || rep.Diagnostics[0].Severity != "error" ||
		!strings.Contains(rep.Diagnostics[0].Summary, "break") {
		t.Fatalf("unexpected diagnostics: %+v", rep.Diagnostics)
	}
	if rep.Diagnostics[0].Location == nil {
		t.Fatalf("expected a source location on the diagnostic")
	}
}

func TestRunJSONThrownErrorKeepsStdoutClean(t *testing.T) {
	// A runtime failure after the program has printed: the printed output stays on
	// stdout, and the thrown assertion's diagnostic (message, operand detail,
	// location) is emitted as pure JSON on stderr — no "functy: ..." line mixed in.
	src := `func main(n: number) -> number {
    println("side effect")
    assert(n > 0, "must be positive")
    return n
}`
	path := writeCty(t, "t.cty", src)
	out, errOut, err := execCLI(t, "run", "--json", path, "--", "-3")
	if err == nil {
		t.Fatalf("expected a non-zero exit; out:\n%s", out)
	}
	if strings.TrimSpace(out) != "side effect" {
		t.Fatalf("stdout should carry only the program output, got:\n%s", out)
	}
	var rep struct {
		Diagnostics []struct {
			Summary  string `json:"summary"`
			Detail   string `json:"detail"`
			Location *struct {
				Line int `json:"line"`
			} `json:"location"`
		} `json:"diagnostics"`
	}
	if uerr := json.Unmarshal([]byte(errOut), &rep); uerr != nil {
		t.Fatalf("stderr is not pure JSON: %v\n%s", uerr, errOut)
	}
	if len(rep.Diagnostics) != 1 {
		t.Fatalf("expected one diagnostic, got %d:\n%s", len(rep.Diagnostics), errOut)
	}
	d := rep.Diagnostics[0]
	if d.Summary != "must be positive" || d.Detail != "n = -3" || d.Location == nil || d.Location.Line != 3 {
		t.Fatalf("unexpected diagnostic: %+v", d)
	}
}

func TestRunJSONSuccessNoDiagnostics(t *testing.T) {
	// On success --json changes nothing observable: the value is on stdout and
	// stderr is empty (no diagnostics report).
	path := writeCty(t, "m.cty", "func main() -> number { return 42 }")
	out, errOut, err := execCLI(t, "run", "--json", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("stdout = %q, want 42", out)
	}
	if strings.TrimSpace(errOut) != "" {
		t.Fatalf("stderr should be empty on success, got:\n%s", errOut)
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

func TestCLITestJSONReport(t *testing.T) {
	// --json emits a single self-contained report covering every test (pass, fail,
	// skip) regardless of -v, with a summary and a non-zero exit on failure.
	src := `func add(a: number, b: number) -> number { return a + b }
test "sums" { assert(add(2, 3) == 5) }
test "positivity" {
    var n = -3
    assert(n > 0, "must be positive")
}
test "wip" { skip("todo") }`
	path := writeCty(t, "t.cty", src)
	// The report is on stderr; stdout is reserved for the tests' own output.
	_, out, err := execCLI(t, "test", "--json", path)
	if err == nil {
		t.Fatalf("expected a non-zero exit for a failing test; report:\n%s", out)
	}

	var rep struct {
		Tests []struct {
			Name       string  `json:"name"`
			Status     string  `json:"status"`
			DurationMs float64 `json:"duration_ms"`
			SkipReason string  `json:"skip_reason"`
			Location   *struct {
				File string `json:"file"`
				Line int    `json:"line"`
			} `json:"location"`
			Failure *struct {
				Message  string `json:"message"`
				Detail   string `json:"detail"`
				Location *struct {
					Line   int `json:"line"`
					Column int `json:"column"`
				} `json:"location"`
			} `json:"failure"`
		} `json:"tests"`
		Summary struct {
			Passed, Failed, Skipped, Deselected int
		} `json:"summary"`
	}
	if uerr := json.Unmarshal([]byte(out), &rep); uerr != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", uerr, out)
	}

	if rep.Summary.Passed != 1 || rep.Summary.Failed != 1 || rep.Summary.Skipped != 1 || rep.Summary.Deselected != 0 {
		t.Fatalf("unexpected summary: %+v", rep.Summary)
	}
	if len(rep.Tests) != 3 {
		t.Fatalf("expected 3 test entries, got %d", len(rep.Tests))
	}

	byName := map[string]int{}
	for i, tc := range rep.Tests {
		byName[tc.Name] = i
	}
	sums := rep.Tests[byName["sums"]]
	if sums.Status != "passed" || sums.Failure != nil {
		t.Fatalf("sums should be a clean pass: %+v", sums)
	}
	if sums.Location == nil || sums.Location.File != path {
		t.Fatalf("sums missing its source location: %+v", sums.Location)
	}

	pos := rep.Tests[byName["positivity"]]
	if pos.Status != "failed" || pos.Failure == nil {
		t.Fatalf("positivity should be a failure: %+v", pos)
	}
	if pos.Failure.Message != "must be positive" || pos.Failure.Detail != "n = -3" {
		t.Fatalf("failure message/detail wrong: %+v", pos.Failure)
	}
	if pos.Failure.Location == nil {
		t.Fatalf("failure should carry a source range to underline: %+v", pos.Failure)
	}

	wip := rep.Tests[byName["wip"]]
	if wip.Status != "skipped" || wip.SkipReason != "todo" {
		t.Fatalf("wip should be skipped with a reason: %+v", wip)
	}
}

func TestCLITestNoArgsDiscoversCwd(t *testing.T) {
	// With no path arguments, `functy test` discovers .cty files in the working
	// directory tree (equivalent to `functy test .`).
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "m.cty"),
		[]byte(`test "discovered" { assert(true) }`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	out, _, err := execCLI(t, "test", "-v")
	if err != nil {
		t.Fatalf("unexpected error: %v (out: %s)", err, out)
	}
	for _, want := range []string{"ok   discovered", "1 passed, 0 failed, 0 skipped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q; got:\n%s", want, out)
		}
	}
}

func TestCLITestJSONCompileFailureIsValid(t *testing.T) {
	// Even a compilation failure emits a well-formed (empty) report to stderr so
	// consumers can always parse it, and still exits non-zero.
	path := writeCty(t, "t.cty", `test "broken" { this is not valid `)
	_, out, err := execCLI(t, "test", "--json", path)
	if err == nil {
		t.Fatal("expected a non-zero exit for a compile failure")
	}
	var rep struct {
		Tests   []any                                 `json:"tests"`
		Summary struct{ Passed, Failed, Skipped int } `json:"summary"`
	}
	if uerr := json.Unmarshal([]byte(out), &rep); uerr != nil {
		t.Fatalf("compile-failure report is not valid JSON: %v\n%s", uerr, out)
	}
	if len(rep.Tests) != 0 {
		t.Fatalf("expected no test entries on compile failure, got %d", len(rep.Tests))
	}
}

func TestCLITestJSONAllPassExitsZero(t *testing.T) {
	src := `test "a" { assert(true) }
test "b" { assert(1 + 1 == 2) }`
	path := writeCty(t, "t.cty", src)
	_, out, err := execCLI(t, "test", "--json", path)
	if err != nil {
		t.Fatalf("all-pass run should exit 0: %v\n%s", err, out)
	}
	if !strings.Contains(out, `"passed": 2`) {
		t.Fatalf("expected 2 passed in summary; got:\n%s", out)
	}
}

func TestCLITestJSONPrintlnStaysOnStdout(t *testing.T) {
	// The report goes to stderr, so a test that calls println() does not corrupt it:
	// the printed line lands on stdout, and stderr parses as pure JSON.
	src := `test "prints" {
    println("hello from a test")
    assert(true)
}`
	path := writeCty(t, "t.cty", src)
	out, errOut, err := execCLI(t, "test", "--json", path)
	if err != nil {
		t.Fatalf("unexpected error: %v\nstderr:\n%s", err, errOut)
	}
	if strings.TrimSpace(out) != "hello from a test" {
		t.Fatalf("test println should be on stdout, got:\n%s", out)
	}
	var rep struct {
		Summary struct{ Passed int } `json:"summary"`
	}
	if uerr := json.Unmarshal([]byte(errOut), &rep); uerr != nil {
		t.Fatalf("stderr is not pure JSON (println leaked in?): %v\n%s", uerr, errOut)
	}
	if rep.Summary.Passed != 1 {
		t.Fatalf("expected 1 passed; got:\n%s", errOut)
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

func TestFmtStdin(t *testing.T) {
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader("func  f(a,b){return a+b}\n"))
	c.SetArgs([]string{"fmt", "-"})
	if err := c.Execute(); err != nil {
		t.Fatalf("unexpected error: %v (stderr: %s)", err, errb.String())
	}
	want := "func f(a, b) {\n    return a + b\n}\n"
	if out.String() != want {
		t.Fatalf("fmt stdin =\n%q\nwant\n%q", out.String(), want)
	}
}

func TestFmtWriteAndList(t *testing.T) {
	path := writeCty(t, "messy.cty", "func  g( a ){return a}\n")

	// -l reports the file as differing.
	out, _, err := execCLI(t, "fmt", "-l", path)
	if err != nil {
		t.Fatalf("fmt -l: %v", err)
	}
	if strings.TrimSpace(out) != path {
		t.Fatalf("fmt -l = %q, want %q", out, path)
	}

	// -w rewrites it in place.
	if _, _, err := execCLI(t, "fmt", "-w", path); err != nil {
		t.Fatalf("fmt -w: %v", err)
	}
	got, _ := os.ReadFile(path)
	want := "func g(a) {\n    return a\n}\n"
	if string(got) != want {
		t.Fatalf("after -w file =\n%q\nwant\n%q", got, want)
	}

	// -l now reports nothing (already formatted).
	out, _, err = execCLI(t, "fmt", "-l", path)
	if err != nil {
		t.Fatalf("fmt -l (2): %v", err)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("fmt -l after -w = %q, want empty", out)
	}
}

func TestFmtParseErrorFails(t *testing.T) {
	path := writeCty(t, "broken.cty", "func f( {\n")
	_, _, err := execCLI(t, "fmt", path)
	if err == nil {
		t.Fatal("expected fmt to fail on a file that does not parse")
	}
}

func TestEvalWithFileContext(t *testing.T) {
	path := writeCty(t, "lib.cty", "func add(a: number, b: number) -> number { return a + b }")
	out, _, err := execCLI(t, "eval", "add(2, 3)", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "5" {
		t.Fatalf("got %q, want 5", out)
	}
}

func TestEvalNoFiles(t *testing.T) {
	// Zero files is allowed: the expression evaluates against the baseline context.
	out, _, err := execCLI(t, "eval", "1 + 2 * 3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "7" {
		t.Fatalf("got %q, want 7", out)
	}
}

func TestEvalJSONError(t *testing.T) {
	// An evaluation error emits a structured diagnostic to stderr, nothing to
	// stdout, and exits non-zero.
	out, errOut, err := execCLI(t, "eval", "--json", "nosuchfunc(1)")
	if err == nil {
		t.Fatalf("expected eval to fail; stderr:\n%s", errOut)
	}
	if strings.TrimSpace(out) != "" {
		t.Fatalf("--json stdout should be empty, got:\n%s", out)
	}
	var rep struct {
		Diagnostics []struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
		} `json:"diagnostics"`
	}
	if uerr := json.Unmarshal([]byte(errOut), &rep); uerr != nil {
		t.Fatalf("stderr is not valid JSON: %v\n%s", uerr, errOut)
	}
	if len(rep.Diagnostics) != 1 || rep.Diagnostics[0].Severity != "error" {
		t.Fatalf("expected one error diagnostic, got %+v", rep.Diagnostics)
	}
}

func TestSymbolsJSON(t *testing.T) {
	src := "// Add two numbers.\n" +
		"func add(a: number, b: number = 0) -> number { return a + b }\n" +
		"const pi = 3.14159\n" +
		"test \"adds\" { assert(add(1, 2) == 3) }\n"
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader(src))
	c.SetArgs([]string{"symbols", "--json", "-", "--filename", "x.cty"})
	if err := c.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var rep struct {
		Symbols []struct {
			Kind   string `json:"kind"`
			Name   string `json:"name"`
			Detail string `json:"detail"`
			Doc    string `json:"doc"`
			Range  struct {
				File string `json:"file"`
				Line int    `json:"line"`
			} `json:"range"`
		} `json:"symbols"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	// Source order: func add, const pi, test adds.
	if len(rep.Symbols) != 3 {
		t.Fatalf("expected 3 symbols, got %d: %+v", len(rep.Symbols), rep.Symbols)
	}
	fn := rep.Symbols[0]
	if fn.Kind != "func" || fn.Name != "add" {
		t.Fatalf("symbol[0] = %+v, want func add", fn)
	}
	if fn.Detail != "(a: number, b: number = 0) -> number" {
		t.Fatalf("add detail = %q", fn.Detail)
	}
	if fn.Doc != "Add two numbers." {
		t.Fatalf("add doc = %q", fn.Doc)
	}
	if fn.Range.File != "x.cty" || fn.Range.Line != 2 {
		t.Fatalf("add range = %+v, want x.cty line 2", fn.Range)
	}
	if rep.Symbols[1].Kind != "const" || rep.Symbols[1].Name != "pi" {
		t.Fatalf("symbol[1] = %+v, want const pi", rep.Symbols[1])
	}
	if rep.Symbols[2].Kind != "test" || rep.Symbols[2].Name != "adds" {
		t.Fatalf("symbol[2] = %+v, want test adds", rep.Symbols[2])
	}
}

func TestSymbolsText(t *testing.T) {
	// The default (no --json) is a greppable `file:line: kind name` listing.
	src := "func add(a: number, b: number) -> number { return a + b }\n" +
		"test \"adds\" { assert(add(1, 2) == 3) }\n"
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader(src))
	c.SetArgs([]string{"symbols", "-", "--filename", "m.cty"})
	if err := c.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"m.cty:1: func add(a: number, b: number) -> number",
		"m.cty:2: test \"adds\"",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q; got:\n%s", want, got)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	out, _, err := execCLI(t, "version", "--json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var v struct {
		Version string `json:"version"`
		Commit  string `json:"commit"`
		Date    string `json:"date"`
		Go      string `json:"go"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if v.Version == "" {
		t.Error("version field is empty")
	}
	if !strings.HasPrefix(v.Go, "go") {
		t.Errorf("go field %q missing 'go' prefix", v.Go)
	}
}
