package functy

import (
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

func sampleRange() hcl.Range {
	return hcl.Range{
		Filename: "prog.cty",
		Start:    hcl.Pos{Line: 2, Column: 5, Byte: 21},
		End:      hcl.Pos{Line: 2, Column: 14, Byte: 30},
	}
}

func TestCtyToRangeRoundTrips(t *testing.T) {
	want := sampleRange()
	got, ok := ctyToRange(rangeToCty(want))
	if !ok {
		t.Fatal("ctyToRange reported failure on a well-formed range")
	}
	if got != want {
		t.Fatalf("round-trip mismatch:\n got  %#v\n want %#v", got, want)
	}
}

func TestCtyToRangeRejectsMalformed(t *testing.T) {
	for name, v := range map[string]cty.Value{
		"null":       cty.NullVal(cty.DynamicPseudoType),
		"not-object": cty.StringVal("nope"),
		"no-fields":  cty.EmptyObjectVal,
		"bad-pos":    cty.ObjectVal(map[string]cty.Value{"filename": cty.StringVal("f"), "start": cty.StringVal("x"), "end": cty.StringVal("y")}),
	} {
		if _, ok := ctyToRange(v); ok {
			t.Fatalf("%s: expected ctyToRange to fail", name)
		}
	}
}

func TestErrorDiagnosticsFull(t *testing.T) {
	// An assert-shaped error value: message + range + detail.
	ev := withAttr(errorValue(cty.StringVal("must be positive"), sampleRange()), "detail", cty.StringVal("n = -3"))

	diags := ErrorDiagnostics(ev)
	if len(diags) != 1 {
		t.Fatalf("got %d diagnostics, want 1", len(diags))
	}
	d := diags[0]
	if d.Severity != hcl.DiagError {
		t.Fatalf("severity = %v, want DiagError", d.Severity)
	}
	if d.Summary != "must be positive" {
		t.Fatalf("summary = %q", d.Summary)
	}
	if d.Detail != "n = -3" {
		t.Fatalf("detail = %q", d.Detail)
	}
	if d.Subject == nil || *d.Subject != sampleRange() {
		t.Fatalf("subject = %#v, want %#v", d.Subject, sampleRange())
	}
}

func TestErrorDiagnosticsNoRange(t *testing.T) {
	// A message-only error (null/absent range) still renders, without an underline.
	ev := cty.ObjectVal(map[string]cty.Value{"message": cty.StringVal("boom")})
	d := ErrorDiagnostics(ev)[0]
	if d.Summary != "boom" {
		t.Fatalf("summary = %q", d.Summary)
	}
	if d.Subject != nil {
		t.Fatalf("subject = %#v, want nil", d.Subject)
	}
	if d.Detail != "" {
		t.Fatalf("detail = %q, want empty", d.Detail)
	}
}

func TestThrownErrorDiagnosticsDelegates(t *testing.T) {
	ev := errorValue(cty.StringVal("bad"), sampleRange())
	te := &ThrownError{Value: ev}
	got := te.Diagnostics()
	want := ErrorDiagnostics(ev)
	if len(got) != 1 || got[0].Summary != want[0].Summary || *got[0].Subject != *want[0].Subject {
		t.Fatalf("ThrownError.Diagnostics did not match ErrorDiagnostics")
	}
}
