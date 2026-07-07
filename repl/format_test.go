package repl

import (
	"reflect"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestFormatValue(t *testing.T) {
	tests := []struct {
		name string
		in   cty.Value
		want string
	}{
		{"string", cty.StringVal("ada"), `"ada"`},
		{"int", cty.NumberIntVal(3), "3"},
		{"float", cty.NumberFloatVal(1.5), "1.5"},
		{"bool true", cty.True, "true"},
		{"bool false", cty.False, "false"},
		{"null", cty.NullVal(cty.String), "null"},
		{"unknown", cty.UnknownVal(cty.String), "(unknown)"},
		{"empty list", cty.ListValEmpty(cty.String), "[]"},
		{"empty object", cty.EmptyObjectVal, "{}"},
		{
			"scalar list inline",
			cty.ListVal([]cty.Value{cty.StringVal("admin"), cty.StringVal("dev")}),
			`["admin", "dev"]`,
		},
		{
			"flat object inline",
			cty.ObjectVal(map[string]cty.Value{
				"a": cty.NumberIntVal(1),
				"b": cty.NumberIntVal(2),
			}),
			"{ a = 1, b = 2 }",
		},
		{
			// Alphabetical key order; nested non-scalar forces multi-line; aligned =.
			"object with nested list multiline",
			cty.ObjectVal(map[string]cty.Value{
				"name":   cty.StringVal("ada"),
				"roles":  cty.ListVal([]cty.Value{cty.StringVal("admin"), cty.StringVal("dev")}),
				"active": cty.True,
			}),
			"{\n" +
				`  active = true` + "\n" +
				`  name   = "ada"` + "\n" +
				`  roles  = ["admin", "dev"]` + "\n" +
				"}",
		},
		{
			"map quoted non-identifier key",
			cty.MapVal(map[string]cty.Value{
				"my key": cty.StringVal("v"),
			}),
			`{ "my key" = "v" }`,
		},
		{
			"string with interpolation escaped",
			cty.StringVal("a${b}c"),
			`"a$${b}c"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatValue(tt.in)
			if got != tt.want {
				t.Errorf("formatValue()\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// TestFormatCapsule verifies capsules render via their CapsuleOps.GoString
// summary rather than a verbose cty dump.
func TestFormatCapsule(t *testing.T) {
	capType := cty.CapsuleWithOps("widget", reflect.TypeOf(0), &cty.CapsuleOps{
		GoString: func(interface{}) string { return `widget("w")` },
	})
	n := 7
	v := cty.CapsuleVal(capType, &n)
	if got := formatValue(v); got != `widget("w")` {
		t.Errorf("capsule format = %q, want %q", got, `widget("w")`)
	}

	// Rich object: the _capsule attribute's GoString is used.
	obj := cty.ObjectVal(map[string]cty.Value{
		"name":     cty.StringVal("w"),
		"_capsule": v,
	})
	if got := formatValue(obj); got != `widget("w")` {
		t.Errorf("rich object format = %q, want %q", got, `widget("w")`)
	}
}
