package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

const externSrc = "//functy:extern\n\n" +
	"// Read a value from a thing.\n" +
	"func get(ctx?: ctx, thing, fallback = null) -> any\n"

// testSymbol mirrors the fields of jsonSymbol this file asserts on. It is decoded
// from the CLI's actual JSON rather than reusing jsonSymbol, so a change to the
// wire format that would break an existing consumer (vscode-functy) fails here.
type testSymbol struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Qualified string `json:"qualified"`
	Private   bool   `json:"private"`
	Detail    string `json:"detail"`
	Doc       string `json:"doc"`
	Range     struct {
		File    string `json:"file"`
		Line    int    `json:"line"`
		EndLine int    `json:"end_line"`
	} `json:"range"`
}

// symbolsJSON runs `symbols --json` over src read from stdin.
func symbolsJSON(t *testing.T, src string) []testSymbol {
	t.Helper()
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader(src))
	c.SetArgs([]string{"symbols", "--json", "-", "--filename", "x.cty"})
	if err := c.Execute(); err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, errb.String())
	}
	var rep struct {
		Symbols []testSymbol `json:"symbols"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, out.String())
	}
	return rep.Symbols
}

func TestSymbolsExternJSON(t *testing.T) {
	symbols := symbolsJSON(t, externSrc)
	if len(symbols) != 1 {
		t.Fatalf("expected 1 symbol, got %d: %+v", len(symbols), symbols)
	}
	s := symbols[0]
	if s.Kind != "extern" {
		t.Errorf("kind = %q, want %q", s.Kind, "extern")
	}
	if s.Name != "get" {
		t.Errorf("name = %q", s.Name)
	}
	if s.Detail != "(ctx?: ctx, thing, fallback = null) -> any" {
		t.Errorf("detail = %q", s.Detail)
	}
	if s.Doc != "Read a value from a thing." {
		t.Errorf("doc = %q", s.Doc)
	}
	if s.Private {
		t.Error("an extern is never private")
	}
	// An extern has no body, so its range comes from SigRange. A zero end (line 0)
	// is the signature that FuncDecl.BodyRange was used by mistake.
	if s.Range.Line != 4 {
		t.Errorf("range starts at line %d, want 4", s.Range.Line)
	}
	if s.Range.EndLine != 4 {
		t.Errorf("range ends at line %d, want 4 (a zero end means BodyRange was used)", s.Range.EndLine)
	}
}

func TestSymbolsExternText(t *testing.T) {
	c := rootCmd()
	var out, errb bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errb)
	c.SetIn(strings.NewReader(externSrc))
	c.SetArgs([]string{"symbols", "-", "--filename", "x.cty"})
	if err := c.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "x.cty:4: extern get(ctx?: ctx, thing, fallback = null) -> any\n"
	if out.String() != want {
		t.Fatalf("symbols =\n%q\nwant\n%q", out.String(), want)
	}
}

// An extern file compiles nothing, so `check` has nothing to reject. In
// particular the unregistered type `ctx` must not fail: the CLI registers no host
// types, and an extern names its host's.
func TestCheckExternFile(t *testing.T) {
	path := writeCty(t, "e.cty", externSrc)
	out, _, err := execCLI(t, "check", path)
	if err != nil {
		t.Fatalf("check failed on an extern file: %v", err)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("check output = %q, want ok", out)
	}
}

// An extern is *expected* to name a function the host already provides — that is
// its whole purpose. So neither the reserved-name check nor the shadow warning may
// fire on one, even for a baseline builtin like upper().
func TestExternMayNameABaselineFunction(t *testing.T) {
	path := writeCty(t, "e.cty", "//functy:extern\n\nfunc upper(s: string) -> string\n")
	out, errb, err := execCLI(t, "check", path)
	if err != nil {
		t.Fatalf("declaring an extern for a baseline function must be allowed: %v\n%s", err, errb)
	}
	if strings.Contains(errb, "reserved") || strings.Contains(errb, "shadow") {
		t.Fatalf("unexpected reserved-name or shadow diagnostic:\n%s", errb)
	}
	if !strings.Contains(out, "ok") {
		t.Fatalf("check output = %q, want ok", out)
	}
}
