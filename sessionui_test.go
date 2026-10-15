package main

import (
	"strings"
	"testing"
	"time"

	"fuckyourcli/types"
)

// recordSink is a minimal OutputSink that captures text and answers the
// selector with a fixed index.
type recordSink struct {
	buf         strings.Builder
	selectIndex int
	selectQueue []int
	readLine    string
	readLineOK  bool
}

func (r *recordSink) reset()                         { r.buf.Reset() }
func (r *recordSink) text() string                   { return r.buf.String() }
func (r *recordSink) WriteText(s string)             { r.buf.WriteString(s) }
func (r *recordSink) WriteNewline()                  { r.buf.WriteString("\n") }
func (r *recordSink) WriteReasoning(string)          {}
func (r *recordSink) StartSpinner(string)            {}
func (r *recordSink) StopSpinner()                   {}
func (r *recordSink) WriteDiffCraft(int, int)        {}
func (r *recordSink) FinishDiffCraft()               {}
func (r *recordSink) WriteToolOutput(string, string) {}
func (r *recordSink) WriteInterrupt()                {}
func (r *recordSink) WriteStats(string, string)      {}
func (r *recordSink) Reset()                         {}
func (r *recordSink) Finalize()                      {}
func (r *recordSink) SelectOption(string, []string) int {
	if len(r.selectQueue) > 0 {
		v := r.selectQueue[0]
		r.selectQueue = r.selectQueue[1:]
		return v
	}
	return r.selectIndex
}
func (r *recordSink) ReadLine() (string, bool)              { return r.readLine, r.readLineOK }
func (r *recordSink) StartSubagent(string, string)          {}
func (r *recordSink) UpdateSubagent(string, string)         {}
func (r *recordSink) FinishSubagent(string, string, string) {}
func (r *recordSink) StartSubagentBatch(int)                {}
func (r *recordSink) FinishSubagentBatch()                  {}
func (r *recordSink) StartSubagentPanel(interface{})        {}

func historyContains(msgs []types.Message, needle string) bool {
	for _, m := range msgs {
		if strings.Contains(m.Content, needle) {
			return true
		}
	}
	return false
}

// stubSessionMultiSelect replaces the checklist seam, selecting every toggle
// row (the trailing action row included, which the caller ignores).
func stubSessionMultiSelect(t *testing.T) {
	t.Helper()
	orig := sessionMultiSelect
	sessionMultiSelect = func(_ string, options []string, _ []bool, _ bool) []bool {
		out := make([]bool, len(options))
		for i := range out {
			out[i] = true
		}
		return out
	}
	t.Cleanup(func() { sessionMultiSelect = orig })
}

// TestSessionsMenuLifecycle drives the /sessions pre-menu through rename,
// switch and new chat. The session list is most-recent-first.
func TestSessionsMenuLifecycle(t *testing.T) {
	ws := testWorkspace(t)
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: "/w", Sink: sink}

	// Two sessions: "alpha" (older) and "beta" (newest, active).
	s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "alpha"}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	idA := s.SessionID

	s.SessionID, s.SessionTitle, s.SessionCreatedAt = "", "", time.Time{}
	s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "beta"}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	idB := s.SessionID

	metas, err := listSessions(ws)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].ID != idB || metas[1].ID != idA {
		t.Fatalf("unexpected session order: %+v", metas)
	}

	// Rename: pre-menu "Rename session" (2) → picker index 1 (alpha) → title.
	origPrompt := promptText
	t.Cleanup(func() { promptText = origPrompt })
	promptText = func(string) (string, bool) { return "Renamed", true }

	sink.selectQueue = []int{2, 1, 4}
	handleSessionsCommand(s, "/sessions")
	metas, _ = listSessions(ws)
	if len(metas) != 2 || metas[1].ID != idA || metas[1].Title != "Renamed" {
		t.Fatalf("rename did not stick: %+v", metas)
	}

	// Switch to the older session: pre-menu "Switch session" (0) → picker 1.
	sink.selectQueue = []int{0, 1}
	handleSessionsCommand(s, "/sessions")
	if s.SessionID != idA {
		t.Fatalf("switch failed: session = %q, want %q", s.SessionID, idA)
	}
	if !historyContains(s.ChatHistory, "alpha") {
		t.Fatalf("switched history: %+v", s.ChatHistory)
	}

	// New chat: pre-menu "New chat" (1) clears the active session.
	sink.selectQueue = []int{1}
	handleSessionsCommand(s, "/sessions")
	if s.SessionID != "" {
		t.Fatalf("new chat kept session id %q", s.SessionID)
	}
	if len(s.ChatHistory) != 1 || s.ChatHistory[0].Role != "system" {
		t.Fatalf("new chat history: %+v", s.ChatHistory)
	}
}

// TestSessionsMenuDeleteMultiSelect deletes several sessions at once: the
// checklist ticks every row, then the confirmation says "Yes, delete".
func TestSessionsMenuDeleteMultiSelect(t *testing.T) {
	ws := testWorkspace(t)
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: "/w", Sink: sink}

	for _, c := range []string{"one", "two", "three"} {
		s.SessionID, s.SessionTitle, s.SessionCreatedAt = "", "", time.Time{}
		s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: c}}
		if err := saveChatSession(s); err != nil {
			t.Fatal(err)
		}
	}
	stubSessionMultiSelect(t)

	sink.selectQueue = []int{3, 1, 4} // pre-menu "Delete sessions…" → confirm "Yes, delete" → Cancel
	handleSessionsCommand(s, "/sessions")

	metas, _ := listSessions(ws)
	if len(metas) != 0 {
		t.Fatalf("delete left %d sessions: %+v", len(metas), metas)
	}
}

// TestSessionsMenuDeleteCancelled confirms that dismissing the confirmation
// keeps every session.
func TestSessionsMenuDeleteCancelled(t *testing.T) {
	ws := testWorkspace(t)
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: "/w", Sink: sink}
	s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "keep"}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	stubSessionMultiSelect(t)

	sink.selectQueue = []int{3, 0, 4} // pre-menu "Delete sessions…" → confirm "No, keep them" → Cancel
	handleSessionsCommand(s, "/sessions")

	if metas, _ := listSessions(ws); len(metas) != 1 {
		t.Fatalf("cancelled delete removed a session: %+v", metas)
	}
}

// TestSessionsMenuCancel: dismissing the pre-menu is a no-op, and q/Esc inside a
// sub-picker returns to the pre-menu instead of leaving /sessions.
func TestSessionsMenuCancel(t *testing.T) {
	ws := testWorkspace(t)
	sink := &recordSink{}
	s := &ChatSession{RestrictedRoot: "/w", Sink: sink}
	s.ChatHistory = []types.Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "keep"}}
	if err := saveChatSession(s); err != nil {
		t.Fatal(err)
	}
	id := s.SessionID

	// Dismissing the pre-menu is a no-op.
	sink.selectQueue = []int{-1}
	handleSessionsCommand(s, "/sessions")
	if s.SessionID != id {
		t.Fatalf("cancel changed the session: %q -> %q", id, s.SessionID)
	}

	// Switch → picker dismissed (q/Esc) → back to the pre-menu → Cancel.
	sink.selectQueue = []int{0, -1, 4}
	handleSessionsCommand(s, "/sessions")
	if s.SessionID != id {
		t.Fatalf("sub-menu cancel changed the session: %q -> %q", id, s.SessionID)
	}
	if metas, _ := listSessions(ws); len(metas) != 1 {
		t.Fatalf("unexpected sessions after cancel: %+v", metas)
	}
}
