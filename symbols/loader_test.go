package symbols

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
)

// countingLoader returns a SourceLoader serving in-memory units from m,
// recording each invocation (argument and count) in calls. Unknown sources
// return a host-flavored error diagnostic.
func countingLoader(m map[string][]functy.Source, calls map[string]int) SourceLoader {
	return func(source string) ([]functy.Source, hcl.Diagnostics) {
		calls[source]++
		srcs, ok := m[source]
		if !ok {
			return nil, hcl.Diagnostics{&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Symbol library not found",
				Detail:   "No library named " + source + " (host-flavored).",
			}}
		}
		return srcs, nil
	}
}

func libSources(t *testing.T) []functy.Source {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "lib", "lib.cty"))
	if err != nil {
		t.Fatalf("reading fixture: %s", err)
	}
	return []functy.Source{{Filename: "mem/lib.cty", Bytes: b}}
}

func TestBuild_LoaderHappyPath(t *testing.T) {
	// Loader-provided sources for two blocks and two labels produce the same
	// Built shape as the on-disk equivalent (TestBuild_SharedSourceTwoLabels).
	calls := map[string]int{}
	built, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(countingLoader(map[string][]functy.Source{"lib": libSources(t)}, calls)).
		WithBlocks(
			SymbolsBlock{Label: "a", Source: "lib"},
			SymbolsBlock{Label: "b", Source: "lib"},
		).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}
	for _, k := range []string{"symbols::a::non_empty", "symbols::a::shout", "symbols::b::non_empty", "symbols::b::shout"} {
		if _, ok := built.Functions[k]; !ok {
			t.Errorf("missing %s, got %v", k, keys(built.Functions))
		}
	}
	for _, label := range []string{"a", "b"} {
		if _, ok := built.Type(label, "items"); !ok {
			t.Errorf("missing Type(%s, items)", label)
		}
		if g := built.Symbols.GetAttr(label).GetAttr("greeting"); g.AsString() != "HI" {
			t.Errorf("symbols.%s.greeting = %#v, want \"HI\"", label, g)
		}
	}
	// Built.Files is keyed by the loader-provided Filename, verbatim.
	if _, ok := built.Files["mem/lib.cty"]; !ok {
		t.Errorf("expected Built.Files key mem/lib.cty, got %v", fileKeys(built.Files))
	}
	for k := range built.Files {
		if k != "mem/lib.cty" {
			t.Errorf("unexpected Built.Files key %q (must be loader-provided Filename verbatim)", k)
		}
	}
}

func TestBuild_LoaderCalledOncePerSource(t *testing.T) {
	calls := map[string]int{}
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(countingLoader(map[string][]functy.Source{"lib": libSources(t)}, calls)).
		WithBlocks(
			SymbolsBlock{Label: "a", Source: "lib"},
			SymbolsBlock{Label: "b", Source: "lib"},
		).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}
	if calls["lib"] != 1 {
		t.Errorf("loader called %d times for \"lib\", want 1", calls["lib"])
	}
}

func TestBuild_LoaderFailureCachedAndReportedOnce(t *testing.T) {
	calls := map[string]int{}
	built, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(countingLoader(map[string][]functy.Source{}, calls)).
		WithBlocks(
			SymbolsBlock{Label: "a", Source: "bad"},
			SymbolsBlock{Label: "b", Source: "bad"},
		).
		Build()
	if calls["bad"] != 1 {
		t.Errorf("loader called %d times for \"bad\", want 1 (failures must be cached)", calls["bad"])
	}
	n := 0
	for _, d := range diags {
		if d.Summary == "Symbol library not found" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("loader error reported %d times, want exactly 1: %s", n, diags)
	}
	if len(built.Functions) != 0 {
		t.Errorf("failed unit must project nothing, got %v", keys(built.Functions))
	}
}

func TestBuild_LoaderDiagnosticsVerbatim(t *testing.T) {
	// The Builder must pass loader diagnostics through untouched: same object,
	// no wrapping, no re-summarizing, no substituted subject.
	subject := hcl.Range{
		Filename: "host-config.tf",
		Start:    hcl.Pos{Line: 7, Column: 3, Byte: 120},
		End:      hcl.Pos{Line: 7, Column: 20, Byte: 137},
	}
	want := &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  "Symbol library directory not found",
		Detail:   "Module \"x\" has no directory named lib/.",
		Subject:  subject.Ptr(),
	}
	loader := func(source string) ([]functy.Source, hcl.Diagnostics) {
		return nil, hcl.Diagnostics{want}
	}
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(loader).
		WithBlocks(SymbolsBlock{Label: "a", Source: "lib"}).
		Build()
	if len(diags) != 1 {
		t.Fatalf("want exactly the loader's diagnostic, got %d: %s", len(diags), diags)
	}
	if diags[0] != want {
		t.Errorf("loader diagnostic was not passed through verbatim: got %#v", diags[0])
	}
}

func TestBuild_NoLoaderIsError(t *testing.T) {
	// The loader is required: a Builder with blocks but no loader reports one
	// error and projects nothing.
	built, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithBlocks(SymbolsBlock{Label: "a", Source: "lib"}).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected an error for a missing source loader")
	}
	if !hasSummary(diags, "No source loader configured") {
		t.Errorf("expected No source loader configured, got %s", diags)
	}
	if len(built.Functions) != 0 {
		t.Errorf("expected no functions, got %v", keys(built.Functions))
	}
}

func TestBuild_ShadowWarning(t *testing.T) {
	// An exported global-namespace function colliding with a base-table name
	// warns exactly once, with the declaration as subject.
	src := []functy.Source{{
		Filename: "mem/shadow.cty",
		Bytes:    []byte("func upper(s: string) -> string {\n    return s\n}\n"),
	}}
	loader := func(source string) ([]functy.Source, hcl.Diagnostics) { return src, nil }
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(loader).
		WithBlocks(
			SymbolsBlock{Label: "a", Source: "s"},
			SymbolsBlock{Label: "b", Source: "s"},
		).
		Build()
	if diags.HasErrors() {
		t.Fatalf("shadowing must warn, not error: %s", diags)
	}
	var warns []*hcl.Diagnostic
	for _, d := range diags {
		if d.Summary == "Library function shadows a base function" {
			warns = append(warns, d)
		}
	}
	if len(warns) != 1 {
		t.Fatalf("want exactly 1 shadow warning (per unit, even with two blocks), got %d: %s", len(warns), diags)
	}
	w := warns[0]
	if w.Severity != hcl.DiagWarning {
		t.Errorf("shadow diagnostic severity = %v, want warning", w.Severity)
	}
	if w.Subject == nil || w.Subject.Filename != "mem/shadow.cty" || w.Subject.Start.Line != 1 {
		t.Errorf("shadow warning subject = %v, want the declaration range in mem/shadow.cty line 1", w.Subject)
	}
}

func TestBuild_NoShadowWarningForNamespacedFunction(t *testing.T) {
	// ns::upper cannot collide with the bare base name upper: no warning.
	src := []functy.Source{{
		Filename: "mem/ns.cty",
		Bytes:    []byte("namespace ns\n\nfunc upper(s: string) -> string {\n    return s\n}\n"),
	}}
	loader := func(source string) ([]functy.Source, hcl.Diagnostics) { return src, nil }
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(loader).
		WithBlocks(SymbolsBlock{Label: "a", Source: "s", Namespace: "ns"}).
		Build()
	if diags.HasErrors() {
		t.Fatalf("unexpected diagnostics: %s", diags)
	}
	if hasSummary(diags, "Library function shadows a base function") {
		t.Errorf("namespaced function must not trigger a shadow warning: %s", diags)
	}
}

func TestBuild_NamespaceNotFound(t *testing.T) {
	// A namespace with no declarations is a bind-time error listing what exists.
	src := []functy.Source{
		{Filename: "mem/global.cty", Bytes: []byte("const g = 1\n")},
		{Filename: "mem/a.cty", Bytes: []byte("namespace a\n\nconst x = 2\n")},
	}
	loader := func(source string) ([]functy.Source, hcl.Diagnostics) { return src, nil }
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(loader).
		WithBlocks(SymbolsBlock{Label: "oops", Source: "s", Namespace: "nope"}).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected a namespace-not-found error")
	}
	var errs []*hcl.Diagnostic
	for _, d := range diags {
		if d.Severity == hcl.DiagError {
			errs = append(errs, d)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("want exactly 1 error, got %d: %s", len(errs), diags)
	}
	e := errs[0]
	if e.Summary != "Symbols namespace has no declarations" {
		t.Errorf("summary = %q", e.Summary)
	}
	if !strings.Contains(e.Detail, "(global)") || !strings.Contains(e.Detail, "a") {
		t.Errorf("detail should list available namespaces (global) and a, got %q", e.Detail)
	}
}

func TestBuild_EmptyUnitGlobalBindIsError(t *testing.T) {
	// A loader returning zero sources yields an empty unit; binding even the
	// global namespace against it errors — this is how "your source directory
	// has no .cty files" surfaces at bind time.
	loader := func(source string) ([]functy.Source, hcl.Diagnostics) { return nil, nil }
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(loader).
		WithBlocks(SymbolsBlock{Label: "a", Source: "empty"}).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected an error binding the global namespace of an empty unit")
	}
	if !hasSummary(diags, "Symbols namespace has no declarations") {
		t.Errorf("expected Symbols namespace has no declarations, got %s", diags)
	}
}

func TestBuild_FullyPrivateNamespaceIsDistinctError(t *testing.T) {
	// A namespace that exists but holds only _-private declarations gets the
	// distinguished "exports nothing" message.
	src := []functy.Source{{
		Filename: "mem/priv.cty",
		Bytes:    []byte("namespace p\n\nfunc _x(s: string) -> string {\n    return s\n}\n"),
	}}
	loader := func(source string) ([]functy.Source, hcl.Diagnostics) { return src, nil }
	_, diags := NewBuilder().
		WithBaseFunctions(baseFuncs()).
		WithSourceLoader(loader).
		WithBlocks(SymbolsBlock{Label: "p", Source: "s", Namespace: "p"}).
		Build()
	if !diags.HasErrors() {
		t.Fatalf("expected an error binding a fully-private namespace")
	}
	found := false
	for _, d := range diags {
		if d.Summary == "Symbols namespace has no declarations" && strings.Contains(d.Detail, "only private") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the exports-nothing variant, got %s", diags)
	}
}

func fileKeys(m map[string]*hcl.File) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
