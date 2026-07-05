package functy

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// aliasDecl is a collected, unresolved top-level `type Name = <type>` declaration.
type aliasDecl struct {
	name     string
	expr     hcl.Expression
	rhsSrc   string // source text of the aliased type (right-hand side), for rendering (fmt)
	defRange hcl.Range
}

// collectTypeAliases scans a source's token stream for top-level
// `type Name = <type>` declarations, returning them unresolved. It is a linear
// scan independent of the main recursive-descent parse, so aliases from every
// co-loaded source can be gathered (and resolved together) before any source's
// annotations are resolved — giving aliases a project-wide, order-independent
// namespace. `type` is recognized only at depth 0 and at a statement start, so it
// stays usable as an ordinary identifier elsewhere.
func collectTypeAliases(tokens []token, src []byte, filename string) ([]aliasDecl, hcl.Diagnostics) {
	var out []aliasDecl
	var diags hcl.Diagnostics

	depth := 0
	atStmtStart := true
	for i := 0; i < len(tokens); {
		t := tokens[i]
		if t.Type == hclsyntax.TokenEOF {
			break
		}
		if isTerminator(t.Type) {
			atStmtStart = true
			i++
			continue
		}
		if depth == 0 && atStmtStart && t.Type == hclsyntax.TokenIdent && string(t.Bytes) == "type" {
			decl, next, adiags := parseAliasAt(tokens, i, src, filename)
			diags = diags.Extend(adiags)
			if decl != nil {
				out = append(out, *decl)
			}
			i = next
			atStmtStart = true
			continue
		}
		if isOpenBracket(t.Type) {
			depth++
		} else if isCloseBracket(t.Type) && depth > 0 {
			depth--
		}
		atStmtStart = false
		i++
	}
	return out, diags
}

// parseAliasAt parses `type Name = <type>` beginning at the `type` token at index
// i. It returns the declaration (nil on error), the index to continue scanning
// from (the terminating newline/';'/EOF), and any diagnostics.
func parseAliasAt(tokens []token, i int, src []byte, filename string) (*aliasDecl, int, hcl.Diagnostics) {
	typeTok := tokens[i]

	nameIdx := i + 1
	if nameIdx >= len(tokens) || tokens[nameIdx].Type != hclsyntax.TokenIdent || keywords[string(tokens[nameIdx].Bytes)] {
		return nil, skipToTerminator(tokens, i+1), hcl.Diagnostics{aliasScanDiag(typeTok.Range,
			"Expected type alias name", "A type alias is written: type Name = <type>.")}
	}
	nameTok := tokens[nameIdx]

	eqIdx := nameIdx + 1
	if eqIdx >= len(tokens) || tokens[eqIdx].Type != hclsyntax.TokenEqual {
		return nil, skipToTerminator(tokens, eqIdx), hcl.Diagnostics{aliasScanDiag(nameTok.Range,
			"Expected = in type alias", "A type alias is written: type Name = <type>.")}
	}

	start := eqIdx + 1
	if start >= len(tokens) || isTerminator(tokens[start].Type) || tokens[start].Type == hclsyntax.TokenEOF {
		return nil, skipToTerminator(tokens, start), hcl.Diagnostics{aliasScanDiag(tokens[eqIdx].Range,
			"Expected type", "A type alias needs a type after =.")}
	}

	end := scanTypeSpanEnd(tokens, start)
	sb := tokens[start].Range.Start.Byte
	sp := tokens[start].Range.Start
	eb := tokens[end].Range.Start.Byte

	expr, ediags := hclsyntax.ParseExpression(src[sb:eb], filename, sp)
	if ediags.HasErrors() {
		return nil, end, ediags
	}
	return &aliasDecl{
		name:     string(nameTok.Bytes),
		expr:     expr,
		rhsSrc:   strings.TrimSpace(string(src[sb:eb])),
		defRange: nameTok.Range,
	}, end, nil
}

// scanTypeSpanEnd returns the index of the token that ends a type annotation
// starting at index start: the first depth-0 newline/';'/EOF. Object-type braces
// are balanced at depth > 0 and do not terminate.
func scanTypeSpanEnd(tokens []token, start int) int {
	depth := 0
	j := start
	for j < len(tokens) {
		t := tokens[j]
		if t.Type == hclsyntax.TokenEOF {
			break
		}
		if depth == 0 && isTerminator(t.Type) {
			break
		}
		if isOpenBracket(t.Type) {
			depth++
		} else if isCloseBracket(t.Type) && depth > 0 {
			depth--
		}
		j++
	}
	return j
}

// skipToTerminator returns the index of the next newline/';'/EOF at or after i.
func skipToTerminator(tokens []token, i int) int {
	for i < len(tokens) {
		t := tokens[i]
		if t.Type == hclsyntax.TokenEOF || isTerminator(t.Type) {
			break
		}
		i++
	}
	return i
}

// resolveTypeAliases resolves the collected aliases into env, order-independently
// and across all sources. Names are validated up front (no shadowing of built-ins,
// no duplicates, no collision with host-registered types); the bodies are then
// resolved in dependency order via a fixpoint, with cycles reported.
func resolveTypeAliases(aliases []aliasDecl, env *typeEnv) hcl.Diagnostics {
	var diags hcl.Diagnostics

	names := make(map[string]bool, len(aliases))
	valid := make([]aliasDecl, 0, len(aliases))
	for _, a := range aliases {
		switch {
		case builtinTypeNames[a.name]:
			diags = diags.Append(aliasScanDiag(a.defRange, "Type alias shadows a built-in type",
				fmt.Sprintf("%q is a built-in type and cannot be redefined.", a.name)))
			continue
		case names[a.name]:
			diags = diags.Append(aliasScanDiag(a.defRange, "Duplicate type alias",
				fmt.Sprintf("Type %q is already defined.", a.name)))
			continue
		}
		if _, exists := env.named[a.name]; exists {
			diags = diags.Append(aliasScanDiag(a.defRange, "Type alias collides with a registered type",
				fmt.Sprintf("A type named %q is already registered by the host.", a.name)))
			continue
		}
		names[a.name] = true
		valid = append(valid, a)
	}

	pending := valid
	for len(pending) > 0 {
		var stuck []aliasDecl
		progressed := false
		for _, a := range pending {
			if !aliasDepsReady(a, names, env) {
				stuck = append(stuck, a)
				continue
			}
			tc, rdiags := env.resolveType(a.expr, false)
			diags = diags.Extend(rdiags)
			if rdiags.HasErrors() {
				env.named[a.name] = anyConstraint{} // placeholder so dependents don't cascade
			} else {
				env.named[a.name] = tc
			}
			progressed = true
		}
		if !progressed {
			for _, a := range stuck {
				diags = diags.Append(aliasScanDiag(a.defRange, "Unresolvable type alias",
					fmt.Sprintf("%q is part of a type-alias cycle or references an undefined type.", a.name)))
			}
			break
		}
		pending = stuck
	}
	return diags
}

// aliasDepsReady reports whether every alias that a references has already been
// resolved into env. References to non-alias names (built-ins, host types) are
// left for the resolver to handle or reject.
func aliasDepsReady(a aliasDecl, names map[string]bool, env *typeEnv) bool {
	for _, tr := range a.expr.Variables() {
		root := tr.RootName()
		if names[root] {
			if _, ok := env.named[root]; !ok {
				return false
			}
		}
	}
	return true
}

func aliasScanDiag(rng hcl.Range, summary, detail string) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  rng.Ptr(),
	}
}
