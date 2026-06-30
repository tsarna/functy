package main

import (
	"fmt"
	"io"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// loadProgram reads, parses, and compiles the given source paths against the
// baseline functions. Top-level const and var declarations are enabled and
// evaluated into the context's variables, so functions can reference constants
// declared near their point of use. It returns the assembled eval context
// (baseline + compiled functy functions, and the evaluated declarations as
// variables), a filename->file map for rendering diagnostics with source
// snippets, and any diagnostics produced along the way.
func loadProgram(paths []string, baseline map[string]function.Function) (*hcl.EvalContext, map[string]*hcl.File, hcl.Diagnostics) {
	sources, diags := functy.ParseSources(paths)

	files := make(map[string]*hcl.File, len(sources))
	for _, s := range sources {
		files[s.Filename] = &hcl.File{Bytes: s.Bytes}
	}
	if diags.HasErrors() {
		return nil, files, diags
	}

	var ctx *hcl.EvalContext
	res, pdiags := functy.NewParser().
		AllowTopLevelConst(true).
		AllowTopLevelVar(true).
		ParseAll(sources)
	diags = diags.Extend(pdiags)

	funcs, cdiags := res.Compile(func() *hcl.EvalContext { return ctx })
	diags = diags.Extend(cdiags)

	all := make(map[string]function.Function, len(baseline)+len(funcs))
	for k, v := range baseline {
		all[k] = v
	}
	for k, v := range funcs {
		if _, reserved := baseline[k]; reserved {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "Function name is reserved",
				Detail:   fmt.Sprintf("%q is a built-in baseline function and cannot be redefined.", k),
			})
			continue
		}
		all[k] = v
	}

	// The context is shared by reference: functy functions late-bind to it, and
	// the declaration evaluator below fills its Variables in dependency order.
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}

	// const declarations resolve before var declarations, matching the common
	// host ordering (a var may reference a const, not vice versa).
	decls := append(append([]functy.Decl{}, res.Consts...), res.Vars...)
	diags = diags.Extend(evalTopLevelDecls(decls, ctx))

	return ctx, files, diags
}

// evalTopLevelDecls evaluates collected const/var declarations into ctx.Variables.
// Declarations may reference one another out of source order, so this resolves
// them iteratively: each pass evaluates every declaration whose referenced
// declaration names are already available, until no progress is made. Anything
// left unresolved is a cyclic or dangling reference and is reported.
func evalTopLevelDecls(decls []functy.Decl, ctx *hcl.EvalContext) hcl.Diagnostics {
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

	pending := make([]functy.Decl, len(decls))
	copy(pending, decls)

	for len(pending) > 0 {
		var stuck []functy.Decl
		progress := false

		for _, d := range pending {
			if !depsReady(d, names, ctx.Variables) {
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

// depsReady reports whether every top-level declaration that d references has
// already been evaluated (references to non-declaration names are left for the
// expression evaluator to resolve or reject).
func depsReady(d functy.Decl, names map[string]bool, available map[string]cty.Value) bool {
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

// writeDiags renders diagnostics with source context to w.
func writeDiags(w io.Writer, files map[string]*hcl.File, diags hcl.Diagnostics) {
	if len(diags) == 0 {
		return
	}
	wr := hcl.NewDiagnosticTextWriter(w, files, 78, false)
	_ = wr.WriteDiagnostics(diags)
}
