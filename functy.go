// Package functy implements an imperative language whose values are cty values
// and whose expressions are HCL. A functy source file is a sequence of function
// declarations; compiling it yields ordinary cty function.Function values that
// can be added to an *hcl.EvalContext and called from any HCL expression.
//
// The statement grammar (func, var, if/else, for/while, switch, ...) is parsed
// by functy itself, while every embedded expression and type annotation is
// handed to HCL (hclsyntax.ParseExpression and ext/typeexpr), so operators,
// templates, function calls, and the cty type-constraint grammar behave exactly
// as they do elsewhere in HCL.
package functy

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// Source is a single functy source: its filename (used in diagnostics) and raw
// bytes. ParseSources collects these from files, directories, and embedded
// filesystems.
type Source struct {
	Filename string
	Bytes    []byte
}

// Parser parses functy source into a Result. Options accrue via chained setters;
// the zero value (via NewParser) accepts only function declarations.
type Parser struct {
	allowTopLevelVar   bool
	allowTopLevelConst bool
}

// NewParser returns a Parser with default options.
func NewParser() *Parser { return &Parser{} }

// AllowTopLevelVar controls whether a top-level `var` declaration is collected
// into Result.Vars (true) or reported as a parse error (false, the default).
func (p *Parser) AllowTopLevelVar(v bool) *Parser {
	p.allowTopLevelVar = v
	return p
}

// AllowTopLevelConst controls whether a top-level `const` declaration is
// collected into Result.Consts (true) or reported as a parse error (false, the
// default).
func (p *Parser) AllowTopLevelConst(v bool) *Parser {
	p.allowTopLevelConst = v
	return p
}

// Parse parses a single functy source. The returned Result holds the parsed
// declarations even when diagnostics contain errors (best-effort recovery), so
// callers should check diags before using it.
func (p *Parser) Parse(src []byte, filename string) (*Result, hcl.Diagnostics) {
	tokens, diags := lex(src, filename)
	pr := &parser{
		src:                src,
		filename:           filename,
		tokens:             tokens,
		allowTopLevelVar:   p.allowTopLevelVar,
		allowTopLevelConst: p.allowTopLevelConst,
	}
	result := pr.parseFile()
	diags = diags.Extend(pr.diags)
	return result, diags
}

// ParseAll parses several sources and merges their declarations into one Result.
// Per-source declarations are concatenated in order; duplicate function names
// across sources are detected later by Result.Compile.
func (p *Parser) ParseAll(sources []Source) (*Result, hcl.Diagnostics) {
	merged := &Result{}
	var diags hcl.Diagnostics
	for _, s := range sources {
		r, d := p.Parse(s.Bytes, s.Filename)
		diags = diags.Extend(d)
		merged.Funcs = append(merged.Funcs, r.Funcs...)
		merged.Consts = append(merged.Consts, r.Consts...)
		merged.Vars = append(merged.Vars, r.Vars...)
	}
	return merged, diags
}

// Result is the outcome of parsing one or more functy sources. It is a struct
// (rather than a bare map) so additional collected output can be added without
// breaking callers.
type Result struct {
	Funcs  []*FuncDecl // parsed function declarations
	Consts []Decl      // top-level const declarations (only when enabled)
	Vars   []Decl      // top-level var declarations (only when enabled)
}

// Decl is a collected top-level var/const declaration, returned unevaluated so a
// host can fold it into its own dependency-sorting and evaluation pass.
// Expr.Variables() exposes the references needed for that sort.
type Decl struct {
	Name     string
	Type     cty.Type       // from an optional `: T`; cty.NilType if unannotated
	Expr     hcl.Expression // initializer, lazily evaluated (nil if none)
	DefRange hcl.Range
}
