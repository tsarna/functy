package functy

import "github.com/zclconf/go-cty/cty"

// SignalKind classifies a non-local control-flow transfer that propagates out of
// statement execution.
type SignalKind int

const (
	// SignalReturn unwinds to the enclosing function with a value.
	SignalReturn SignalKind = iota
	// SignalBreak exits the innermost enclosing loop.
	SignalBreak
	// SignalContinue skips to the next iteration of the innermost loop.
	SignalContinue
	// SignalError unwinds a raised error (from throw or a failing expression)
	// until a try/catch handles it or it leaves the function.
	SignalError
	// SignalFallthrough transfers control to the next clause of the enclosing
	// switch. It is produced only by a Fallthrough statement and is always
	// consumed by execSwitch, never escaping the switch.
	SignalFallthrough
)

// Signal carries a control-flow transfer up the call stack. A nil *Signal means
// normal completion (fall-through to the next statement).
type Signal struct {
	Kind  SignalKind
	Value cty.Value // meaningful only for SignalReturn
	Label string    // target loop label for SignalBreak / SignalContinue ("" = innermost)
}
