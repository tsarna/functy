package functy

import (
	"testing"
)

// findFunc returns the named function from a Result, or nil.
func findFunc(r *Result, name string) *FuncDecl {
	for _, fn := range r.Funcs {
		if fn.Name == name {
			return fn
		}
	}
	return nil
}

func TestResultCommentsCaptured(t *testing.T) {
	src := "# hash top\n" +
		"// slash top\n" +
		"func f(a) {\n" +
		"    /* block */\n" +
		"    return a  // trailing\n" +
		"}\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	if len(res.Comments) != 4 {
		t.Fatalf("expected 4 comments, got %d: %+v", len(res.Comments), res.Comments)
	}
	want := []struct {
		text string
		line bool
	}{
		{"# hash top", true},
		{"// slash top", true},
		{"/* block */", false},
		{"// trailing", true},
	}
	for i, w := range want {
		if res.Comments[i].Text != w.text || res.Comments[i].Line != w.line {
			t.Errorf("comment %d = {%q, line=%v}, want {%q, line=%v}",
				i, res.Comments[i].Text, res.Comments[i].Line, w.text, w.line)
		}
	}
}

func TestDocCommentSlashAndHash(t *testing.T) {
	// A contiguous // + # block directly above a func is its doc; markers and one
	// leading space are stripped, lines joined with "\n".
	src := "# hash top\n" +
		"// slash top\n" +
		"func f(a) { return a }\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	fn := findFunc(res, "f")
	if fn == nil {
		t.Fatal("func f not found")
	}
	if got, want := fn.Doc, "hash top\nslash top"; got != want {
		t.Fatalf("Doc = %q, want %q", got, want)
	}
}

func TestDocCommentBlankLineBreaks(t *testing.T) {
	// A blank line between the comment and the declaration means it is not a doc.
	src := "// not a doc\n\nfunc f(a) { return a }\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	fn := findFunc(res, "f")
	if fn == nil {
		t.Fatal("func f not found")
	}
	if fn.Doc != "" {
		t.Fatalf("Doc = %q, want empty (blank line breaks the block)", fn.Doc)
	}
}

func TestDocCommentExcludesDirectivesButContinues(t *testing.T) {
	// A directive line inside the block is excluded from the prose, but does not
	// truncate the block: prose above the directive is still collected.
	src := "// prose one\n" +
		"//myapp:note hi\n" +
		"// prose two\n" +
		"func f(a) { return a }\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	fn := findFunc(res, "f")
	if fn == nil {
		t.Fatal("func f not found")
	}
	if got, want := fn.Doc, "prose one\nprose two"; got != want {
		t.Fatalf("Doc = %q, want %q", got, want)
	}
}

func TestDocCommentTrailingNotAttached(t *testing.T) {
	// A comment trailing code on the same line is not a lead comment.
	src := "func f(a) { return a } // trailing\nfunc g(b) { return b }\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	if g := findFunc(res, "g"); g == nil {
		t.Fatal("func g not found")
	} else if g.Doc != "" {
		t.Fatalf("g.Doc = %q, want empty (previous line's trailing comment is not g's doc)", g.Doc)
	}
}

func TestDocCommentBlockCommentIsNotDoc(t *testing.T) {
	// A /* */ block comment above a func is captured but does not form a doc.
	src := "/* not a doc */\nfunc f(a) { return a }\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	fn := findFunc(res, "f")
	if fn == nil {
		t.Fatal("func f not found")
	}
	if fn.Doc != "" {
		t.Fatalf("Doc = %q, want empty (block comments are not docs)", fn.Doc)
	}
	if len(res.Comments) != 1 || res.Comments[0].Line {
		t.Fatalf("expected one block comment captured, got %+v", res.Comments)
	}
}

func TestDocCommentPerDeclaration(t *testing.T) {
	src := "// doc a\nfunc a() { return 1 }\n// doc b\nfunc b() { return 2 }\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	if a := findFunc(res, "a"); a == nil || a.Doc != "doc a" {
		t.Fatalf("a.Doc = %q, want %q", docOf(a), "doc a")
	}
	if b := findFunc(res, "b"); b == nil || b.Doc != "doc b" {
		t.Fatalf("b.Doc = %q, want %q", docOf(b), "doc b")
	}
}

func TestDocCommentTripleSlash(t *testing.T) {
	src := "/// triple\nfunc f(a) { return a }\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	fn := findFunc(res, "f")
	if fn == nil || fn.Doc != "triple" {
		t.Fatalf("Doc = %q, want %q", docOf(fn), "triple")
	}
}

func TestDocCommentOnConstAndVar(t *testing.T) {
	src := "// a constant\nconst pi = 3\n// a variable\nvar counter = 0\n"
	res, diags := NewParser().AllowTopLevelConst(true).AllowTopLevelVar(true).
		Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	if len(res.Consts) != 1 || res.Consts[0].Doc != "a constant" {
		t.Fatalf("const Doc wrong: %+v", res.Consts)
	}
	if len(res.Vars) != 1 || res.Vars[0].Doc != "a variable" {
		t.Fatalf("var Doc wrong: %+v", res.Vars)
	}
}

// docOf is a nil-safe accessor used in failure messages.
func docOf(fn *FuncDecl) string {
	if fn == nil {
		return "<nil>"
	}
	return fn.Doc
}

func TestParamDocTrailing(t *testing.T) {
	src := "func add(\n" +
		"    a: number,  // the first addend\n" +
		"    b: number,  // the second addend\n" +
		") -> number { return a + b }\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	fn := findFunc(res, "add")
	if fn == nil {
		t.Fatal("func add not found")
	}
	if fn.Params[0].Doc != "the first addend" {
		t.Errorf("param a Doc = %q", fn.Params[0].Doc)
	}
	if fn.Params[1].Doc != "the second addend" {
		t.Errorf("param b Doc = %q", fn.Params[1].Doc)
	}
}

func TestParamDocLeadingBlock(t *testing.T) {
	src := "func f(\n" +
		"    // The retry policy.\n" +
		"    // One of none|linear|exponential.\n" +
		"    policy: string = \"linear\",\n" +
		") { return policy }\n"
	res, diags := NewParser().Parse([]byte(src), "test")
	if diags.HasErrors() {
		t.Fatalf("unexpected: %s", diags.Error())
	}
	fn := findFunc(res, "f")
	if fn == nil {
		t.Fatal("func f not found")
	}
	if got, want := fn.Params[0].Doc, "The retry policy.\nOne of none|linear|exponential."; got != want {
		t.Fatalf("policy Doc = %q, want %q", got, want)
	}
}

func TestParamDocLeadingWinsOverTrailing(t *testing.T) {
	src := "func f(\n" +
		"    // detailed\n" +
		"    x: number,  // short\n" +
		") { return x }\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	fn := findFunc(res, "f")
	if fn == nil || fn.Params[0].Doc != "detailed" {
		t.Fatalf("x Doc = %q, want %q", fn.Params[0].Doc, "detailed")
	}
}

func TestParamDocSingleLineNoMisattribution(t *testing.T) {
	// On a single-line list neither parameter starts its own line, so a trailing
	// comment attaches to none of them.
	src := "func f(a: number, b: number) { return a } // not a param doc\n"
	res, _ := NewParser().Parse([]byte(src), "test")
	fn := findFunc(res, "f")
	if fn == nil {
		t.Fatal("func f not found")
	}
	if fn.Params[0].Doc != "" || fn.Params[1].Doc != "" {
		t.Fatalf("params should have no doc, got a=%q b=%q", fn.Params[0].Doc, fn.Params[1].Doc)
	}
}

func TestParamDocReachesCtyDescription(t *testing.T) {
	// A required parameter's Doc flows to the compiled cty function.Parameter.
	ctx := buildContextWithDoc(t, "func f(\n    a: number,  // the addend\n) { return a }\n")
	params := ctx.Functions["f"].Params()
	if len(params) != 1 {
		t.Fatalf("expected 1 required param, got %d", len(params))
	}
	if params[0].Description != "the addend" {
		t.Fatalf("param Description = %q, want %q", params[0].Description, "the addend")
	}
}
