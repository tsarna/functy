package repl

import (
	"context"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2"
)

// staticHost is a Host over a single fixed *hcl.EvalContext: every input and
// every completion resolves against the same context, there is nothing to
// finish, and the only reserved session-binding names are the context's own
// variables. It suits the standalone functy CLI and any embedder whose context
// does not change between inputs.
type staticHost struct {
	ctx *hcl.EvalContext
}

// NewStaticHost returns a Host that evaluates against ctx unchanged. ctx must be
// non-nil and carry the Functions (and any Variables) available to the REPL.
func NewStaticHost(ctx *hcl.EvalContext) Host {
	return &staticHost{ctx: ctx}
}

func (h *staticHost) EvalContext(context.Context, string) (*hcl.EvalContext, func(), error) {
	return h.ctx, nil, nil
}

func (h *staticHost) CompletionContext(context.Context) (*hcl.EvalContext, error) {
	return h.ctx, nil
}

func (h *staticHost) Reserved(name string) bool {
	_, ok := h.ctx.Variables[name]
	return ok
}

// DefaultHistoryPath returns a conventional readline history location for an
// application: $XDG_STATE_HOME/<app>/history when XDG_STATE_HOME is set, else
// ~/.<app>_history. It returns "" (history disabled) if no location is usable.
func DefaultHistoryPath(app string) string {
	if x := os.Getenv("XDG_STATE_HOME"); x != "" {
		dir := filepath.Join(x, app)
		if err := os.MkdirAll(dir, 0o700); err == nil {
			return filepath.Join(dir, "history")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, "."+app+"_history")
	}
	return ""
}
