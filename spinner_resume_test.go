package main

import (
	"testing"

	"fuckyourcli/ui"
)

// A tool that asks for permission (sh, write, git_*, ...) releases the terminal
// for the prompt; the pause clears the transient lines and the resume must bring
// the running tool's spinner back, so the wait stays visible while it executes.
func TestSpinnerSurvivesPauseResume(t *testing.T) {
	m := newChatModel(&ChatSession{}, nil, nil, nil)

	updated, _ := m.Update(ui.StreamSpinnerStartMsg{Label: "running 'npm test'"})
	m = updated.(chatModel)
	if m.spinnerLabel != "running 'npm test'" {
		t.Fatalf("the spinner must be shown when the tool starts, got %q", m.spinnerLabel)
	}

	// The permission prompt releases the terminal.
	updated, _ = m.Update(ui.StreamPauseMsg{Ready: make(chan struct{})})
	m = updated.(chatModel)
	if m.spinnerLabel != "" {
		t.Fatalf("the pause must clear the displayed spinner, got %q", m.spinnerLabel)
	}

	// The prompt is answered and the tool keeps running: the spinner is back.
	updated, _ = m.Update(ui.StreamResumeMsg{})
	m = updated.(chatModel)
	if m.spinnerLabel != "running 'npm test'" {
		t.Fatalf("the spinner must be restored on resume, got %q", m.spinnerLabel)
	}

	// The tool ends: the spinner is gone for good.
	updated, _ = m.Update(ui.StreamSpinnerStopMsg{})
	m = updated.(chatModel)
	if m.spinnerLabel != "" || m.activeSpinnerLabel != "" {
		t.Fatalf("stop must clear the displayed and the active spinner, got %q/%q", m.spinnerLabel, m.activeSpinnerLabel)
	}

	// A later resume (another prompt) must not resurrect a finished spinner.
	updated, _ = m.Update(ui.StreamResumeMsg{})
	m = updated.(chatModel)
	if m.spinnerLabel != "" {
		t.Fatalf("a stop followed by a resume must not resurrect the spinner, got %q", m.spinnerLabel)
	}
}
