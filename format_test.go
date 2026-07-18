package functy

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/fmt/*.golden from the formatter output")

// TestFormatGolden formats each testdata/fmt/*.cty input and compares it to the
// sibling *.golden, and checks that formatting is idempotent (formatting the golden
// yields the golden unchanged). Run with -update to regenerate the goldens.
func TestFormatGolden(t *testing.T) {
	inputs, err := filepath.Glob("testdata/fmt/*.cty")
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 0 {
		t.Fatal("no testdata/fmt/*.cty fixtures found")
	}
	for _, in := range inputs {
		in := in
		name := strings.TrimSuffix(filepath.Base(in), ".cty")
		t.Run(name, func(t *testing.T) {
			src, err := os.ReadFile(in)
			if err != nil {
				t.Fatal(err)
			}
			got, diags := Format(src, in)
			if diags.HasErrors() {
				t.Fatalf("format produced diagnostics: %s", diags.Error())
			}

			golden := strings.TrimSuffix(in, ".cty") + ".golden"
			if *updateGolden {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden (run -update to create it): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("formatted output differs from %s:\n--- got ---\n%s\n--- want ---\n%s",
					golden, got, want)
			}

			// Idempotency: formatting an already-formatted file is a no-op.
			again, diags := Format(want, golden)
			if diags.HasErrors() {
				t.Fatalf("re-formatting golden produced diagnostics: %s", diags.Error())
			}
			if string(again) != string(want) {
				t.Errorf("not idempotent — re-formatting %s changed it:\n--- again ---\n%s", golden, again)
			}
		})
	}
}

// TestFormatPreservesHeredocValues asserts that fmt is meaning-preserving for
// heredoc strings: each function in the heredoc fixture returns the identical cty
// value before and after formatting (heredoc body bytes are literal content, so
// re-indenting them would silently change the value), and that formatting the
// original messy input is idempotent from the first application onward.
func TestFormatPreservesHeredocValues(t *testing.T) {
	src, err := os.ReadFile("testdata/fmt/heredoc.cty")
	if err != nil {
		t.Fatal(err)
	}
	formatted, diags := Format(src, "heredoc.cty")
	if diags.HasErrors() {
		t.Fatalf("format produced diagnostics: %s", diags.Error())
	}

	before := compileFuncs(t, string(src))
	after := compileFuncs(t, string(formatted))
	calls := []struct {
		fn   string
		args []cty.Value
	}{
		{"plain", nil},
		{"dashed", []cty.Value{cty.StringVal("Ada")}},
		{"multi", nil},
	}
	for _, c := range calls {
		want := call(t, before, c.fn, c.args...)
		got := call(t, after, c.fn, c.args...)
		if !got.RawEquals(want) {
			t.Errorf("%s: formatting changed the value:\n--- before ---\n%#v\n--- after ---\n%#v", c.fn, want, got)
		}
	}

	again, diags := Format(formatted, "heredoc.cty")
	if diags.HasErrors() {
		t.Fatalf("re-format produced diagnostics: %s", diags.Error())
	}
	if string(again) != string(formatted) {
		t.Errorf("not idempotent — format(format(x)) != format(x):\n--- again ---\n%s\n--- formatted ---\n%s", again, formatted)
	}
}
