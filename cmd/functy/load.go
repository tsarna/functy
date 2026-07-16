package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/tsarna/functy"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// defaultCLIMaxSteps is the CLI's generous default execution-limit ceiling (steps
// per function invocation). It is high enough that ordinary interactive exploration
// is never clipped, yet finite so a runaway `for` / `while` aborts with a
// *LimitError instead of wedging the process. Override with --max-steps; 0 disables.
const defaultCLIMaxSteps = 100_000_000

// maxSteps is bound to the root command's persistent --max-steps flag (see rootCmd)
// and applied to the Parser in loadProgram.
var maxSteps = defaultCLIMaxSteps

// loadProgram reads, parses, and compiles the given source paths against the
// baseline functions. Top-level const and var declarations are enabled and
// evaluated into the context's variables, so functions can reference constants
// declared near their point of use. It returns the assembled eval context
// (baseline + compiled functy functions, and the evaluated declarations as
// variables), a filename->file map for rendering diagnostics with source
// snippets, the parsed Result (carrying test blocks and declarations), and any
// diagnostics produced along the way.
// resolveSourceInput turns command arguments into a ParseSources input: a single
// "-" reads one buffer from stdin (named `filename`, or "<stdin>"); no arguments
// means the current directory tree; otherwise the args (files/dirs) are used
// as-is. Shared by check and symbols so their file/dir/stdin handling matches.
func resolveSourceInput(stdin io.Reader, args []string, filename string) (any, error) {
	switch {
	case len(args) == 1 && args[0] == "-":
		src, err := io.ReadAll(stdin)
		if err != nil {
			return nil, err
		}
		name := filename
		if name == "" {
			name = "<stdin>"
		}
		return functy.Source{Filename: name, Bytes: src}, nil
	case len(args) == 0:
		return []string{"."}, nil
	default:
		return args, nil
	}
}

// The input is anything functy.ParseSources understands — typically a []string
// of paths/directories, but also a functy.Source for in-memory content (e.g. an
// editor buffer piped to `check -`).
func loadProgram(input any, baseline map[string]function.Function) (*functy.Result, *functy.Compiled, *hcl.EvalContext, map[string]*hcl.File, hcl.Diagnostics) {
	sources, diags := functy.ParseSources(input)

	files := make(map[string]*hcl.File, len(sources))
	for _, s := range sources {
		files[s.Filename] = &hcl.File{Bytes: s.Bytes}
	}
	if diags.HasErrors() {
		return nil, nil, nil, files, diags
	}

	var ctx *hcl.EvalContext
	evalCtxFn := func() *hcl.EvalContext { return ctx }

	res, pdiags := functy.NewParser().
		MaxSteps(maxSteps).
		AllowTopLevelConst(true).
		AllowTopLevelVar(true).
		ParseAll(sources)
	diags = diags.Extend(pdiags)

	// Reflection builtins need the parsed declarations and the assembled context,
	// so they join the baseline (and its reserved-name set) here rather than in the
	// static baselineFunctions. help() renders functy functions from res.Funcs and
	// falls back to cty metadata for host functions; doc() reads descriptions. The
	// whole Result is passed so help() also sees res.Externs — a declared signature
	// beats the cty-metadata fallback, which is the reason externs exist.
	baseline["doc"] = functy.DocFunc(evalCtxFn)
	baseline["help"] = functy.HelpFunc(res, evalCtxFn)

	compiled, cdiags := res.CompileUnits(evalCtxFn)
	diags = diags.Extend(cdiags)

	all := make(map[string]function.Function, len(baseline)+len(compiled.Funcs))
	for k, v := range baseline {
		all[k] = v
	}
	for k, v := range compiled.Funcs {
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
	diags = diags.Extend(shadowWarnings(res, compiled, baseline))

	// The context is shared by reference: functy functions late-bind to it, and
	// the declaration evaluator below fills its Variables in dependency order.
	ctx = &hcl.EvalContext{Functions: all, Variables: map[string]cty.Value{}}

	// const declarations resolve before var declarations, matching the common
	// host ordering (a var may reference a const, not vice versa).
	decls := append(append([]functy.Decl{}, res.Consts...), res.Vars...)
	diags = diags.Extend(functy.EvalDecls(decls, ctx))

	return res, compiled, ctx, files, diags
}

// shadowWarnings reports a namespaced function whose bare name shadows a baseline
// builtin inside its own namespace.
//
// The reserved-name *error* above cannot catch this: a namespaced function reaches
// the host's map as `foo::upper`, which collides with nothing, while bare `upper`
// still wins inside `foo`'s bodies because a namespace's unit layer is consulted
// before the host's context. Namespacing therefore disarms the collision check
// precisely where the shadowing still happens, so the diagnostic is rebuilt here,
// where the host's function set is actually known. functy itself cannot do this —
// its eval context is late-bound and may not exist at compile time — which is why
// Compiled.Units is exported: a library host can run the same check.
//
// A warning, not an error: shadowing a builtin inside your own namespace is legal
// and occasionally deliberate. Private names cannot trigger it — no baseline
// function is `_`-prefixed.
func shadowWarnings(res *functy.Result, compiled *functy.Compiled, baseline map[string]function.Function) hcl.Diagnostics {
	var diags hcl.Diagnostics
	for _, fn := range res.Funcs {
		if fn.Namespace == "" || fn.IsPrivate() {
			continue
		}
		if _, shadows := baseline[fn.Name]; !shadows {
			continue
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagWarning,
			Summary:  "Function shadows a built-in inside its namespace",
			Detail: fmt.Sprintf(
				"%q is a built-in function. Within namespace %s, a call to %s resolves to this declaration rather than the built-in; elsewhere the built-in is unaffected. Call it as %s, or rename this function.",
				fn.Name, fn.Namespace, fn.Name, fn.QualifiedName()),
			Subject: fn.DefRange.Ptr(),
		})
	}
	return diags
}

// errEntryNotFound distinguishes "no such entry function" from a real resolution
// failure (an ambiguous bare name), because `run -i` treats a missing default
// `main` as fine — it just drops into the REPL — while an ambiguous name is still
// an error the user must resolve.
var errEntryNotFound = errors.New("entry function not found")

// resolveEntry finds an entry function by name, tolerating namespaces.
//
// A file that declares `namespace foo` registers its `main` as `foo::main`, so a
// plain `functy run file.cty` would otherwise stop working the moment a namespace
// is added. Resolution, in order:
//
//  1. an exact key in the assembled context — a host function, or an exported
//     functy function under its qualified name;
//  2. a bare name declared in exactly one namespace — which is what makes the
//     default `--func main` keep working, and what reaches a private `_helper`,
//     since neither is in the context at all.
//
// A bare name declared in several namespaces is ambiguous and is reported as such
// rather than guessed at. Returns the resolved (display) name alongside the
// function, so errors and traces can name what actually ran.
func resolveEntry(compiled *functy.Compiled, ctx *hcl.EvalContext, name string) (function.Function, string, error) {
	if ctx != nil {
		if fn, ok := ctx.Functions[name]; ok {
			return fn, name, nil
		}
	}

	var found function.Function
	var matches []string
	if compiled != nil {
		for ns, table := range compiled.Units {
			if fn, ok := table[name]; ok {
				found = fn
				matches = append(matches, functy.Qualify(ns, name))
			}
		}
	}
	switch len(matches) {
	case 0:
		return function.Function{}, "", fmt.Errorf("%w: %q", errEntryNotFound, name)
	case 1:
		return found, matches[0], nil
	default:
		sort.Strings(matches)
		return function.Function{}, "", fmt.Errorf(
			"entry function %q is declared in more than one namespace (%s); name one of them with --func",
			name, strings.Join(matches, ", "))
	}
}

// writeDiags renders diagnostics with source context to w, colorizing (which is how
// hcl's writer highlights the offending source span) only when w is an interactive
// terminal.
func writeDiags(w io.Writer, files map[string]*hcl.File, diags hcl.Diagnostics) {
	if len(diags) == 0 {
		return
	}
	wr := hcl.NewDiagnosticTextWriter(w, files, 78, isTerminal(w))
	_ = wr.WriteDiagnostics(diags)
}

// isTerminal reports whether w is an interactive terminal, so color is used only when
// a human is watching (not when output is piped or redirected to a file). It honors
// the NO_COLOR convention and detects a terminal dependency-free, by testing for a
// character device — the standard heuristic when x/term / isatty aren't in the module.
func isTerminal(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
