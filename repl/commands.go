package repl

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// assignRe matches a bare assignment "NAME = EXPR". The trailing group captures
// everything after the first '='; the caller rejects it as a comparison ("==")
// when that group begins with '='. HCL's expression grammar has no top-level
// '=', so "NAME = …" is never itself a valid expression.
var assignRe = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_]*)\s*=(.*)$`)

// resultNameRe matches the managed numbered-result names (_1, _2, …).
var resultNameRe = regexp.MustCompile(`^_[0-9]+$`)

// parseAssignment reports whether src is a bare assignment and, if so, returns
// the target name and the right-hand expression source. A "==" comparison is
// not an assignment.
func parseAssignment(src string) (name, rhs string, ok bool) {
	m := assignRe.FindStringSubmatch(src)
	if m == nil {
		return "", "", false
	}
	if strings.HasPrefix(m[2], "=") {
		return "", "", false // "==" is a comparison expression, not an assignment
	}
	return m[1], m[2], true
}

// handleMeta dispatches a meta-command line (first non-space char is ':'). It
// returns true if the REPL should exit. Unknown commands fall through to the
// host-registered meta-commands before being reported as unknown.
func (s *Session) handleMeta(line string) (exit bool) {
	fields := strings.Fields(line)
	switch fields[0] {
	case ":quit", ":q", ":exit":
		return true
	case ":help", ":?":
		s.printHelp()
	case ":set":
		s.metaSet(line)
	case ":unset":
		s.metaUnset(fields)
	case ":vars":
		s.printVars()
	default:
		for _, m := range s.metaCmds {
			for _, n := range m.Names {
				if n == fields[0] {
					return m.Run(fields[1:], s.errOut)
				}
			}
		}
		fmt.Fprintf(s.errOut, "unknown command: %s (try :help)\n", fields[0])
	}
	return false
}

// setBinding evaluates rhsSrc and binds the result to name, after rejecting
// reserved names. A non-null result is also numbered into _/_N and echoed; a
// null result still creates the named binding but prints nothing and leaves _
// untouched.
func (s *Session) setBinding(name, rhsSrc string) {
	if err := s.checkAssignable(name); err != nil {
		fmt.Fprintf(s.errOut, "%s\n", err)
		return
	}
	val, ok := s.parseAndEval(rhsSrc)
	if !ok {
		return
	}
	s.bindings[name] = val
	s.echoAndNumber(val)
}

// checkAssignable returns an error if name may not be bound: the managed result
// names (_ and _N), or any name the host reserves (ctx, built-in namespaces, …).
func (s *Session) checkAssignable(name string) error {
	if name == "_" || resultNameRe.MatchString(name) {
		return fmt.Errorf("cannot assign %q: managed result name", name)
	}
	if s.host.Reserved(name) {
		return fmt.Errorf("cannot assign %q: reserved name", name)
	}
	return nil
}

func (s *Session) metaSet(line string) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), ":set"))
	name, rhs, ok := parseAssignment(rest)
	if !ok {
		fmt.Fprintln(s.errOut, "usage: :set NAME = EXPR")
		return
	}
	s.setBinding(name, rhs)
}

func (s *Session) metaUnset(fields []string) {
	if len(fields) != 2 {
		fmt.Fprintln(s.errOut, "usage: :unset NAME")
		return
	}
	name := fields[1]
	if name == "_" || resultNameRe.MatchString(name) {
		fmt.Fprintf(s.errOut, "cannot unset %q: managed result name\n", name)
		return
	}
	if _, ok := s.bindings[name]; !ok {
		fmt.Fprintf(s.errOut, "no such binding: %s\n", name)
		return
	}
	delete(s.bindings, name)
}

// printVars lists the user's named session bindings (the managed _ / _N results
// are omitted), with each value's type and a truncated rendering.
func (s *Session) printVars() {
	names := make([]string, 0, len(s.bindings))
	for k := range s.bindings {
		if k == "_" || resultNameRe.MatchString(k) {
			continue
		}
		names = append(names, k)
	}
	if len(names) == 0 {
		fmt.Fprintln(s.out, "(no bindings)")
		return
	}
	sort.Strings(names)

	width := 0
	for _, n := range names {
		if len(n) > width {
			width = len(n)
		}
	}
	for _, n := range names {
		v := s.bindings[n]
		fmt.Fprintf(s.out, "%-*s : %s = %s\n", width, n,
			v.Type().FriendlyName(), truncateOneLine(formatValue(v), 60))
	}
}

// printHelp prints the built-in commands followed by any host-registered
// meta-commands, aligned in one column.
func (s *Session) printHelp() {
	type row struct{ cmd, desc string }
	rows := []row{
		{":help, :?", "show this help"},
		{":quit, :q, :exit", "exit the REPL"},
		{":set NAME = EXPR", `bind EXPR to NAME (same as bare "NAME = EXPR")`},
		{":unset NAME", "remove a session binding"},
		{":vars", "list session bindings"},
	}
	for _, m := range s.metaCmds {
		rows = append(rows, row{strings.Join(m.Names, ", "), m.Summary})
	}

	width := 0
	for _, r := range rows {
		if len(r.cmd) > width {
			width = len(r.cmd)
		}
	}
	fmt.Fprintln(s.out, "Commands:")
	for _, r := range rows {
		fmt.Fprintf(s.out, "  %-*s   %s\n", width, r.cmd, r.desc)
	}
	fmt.Fprintln(s.out, "\nType any expression to evaluate it. Results are bound to _ and _1.._N.")
}

// truncateOneLine collapses a (possibly multi-line) rendering to a single line
// and truncates it to max runes, appending an ellipsis when shortened.
func truncateOneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
