package functy

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
