// Package reasoning holds the provider-independent model of "thinking" that fyc
// exposes to the user, plus the translation of that intent into the wire fields
// each transport expects. Translate is pure: the same Intent can be translated
// for any transport and the result is unit-testable in isolation. Lookup is the
// only I/O-adjacent entry point: it may refine a Profile with the optional
// models.dev catalog (see catalog.go), which is read through a cache and is
// best-effort by design.
//
// The design separates three things that the legacy code conflated:
//
//   - Intent: what the user asked for (off, adaptive, a named effort, a budget).
//   - Profile: how a given (transport, model) pair exposes thinking on the wire.
//   - Plan: the concrete request fields to set, produced by Translate.
package reasoning

// Mode is the user's desired thinking behaviour, independent of any provider.
type Mode int

const (
	// ModeDefault sends nothing and lets the server decide. This is the
	// defensive default: the CLI never guesses a field a model might reject.
	ModeDefault Mode = iota
	// ModeOff asks to disable thinking explicitly, when the model allows it.
	ModeOff
	// ModeAdaptive lets the model pick the depth (Anthropic "adaptive",
	// Gemini dynamic thinking).
	ModeAdaptive
	// ModeEffort selects a named effort level ("low", "high", ...).
	ModeEffort
	// ModeBudget selects an explicit token budget for thinking.
	ModeBudget
)

// String renders the mode for logs and the /reasoning UI.
func (m Mode) String() string {
	switch m {
	case ModeDefault:
		return "default"
	case ModeOff:
		return "off"
	case ModeAdaptive:
		return "adaptive"
	case ModeEffort:
		return "effort"
	case ModeBudget:
		return "budget"
	default:
		return "unknown"
	}
}

// Intent is what the user asked for in /reasoning, before any provider quirk is
// taken into account.
type Intent struct {
	Mode   Mode
	Effort string // ModeEffort only
	Budget int    // ModeBudget only
	// ExtraBody is a free-form escape hatch. Its dotted keys are merged into
	// the request after every translated field, so it always wins; it is how a
	// user covers a provider the built-in tables do not know yet.
	ExtraBody map[string]any
}
