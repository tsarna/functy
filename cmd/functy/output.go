package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// printResult writes a function's return value in the requested format. A null
// value prints nothing, in every format.
func printResult(w io.Writer, v cty.Value, format string) error {
	if v.IsNull() {
		return nil
	}
	switch format {
	case "json":
		fmt.Fprintln(w, string(toJSON(v)))
	case "hcl":
		fmt.Fprintln(w, string(hclwrite.TokensForValue(v).Bytes()))
	case "raw":
		if v.Type() == cty.String && v.IsKnown() {
			fmt.Fprintln(w, v.AsString())
		} else {
			fmt.Fprintln(w, string(toJSON(v)))
		}
	default:
		return fmt.Errorf("unknown output format %q (want json, hcl, or raw)", format)
	}
	return nil
}

// toJSON renders a cty value as JSON using its own type.
func toJSON(v cty.Value) []byte {
	b, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return []byte(fmt.Sprintf("%q", err.Error()))
	}
	return b
}

// jsonDiagnostics is the top-level --json document for diagnostics-producing
// verbs (check, and later run): a single self-contained object with one entry
// per diagnostic. It is always well-formed — an empty slice serializes to
// {"diagnostics": []} — so a consumer (e.g. the VSCode extension) can parse
// stdout unconditionally.
type jsonDiagnostics struct {
	Diagnostics []jsonDiagnostic `json:"diagnostics"`
}

// jsonDiagnostic is an hcl.Diagnostic flattened for JSON: severity, the summary
// and optional detail text, and the source range to underline (when the
// diagnostic carries one). Ranges are 1-based, mirroring jsonRange.
type jsonDiagnostic struct {
	Severity string     `json:"severity"` // "error" or "warning"
	Summary  string     `json:"summary"`
	Detail   string     `json:"detail,omitempty"`
	Location *jsonRange `json:"location,omitempty"`
}

// writeDiagsJSON emits the --json diagnostics report to w. Diagnostics of every
// severity are included; the report is emitted whether or not there are errors so
// callers can print it before deciding the exit status.
func writeDiagsJSON(w io.Writer, diags hcl.Diagnostics) {
	rep := jsonDiagnostics{Diagnostics: make([]jsonDiagnostic, 0, len(diags))}
	for _, d := range diags {
		jd := jsonDiagnostic{Severity: severityString(d.Severity), Summary: d.Summary, Detail: d.Detail}
		if d.Subject != nil {
			jd.Location = rangeToJSON(*d.Subject)
		}
		rep.Diagnostics = append(rep.Diagnostics, jd)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	// Encode only errors on an unmarshalable value (none here) or a broken writer;
	// nothing actionable to do with either at this point.
	_ = enc.Encode(rep)
}

// severityString maps an hcl severity to the string used in the JSON report.
// Unknown severities fall back to "error" so a consumer never mistakes a problem
// for benign output.
func severityString(s hcl.DiagnosticSeverity) string {
	if s == hcl.DiagWarning {
		return "warning"
	}
	return "error"
}
