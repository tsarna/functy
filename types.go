package functy

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// parseTypeConstraint parses a functy type annotation. The annotation uses the
// standard cty type-constraint grammar (the same one Terraform variable blocks
// use), so the byte span after ':' is handed to typeexpr.
func parseTypeConstraint(src []byte, filename string, start hcl.Pos) (cty.Type, hcl.Diagnostics) {
	expr, diags := hclsyntax.ParseExpression(src, filename, start)
	if diags.HasErrors() {
		return cty.NilType, diags
	}
	ty, tdiags := typeexpr.TypeConstraint(expr)
	diags = diags.Extend(tdiags)
	if diags.HasErrors() {
		return cty.NilType, diags
	}
	return ty, diags
}

// convertValue converts a value to a declared type, returning a friendly error
// suitable for surfacing as a diagnostic or a catch-able functy error. A
// cty.NilType target means "dynamic" and the value passes through unchanged.
func convertValue(val cty.Value, ty cty.Type) (cty.Value, error) {
	if ty == cty.NilType {
		return val, nil
	}
	return convert.Convert(val, ty)
}
