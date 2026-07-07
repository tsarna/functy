package repl

import (
	"strings"
	"testing"
)

func TestParseAssignment(t *testing.T) {
	tests := []struct {
		in       string
		wantOK   bool
		wantName string
	}{
		{"x = 1", true, "x"},
		{"y=2", true, "y"},
		{"  spaced  =  3", true, "spaced"},
		{"x == 1", false, ""},  // comparison, not assignment
		{"x >= 1", false, ""},  // comparison
		{"1 + 1", false, ""},   // expression
		{"x.y = 1", false, ""}, // not a bare identifier
		{`{a = 1}`, false, ""}, // object literal
		{"length(x)", false, ""},
	}
	for _, tt := range tests {
		name, _, ok := parseAssignment(tt.in)
		if ok != tt.wantOK || name != tt.wantName {
			t.Errorf("parseAssignment(%q) = (%q, %v), want (%q, %v)", tt.in, name, ok, tt.wantName, tt.wantOK)
		}
	}
}

func TestBareAssignmentBinds(t *testing.T) {
	s, out, _ := newTestSession(t)

	name, rhs, ok := parseAssignment("users = [1, 2, 3]")
	if !ok {
		t.Fatal("expected assignment")
	}
	s.setBinding(name, rhs)
	if got := out.String(); got != "[1, 2, 3]\n" {
		t.Fatalf("assignment echo = %q, want %q", got, "[1, 2, 3]\n")
	}

	out.Reset()
	s.evalExpr("length(users)")
	if got := out.String(); got != "3\n" {
		t.Fatalf("length(users) = %q, want %q", got, "3\n")
	}
}

func TestSetMetaCommand(t *testing.T) {
	s, _, _ := newTestSession(t)
	s.handleMeta(":set greeting2 = \"hi\"")
	if v, ok := s.bindings["greeting2"]; !ok || v.AsString() != "hi" {
		t.Fatalf("greeting2 binding = %v, ok=%v", v, ok)
	}
}

// TestReservedNamesRejected checks the managed result names plus any name the
// host reserves (here: the static host's context variables).
func TestReservedNamesRejected(t *testing.T) {
	s, _, errOut := newTestSession(t)

	for _, name := range []string{"_", "_1", "greeting", "env"} {
		errOut.Reset()
		s.setBinding(name, "1")
		if errOut.Len() == 0 {
			t.Errorf("assigning %q should be rejected", name)
		}
		if _, ok := s.bindings[name]; ok {
			t.Errorf("reserved name %q was bound anyway", name)
		}
	}
}

func TestNullRHSCreatesBindingNotNumbered(t *testing.T) {
	s, out, _ := newTestSession(t)

	s.setBinding("x", "null")
	if v, ok := s.bindings["x"]; !ok || !v.IsNull() {
		t.Fatalf("x should be a null binding, got %v ok=%v", v, ok)
	}
	if s.resultCounter != 0 {
		t.Fatalf("null RHS advanced result counter to %d", s.resultCounter)
	}
	if _, ok := s.bindings["_"]; ok {
		t.Fatal("null RHS rebound _")
	}
	if out.Len() != 0 {
		t.Fatalf("null RHS printed %q", out.String())
	}
}

func TestUnset(t *testing.T) {
	s, _, errOut := newTestSession(t)
	s.setBinding("x", "1")

	s.handleMeta(":unset x")
	if _, ok := s.bindings["x"]; ok {
		t.Fatal(":unset x did not remove the binding")
	}

	errOut.Reset()
	s.handleMeta(":unset _")
	if !strings.Contains(errOut.String(), "managed") {
		t.Fatalf(":unset _ should be rejected, got %q", errOut.String())
	}

	errOut.Reset()
	s.handleMeta(":unset nope")
	if !strings.Contains(errOut.String(), "no such binding") {
		t.Fatalf(":unset nope = %q", errOut.String())
	}
}

func TestVars(t *testing.T) {
	s, out, _ := newTestSession(t)

	s.handleMeta(":vars")
	if got := out.String(); !strings.Contains(got, "no bindings") {
		t.Fatalf("empty :vars = %q", got)
	}

	s.setBinding("a", "1")
	s.setBinding("b", `"hello"`)
	out.Reset()
	s.handleMeta(":vars")
	got := out.String()
	if !strings.Contains(got, "a") || !strings.Contains(got, "b") || !strings.Contains(got, "string") {
		t.Fatalf(":vars output missing entries: %q", got)
	}
	// The managed _N results must not appear.
	if strings.Contains(got, "_1") {
		t.Fatalf(":vars leaked managed result names: %q", got)
	}
}

func TestQuitCommands(t *testing.T) {
	s, _, _ := newTestSession(t)
	for _, q := range []string{":quit", ":q", ":exit"} {
		if !s.handleMeta(q) {
			t.Errorf("%q should exit", q)
		}
	}
	if s.handleMeta(":help") {
		t.Error(":help should not exit")
	}
}
