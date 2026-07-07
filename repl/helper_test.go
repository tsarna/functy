package repl

import (
	"bytes"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// testEvalContext is the fixed context the tests' static host evaluates against:
// a few variables (including a nested "env" object for dotted-completion tests)
// and a couple of functions.
func testEvalContext() *hcl.EvalContext {
	return &hcl.EvalContext{
		Variables: map[string]cty.Value{
			"greeting": cty.StringVal("hi"),
			"env": cty.ObjectVal(map[string]cty.Value{
				"HOME": cty.StringVal("/root"),
				"PATH": cty.StringVal("/bin"),
			}),
			"nested": cty.ObjectVal(map[string]cty.Value{
				"outer": cty.ObjectVal(map[string]cty.Value{
					"inner": cty.NumberIntVal(1),
				}),
			}),
		},
		Functions: map[string]function.Function{
			"upper":  stdlib.UpperFunc,
			"length": stdlib.LengthFunc,
		},
	}
}

// newTestSession returns a Session over a static host with the fixed test
// context, capturing its result and diagnostic streams into buffers.
func newTestSession(t *testing.T) (*Session, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errOut bytes.Buffer
	s := New(NewStaticHost(testEvalContext()), Options{Out: &out, ErrOut: &errOut})
	return s, &out, &errOut
}
