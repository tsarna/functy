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

	pending := make([]Decl, len(decls))
	copy(pending, decls)

	for len(pending) > 0 {
		var stuck []Decl
		progress := false

		for _, d := range pending {
			if !declDepsReady(d, names, ctx.Variables) {
				stuck = append(stuck, d)
				continue
			}
			val := cty.NullVal(cty.DynamicPseudoType)
			if d.Type != nil {
				val = cty.NullVal(d.Type.Cty())
			}
			if d.Expr != nil {
				v, vdiags := d.Expr.Value(ctx)
				diags = diags.Extend(vdiags)
				if vdiags.HasErrors() {
					ctx.Variables[d.Name] = val // placeholder so dependents don't loop
					progress = true
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
			progress = true
		}

		if !progress {
			for _, d := range stuck {
				diags = diags.Append(&hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "Unresolved declaration",
					Detail: fmt.Sprintf(
						"%q could not be resolved; it may reference an undefined name or form a dependency cycle.", d.Name),
					Subject: d.DefRange.Ptr(),
				})
			}
			break
		}
		pending = stuck
	}
	return diags
}

// declDepsReady reports whether every top-level declaration that d references has
// already been evaluated (references to non-declaration names are left for the
// expression evaluator to resolve or reject).
func declDepsReady(d Decl, names map[string]bool, available map[string]cty.Value) bool {
	if d.Expr == nil {
		return true
	}
	for _, traversal := range d.Expr.Variables() {
		root := traversal.RootName()
		if root == d.Name {
			continue
		}
		if names[root] {
			if _, ok := available[root]; !ok {
				return false
			}
		}
	}
	return true
}
