package functy

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// EvalDecls evaluates a set of top-level const/var declarations into
// ctx.Variables, resolving cross-references order-independently.
//
// Declarations may reference one another out of source order, so this resolves
// them iteratively: each pass evaluates every declaration whose referenced
// declaration names are already available, until no progress is made. Anything
// left unresolved is a cyclic or dangling reference and is reported. A
// declaration with a `: T` annotation has its value coerced to that type.
//
// The caller chooses which declarations to pass and in what precedence: to make
// consts resolve before vars (the common host ordering — a var may reference a
// const, not vice versa), pass append(consts, vars...). A host embedding functy
// as a symbol library typically passes Result.Consts alone.
//
// It is exported (rather than kept in cmd/functy) precisely because a host needs
// it: functy hands back parsed Decls but pre-evaluates nothing, so turning a
// const into a self-contained cty.Value is the host's job, and the dependency
// ordering is fiddly enough to be worth sharing.
func EvalDecls(decls []Decl, ctx *hcl.EvalContext) hcl.Diagnostics {
	var diags hcl.Diagnostics

	names := make(map[string]bool, len(decls))
	for _, d := range decls {
		if names[d.Name] {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Duplicate declaration",
				Detail:   fmt.Sprintf("%q is declared more than once at the top level.", d.Name),
				Subject:  d.DefRange.Ptr(),
			})
		}
		names[d.Name] = true
	}

	// Order the declarations so every one is evaluated after those it references, via
	// a worklist topological sort (O(n+e)) instead of a rescan-everything fixpoint
	// (O(n²) on a reverse-ordered chain, re-walking each Expr's Variables() every
	// pass). Each dependency name-set is computed once. `unresolved` holds any decl in
	// a dependency cycle (or depending on one), reported below in source order.
	order, unresolved := topoResolveOrder(len(decls),
		func(i int) string { return decls[i].Name },
		func(i int) map[string]bool { return declDepNames(decls[i], names) })

	for _, i := range order {
		d := decls[i]
		val := cty.NullVal(cty.DynamicPseudoType)
		if d.Type != nil {
			val = cty.NullVal(d.Type.Cty())
		}
		if d.Expr != nil {
			v, vdiags := d.Expr.Value(ctx)
			diags = diags.Extend(vdiags)
			if vdiags.HasErrors() {
				ctx.Variables[d.Name] = val // placeholder so dependents don't cascade
				continue
			}
			val = v
		}
		if d.Type != nil {
			conv, err := d.Type.Coerce(val)
			if err != nil {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Invalid declaration value",
					Detail:   fmt.Sprintf("%s: %s", d.Name, err),
					Subject:  d.DefRange.Ptr(),
				})
			} else {
				val = conv
			}
		}
		ctx.Variables[d.Name] = val
	}

	for _, i := range unresolved {
		d := decls[i]
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "Unresolved declaration",
			Detail: fmt.Sprintf(
				"%q could not be resolved; it may reference an undefined name or form a dependency cycle.", d.Name),
			Subject: d.DefRange.Ptr(),
		})
	}
	return diags
}

// EvalNamespacedDecls evaluates const/var Decls grouped by Decl.Namespace into a
// Compiled's per-namespace variable tables under the own+global policy.
//
// The global namespace ("") is evaluated first into baseCtx.Variables — which the
// caller must set to compiled.Vars[""] so that (a) the global consts land in the
// table the host projects and (b) they are visible to every namespace's bodies,
// which late-bind to baseCtx as their parent. Each other namespace is then
// evaluated into compiled.Vars[ns] via a child of baseCtx that also exposes that
// namespace's sibling functions (compiled.Units[ns]). A namespaced initializer thus
// sees its own namespace's names, then the global names, with the local winning —
// mirroring how a namespaced function call resolves.
//
// Because the global namespace is evaluated first, a namespaced declaration may
// reference a global one; the reverse cannot (a global sees only globals). Duplicate
// detection is per namespace: two namespaces may each declare `const greeting`, but a
// name declared twice within one namespace is still reported.
//
// This is one policy, not the only one. A host wanting strict isolation (a namespace
// sees only its own consts) can skip this helper and call EvalDecls against each
// compiled.Vars[ns] directly, with no shared parent carrying the globals.
func EvalNamespacedDecls(decls []Decl, baseCtx *hcl.EvalContext, compiled *Compiled) hcl.Diagnostics {
	byNS := make(map[string][]Decl)
	var order []string
	for _, d := range decls {
		if _, ok := byNS[d.Namespace]; !ok {
			order = append(order, d.Namespace)
		}
		byNS[d.Namespace] = append(byNS[d.Namespace], d)
	}

	var diags hcl.Diagnostics

	// Global first, so namespaced initializers can resolve global names through the
	// parent chain. baseCtx.Variables is compiled.Vars[""] by contract.
	if globals, ok := byNS[""]; ok {
		diags = diags.Extend(EvalDecls(globals, baseCtx))
	}

	for _, ns := range order {
		if ns == "" {
			continue
		}
		table, ok := compiled.Vars[ns]
		if !ok {
			table = make(map[string]cty.Value)
			compiled.Vars[ns] = table
		}
		child := baseCtx.NewChild()
		child.Functions = compiled.Units[ns] // bare siblings; nil is fine
		child.Variables = table
		diags = diags.Extend(EvalDecls(byNS[ns], child))
	}

	return diags
}

// declDepNames returns the set of sibling-declaration names that d references (those
// present in names, excluding d itself). References to names not declared here are
// left for the expression evaluator to resolve or reject, so they are not
// dependencies for ordering.
func declDepNames(d Decl, names map[string]bool) map[string]bool {
	if d.Expr == nil {
		return nil
	}
	var deps map[string]bool
	for _, traversal := range d.Expr.Variables() {
		root := traversal.RootName()
		if root == d.Name || !names[root] {
			continue
		}
		if deps == nil {
			deps = make(map[string]bool)
		}
		deps[root] = true
	}
	return deps
}

// topoResolveOrder orders n named, cross-referencing items so each is visited after
// the items it depends on. nameOf(i) is the name item i defines; depsOf(i) is the set
// of names item i references (already filtered to in-scope names, self excluded). It
// returns `order`, a dependency-respecting visit order, and `unresolved`, the items
// that could never be ordered — those in a dependency cycle or transitively
// depending on one — both in ascending index (source) order.
//
// This is Kahn's algorithm: O(n + e), replacing the O(n²) rescan-until-fixpoint that
// both the const/var and type-alias resolvers used to share. The queue is seeded and
// dependents are recorded in index order, so the visit order is deterministic and
// keeps independent items in source order (e.g. consts before vars when the caller
// concatenates them that way). A duplicate name wakes its dependents only on its
// first resolution.
func topoResolveOrder(n int, nameOf func(i int) string, depsOf func(i int) map[string]bool) (order, unresolved []int) {
	remaining := make([]int, n)
	dependents := make(map[string][]int)
	for i := 0; i < n; i++ {
		ds := depsOf(i)
		remaining[i] = len(ds)
		for name := range ds {
			dependents[name] = append(dependents[name], i)
		}
	}

	queue := make([]int, 0, n)
	for i := 0; i < n; i++ {
		if remaining[i] == 0 {
			queue = append(queue, i)
		}
	}

	processed := make([]bool, n)
	resolvedName := make(map[string]bool)
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		if processed[i] {
			continue
		}
		processed[i] = true
		order = append(order, i)

		if name := nameOf(i); !resolvedName[name] {
			resolvedName[name] = true
			for _, j := range dependents[name] {
				if !processed[j] && remaining[j] > 0 {
					remaining[j]--
					if remaining[j] == 0 {
						queue = append(queue, j)
					}
				}
			}
		}
	}

	for i := 0; i < n; i++ {
		if !processed[i] {
			unresolved = append(unresolved, i)
		}
	}
	return order, unresolved
}
