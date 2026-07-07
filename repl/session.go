// Package repl implements a generic interactive read-eval-print loop over an
// HCL expression engine. Each input line is parsed as a single HCL expression
// (hclsyntax.ParseExpression) and evaluated against an *hcl.EvalContext supplied
// by a Host; results are echoed HCL-style and numbered into a "_" / "_N" history.
//
// The loop, prompt/result numbering, session bindings, meta-command dispatch,
// multi-line accumulation, value formatting, and tab-completion are all generic.
// A Host supplies the language/runtime specifics: the parent eval context for a
// given input (so a host may inject its own functions, ambient values, or a
// per-eval "ctx" object and open a trace span), the completion context, and the
// reserved-name policy. Heavy host concerns — logging, tracing — stay in the Host
// so this package depends only on readline, HCL, and cty.
//
// functy's own CLI drives this over its baseline context (see NewStaticHost); a
// richer host (e.g. Vinculum) layers its live configuration on top.
package repl

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/chzyer/readline"
	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
)

// Host supplies the runtime specifics the generic loop depends on. A minimal
// static host is available via NewStaticHost; richer hosts implement it directly.
type Host interface {
	// EvalContext returns the parent eval context for evaluating src, plus a
	// finish func run when that input's evaluation completes (e.g. to end a trace
	// span). finish may be nil. The Session layers session bindings (_, _N, and
	// named bindings) as a child of parent before evaluating. ctx is tied to the
	// session lifetime (cancelled on exit / SIGTERM) and is suitable as a span
	// parent.
	EvalContext(ctx context.Context, src string) (parent *hcl.EvalContext, finish func(), err error)

	// CompletionContext returns the context whose variables and functions are the
	// tab-completion candidates. It should be cheap and side-effect-free (no trace
	// spans): it is called per keystroke. It typically returns the same
	// variables/functions a user can reference, including any per-eval "ctx".
	CompletionContext(ctx context.Context) (*hcl.EvalContext, error)

	// Reserved reports whether name may not be bound as a session variable. The
	// Session already reserves "_" and the numbered "_N"; a host adds its own
	// (e.g. "ctx" and built-in top-level namespaces).
	Reserved(name string) bool
}

// MetaCommand is a host-provided extension to the base ":"-command set. Its
// Names are the accepted spellings (each beginning with ':'); Run receives the
// whitespace-split arguments that follow the command word and the session's
// diagnostic writer (for usage/error messages), and returns true to exit the
// REPL.
type MetaCommand struct {
	Names   []string
	Summary string // shown in :help
	Run     func(args []string, errOut io.Writer) (exit bool)
}

// Options configures a Session. All fields are optional.
type Options struct {
	// Banner is printed once at startup, before a generic usage hint. Empty
	// prints only the hint.
	Banner string

	// HistoryPath is the readline history file; "" disables persistence.
	HistoryPath string

	// Meta are additional meta-commands (e.g. log control) beyond the built-ins.
	Meta []MetaCommand

	// OnStart is invoked once after the line editor is live, with the editor's
	// redraw-aware writer. A host can point its async log output there so writes
	// erase and redraw the prompt cleanly. Nil if unused.
	OnStart func(refresh io.Writer)

	// OnDetach is invoked once during teardown, before the editor is closed, so a
	// host can repoint async output away from the (about-to-close) editor writer.
	OnDetach func()

	// Out and ErrOut override the result and diagnostic streams (used by tests).
	// When nil they default to the editor's stdout and os.Stderr.
	Out    io.Writer
	ErrOut io.Writer
}

// Session holds the state of a single interactive REPL run.
type Session struct {
	host Host
	rl   *readline.Instance

	banner      string
	historyPath string
	metaCmds    []MetaCommand
	metaNames   []string // completion candidates for ':' words
	onStart     func(io.Writer)
	onDetach    func()

	// out receives results and the banner (stdout); errOut receives diagnostics
	// and meta-command errors (stderr).
	out    io.Writer
	errOut io.Writer

	// baseCtx is cancelled on exit / SIGTERM; passed to Host methods.
	baseCtx context.Context
	cancel  context.CancelFunc

	// detachOnce guards the repoint-then-close teardown so it runs exactly once.
	detachOnce sync.Once

	// bindings holds session variables: named bindings plus the managed "_" and
	// "_N" results. Touched only by the foreground REPL goroutine.
	bindings map[string]cty.Value

	// files maps each synthetic "<repl:N>" input filename to its source, so
	// diagnostics can render caret-underlined snippets.
	files map[string]*hcl.File

	// inputCounter advances on every parsed input (the N in <repl:N>);
	// resultCounter advances only on successful, non-null evaluations (the N in _N).
	inputCounter  int
	resultCounter int
}

// baseMetaNames are the built-in meta-command spellings offered for completion.
func baseMetaNames() []string {
	return []string{":help", ":?", ":quit", ":q", ":exit", ":set", ":unset", ":vars"}
}

// New constructs a REPL session over host with the given options. It does not
// touch the terminal until Run is called.
func New(host Host, opts Options) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		host:        host,
		banner:      opts.Banner,
		historyPath: opts.HistoryPath,
		metaCmds:    opts.Meta,
		onStart:     opts.OnStart,
		onDetach:    opts.OnDetach,
		out:         opts.Out,
		errOut:      opts.ErrOut,
		baseCtx:     ctx,
		cancel:      cancel,
		bindings:    make(map[string]cty.Value),
		files:       make(map[string]*hcl.File),
	}
	s.metaNames = baseMetaNames()
	for _, m := range opts.Meta {
		s.metaNames = append(s.metaNames, m.Names...)
	}
	return s
}

// primaryPrompt is the main prompt; it embeds the number the next successful,
// non-null result will be bound to (e.g. "9> " means a result becomes _9), so
// scrollback doubles as an index into the _N history.
func (s *Session) primaryPrompt() string {
	return fmt.Sprintf("%d> ", s.resultCounter+1)
}

// continuationPrompt is shown for multi-line input. It is dotted and padded to
// the same width as the current primary prompt so the input column stays aligned.
func (s *Session) continuationPrompt() string {
	p := s.primaryPrompt()
	return strings.Repeat(".", len(p)-1) + " "
}

// Run drives the read-eval-print loop on the calling (foreground) goroutine. It
// returns when the user exits (EOF / :quit) or SIGTERM is received.
func (s *Session) Run() error {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:          s.primaryPrompt(),
		InterruptPrompt: "^C",
		EOFPrompt:       "",
		Stderr:          os.Stderr,
		HistoryFile:     s.historyPath,
		AutoComplete:    &completer{s: s},
	})
	if err != nil {
		return fmt.Errorf("failed to start line editor: %w", err)
	}
	s.rl = rl
	if s.out == nil {
		s.out = rl.Stdout()
	}
	if s.errOut == nil {
		s.errOut = os.Stderr
	}

	// Hand async output off to the editor's redraw-aware writer while the prompt
	// is live. detach() runs OnDetach (which repoints it) before closing.
	if s.onStart != nil {
		s.onStart(rl.Stderr())
	}
	defer s.detach()

	// SIGTERM breaks the loop (graceful exit). SIGINT is NOT handled here:
	// readline puts the terminal in raw mode, so Ctrl-C arrives as ErrInterrupt
	// on the current line rather than as a process signal.
	sigterm := make(chan os.Signal, 1)
	signal.Notify(sigterm, syscall.SIGTERM)
	defer signal.Stop(sigterm)
	go func() {
		select {
		case <-sigterm:
			s.cancel()
			s.detach() // repoint, then close to unblock Readline()
		case <-s.baseCtx.Done():
		}
	}()

	s.printBanner()
	s.loop()
	return nil
}

// detach runs the host's OnDetach (e.g. repoint async logs) and then closes the
// editor, in that order, so a concurrent write can never reach a closed editor
// writer. Safe to call multiple times.
func (s *Session) detach() {
	s.detachOnce.Do(func() {
		if s.onDetach != nil {
			s.onDetach()
		}
		if s.rl != nil {
			s.rl.Close()
		}
	})
}

func (s *Session) printBanner() {
	if s.banner != "" {
		fmt.Fprintln(s.out, s.banner)
	}
	fmt.Fprintln(s.out, "Type an expression to evaluate it, or :help for commands.")
}

func (s *Session) loop() {
	for {
		// Refresh the prompt each iteration so its result number tracks the
		// counter, which advances on every successful, non-null evaluation.
		s.rl.SetPrompt(s.primaryPrompt())

		line, err := s.rl.Readline()
		switch err {
		case readline.ErrInterrupt:
			// Ctrl-C: discard the current line and re-prompt.
			continue
		case io.EOF:
			// Ctrl-D on an empty line: exit.
			return
		case nil:
			// fall through
		default:
			// Editor closed (e.g. SIGTERM) or other read error: exit.
			return
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// A leading ':' marks a meta-command (':' starts no HCL expression).
		// Meta-commands are always single-line.
		if strings.HasPrefix(trimmed, ":") {
			if s.handleMeta(trimmed) {
				return
			}
			continue
		}

		// Expressions and assignments may span multiple lines: accumulate
		// continuation lines until the input parses (or yields a real error).
		buffer, ok := s.accumulate(line)
		if !ok {
			continue // discarded at the continuation prompt
		}
		s.dispatchInput(buffer)
	}
}
