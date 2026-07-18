package functy

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

// aliasDecl is a collected, unresolved top-level `type Name = <type>` declaration.
type aliasDecl struct {
	name string
	// namespace is the enclosing namespace of the file the alias was collected
	// from ("" = global), taken from the file's leading `namespace a::b`
	// declaration. Aliases from the same file share one namespace.
	namespace string
	expr      hcl.Expression
	rhsSrc    string // source text of the aliased type (right-hand side), for rendering (fmt)
	defRange  hcl.Range
}

// collectTypeAliases scans a source's token stream for top-level
// `type Name = <type>` declarations, returning them unresolved. It is a linear
// scan independent of the main recursive-descent parse, so aliases from every
// co-loaded source can be gathered (and resolved together) before any source's
// annotations are resolved — giving aliases an order-independent, per-namespace
// name space. `type` is recognized only at depth 0 and at a statement start, so it
// stays usable as an ordinary identifier elsewhere. ns is the file's namespace
// (from a leading `namespace a::b`, "" = global); every alias collected here is
// stamped with it so resolution can scope names own-then-global.
func collectTypeAliases(tokens []token, src []byte, filename, ns string) ([]aliasDecl, hcl.Diagnostics) {
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
				decl.namespace = ns
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

// leadingNamespace returns the file's namespace from a leading `namespace a::b`
// declaration ("" = global). A namespace, when present, is required to be the
// first declaration in the file (enforced by the recursive-descent parser), so it
// is the first non-terminator token here. This is a best-effort read for stamping
// aliases; a malformed namespace declaration is diagnosed by parseNamespaceDecl,
// not here.
func leadingNamespace(tokens []token) string {
	i := 0
	for i < len(tokens) && isTerminator(tokens[i].Type) {
		i++
	}
	if i >= len(tokens) || tokens[i].Type != hclsyntax.TokenIdent || string(tokens[i].Bytes) != "namespace" {
		return ""
	}
	i++ // consume `namespace`

	var segs []string
	for i < len(tokens) && tokens[i].Type == hclsyntax.TokenIdent {
		segs = append(segs, string(tokens[i].Bytes))
		i++
		if i < len(tokens) && tokens[i].Type == hclsyntax.TokenDoubleColon {
			i++
			continue
		}
		break
	}
	return strings.Join(segs, "::")
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

	if string(nameTok.Bytes) == "_" {
		return nil, skipToTerminator(tokens, nameIdx), hcl.Diagnostics{aliasScanDiag(nameTok.Range,
			"Invalid type alias name",
			"`_` is the blank identifier and cannot name a type alias. Use a name like `_spec` for a namespace-local type.")}
	}

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

	end, maxDepth := scanTypeSpanEnd(tokens, start)
	sb := tokens[start].Range.Start.Byte
	sp := tokens[start].Range.Start
	eb := tokens[end].Range.Start.Byte

	// A type RHS nested deeper than maxExprDepth would overflow HCL's own recursive
	// parser (an uncatchable crash), so reject it before ParseExpression — mirroring
	// the guard the main parser applies to expression/type spans.
	if maxDepth > maxExprDepth {
		return nil, end, hcl.Diagnostics{aliasScanDiag(
			hcl.RangeBetween(tokens[start].Range, tokens[end].Range),
			"Nesting too deep",
			fmt.Sprintf("This type nests brackets more than %d levels deep; reduce the nesting.", maxExprDepth))}
	}

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
// starting at index start (the first depth-0 newline/';'/EOF; object-type braces
// are balanced at depth > 0 and do not terminate) and the maximum bracket depth
// reached, so the caller can reject a span too deep for hclsyntax.ParseExpression.
func scanTypeSpanEnd(tokens []token, start int) (end, maxDepth int) {
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
			if depth > maxDepth {
				maxDepth = depth
			}
		} else if isCloseBracket(t.Type) && depth > 0 {
			depth--
		}
		j++
	}
	return j, maxDepth
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

// resolveTypeAliases resolves one namespace's collected aliases into env,
// order-independently. It is called once per namespace: namespace is that
// namespace ("" = global) and env is that namespace's type environment — for the
// global namespace, a clone of the host's registered types; for a namespaced call,
// a clone of the resolved global env, so bare names fall back own-then-global.
// hostTypes is the set of host-registered type names (captured before any alias
// resolution) used only to warn on a namespaced shadow.
//
// Names are validated up front: a built-in keyword may never be redefined (it is
// resolved before the alias table, so such an alias would be silently dead);
// duplicates within the namespace are rejected; a *global* alias may not collide
// with a host-registered type. A *namespaced* alias may shadow a global alias or a
// host type (own-then-global), but shadowing a host type warns, since it silently
// changes enforcement. Bodies are then resolved in dependency order via a fixpoint,
// with cycles reported.
func resolveTypeAliases(aliases []aliasDecl, env *typeEnv, namespace string, hostTypes map[string]bool) hcl.Diagnostics {
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
				fmt.Sprintf("Type %q is already defined in this namespace.", a.name)))
			continue
		}
		if namespace == "" {
			if _, exists := env.named[a.name]; exists {
				diags = diags.Append(aliasScanDiag(a.defRange, "Type alias collides with a registered type",
					fmt.Sprintf("A type named %q is already registered by the host.", a.name)))
				continue
			}
		} else if hostTypes[a.name] {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagWarning,
				Summary:  "Type alias shadows a host-registered type",
				Detail: fmt.Sprintf("%q is registered by the host. Within namespace %q this alias shadows it, so annotations using %q here enforce this alias instead of the host type.",
					a.name, namespace, a.name),
				Subject: a.defRange.Ptr(),
			})
		}
		names[a.name] = true
		valid = append(valid, a)
	}

	// Resolve in dependency order via a worklist topological sort (O(n+e)), each
	// alias visited after those it references — replacing the O(n²) rescan-until-
	// fixpoint that re-walked every body's Variables() each pass. `unresolved` holds
	// any alias in a reference cycle (or depending on one), reported in source order.
	order, unresolved := topoResolveOrder(len(valid),
		func(i int) string { return valid[i].name },
		func(i int) map[string]bool { return aliasDepNames(valid[i], names) })

	for _, i := range order {
		a := valid[i]
		tc, rdiags := env.resolveType(a.expr, false)
		diags = diags.Extend(rdiags)
		if rdiags.HasErrors() {
			env.named[a.name] = anyConstraint{} // placeholder so dependents don't cascade
		} else {
			env.named[a.name] = tc
		}
	}

	for _, i := range unresolved {
		a := valid[i]
		diags = diags.Append(aliasScanDiag(a.defRange, "Unresolvable type alias",
			fmt.Sprintf("%q is part of a type-alias cycle or references an undefined type.", a.name)))
	}
	return diags
}

// aliasDepNames returns the set of sibling-alias names that a references (those
// present in names). References to non-alias names — built-ins, host types — are left
// for the resolver to handle or reject, so they are not dependencies for ordering.
func aliasDepNames(a aliasDecl, names map[string]bool) map[string]bool {
	var deps map[string]bool
	for _, tr := range a.expr.Variables() {
		root := tr.RootName()
		if !names[root] {
			continue
		}
		if deps == nil {
			deps = make(map[string]bool)
		}
		deps[root] = true
	}
	return deps
}

func aliasScanDiag(rng hcl.Range, summary, detail string) *hcl.Diagnostic {
	return &hcl.Diagnostic{
		Severity: hcl.DiagError,
		Summary:  summary,
		Detail:   detail,
		Subject:  rng.Ptr(),
	}
}
