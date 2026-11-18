package main

import (
	"fuckyourcli/ui"

	"github.com/charmbracelet/x/ansi"
)

// hoverTarget is the clickable part of the alt-screen frame under the pointer.
type hoverTarget int

const (
	hoverNone hoverTarget = iota
	// hoverMode is the "[Mode: manual/auto]" badge of the status bar: clicking
	// it toggles auto mode, exactly like ctrl+o.
	hoverMode
	// hoverScrollBack is the "N rows below" hint that appears while the reader
	// is away from the live output: clicking it jumps back to the bottom.
	hoverScrollBack
)

// frameHit is the geometry of the clickable parts of the last drawn frame.
// View runs on a copy of the model (Bubble Tea passes it around by value), so
// the geometry hangs off a pointer: a mouse event has to be matched against the
// rows the user is actually looking at, not against a re-computation that could
// disagree with them.
type frameHit struct {
	modeRow, modeCol, modeWidth int
	modeOK                      bool
	scrollRow, scrollWidth      int
	scrollOK                    bool
}

// target reports what sits at a screen cell of the last frame.
func (h frameHit) target(x, y int) hoverTarget {
	if h.scrollOK && y == h.scrollRow && x >= 0 && x < h.scrollWidth {
		return hoverScrollBack
	}
	if h.modeOK && y == h.modeRow && x >= h.modeCol && x < h.modeCol+h.modeWidth {
		return hoverMode
	}
	return hoverNone
}

// hitAt is target on a model that may have no frame yet (or the inline REPL,
// which has no frame at all).
func (m chatModel) hitAt(x, y int) hoverTarget {
	if m.hit == nil {
		return hoverNone
	}
	return m.hit.target(x, y)
}

// recordHit stores where the clickable parts of the frame landed: frame is the
// rows altScreenView is about to return, hintRow the row of the "rows below"
// hint and statusRow the row of the provider/mode line (-1 for either when the
// part is not on screen).
func (m chatModel) recordHit(frame []string, hintRow, statusRow int) {
	if m.hit == nil {
		return
	}
	*m.hit = frameHit{}
	if hintRow >= 0 && hintRow < len(frame) {
		m.hit.scrollRow = hintRow
		m.hit.scrollWidth = ansi.StringWidth(frame[hintRow])
		m.hit.scrollOK = true
	}
	if statusRow < 0 || statusRow >= len(frame) {
		return
	}
	row := statusRow
	_, _, badge, ok := ui.SplitModeSegment(m.statsProviderInfo)
	if !ok {
		return
	}
	// Only what sits between the brackets is clickable: the badge is drawn as
	// "[Mode: x]" and the brackets are decoration, not part of the button.
	badgeWidth := ansi.StringWidth(badge)
	if badgeWidth <= 2 {
		return
	}
	m.hit.modeRow = row
	m.hit.modeWidth = badgeWidth - 2
	m.hit.modeCol = ansi.StringWidth(ansi.Strip(frame[row])) - badgeWidth + 1
	m.hit.modeOK = true
}
