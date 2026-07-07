package repl

import (
	"slices"
	"testing"
)

func TestCompletions(t *testing.T) {
	s, _, _ := newTestSession(t)
	s.setBinding("myvar", "1")

	tests := []struct {
		word       string
		wantSubset []string
		wantNone   bool
	}{
		{word: ":he", wantSubset: []string{":help"}},
		{word: ":q", wantSubset: []string{":quit"}},
		{word: "gre", wantSubset: []string{"greeting"}},
		{word: "up", wantSubset: []string{"upper"}},
		{word: "len", wantSubset: []string{"length"}},
		{word: "myv", wantSubset: []string{"myvar"}},
		{word: "zzz_no_match", wantNone: true},
	}

	for _, tt := range tests {
		got := s.completions(tt.word)
		if tt.wantNone {
			if len(got) != 0 {
				t.Errorf("completions(%q) = %v, want none", tt.word, got)
			}
			continue
		}
		for _, want := range tt.wantSubset {
			if !slices.Contains(got, want) {
				t.Errorf("completions(%q) = %v, missing %q", tt.word, got, want)
			}
		}
	}

	// Managed result names are never offered.
	s.bindings["_1"] = s.bindings["myvar"]
	if slices.Contains(s.completions("_"), "_1") {
		t.Error("completions leaked managed result name _1")
	}
}

func TestCompleterDo(t *testing.T) {
	s, _, _ := newTestSession(t)
	c := &completer{s: s}

	// "up" → "upper": suffix "per", prefix length 2.
	line := []rune("up")
	got, length := c.Do(line, len(line))
	if length != 2 {
		t.Fatalf("Do length = %d, want 2", length)
	}
	if !slices.ContainsFunc(got, func(r []rune) bool { return string(r) == "per" }) {
		t.Fatalf("Do(%q) suffixes = %v, want to contain \"per\"", string(line), runesToStrings(got))
	}

	// After a '.', the final segment is completed against the base's attributes:
	// "env." → the env keys, replacing nothing (length 0).
	dotLine := []rune("env.")
	got, length = c.Do(dotLine, len(dotLine))
	if length != 0 {
		t.Fatalf("Do after '.' length = %d, want 0", length)
	}
	if !slices.ContainsFunc(got, func(r []rune) bool { return string(r) == "HOME" }) {
		t.Fatalf("Do(%q) = %v, want to contain \"HOME\"", string(dotLine), runesToStrings(got))
	}
}

func TestNestedCompletions(t *testing.T) {
	s, _, _ := newTestSession(t)

	// Object attributes, with prefix filtering.
	envAttrs := s.attrCompletions("env", "")
	if !slices.Contains(envAttrs, "HOME") || !slices.Contains(envAttrs, "PATH") {
		t.Fatalf(`attrCompletions("env","") = %v, want to contain HOME and PATH`, envAttrs)
	}
	if got := s.attrCompletions("env", "HO"); !slices.Equal(got, []string{"HOME"}) {
		t.Fatalf(`attrCompletions("env","HO") = %v, want [HOME]`, got)
	}

	// Deep walk through nested objects (from the host context).
	if got := s.attrCompletions("nested", ""); !slices.Equal(got, []string{"outer"}) {
		t.Fatalf(`attrCompletions("nested","") = %v, want [outer]`, got)
	}
	if got := s.attrCompletions("nested.outer", ""); !slices.Equal(got, []string{"inner"}) {
		t.Fatalf(`attrCompletions("nested.outer","") = %v, want [inner]`, got)
	}

	// Deep walk via a session binding.
	s.setBinding("m", `{a = {b = 1}}`)
	if got := s.attrCompletions("m", ""); !slices.Equal(got, []string{"a"}) {
		t.Fatalf(`attrCompletions("m","") = %v, want [a]`, got)
	}
	if got := s.attrCompletions("m.a", ""); !slices.Equal(got, []string{"b"}) {
		t.Fatalf(`attrCompletions("m.a","") = %v, want [b]`, got)
	}

	// An unknown base resolves to nothing.
	if got := s.attrCompletions("nope", ""); got != nil {
		t.Errorf(`attrCompletions("nope","") = %v, want nil`, got)
	}
}

func runesToStrings(rs [][]rune) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
