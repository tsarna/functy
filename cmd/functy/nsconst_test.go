package main

import (
	"strings"
	"testing"
)

// Two namespaces in one unit each declaring `const greeting` no longer collide:
// each namespace's function resolves its own const. On the flat pre-namespaced
// evaluator this failed to load with a duplicate-declaration diagnostic.
func TestRunNamespaceConstsDoNotCollide(t *testing.T) {
	dir := writeCtyDir(t, map[string]string{
		"foo.cty": "namespace foo\nconst greeting = \"hello\"\nfunc greet(who: string) -> string { return \"${greeting} ${who}\" }\n",
		"bar.cty": "namespace bar\nconst greeting = \"goodbye\"\nfunc greet(who: string) -> string { return \"${greeting} ${who}\" }\n",
	})

	for _, tc := range []struct{ fn, want string }{
		{"foo::greet", "hello world"},
		{"bar::greet", "goodbye world"},
	} {
		out, _, err := execCLI(t, "run", "--func", tc.fn, "--output", "raw", dir, "--", `"world"`)
		if err != nil {
			t.Fatalf("running %s: %v", tc.fn, err)
		}
		if strings.TrimSpace(out) != tc.want {
			t.Errorf("%s = %q, want %q", tc.fn, strings.TrimSpace(out), tc.want)
		}
	}
}

// A namespaced function sees a global (unnamespaced) const by bare name (own+global).
func TestRunNamespaceSeesGlobalConst(t *testing.T) {
	dir := writeCtyDir(t, map[string]string{
		"g.cty":   "const tld = \"com\"\n",
		"foo.cty": "namespace foo\nfunc host(name: string) -> string { return \"${name}.${tld}\" }\n",
	})
	out, _, err := execCLI(t, "run", "--func", "foo::host", "--output", "raw", dir, "--", `"example"`)
	if err != nil {
		t.Fatalf("running foo::host: %v", err)
	}
	if strings.TrimSpace(out) != "example.com" {
		t.Errorf("foo::host = %q, want %q", strings.TrimSpace(out), "example.com")
	}
}
