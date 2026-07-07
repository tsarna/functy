package repl

import (
	"io"
	"strings"
	"testing"
)

func TestEvalExpression(t *testing.T) {
	s, out, errOut := newTestSession(t)

	s.evalExpr("1 + 1")
	if got := out.String(); got != "2\n" {
		t.Fatalf("1 + 1 => %q, want %q", got, "2\n")
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	if s.resultCounter != 1 {
		t.Fatalf("resultCounter = %d, want 1", s.resultCounter)
	}

	// "_" and "_1" carry the prior result.
	out.Reset()
	s.evalExpr("_ + 1")
	if got := out.String(); got != "3\n" {
		t.Fatalf("_ + 1 => %q, want %q", got, "3\n")
	}
	out.Reset()
	s.evalExpr("_1")
	if got := out.String(); got != "2\n" {
		t.Fatalf("_1 => %q, want %q", got, "2\n")
	}
	if s.resultCounter != 3 {
		t.Fatalf("resultCounter = %d, want 3", s.resultCounter)
	}
}

func TestEvalFunctionsAndVariables(t *testing.T) {
	s, out, _ := newTestSession(t)

	// A function resolves up the eval-context chain.
	s.evalExpr(`length(["a", "b", "c"])`)
	if got := out.String(); got != "3\n" {
		t.Fatalf("length => %q, want %q", got, "3\n")
	}

	// A host context variable is visible.
	out.Reset()
	s.evalExpr("greeting")
	if got := out.String(); got != `"hi"`+"\n" {
		t.Fatalf("greeting => %q, want %q", got, `"hi"`+"\n")
	}
}

func TestEvalNullSuppressed(t *testing.T) {
	s, out, errOut := newTestSession(t)

	s.evalExpr("null")
	if out.Len() != 0 {
		t.Fatalf("top-level null printed %q, want nothing", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	if s.resultCounter != 0 {
		t.Fatalf("resultCounter = %d, want 0 (null not numbered)", s.resultCounter)
	}
	if _, ok := s.bindings["_"]; ok {
		t.Fatalf("null result rebound _")
	}
}

func TestEvalErrorNotNumbered(t *testing.T) {
	s, out, errOut := newTestSession(t)

	// Seed a successful result first.
	s.evalExpr("42")
	out.Reset()

	s.evalExpr("1 +") // parse error
	if out.Len() != 0 {
		t.Fatalf("error produced stdout: %q", out.String())
	}
	if errOut.Len() == 0 {
		t.Fatal("expected diagnostics on stderr")
	}
	if s.resultCounter != 1 {
		t.Fatalf("resultCounter = %d, want 1 (error must not advance it)", s.resultCounter)
	}
	if got := s.bindings["_"].AsBigFloat().Text('f', -1); got != "42" {
		t.Fatalf("_ changed after error: %q, want 42", got)
	}
}

func TestPromptTracksResultCounter(t *testing.T) {
	s, _, _ := newTestSession(t)

	// Before any result, the next result is _1.
	if got := s.primaryPrompt(); got != "1> " {
		t.Fatalf("initial prompt = %q, want %q", got, "1> ")
	}
	// Continuation is dotted and width-matched to the primary prompt.
	if got := s.continuationPrompt(); got != ".. " {
		t.Fatalf("initial continuation = %q, want %q", got, ".. ")
	}

	// A successful, non-null result advances the prompt.
	s.evalExpr("42")
	if got := s.primaryPrompt(); got != "2> " {
		t.Fatalf("prompt after one result = %q, want %q", got, "2> ")
	}

	// A top-level null does not advance it.
	s.evalExpr("null")
	if got := s.primaryPrompt(); got != "2> " {
		t.Fatalf("prompt after null = %q, want %q (unchanged)", got, "2> ")
	}

	// Width-matching scales with the number of digits.
	s.resultCounter = 11
	if got := s.primaryPrompt(); got != "12> " {
		t.Fatalf("prompt = %q, want %q", got, "12> ")
	}
	if got := s.continuationPrompt(); got != "... " {
		t.Fatalf("continuation = %q, want %q", got, "... ")
	}
}

// TestHostMetaCommand verifies a host-registered meta-command is dispatched,
// can consume args, can signal exit, and appears in :help completion candidates.
func TestHostMetaCommand(t *testing.T) {
	var out, errOut strings.Builder
	var gotArgs []string
	s := New(NewStaticHost(testEvalContext()), Options{
		Out:    &out,
		ErrOut: &errOut,
		Meta: []MetaCommand{{
			Names:   []string{":ping"},
			Summary: "a test command",
			Run: func(args []string, _ io.Writer) bool {
				gotArgs = args
				return args != nil && len(args) == 1 && args[0] == "bye"
			},
		}},
	})

	if s.handleMeta(":ping now") {
		t.Fatal(":ping now should not exit")
	}
	if len(gotArgs) != 1 || gotArgs[0] != "now" {
		t.Fatalf(":ping args = %v, want [now]", gotArgs)
	}
	if !s.handleMeta(":ping bye") {
		t.Fatal(":ping bye should exit")
	}

	// The command name is offered for completion.
	if got := s.completions(":pi"); len(got) == 0 || got[0] != ":ping" {
		t.Fatalf("completions(:pi) = %v, want [:ping]", got)
	}
}
