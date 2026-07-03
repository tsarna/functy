package functy

import (
	"strings"

	"github.com/hashicorp/hcl/v2"
)

// Directive is a collected directive comment, following Go's convention: a line
// comment with no space after `//`, of the form `//<namespace>:<name> [args]`. A
// space after `//` (`// functy: …`) makes it ordinary prose, not a directive.
//
// functy interprets only its own `functy:` namespace (strict, require); every
// other namespace is collected and passed through untouched for the host to act
// on (e.g. `//vinculum:cache 5m`).
type Directive struct {
	Namespace string
	Name      string
	Args      string
	Range     hcl.Range
}

// parseDirectiveComment parses a single comment token as a directive. It returns
// ok=false unless the comment is `//<ns>:<name>[ args]` with no space after `//`.
func parseDirectiveComment(b []byte, rng hcl.Range) (Directive, bool) {
	text := strings.TrimRight(string(b), "\r\n")
	if !strings.HasPrefix(text, "//") {
		return Directive{}, false
	}
	body := text[2:]
	if body == "" || body[0] == ' ' || body[0] == '\t' {
		return Directive{}, false // a space after // is prose, not a directive
	}
	colon := strings.IndexByte(body, ':')
	if colon <= 0 {
		return Directive{}, false // no namespace:name shape
	}
	namespace := body[:colon]
	rest := body[colon+1:]

	name := rest
	args := ""
	if sp := strings.IndexAny(rest, " \t"); sp >= 0 {
		name = rest[:sp]
		args = strings.TrimSpace(rest[sp+1:])
	}
	if name == "" {
		return Directive{}, false
	}
	return Directive{Namespace: namespace, Name: name, Args: args, Range: rng}, true
}
