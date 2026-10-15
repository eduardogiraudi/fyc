package ui

import (
	"strings"
	"time"
)

// ReasoningTracker tracks the reasoning state in a shared way.
// Used by ReasoningView (main agent) and PanelBuffer (subagent).
type ReasoningTracker struct {
	active  bool
	started time.Time
	text    *strings.Builder
}

// Start begins or restarts the reasoning.
func (r *ReasoningTracker) Start() {
	r.active = true
	r.started = time.Now()
	if r.text == nil {
		r.text = &strings.Builder{}
	}
}

// Append adds a delta to the reasoning. Returns true if it just started.
func (r *ReasoningTracker) Append(delta string) (justStarted bool) {
	if delta == "" {
		return false
	}
	if !r.active {
		r.Start()
		justStarted = true
	}
	r.text.WriteString(delta)
	return justStarted
}

// Finish stops the reasoning and returns the "reasoned for Xs" label.
func (r *ReasoningTracker) Finish() string {
	if !r.active {
		return ""
	}
	dur := time.Since(r.started)
	if dur < 100*time.Millisecond {
		dur = 100 * time.Millisecond
	}
	label := "reasoned for " + FormatDuration(dur)
	r.Reset()
	return label
}

// Reset clears the state.
func (r *ReasoningTracker) Reset() {
	r.active = false
	r.started = time.Time{}
	if r.text != nil {
		r.text.Reset()
	}
}

// Active returns true if reasoning is in progress.
func (r *ReasoningTracker) Active() bool { return r.active }

// Text returns the accumulated text.
func (r *ReasoningTracker) Text() string {
	if r.text == nil {
		return ""
	}
	return r.text.String()
}
