package repl

import (
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// completer implements readline.AutoCompleter, completing the word under the
// cursor against meta-commands, the host's context variables/functions, and
// session bindings. A dotted path (e.g. "env.HO", "bus.") completes its final
// segment against the attributes/keys of the value the leading path resolves to.
type completer struct{ s *Session }

// Do returns the candidate suffixes for the word under the cursor plus the
// length of the already-typed prefix, per readline's AutoCompleter contract.
func (c *completer) Do(line []rune, pos int) ([][]rune, int) {
	start := pos
	for start > 0 && isPathRune(line[start-1]) {
		start--
	}
	path := string(line[start:pos])

	// seg is the segment being completed (and replaced); cands are full names.
	seg := path
	var cands []string
	if i := strings.LastIndex(path, "."); i >= 0 {
		seg = path[i+1:]
		cands = c.s.attrCompletions(path[:i], seg)
	} else {
		cands = c.s.completions(path)
	}

	out := make([][]rune, 0, len(cands))
	for _, cand := range cands {
		out = append(out, []rune(cand[len(seg):]))
	}
	return out, len([]rune(seg))
}

func isWordRune(r rune) bool {
	return r == '_' || r == ':' ||
		(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
		(r >= '0' && r <= '9')
}

// isPathRune extends isWordRune with '.' so a dotted attribute path is treated
// as a single token by the completer.
func isPathRune(r rune) bool {
	return r == '.' || isWordRune(r)
}

// completionContext returns the host's completion context, or nil if it cannot
// be built (completion then falls back to bindings and meta-commands only).
func (s *Session) completionContext() *hcl.EvalContext {
	ctx, err := s.host.CompletionContext(s.baseCtx)
	if err != nil {
		return nil
	}
	return ctx
}

// completions returns the sorted candidate names with the given prefix. A word
// starting with ':' completes meta-commands; otherwise the host's context
// variables and functions plus session bindings (excluding the managed _/_N).
func (s *Session) completions(word string) []string {
	var cands []string

	if strings.HasPrefix(word, ":") {
		cands = append(cands, s.metaNames...)
	} else {
		// Walk the whole context chain: a host layers per-eval variables (e.g.
		// "ctx") in a child whose parent holds the base namespaces/functions.
		for c := s.completionContext(); c != nil; c = c.Parent() {
			for name := range c.Variables {
				cands = append(cands, name)
			}
			for name := range c.Functions {
				cands = append(cands, name)
			}
		}
		for name := range s.bindings {
			if name == "_" || resultNameRe.MatchString(name) {
				continue
			}
			cands = append(cands, name)
		}
	}

	matched := cands[:0]
	for _, c := range cands {
		if strings.HasPrefix(c, word) {
			matched = append(matched, c)
		}
	}
	sort.Strings(matched)
	return matched
}

// attrCompletions returns the sorted attribute/key names (matching prefix) of
// the value that base resolves to. Internal (_-prefixed) attributes and keys
// that are not valid identifiers are omitted, as is anything base does not
// resolve to a traversable object or map.
func (s *Session) attrCompletions(base, prefix string) []string {
	v, ok := s.resolvePath(base)
	if !ok {
		return nil
	}

	var names []string
	t := v.Type()
	switch {
	case t.IsObjectType():
		for name := range t.AttributeTypes() {
			names = append(names, name)
		}
	case t.IsMapType() && !v.IsNull() && v.IsKnown():
		for name := range v.AsValueMap() {
			names = append(names, name)
		}
	default:
		return nil
	}

	matched := names[:0]
	for _, n := range names {
		if strings.HasPrefix(n, "_") || !hclsyntax.ValidIdentifier(n) {
			continue // internal (_capsule, _ctx) or not reachable via dot syntax
		}
		if strings.HasPrefix(n, prefix) {
			matched = append(matched, n)
		}
	}
	sort.Strings(matched)
	return matched
}

// resolvePath resolves a dotted base path (e.g. "sys", "env", "bus.main") to a
// cty value: it starts at a host context variable or session binding, then walks
// object attributes / map keys. It returns false if any segment is missing or
// leads into a non-traversable value.
func (s *Session) resolvePath(base string) (cty.Value, bool) {
	segs := strings.Split(base, ".")
	if segs[0] == "" {
		return cty.NilVal, false
	}

	v, ok := s.rootValue(segs[0])
	if !ok {
		return cty.NilVal, false
	}

	for _, seg := range segs[1:] {
		t := v.Type()
		switch {
		case t.IsObjectType() && t.HasAttribute(seg):
			v = v.GetAttr(seg)
		case t.IsMapType() && !v.IsNull() && v.IsKnown() && v.HasIndex(cty.StringVal(seg)).True():
			v = v.Index(cty.StringVal(seg))
		default:
			return cty.NilVal, false
		}
	}
	return v, true
}

// rootValue resolves the first path segment to a cty value: a host context
// variable (searched down the whole chain, so a per-eval "ctx" in a child and a
// base namespace in its parent are both found) or a session binding.
func (s *Session) rootValue(name string) (cty.Value, bool) {
	for c := s.completionContext(); c != nil; c = c.Parent() {
		if v, ok := c.Variables[name]; ok {
			return v, true
		}
	}
	if v, ok := s.bindings[name]; ok {
		return v, true
	}
	return cty.NilVal, false
}
