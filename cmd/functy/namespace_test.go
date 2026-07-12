package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeCtyDir writes several .cty files into one temp dir and returns the dir, for
// the multi-namespace cases (a namespace spans files, and ambiguity needs two).
func writeCtyDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const nsMath = `namespace acme::math

func double(n: number) -> number { return _twice(n) }
func _twice(n: number) -> number { return n * 2 }
func main() -> number { return double(21) }
`

// A file's `main` becomes `acme::math::main`, so a bare --func (and its "main"
// default) must still resolve when the name is unambiguous — otherwise adding a
// namespace would break `functy run file.cty`.
func TestRunNamespacedMainByDefault(t *testing.T) {
	path := writeCty(t, "m.cty", nsMath)
	out, _, err := execCLI(t, "run", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("got %q, want 42", out)
	}
}

func TestRunNamespacedQualifiedEntry(t *testing.T) {
	path := writeCty(t, "m.cty", nsMath)
	out, _, err := execCLI(t, "run", "--func", "acme::math::double", path, "--", "5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "10" {
		t.Fatalf("got %q, want 10", out)
	}
}

// A private function is never handed to the host, but --func must still reach it:
// it is the one way to exercise a helper directly while debugging.
func TestRunPrivateEntryForDebugging(t *testing.T) {
	path := writeCty(t, "m.cty", nsMath)
	out, _, err := execCLI(t, "run", "--func", "_twice", path, "--", "8")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != "16" {
		t.Fatalf("got %q, want 16", out)
	}
}

// A bare name declared in two namespaces is ambiguous: report it, listing the
// candidates, rather than silently picking one.
func TestRunAmbiguousEntryAcrossNamespaces(t *testing.T) {
	dir := writeCtyDir(t, map[string]string{
		"a.cty": "namespace acme::math\nfunc main() -> number { return 1 }\n",
		"b.cty": "namespace acme::text\nfunc main() -> number { return 2 }\n",
	})
	_, _, err := execCLI(t, "run", dir)
	if err == nil {
		t.Fatal("expected an ambiguity error")
	}
	msg := err.Error()
	for _, want := range []string{"more than one namespace", "acme::math::main", "acme::text::main"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should mention %q, got: %s", want, msg)
		}
	}
}

// Namespacing disarms the reserved-name error (foo::upper collides with nothing),
// but the bare name still shadows the built-in inside the namespace — so the
// diagnostic is rebuilt as a warning. It must warn, and must not fail the check.
func TestCheckWarnsOnNamespacedShadow(t *testing.T) {
	path := writeCty(t, "m.cty", "namespace acme::text\nfunc upper(s: string) -> string { return s }\n")
	out, errOut, err := execCLI(t, "check", path)
	if err != nil {
		t.Fatalf("a shadow is a warning, not an error: %v", err)
	}
	if !strings.Contains(errOut, "shadows a built-in") {
		t.Errorf("expected a shadow warning on stderr, got: %s", errOut)
	}
	if !strings.Contains(errOut, "acme::text::upper") {
		t.Errorf("warning should name the qualified call, got: %s", errOut)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("check should still pass, got stdout: %s", out)
	}
}

// The same name in the GLOBAL namespace is still a hard error — it really would
// replace the built-in in the host's map.
func TestCheckGlobalReservedNameStillErrors(t *testing.T) {
	path := writeCty(t, "m.cty", "func upper(s: string) -> string { return s }\n")
	_, errOut, err := execCLI(t, "check", path)
	if err == nil {
		t.Fatal("a global function shadowing a baseline builtin must be an error")
	}
	if !strings.Contains(errOut, "reserved") {
		t.Errorf("expected the reserved-name error, got: %s", errOut)
	}
}

func TestSymbolsNamespaceFields(t *testing.T) {
	path := writeCty(t, "m.cty", nsMath)
	out, _, err := execCLI(t, "symbols", "--json", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got jsonSymbols
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	byName := map[string]jsonSymbol{}
	for _, s := range got.Symbols {
		byName[s.Name] = s
	}
	if s := byName["double"]; s.Namespace != "acme::math" || s.Qualified != "acme::math::double" || s.Private {
		t.Errorf("double: got %+v", s)
	}
	// A private declaration is still listed — an outline shows the whole file —
	// but is flagged.
	if s, ok := byName["_twice"]; !ok || !s.Private {
		t.Errorf("_twice should be listed and marked private, got %+v (present=%v)", s, ok)
	}
}

// A global-namespace file must serialize exactly as it did before namespaces
// existed: the new fields are omitempty, so an existing consumer (vscode-functy
// asserts on this shape) sees no change at all.
func TestSymbolsGlobalNamespaceJSONUnchanged(t *testing.T) {
	path := writeCty(t, "m.cty", "func a(x: number) -> number { return x }\n")
	out, _, err := execCLI(t, "symbols", "--json", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw struct {
		Symbols []map[string]any `json:"symbols"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(raw.Symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d", len(raw.Symbols))
	}
	for _, key := range []string{"namespace", "qualified", "private"} {
		if _, present := raw.Symbols[0][key]; present {
			t.Errorf("%q must be omitted in the global namespace (kept the JSON stable for existing consumers)", key)
		}
	}
}

// run --json promises exactly ONE well-formed object on stderr. A shadow warning
// must therefore not be emitted as its own report and then followed by an error
// report — a consumer would get two concatenated objects. The warning has to be
// folded into the single report instead.
func TestRunJSONWarningAndErrorStayOneObject(t *testing.T) {
	path := writeCty(t, "m.cty", `namespace acme::text
func upper(s: string) -> string { return s }
func main() -> string { throw "boom" }
`)
	_, errOut, err := execCLI(t, "run", "--json", path)
	if err == nil {
		t.Fatal("expected the throw to fail the run")
	}

	dec := json.NewDecoder(strings.NewReader(errOut))
	var report struct {
		Diagnostics []struct {
			Severity string `json:"severity"`
			Summary  string `json:"summary"`
		} `json:"diagnostics"`
	}
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("stderr is not a well-formed JSON object: %v\n%s", err, errOut)
	}
	if dec.More() {
		t.Fatalf("stderr must be a SINGLE JSON object, got more than one:\n%s", errOut)
	}

	var sawWarning, sawError bool
	for _, d := range report.Diagnostics {
		switch d.Severity {
		case "warning":
			sawWarning = true
		case "error":
			sawError = true
		}
	}
	if !sawWarning {
		t.Errorf("the shadow warning should be folded into the report, got: %s", errOut)
	}
	if !sawError {
		t.Errorf("the thrown error should be in the report, got: %s", errOut)
	}
}

// The same contract on the success path: a warning alone is still one object, and
// stdout stays clean for the program's own output.
func TestRunJSONWarningOnlyStaysOneObject(t *testing.T) {
	path := writeCty(t, "m.cty", `namespace acme::text
func upper(s: string) -> string { return s }
func main() -> number { return 1 }
`)
	out, errOut, err := execCLI(t, "run", "--json", path)
	if err != nil {
		t.Fatalf("a warning must not fail the run: %v", err)
	}
	if strings.TrimSpace(out) != "1" {
		t.Errorf("stdout should carry the result, got %q", out)
	}
	dec := json.NewDecoder(strings.NewReader(errOut))
	var report map[string]any
	if err := dec.Decode(&report); err != nil {
		t.Fatalf("stderr is not a well-formed JSON object: %v\n%s", err, errOut)
	}
	if dec.More() {
		t.Fatalf("stderr must be a SINGLE JSON object:\n%s", errOut)
	}
}

// A namespaced test block calls its file's functions — public and private — by
// their bare names.
func TestCLITestInNamespace(t *testing.T) {
	path := writeCty(t, "m.cty", nsMath+`
test "doubles" {
    assert(double(4) == 8)
    assert(_twice(5) == 10)
}
`)
	out, _, err := execCLI(t, "test", path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "1 passed, 0 failed") {
		t.Fatalf("expected the namespaced test to pass, got: %s", out)
	}
}
