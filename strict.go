package functy

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// reqSource records whether a strict-typing requirement is off, or in effect
// because the host set it or a file directive requested it. The host source takes
// precedence in diagnostics (it cannot be relaxed by a file).
type reqSource int

const (
	reqOff reqSource = iota
	reqHost
	reqFile
)

func (s reqSource) on() bool { return s != reqOff }

func (s reqSource) reason() string {
	switch s {
	case reqHost:
		return "required by the host (strict typing)"
	case reqFile:
		return "required by a //functy: directive in this file"
	default:
		return ""
	}
}

// combineReq folds a host flag and a file directive into one requirement,
// tighten-only: on if either asks for it, attributed to the host when both do.
func combineReq(host, file bool) reqSource {
	switch {
	case host:
		return reqHost
	case file:
		return reqFile
	default:
		return reqOff
	}
}

// strictness is the effective set of type requirements for one source file.
type strictness struct {
	paramTypes    reqSource
	returnType    reqSource
	declaredTypes reqSource
}

// interpretFunctyDirectives reads the file's directives for functy's own strict
// typing requests. functy:strict enables all three; functy:require enables the
// named ones (param_types, return_type, declared_types). Unknown functy: names and
// bad require flags are warnings (forward-compatible), and every non-functy
// directive is ignored here (it is still collected into Result.Directives).
func interpretFunctyDirectives(dirs []Directive) (param, ret, decl bool, diags hcl.Diagnostics) {
	for _, d := range dirs {
		if d.Namespace != "functy" {
			continue
		}
		switch d.Name {
		case "strict":
			param, ret, decl = true, true, true
		case "require":
			for _, f := range strings.Fields(d.Args) {
				switch f {
				case "param_types":
					param = true
				case "return_type":
					ret = true
				case "declared_types":
					decl = true
				default:
					diags = diags.Append(&hcl.Diagnostic{
						Severity: hcl.DiagWarning,
						Summary:  "Unknown functy:require flag",
						Detail:   fmt.Sprintf("%q is not a recognized requirement (param_types, return_type, declared_types).", f),
						Subject:  d.Range.Ptr(),
					})
				}
			}
		default:
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagWarning,
				Summary:  "Unknown functy directive",
				Detail:   fmt.Sprintf("//functy:%s is not a recognized directive.", d.Name),
				Subject:  d.Range.Ptr(),
			})
		}
	}
	return param, ret, decl, diags
}
