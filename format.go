package functy

import (
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclwrite"
)

// fmtIndent is one level of indentation. functy source (and its docs/examples) use
// four spaces.
const fmtIndent = "    "

// Format parses src and returns it canonically formatted. It uses a default parser
// permitting top-level var/const; a host that registers named types (or other
// options) can call (*Parser).Format instead so those annotations resolve.
func Format(src []byte, filename string) ([]byte, hcl.Diagnostics) {
	return NewParser().AllowTopLevelVar(true).AllowTopLevelConst(true).Format(src, filename)
}

// Format parses src with the receiver's configuration and returns it canonically
// formatted. On any parse error it returns src unchanged together with the
// diagnostics — a file that does not fully parse is never reformatted, so fmt can
// never drop or reorder code. Expressions are reformatted with hclwrite (which
// preserves their internal comments); statement layout, indentation, blank-line
// runs, and statement/declaration comments are handled here.
func (p *Parser) Format(src []byte, filename string) ([]byte, hcl.Diagnostics) {
	res, diags := p.Parse(src, filename)
	if diags.HasErrors() {
		return src, diags
	}
	f := &formatter{src: src, comments: res.Comments}
	f.file(res)
	return []byte(f.finish()), diags
}

// formatter accumulates formatted output. Comments are held in a position-sorted
// list and flushed by a cursor (ci) as the AST is emitted in source order.
type formatter struct {
	src         []byte
	comments    []Comment
	ci          int
	out         strings.Builder
	indent      int
	lastLine    int  // source end-line of the last emitted content (0 = nothing yet)
	suppressGap bool // skip the next blank-line gap (set right after an opening brace)
}

func (f *formatter) pad() string { return strings.Repeat(fmtIndent, f.indent) }

// writeLine writes one output line at the current indent. content may itself hold
// newlines (a multi-line expression) whose continuation lines are already indented.
func (f *formatter) writeLine(content string) {
	if content != "" {
		f.out.WriteString(f.pad())
		f.out.WriteString(content)
	}
	f.out.WriteByte('\n')
}

// line writes a single statement line, appending any trailing comment that sits on
// the same source line, ending at end.
func (f *formatter) line(content string, end hcl.Pos) {
	f.writeLine(content + f.trailing(end))
}

func (f *formatter) blank() { f.out.WriteByte('\n') }

// openBlock writes a block header line ending in `{` (head is the text before it,
// "" for a bare block), appending any comment trailing the brace on its own line,
// then suppresses the next blank-line gap and increases indent.
func (f *formatter) openBlock(head string, brace hcl.Range) {
	line := "{"
	if head != "" {
		line = head + " {"
	}
	braceEnd := hcl.Pos{Line: brace.Start.Line, Byte: brace.Start.Byte + 1}
	f.writeLine(line + f.trailing(braceEnd))
	f.suppressGap = true
	f.indent++
}

// finish returns the accumulated output with exactly one trailing newline.
func (f *formatter) finish() string {
	s := strings.TrimRight(f.out.String(), "\n")
	if s == "" {
		return ""
	}
	return s + "\n"
}

// gapBefore emits a single blank line when the source had at least one blank line
// between the last emitted content and startLine (runs of blanks collapse to one).
func (f *formatter) gapBefore(startLine int) {
	if f.suppressGap {
		f.suppressGap = false
		return
	}
	if f.lastLine != 0 && startLine-f.lastLine >= 2 {
		f.blank()
	}
}

func (f *formatter) peekComment() *Comment {
	if f.ci < len(f.comments) {
		return &f.comments[f.ci]
	}
	return nil
}

// flushBefore emits, as their own lines, every unconsumed comment starting before
// byte offset `before`, preserving blank lines around them.
func (f *formatter) flushBefore(before int) {
	for c := f.peekComment(); c != nil && c.Range.Start.Byte < before; c = f.peekComment() {
		f.ci++
		f.gapBefore(c.Range.Start.Line)
		f.writeComment(*c)
		f.lastLine = c.Range.Start.Line + strings.Count(c.Text, "\n")
	}
}

func (f *formatter) writeComment(c Comment) {
	if !strings.Contains(c.Text, "\n") {
		f.writeLine(strings.TrimRight(c.Text, " \t"))
		return
	}
	// A multi-line block comment: indent its first line, but emit the interior
	// lines as authored (preserving any internal alignment) rather than forcing
	// them to the current indent.
	first, rest, _ := strings.Cut(c.Text, "\n")
	f.writeLine(strings.TrimRight(first, " \t"))
	f.out.WriteString(strings.TrimRight(rest, " \t\n"))
	f.out.WriteByte('\n')
}

// trailing consumes and returns a trailing comment (prefixed with two spaces) that
// sits on the same source line as, and after, end — or "" if there is none.
func (f *formatter) trailing(end hcl.Pos) string {
	c := f.peekComment()
	if c != nil && c.Line && c.Range.Start.Line == end.Line && c.Range.Start.Byte >= end.Byte {
		f.ci++
		return "  " + c.Text
	}
	return ""
}

// consumeWithin drops comments that fall inside [sb, eb) — they belong to an
// expression span reformatted by hclwrite, which preserves them, so the cursor must
// not emit them a second time.
func (f *formatter) consumeWithin(sb, eb int) {
	for c := f.peekComment(); c != nil && c.Range.Start.Byte < eb; c = f.peekComment() {
		if c.Range.Start.Byte < sb {
			break
		}
		f.ci++
	}
}

// expr formats an expression: hclwrite normalizes its interior (and preserves any
// comments in it), then continuation lines are re-indented to the current level
// (hclwrite formats a fragment as if at column 0).
func (f *formatter) expr(e hcl.Expression) string {
	r := e.Range()
	f.consumeWithin(r.Start.Byte, r.End.Byte)
	// hclwrite only normalizes operator spacing for an expression in attribute
	// position, so wrap the fragment as an attribute value, format, then strip the
	// synthetic `_ = ` prefix. (Structural spacing of {}/[] works either way.)
	wrapped := append([]byte("_ = "), f.src[r.Start.Byte:r.End.Byte]...)
	out := hclwrite.Format(wrapped)
	s := strings.TrimPrefix(strings.TrimRight(string(out), "\n"), "_ = ")
	lines := strings.Split(s, "\n")
	pad := f.pad()
	for i := 1; i < len(lines); i++ {
		if lines[i] != "" {
			lines[i] = pad + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}

// ---- top level --------------------------------------------------------------

type topItem struct {
	start hcl.Pos
	end   hcl.Pos
	emit  func(*formatter)
}

func (f *formatter) file(res *Result) {
	items := topLevelItems(res)
	for _, it := range items {
		f.flushBefore(it.start.Byte)
		f.gapBefore(it.start.Line)
		it.emit(f)
		f.lastLine = it.end.Line
	}
	f.flushBefore(len(f.src) + 1) // any leftover comments (e.g. a trailing block)
}

func topLevelItems(res *Result) []topItem {
	var items []topItem
	for _, fn := range res.Funcs {
		fn := fn
		items = append(items, topItem{fn.DefRange.Start, fn.BodyRange.End, func(f *formatter) { f.funcDecl(fn) }})
	}
	for _, td := range res.Tests {
		td := td
		items = append(items, topItem{td.DefRange.Start, td.BodyRange.End, func(f *formatter) { f.testDecl(td) }})
	}
	for i := range res.Consts {
		d := res.Consts[i]
		items = append(items, topItem{d.DefRange.Start, declEnd(d), func(f *formatter) { f.topDecl("const", d) }})
	}
	for i := range res.Vars {
		d := res.Vars[i]
		items = append(items, topItem{d.DefRange.Start, declEnd(d), func(f *formatter) { f.topDecl("var", d) }})
	}
	for i := range res.Types {
		a := res.Types[i]
		items = append(items, topItem{a.DefRange.Start, a.DefRange.End, func(f *formatter) { f.typeAlias(a) }})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].start.Byte < items[j].start.Byte })
	return items
}

func declEnd(d Decl) hcl.Pos {
	if d.Expr != nil {
		return d.Expr.Range().End
	}
	return d.DefRange.End
}

func (f *formatter) funcDecl(fn *FuncDecl) {
	ret := ""
	if fn.RetTypeSrc != "" {
		ret = " -> " + fn.RetTypeSrc
	}
	if f.multilineParams(fn) {
		f.writeLine("func " + fn.Name + "(")
		f.suppressGap = true
		f.indent++
		for i := range fn.Params {
			p := fn.Params[i]
			f.flushBefore(p.DefRange.Start.Byte) // a leading comment block above the param
			f.writeLine(f.paramString(p) + "," + f.trailing(p.FullRange.End))
			f.lastLine = p.FullRange.End.Line
		}
		f.flushBefore(fn.ParenRange.End.Byte) // dangling comments before `)`
		f.suppressGap = false
		f.indent--
		f.openBlock(")"+ret, fn.BodyRange)
	} else {
		f.openBlock("func "+fn.Name+"("+f.params(fn.Params)+")"+ret, fn.BodyRange)
	}
	f.emitSeq(fn.Body, fn.BodyRange.End.Byte)
	f.indent--
	f.writeLine("}" + f.trailing(fn.BodyRange.End))
}

// multilineParams reports whether the parameter list should be rendered one per
// line: when the author wrote it across multiple source lines, or a comment sits
// inside the parentheses (which only reads sensibly in the multi-line layout).
func (f *formatter) multilineParams(fn *FuncDecl) bool {
	if len(fn.Params) == 0 {
		return false
	}
	if fn.Params[len(fn.Params)-1].FullRange.End.Line > fn.Params[0].DefRange.Start.Line {
		return true
	}
	lo, hi := fn.ParenRange.Start.Byte, fn.ParenRange.End.Byte
	for _, c := range f.comments {
		if c.Range.Start.Byte > lo && c.Range.Start.Byte < hi {
			return true
		}
	}
	return false
}

// paramString renders one parameter: [*]name[: T][ = default].
func (f *formatter) paramString(p Param) string {
	var b strings.Builder
	if p.Variadic {
		b.WriteByte('*')
	}
	b.WriteString(p.Name)
	if p.TypeSrc != "" {
		b.WriteString(": ")
		b.WriteString(p.TypeSrc)
	}
	if p.Default != nil {
		b.WriteString(" = ")
		b.WriteString(p.DefaultSrc)
	}
	return b.String()
}

func (f *formatter) params(params []Param) string {
	parts := make([]string, len(params))
	for i, p := range params {
		parts[i] = f.paramString(p)
	}
	return strings.Join(parts, ", ")
}

func (f *formatter) testDecl(td *TestDecl) {
	f.openBlock("test "+strconv.Quote(td.Name), td.BodyRange)
	f.emitSeq(td.Body, td.BodyRange.End.Byte)
	f.indent--
	f.writeLine("}" + f.trailing(td.BodyRange.End))
}

func (f *formatter) topDecl(kw string, d Decl) {
	var b strings.Builder
	b.WriteString(kw)
	b.WriteByte(' ')
	b.WriteString(d.Name)
	if d.TypeSrc != "" {
		b.WriteString(": ")
		b.WriteString(d.TypeSrc)
	}
	end := d.DefRange.End
	if d.Expr != nil {
		b.WriteString(" = ")
		b.WriteString(f.expr(d.Expr))
		end = d.Expr.Range().End
	}
	f.line(b.String(), end)
}

func (f *formatter) typeAlias(a TypeAlias) {
	f.line("type "+a.Name+" = "+a.TypeSrc, a.DefRange.End)
}

// ---- statement sequences ----------------------------------------------------

// emitSeq emits a body's statements, interleaving comments, and finally flushes any
// dangling comments before the block's closing brace at closeByte.
func (f *formatter) emitSeq(stmts []Statement, closeByte int) {
	for _, s := range stmts {
		start := s.srcRange().Start
		f.flushBefore(start.Byte)
		f.gapBefore(start.Line)
		f.stmt(s)
		f.lastLine = stmtEnd(s).Line
	}
	f.flushBefore(closeByte)
	f.suppressGap = false // an empty body must not suppress the next item's gap
}

func (f *formatter) stmt(s Statement) {
	switch n := s.(type) {
	case *VarDecl:
		f.varDecl(n)
	case *Assign:
		f.line(n.Name+" = "+f.expr(n.Expr), n.Expr.Range().End)
	case *CaptureAssign:
		op := " = "
		if n.Declare {
			op = " := "
		}
		f.line(n.ValName+", "+n.ErrName+op+f.expr(n.Expr), n.Expr.Range().End)
	case *ExprStmt:
		f.line(f.expr(n.Expr), n.Expr.Range().End)
	case *Return:
		if n.Expr == nil {
			f.line("return", n.SrcRange.End)
		} else {
			f.line("return "+f.expr(n.Expr), n.Expr.Range().End)
		}
	case *Throw:
		f.line("throw "+f.expr(n.Expr), n.Expr.Range().End)
	case *Defer:
		f.line("defer "+f.expr(n.Expr), n.Expr.Range().End)
	case *Break:
		f.line(labeled("break", n.Label), n.SrcRange.End)
	case *Continue:
		f.line(labeled("continue", n.Label), n.SrcRange.End)
	case *Fallthrough:
		f.line("fallthrough", n.SrcRange.End)
	case *Block:
		f.block(n)
	case *IfChain:
		f.ifChain(n)
	case *For:
		f.forStmt(n)
	case *Switch:
		f.switchStmt(n)
	case *Try:
		f.try(n)
	}
}

func labeled(kw, label string) string {
	if label != "" {
		return kw + " " + label
	}
	return kw
}

func (f *formatter) varDecl(n *VarDecl) {
	if n.Short {
		f.line(n.Name+" := "+f.expr(n.Init), n.Init.Range().End)
		return
	}
	var b strings.Builder
	b.WriteString("var ")
	b.WriteString(n.Name)
	if n.TypeSrc != "" {
		b.WriteString(": ")
		b.WriteString(n.TypeSrc)
	}
	end := n.SrcRange.End
	if n.Init != nil {
		b.WriteString(" = ")
		b.WriteString(f.expr(n.Init))
		end = n.Init.Range().End
	}
	f.line(b.String(), end)
}

func (f *formatter) block(n *Block) {
	f.openBlock("", n.SrcRange)
	f.emitSeq(n.Body, n.SrcRange.End.Byte)
	f.indent--
	f.writeLine("}" + f.trailing(n.SrcRange.End))
}

func (f *formatter) ifChain(n *IfChain) {
	for i, br := range n.Branches {
		head := "if " + f.expr(br.Condition)
		if i > 0 {
			head = "} else if " + f.expr(br.Condition)
		}
		f.openBlock(head, br.BodyRange)
		f.emitSeq(br.Body, br.BodyRange.End.Byte)
		f.indent--
	}
	if n.ElseRange.End.Byte > 0 {
		f.openBlock("} else", n.ElseRange)
		f.emitSeq(n.Else, n.ElseRange.End.Byte)
		f.indent--
		f.writeLine("}" + f.trailing(n.ElseRange.End))
	} else {
		last := n.Branches[len(n.Branches)-1]
		f.writeLine("}" + f.trailing(last.BodyRange.End))
	}
}

func (f *formatter) forStmt(n *For) {
	f.openBlock(f.forHeader(n), n.BodyRange)
	f.emitSeq(n.Body, n.BodyRange.End.Byte)
	f.indent--
	f.writeLine("}" + f.trailing(n.BodyRange.End))
}

func (f *formatter) forHeader(n *For) string {
	prefix := ""
	if n.Label != "" {
		prefix = n.Label + ": "
	}
	switch n.Kind {
	case ForRange:
		vars := n.ValName
		if n.KeyName != "" {
			vars = n.KeyName + ", " + n.ValName
		}
		return prefix + "for " + vars + " in " + f.expr(n.Collection)
	case ForClause:
		init := ""
		if n.Init != nil {
			init = f.inlineStmt(n.Init)
		}
		cond := ""
		if n.Cond != nil {
			cond = f.expr(n.Cond)
		}
		post := ""
		if n.Post != nil {
			post = f.inlineStmt(n.Post)
		}
		return prefix + "for " + init + "; " + cond + "; " + post
	default: // ForCond
		kw := "for"
		if n.While {
			kw = "while"
		}
		if n.Cond != nil {
			return prefix + kw + " " + f.expr(n.Cond)
		}
		return prefix + "for"
	}
}

// inlineStmt renders a simple statement as a single line without a newline, for use
// in a for-clause header (init/post).
func (f *formatter) inlineStmt(s Statement) string {
	switch n := s.(type) {
	case *VarDecl:
		if n.Short {
			return n.Name + " := " + f.expr(n.Init)
		}
		out := "var " + n.Name
		if n.TypeSrc != "" {
			out += ": " + n.TypeSrc
		}
		if n.Init != nil {
			out += " = " + f.expr(n.Init)
		}
		return out
	case *Assign:
		return n.Name + " = " + f.expr(n.Expr)
	case *CaptureAssign:
		op := " = "
		if n.Declare {
			op = " := "
		}
		return n.ValName + ", " + n.ErrName + op + f.expr(n.Expr)
	case *ExprStmt:
		return f.expr(n.Expr)
	}
	return ""
}

func (f *formatter) switchStmt(n *Switch) {
	head := "switch"
	if n.Subject != nil {
		head += " " + f.expr(n.Subject)
	}
	f.openBlock(head, n.BodyRange)
	for i, cl := range n.Clauses {
		start := cl.SrcRange.Start
		f.flushBefore(start.Byte)
		f.gapBefore(start.Line)
		if cl.IsDefault {
			f.writeLine("default:" + f.trailing(cl.SrcRange.End))
		} else {
			vals := make([]string, len(cl.Values))
			for j, v := range cl.Values {
				vals[j] = f.expr(v)
			}
			labelEnd := cl.Values[len(cl.Values)-1].Range().End
			f.writeLine("case " + strings.Join(vals, ", ") + ":" + f.trailing(labelEnd))
		}
		f.suppressGap = true
		f.indent++
		f.emitSeq(cl.Body, clauseEndByte(n, i))
		f.indent--
		f.lastLine = clauseEndLine(n, i)
	}
	f.indent--
	f.flushBefore(n.BodyRange.End.Byte)
	f.writeLine("}" + f.trailing(n.BodyRange.End))
}

// clauseEndByte bounds clause i's body: the next clause's start, or the switch's
// closing brace for the last clause.
func clauseEndByte(n *Switch, i int) int {
	if i+1 < len(n.Clauses) {
		return n.Clauses[i+1].SrcRange.Start.Byte
	}
	return n.BodyRange.End.Byte
}

func clauseEndLine(n *Switch, i int) int {
	if len(n.Clauses[i].Body) > 0 {
		return stmtEnd(n.Clauses[i].Body[len(n.Clauses[i].Body)-1]).Line
	}
	return n.Clauses[i].SrcRange.End.Line
}

func (f *formatter) try(n *Try) {
	f.openBlock("try", n.BodyRange)
	f.emitSeq(n.Body, n.BodyRange.End.Byte)
	f.indent--
	for _, c := range n.Catches {
		head := "} catch"
		if c.Name != "" {
			head += " " + c.Name
		}
		if c.TypeSrc != "" {
			head += ": " + c.TypeSrc
		}
		if c.Guard != nil {
			head += " if " + f.expr(c.Guard)
		}
		f.openBlock(head, c.BodyRange)
		f.emitSeq(c.Body, c.BodyRange.End.Byte)
		f.indent--
	}
	if n.FinallyRange.End.Byte > 0 {
		f.openBlock("} finally", n.FinallyRange)
		f.emitSeq(n.Finally, n.FinallyRange.End.Byte)
		f.indent--
		f.writeLine("}" + f.trailing(n.FinallyRange.End))
	} else {
		end := n.BodyRange.End
		if k := len(n.Catches); k > 0 {
			end = n.Catches[k-1].BodyRange.End
		}
		f.writeLine("}" + f.trailing(end))
	}
}

// stmtEnd returns the source end position of a statement, spanning its full extent
// (the closing brace for compound statements, the trailing expression otherwise).
func stmtEnd(s Statement) hcl.Pos {
	switch n := s.(type) {
	case *VarDecl:
		if n.Init != nil {
			return n.Init.Range().End
		}
		return n.SrcRange.End
	case *Assign:
		return n.Expr.Range().End
	case *CaptureAssign:
		return n.Expr.Range().End
	case *ExprStmt:
		return n.Expr.Range().End
	case *Return:
		if n.Expr != nil {
			return n.Expr.Range().End
		}
		return n.SrcRange.End
	case *Throw:
		return n.Expr.Range().End
	case *Defer:
		return n.Expr.Range().End
	case *Block:
		return n.SrcRange.End
	case *IfChain:
		if n.ElseRange.End.Byte > 0 {
			return n.ElseRange.End
		}
		if k := len(n.Branches); k > 0 {
			return n.Branches[k-1].BodyRange.End
		}
		return n.SrcRange.End
	case *For:
		return n.BodyRange.End
	case *Switch:
		return n.BodyRange.End
	case *Try:
		if n.FinallyRange.End.Byte > 0 {
			return n.FinallyRange.End
		}
		if k := len(n.Catches); k > 0 {
			return n.Catches[k-1].BodyRange.End
		}
		return n.BodyRange.End
	}
	return s.srcRange().End
}
