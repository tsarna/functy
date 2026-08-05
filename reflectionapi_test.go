package functy

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// The exported reflection primitives: the pieces help() is assembled from, so a
// host can render a function its own way — a structured signature, a parameter
// table, a hyperlinked page — instead of embedding help()'s text as a blob.

func parseForReflection(t *testing.T, src string) *Result {
	t.Helper()
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	return res
}

func TestLookupFuncDecls(t *testing.T) {
	res := parseForReflection(t, "// Doubles a number.\nfunc twice(n: number) -> number { return n * 2 }\n")

	got := res.LookupFuncDecls("twice")
	if len(got) != 1 {
		t.Fatalf("LookupFuncDecls(twice) returned %d decls, want 1", len(got))
	}
	if got[0].Name != "twice" {
		t.Errorf("name = %q, want twice", got[0].Name)
	}
	if got[0].Doc != "Doubles a number." {
		t.Errorf("doc = %q", got[0].Doc)
	}
	if res.LookupFuncDecls("nope") != nil {
		t.Error("LookupFuncDecls found a function that does not exist")
	}
}

// A nil Result is what a host has before it has parsed anything; every method
// must tolerate it, because help() itself does.
func TestReflectionMethodsTolerateANilResult(t *testing.T) {
	var res *Result

	if got := res.LookupFuncDecls("x"); got != nil {
		t.Errorf("LookupFuncDecls = %v, want nil", got)
	}
	if got := res.LookupBareFuncDecls("x"); got != nil {
		t.Errorf("LookupBareFuncDecls = %v, want nil", got)
	}
	if got := res.BareNameCandidates("x"); len(got) != 0 {
		t.Errorf("BareNameCandidates = %v, want empty", got)
	}
	if got := res.FuncNames(nil); len(got) != 0 {
		t.Errorf("FuncNames = %v, want empty", got)
	}
}

// An extern name may carry several signatures. The set is the unit of lookup,
// so a host rendering one form per signature gets them all.
func TestLookupFuncDeclsReturnsAnOverloadSet(t *testing.T) {
	res := parseForReflection(t, "//functy:extern\n\n"+
		"// Parse a timestamp.\n"+
		"func parsetime(s: string) -> string\n"+
		"func parsetime(format: string, s: string) -> string\n")

	got := res.LookupFuncDecls("parsetime")
	if len(got) != 2 {
		t.Fatalf("LookupFuncDecls(parsetime) returned %d decls, want 2 (an overload set)", len(got))
	}
	if len(got[0].Params) != 1 || len(got[1].Params) != 2 {
		t.Errorf("forms out of source order: %d and %d params", len(got[0].Params), len(got[1].Params))
	}
}

// Search order across the declaration sets: parsed functions, then file
// externs, then host externs.
//
// Parsing cannot produce these collisions — checkExternNames rejects an extern
// that duplicates a function, and a duplicate extern too — so the Result is
// assembled by hand, which is also the only way a host can hit this. Pinning the
// order says which declaration wins if one ever does, rather than leaving it to
// map iteration.
func TestLookupFuncDeclsSearchOrder(t *testing.T) {
	decl := func(doc string) *FuncDecl {
		return &FuncDecl{Name: "f", Doc: doc, Params: []Param{{Name: "a"}}}
	}
	res := &Result{
		Funcs:       []*FuncDecl{decl("parsed")},
		Externs:     []*FuncDecl{decl("file extern")},
		HostExterns: []*FuncDecl{decl("host extern")},
	}

	// A parsed function is searched first and answers alone: the externs are a
	// separate set, never reached once this one matched.
	t.Run("a parsed function wins outright", func(t *testing.T) {
		got := res.LookupFuncDecls("f")
		if len(got) != 1 || got[0].Doc != "parsed" {
			t.Fatalf("resolved to %s, want just the parsed function", docsOf(got))
		}
	})

	// The two extern sets are one search set, not a precedence pair: both
	// declarations survive, as the overload set of that name, with the one
	// having an editable source first.
	t.Run("the extern sets merge in order", func(t *testing.T) {
		got := (&Result{Externs: res.Externs, HostExterns: res.HostExterns}).LookupFuncDecls("f")
		if !reflect.DeepEqual(docsOf(got), []string{"file extern", "host extern"}) {
			t.Fatalf("resolved to %s, want [file extern host extern]", docsOf(got))
		}
	})

	t.Run("a host extern alone answers", func(t *testing.T) {
		got := (&Result{HostExterns: res.HostExterns}).LookupFuncDecls("f")
		if len(got) != 1 || got[0].Doc != "host extern" {
			t.Fatalf("resolved to %s", docsOf(got))
		}
	})
}

func docsOf(fns []*FuncDecl) []string {
	out := make([]string, len(fns))
	for i, fn := range fns {
		out[i] = fn.Doc
	}
	return out
}

func TestLookupBareFuncDecls(t *testing.T) {
	res := parseForReflection(t, "namespace ns\n\nfunc baz(a) -> any { return a }\n")

	// The qualified name is the primary spelling.
	if got := res.LookupFuncDecls("ns::baz"); len(got) != 1 {
		t.Fatalf("LookupFuncDecls(ns::baz) returned %d, want 1", len(got))
	}
	// The bare name resolves it too, because it is unambiguous.
	if got := res.LookupBareFuncDecls("baz"); len(got) != 1 {
		t.Fatalf("LookupBareFuncDecls(baz) returned %d, want 1", len(got))
	}
	// A bare lookup is not a qualified one.
	if got := res.LookupFuncDecls("baz"); got != nil {
		t.Error("LookupFuncDecls resolved a bare name; that is LookupBareFuncDecls's job")
	}
}

// The distinction a host cannot otherwise make: "ambiguous" and "no such
// function" both come back as an empty set, and only the candidates tell them
// apart. Reporting them the same way is what makes a mistyped name and a name
// needing a namespace look identical.
func TestBareNameCandidatesDistinguishAmbiguityFromAbsence(t *testing.T) {
	res, diags := NewParser().ParseAll([]Source{
		{Filename: "a.cty", Bytes: []byte("namespace a\n\nfunc dup(x) -> any { return x }\n")},
		{Filename: "b.cty", Bytes: []byte("namespace b\n\nfunc dup(x) -> any { return x }\n")},
	})
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}

	if got := res.LookupBareFuncDecls("dup"); got != nil {
		t.Errorf("an ambiguous bare name resolved to %d decls; it must resolve to none", len(got))
	}
	got := res.BareNameCandidates("dup")
	if !reflect.DeepEqual(got, []string{"a::dup", "b::dup"}) {
		t.Errorf("BareNameCandidates(dup) = %v, want [a::dup b::dup]", got)
	}

	// A name that is declared nowhere has no candidates at all.
	if got := res.BareNameCandidates("nope"); len(got) != 0 {
		t.Errorf("BareNameCandidates(nope) = %v, want empty", got)
	}
	// And one that resolves has exactly one.
	if got := res.BareNameCandidates("dup"); len(got) < 2 {
		t.Errorf("expected the ambiguous name to report every candidate, got %v", got)
	}
}

func TestFuncNamesUnionsDeclarationsWithTheEvalContext(t *testing.T) {
	stub := function.New(&function.Spec{
		Type: function.StaticReturnType(cty.String),
		Impl: func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{"upper": stub}}

	res := parseForReflection(t, "//functy:extern\n\nfunc parsetime(s: string) -> string\n")
	got := res.FuncNames(func() *hcl.EvalContext { return ctx })

	// An extern is by definition absent from the context, so the union is what
	// makes it visible at all.
	for _, want := range []string{"parsetime", "upper"} {
		if !slices.Contains(got, want) {
			t.Errorf("FuncNames = %v, missing %q", got, want)
		}
	}
	if !slices.IsSorted(got) {
		t.Errorf("FuncNames = %v, want sorted", got)
	}

	// Without a context, the declarations alone.
	if got := res.FuncNames(nil); !reflect.DeepEqual(got, []string{"parsetime"}) {
		t.Errorf("FuncNames(nil) = %v, want [parsetime]", got)
	}
}

func TestFuncNamesOmitsPrivateFunctions(t *testing.T) {
	res := parseForReflection(t, "func _helper(a) -> any { return a }\nfunc shown(a) -> any { return a }\n")

	got := res.FuncNames(nil)
	if slices.Contains(got, "_helper") {
		t.Errorf("FuncNames listed a private function: %v", got)
	}
	if !slices.Contains(got, "shown") {
		t.Errorf("FuncNames = %v, missing shown", got)
	}

	// It is still reachable by name: help() is a developer tool, and
	// help("_helper") when debugging one is exactly what you want.
	if d := res.LookupFuncDecls("_helper"); len(d) != 1 {
		t.Error("a private function must still be documentable by name")
	}
}

func TestRenderFuncHelp(t *testing.T) {
	res := parseForReflection(t, "//functy:extern\n\n"+
		"// Read a value from a thing.\n"+
		"func get(\n"+
		"    ctx?: ctx,        // optional context\n"+
		"    thing,            // the thing to read\n"+
		"    fallback = null,  // returned when absent\n"+
		") -> any\n")

	want := "get(ctx?: ctx, thing, fallback = null) -> any\n\n" +
		"Read a value from a thing.\n\n" +
		"Parameters:\n" +
		"  ctx?      optional context\n" +
		"  thing     the thing to read\n" +
		"  fallback  returned when absent"

	if got := RenderFuncHelp(res.LookupFuncDecls("get")); got != want {
		t.Errorf("RenderFuncHelp =\n%q\nwant\n%q", got, want)
	}
	if got := RenderFuncHelp(nil); got != "" {
		t.Errorf("RenderFuncHelp(nil) = %q, want empty", got)
	}
}

func TestRenderCtyHelp(t *testing.T) {
	f := function.New(&function.Spec{
		Description: "Upper-cases a string.",
		Params:      []function.Parameter{{Name: "s", Type: cty.String, Description: "the string"}},
		Type:        function.StaticReturnType(cty.String),
		Impl:        func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})

	got := RenderCtyHelp("upper", f)
	for _, want := range []string{"upper(s: string) -> string", "Upper-cases a string.", "the string"} {
		if !strings.Contains(got, want) {
			t.Errorf("RenderCtyHelp =\n%s\nmissing %q", got, want)
		}
	}
}

func TestRenderFuncSignature(t *testing.T) {
	res := parseForReflection(t, "//functy:extern\n\n"+
		"func get(ctx?: ctx, thing, fallback = null, *rest: string) -> any\n")

	got := res.LookupFuncDecls("get")
	if len(got) != 1 {
		t.Fatalf("got %d decls, want 1", len(got))
	}
	// The markers a name carries, the annotated types, and the return type.
	if want := "get(ctx?: ctx, thing, fallback = null, *rest: string) -> any"; RenderFuncSignature(got[0]) != want {
		t.Errorf("RenderFuncSignature = %q, want %q", RenderFuncSignature(got[0]), want)
	}
}

// The signature is the line RenderFuncHelp leads with — that is what makes it
// safe for a host to render the two parts separately.
func TestRenderFuncSignatureIsHelpsFirstLine(t *testing.T) {
	res := parseForReflection(t, "//functy:extern\n\n"+
		"// Doc.\nfunc f(a: string, // about a\n) -> number\n")

	got := res.LookupFuncDecls("f")
	if len(got) != 1 {
		t.Fatalf("got %d decls, want 1", len(got))
	}
	sig := RenderFuncSignature(got[0])
	if help := RenderFuncHelp(got); !strings.HasPrefix(help, sig+"\n") {
		t.Errorf("RenderFuncHelp does not lead with the signature:\n sig: %q\nhelp: %q", sig, help)
	}
}

func TestParamDisplayName(t *testing.T) {
	res := parseForReflection(t, "//functy:extern\n\nfunc f(plain, opt?, *rest) -> any\n")
	decls := res.LookupFuncDecls("f")
	if len(decls) != 1 {
		t.Fatalf("got %d decls, want 1", len(decls))
	}

	var got []string
	for _, p := range decls[0].Params {
		got = append(got, p.DisplayName())
	}
	if !reflect.DeepEqual(got, []string{"plain", "opt?", "*rest"}) {
		t.Errorf("DisplayName = %v, want [plain opt? *rest]", got)
	}
}

func TestRenderCtySignature(t *testing.T) {
	f := function.New(&function.Spec{
		Params:   []function.Parameter{{Name: "s", Type: cty.String}},
		VarParam: &function.Parameter{Name: "rest", Type: cty.Number},
		Type:     function.StaticReturnType(cty.Bool),
		Impl:     func([]cty.Value, cty.Type) (cty.Value, error) { return cty.True, nil },
	})
	if want := "upper(s: string, *rest: number) -> bool"; RenderCtySignature("upper", f) != want {
		t.Errorf("RenderCtySignature = %q, want %q", RenderCtySignature("upper", f), want)
	}

	// A dynamic parameter carries no annotation: "any" is noise, not
	// information. It also makes cty's own ReturnType dynamic, so there is no
	// static return type left to state either.
	dyn := function.New(&function.Spec{
		Params: []function.Parameter{{Name: "s", Type: cty.String}, {Name: "x", Type: cty.DynamicPseudoType}},
		Type:   function.StaticReturnType(cty.Bool),
		Impl:   func([]cty.Value, cty.Type) (cty.Value, error) { return cty.True, nil },
	})
	if want := "f(s: string, x)"; RenderCtySignature("f", dyn) != want {
		t.Errorf("RenderCtySignature = %q, want %q", RenderCtySignature("f", dyn), want)
	}

	// Unnamed parameters are numbered.
	bare := function.New(&function.Spec{
		Params: []function.Parameter{{Type: cty.String}},
		Type:   function.StaticReturnType(cty.String),
		Impl:   func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})
	if want := "f(arg1: string) -> string"; RenderCtySignature("f", bare) != want {
		t.Errorf("RenderCtySignature = %q, want %q", RenderCtySignature("f", bare), want)
	}
}

// A return-type callback may panic on speculative arguments. Recovering is why
// a host cannot reproduce this signature itself.
func TestRenderCtySignatureSurvivesAPanickingReturnType(t *testing.T) {
	boom := function.New(&function.Spec{
		Params: []function.Parameter{{Name: "x", Type: cty.String}},
		Type:   func([]cty.Value) (cty.Type, error) { panic("boom in Type callback") },
		Impl:   func([]cty.Value, cty.Type) (cty.Value, error) { return cty.NullVal(cty.String), nil },
	})

	got := RenderCtySignature("boom", boom)
	if got != "boom(x: string)" {
		t.Errorf("RenderCtySignature = %q, want the signature with no return type", got)
	}
}

func TestRenderCtySignatureIsHelpsFirstLine(t *testing.T) {
	f := function.New(&function.Spec{
		Description: "Upper-cases a string.",
		Params:      []function.Parameter{{Name: "s", Type: cty.String, Description: "the string"}},
		Type:        function.StaticReturnType(cty.String),
		Impl:        func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})

	sig := RenderCtySignature("upper", f)
	if help := RenderCtyHelp("upper", f); !strings.HasPrefix(help, sig) {
		t.Errorf("RenderCtyHelp does not lead with the signature:\n sig: %q\nhelp: %q", sig, help)
	}
}

// The grammar is functy's own, so a rendered type round-trips as a type
// annotation. A host rendering types its own way produces a second grammar for
// one type system.
func TestTypeString(t *testing.T) {
	for _, tc := range []struct {
		ty   cty.Type
		want string
	}{
		{cty.String, "string"},
		{cty.Number, "number"},
		{cty.Bool, "bool"},
		{cty.DynamicPseudoType, "any"},
		{cty.List(cty.String), "list(string)"},
		{cty.Map(cty.Number), "map(number)"},
		{cty.Set(cty.Bool), "set(bool)"},
		{cty.Object(map[string]cty.Type{"a": cty.String}), "object({ a = string })"},
		{cty.Tuple([]cty.Type{cty.String, cty.Number}), "tuple([string, number])"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := TypeString(tc.ty); got != tc.want {
				t.Errorf("TypeString = %q, want %q", got, tc.want)
			}
		})
	}
}

// The primitives must compose back into exactly what help() returns — that is
// what makes them a decomposition of it rather than a second implementation
// free to drift.
func TestPrimitivesReproduceHelpFunc(t *testing.T) {
	hostGet := function.New(&function.Spec{
		Description: "Host-registered get.",
		Params:      []function.Parameter{{Name: "thing", Type: cty.DynamicPseudoType}},
		VarParam:    &function.Parameter{Name: "args", Type: cty.DynamicPseudoType},
		Type:        function.StaticReturnType(cty.DynamicPseudoType),
		Impl:        func([]cty.Value, cty.Type) (cty.Value, error) { return cty.NullVal(cty.DynamicPseudoType), nil },
	})
	upper := function.New(&function.Spec{
		Description: "Upper-cases a string.",
		Params:      []function.Parameter{{Name: "s", Type: cty.String}},
		Type:        function.StaticReturnType(cty.String),
		Impl:        func([]cty.Value, cty.Type) (cty.Value, error) { return cty.StringVal(""), nil },
	})
	ctx := &hcl.EvalContext{Functions: map[string]function.Function{"get": hostGet, "upper": upper}}
	evalCtxFn := func() *hcl.EvalContext { return ctx }

	res, diags := NewParser().ParseAll([]Source{
		{Filename: "externs.cty", Bytes: []byte("//functy:extern\n\n// The real signature.\nfunc get(ctx?: ctx, thing) -> any\n")},
		{Filename: "lib.cty", Bytes: []byte("namespace ns\n\n// Doubles.\nfunc twice(n: number) -> number { return n * 2 }\n")},
	})
	if diags.HasErrors() {
		t.Fatalf("parse: %s", diags.Error())
	}
	help := HelpFunc(res, evalCtxFn)

	// byPrimitives is the precedence LookupFuncDecls documents.
	byPrimitives := func(name string) (string, bool) {
		if d := res.LookupFuncDecls(name); len(d) > 0 {
			return RenderFuncHelp(d), true
		}
		if f, ok := ctx.Functions[name]; ok {
			return RenderCtyHelp(name, f), true
		}
		if d := res.LookupBareFuncDecls(name); len(d) > 0 {
			return RenderFuncHelp(d), true
		}
		return "", false
	}

	for _, name := range []string{"get", "upper", "ns::twice", "twice", "nope"} {
		t.Run(name, func(t *testing.T) {
			got, err := help.Call([]cty.Value{cty.StringVal(name)})
			if err != nil {
				t.Fatalf("help(%q): %s", name, err)
			}
			want, ok := byPrimitives(name)
			if got.IsNull() {
				if ok {
					t.Fatalf("help(%q) was null but the primitives rendered:\n%s", name, want)
				}
				return
			}
			if !ok {
				t.Fatalf("help(%q) answered but the primitives found nothing", name)
			}
			if got.AsString() != want {
				t.Errorf("help(%q) =\n%q\nprimitives =\n%q", name, got.AsString(), want)
			}
		})
	}

	// And the no-argument listing is FuncNames joined.
	got, err := help.Call(nil)
	if err != nil {
		t.Fatalf("help(): %s", err)
	}
	if want := strings.Join(res.FuncNames(evalCtxFn), "\n"); got.AsString() != want {
		t.Errorf("help() =\n%q\nFuncNames =\n%q", got.AsString(), want)
	}
}
