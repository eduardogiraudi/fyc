package ui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// mouseLeakWindow bounds how long the pieces of one split mouse report may
// trail each other. They come from the same read burst, so a human keystroke
// can never fit between them.
const mouseLeakWindow = 250 * time.Millisecond

type mouseLeakPhase uint8

const (
	leakIdle mouseLeakPhase = iota
	// leakAfterEsc: a lone Esc was just seen; a leaked report continues with "[".
	leakAfterEsc
	// leakAfterBracket: "ESC [" was just seen; a leaked report continues with "<".
	leakAfterBracket
	// leakInReport: inside "<btn;col;row" waiting for the final M/m.
	leakInReport
)

// mouseLeakFilter drops the pieces of an SGR mouse report ("ESC [ < b ; x ; y M")
// that bubbletea v1 failed to reassemble.
//
// Mouse reporting is on in alt-screen mode (all-motion), so a loaded machine
// (cargo, a big build) leaves a backlog of reports in the tty. bubbletea reads
// stdin in 256-byte chunks and, when a chunk ends inside a report, reports the
// "ESC [" as Alt+"[" (or a lone Esc) and the remainder as plain runes, which
// would be typed into the prompt as "[<35;16;5M".
//
// The filter is a small state machine because the pieces arrive as separate
// messages. It is not a parser: anything that does not match the exact shape
// is passed through untouched.
type mouseLeakFilter struct {
	phase mouseLeakPhase
	at    time.Time
}

// filter returns the new state, the (possibly trimmed) key and whether the key
// must be dropped entirely.
func (f mouseLeakFilter) filter(k tea.KeyMsg, now time.Time) (mouseLeakFilter, tea.KeyMsg, bool) {
	if k.Paste {
		return mouseLeakFilter{}, k, false
	}

	// Alt+"[" is never useful text. Dropping it also covers a report cut
	// right after "ESC [".
	if k.Type == tea.KeyRunes && k.Alt && len(k.Runes) == 1 && k.Runes[0] == '[' {
		return mouseLeakFilter{phase: leakAfterBracket, at: now}, k, true
	}

	// A lone Esc keeps its normal meaning, but may be the head of a cut report.
	if k.Type == tea.KeyEsc && !k.Alt {
		return mouseLeakFilter{phase: leakAfterEsc, at: now}, k, false
	}

	if f.phase == leakIdle {
		return f, k, false
	}
	if now.Sub(f.at) > mouseLeakWindow || k.Type != tea.KeyRunes || k.Alt {
		return mouseLeakFilter{}, k, false
	}

	phase := f.phase
	for i, r := range k.Runes {
		switch {
		case phase == leakAfterEsc:
			if r != '[' {
				return mouseLeakFilter{}, k, false
			}
			phase = leakAfterBracket
		case phase == leakAfterBracket:
			if r != '<' {
				return mouseLeakFilter{}, k, false
			}
			phase = leakInReport
		case r >= '0' && r <= '9', r == ';':
			// still inside the report
		case r == 'M' || r == 'm':
			rest := k.Runes[i+1:]
			if len(rest) == 0 {
				return mouseLeakFilter{}, k, true
			}
			k.Runes = rest
			return mouseLeakFilter{}, k, false
		default:
			return mouseLeakFilter{}, k, false
		}
	}
	return mouseLeakFilter{phase: phase, at: now}, k, true
}
