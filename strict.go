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

// fileDirectives is what functy itself takes from a file's leading directive
// block: its strict-typing requests, and whether the file is an extern file.
type fileDirectives struct {
	paramTypes    bool
	returnType    bool
	declaredTypes bool

	// extern is set by //functy:extern. The file then holds only bodiless func
	// declarations (see Result.Externs), which changes what the parser accepts —
	// so it must be known before the file is parsed, and indeed before type
	// aliases are collected from it.
	extern bool
}

// interpretFunctyDirectives reads the file's directives for functy's own use.
// functy:strict enables all three type requirements; functy:require enables the
// named ones (param_types, return_type, declared_types); functy:extern marks the
// file as declarations-only. Unknown functy: names and bad require flags are
// warnings (forward-compatible), and every non-functy directive is ignored here
// (it is still collected into Result.Directives).
func interpretFunctyDirectives(dirs []Directive) (fd fileDirectives, diags hcl.Diagnostics) {
	for _, d := range dirs {
		if d.Namespace != "functy" {
			continue
		}
		switch d.Name {
		case "strict":
			fd.paramTypes, fd.returnType, fd.declaredTypes = true, true, true
		case "extern":
			fd.extern = true
			if strings.TrimSpace(d.Args) != "" {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagWarning,
					Summary:  "functy:extern takes no arguments",
					Detail:   fmt.Sprintf("//functy:extern marks the whole file as declarations-only; %q is ignored.", d.Args),
					Subject:  d.Range.Ptr(),
				})
			}
		case "require":
			for _, f := range strings.Fields(d.Args) {
				switch f {
				case "param_types":
					fd.paramTypes = true
				case "return_type":
					fd.returnType = true
				case "declared_types":
					fd.declaredTypes = true
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
	return fd, diags
}
