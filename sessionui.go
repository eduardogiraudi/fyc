package main

import (
	"fmt"
	"strings"
	"time"

	"fuckyourcli/types"
	"fuckyourcli/ui"
)

// sessionAge renders a coarse "time ago" label for the session list.
func sessionAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// switchChatSession makes id the active session: the current one is saved first
// so nothing is lost, then the stored conversation replaces the in-memory one.
// The authoritative system prompt (index 0) is preserved.
func switchChatSession(s *ChatSession, id string) error {
	if id == "" || id == s.SessionID {
		return nil
	}
	if len(s.ChatHistory) > 0 {
		if err := saveChatSession(s); err != nil {
			return err
		}
	}
	sf, err := readSessionFile(getChatHistoryDir(), id)
	if err != nil {
		return err
	}
	msgs := make([]types.Message, 0, len(sf.Messages)+1)
	if len(s.ChatHistory) > 0 && s.ChatHistory[0].Role == "system" {
		msgs = append(msgs, s.ChatHistory[0])
	}
	for _, m := range sf.Messages {
		if m.Role == "system" {
			continue
		}
		msgs = append(msgs, m)
	}
	s.ChatHistory = msgs
	s.retryRequest = nil
	// The loaded conversation starts as plain chat: drop the runtime mode and
	// its injection flag so nothing dangles.
	s.persistentModeInjected = ""
	s.CurrentMode = nil
	updateSystemPrompt(s)
	s.SessionID = sf.ID
	s.SessionCreatedAt = sf.CreatedAt
	s.SessionTitle = sessionTitle(msgs)
	return nil
}

// startNewChat saves the current session and resets the in-memory one to a
// fresh chat (keeping the system prompt); the next turn creates a new session.
func startNewChat(s *ChatSession) error {
	if len(s.ChatHistory) > 0 {
		if err := saveChatSession(s); err != nil {
			return err
		}
	}
	filtered := make([]types.Message, 0, 1)
	for _, m := range s.ChatHistory {
		if m.Role == "system" {
			filtered = append(filtered, m)
		}
	}
	s.ChatHistory = filtered
	s.retryRequest = nil
	// A new chat starts as plain chat: drop the runtime mode and its injection
	// flag so nothing dangles.
	s.persistentModeInjected = ""
	s.CurrentMode = nil
	updateSystemPrompt(s)
	s.SessionID = ""
	s.SessionCreatedAt = time.Time{}
	s.SessionTitle = ""
	return nil
}

// renameChatSession updates a session title in the workspace index.
func renameChatSession(id, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("session title cannot be empty")
	}
	if !validSessionID(id) {
		return fmt.Errorf("unknown session %q", id)
	}
	workspaceDir := getChatHistoryDir()
	idx, err := loadSessionIndex(workspaceDir)
	if err != nil {
		return err
	}
	for i := range idx.Sessions {
		if idx.Sessions[i].ID == id {
			idx.Sessions[i].Title = title
			return saveSessionIndex(workspaceDir, idx)
		}
	}
	return fmt.Errorf("unknown session %q", id)
}

// deleteChatSession removes a session, clearing the active one when it is the
// deleted one.
func deleteChatSession(s *ChatSession, id string) error {
	if err := deleteSession(getChatHistoryDir(), id); err != nil {
		return err
	}
	if s.SessionID == id {
		s.SessionID = ""
		s.SessionCreatedAt = time.Time{}
		s.SessionTitle = ""
	}
	return nil
}

// sessionUIOps is the IO surface of the session manager. Keeping the selectors
// behind functions lets the menu run on top of the chat output sink and lets
// tests drive it without a terminal.
type sessionUIOps struct {
	selectOne  func(title string, options []string) int
	selectMany func(title string, options []string, states []bool, requireSelection bool) []bool
	prompt     func(prompt string) (string, bool)
}

// sessionMultiSelect is the checklist used for bulk actions; a seam so tests can
// stub the terminal prompt (like promptText).
var sessionMultiSelect = ui.MultiSelectOptionWithAction

// sinkSessionOps wires the menu to the active sink, so /sessions behaves like
// the rest of the TUI.
func sinkSessionOps(s *ChatSession) sessionUIOps {
	return sessionUIOps{
		selectOne:  s.Sink.SelectOption,
		selectMany: sessionMultiSelect,
		prompt:     promptLine,
	}
}

// sessionMenuItems are the pre-menu rows; the order is the contract with
// runSessionMenu's switch.
func sessionMenuItems() []string {
	return []string{"Switch session", "New chat", "Rename session", "Delete sessions…", "Cancel"}
}

// runSessionMenu is the single entry point for /sessions: a pre-menu that
// dispatches to switch, new, rename and delete. Sub-selectors treat q/Esc as
// "back to the menu" (the rename text prompt keeps q as a normal character;
// only Esc cancels there). Only Cancel — or dismissing the pre-menu — closes
// the command.
func runSessionMenu(s *ChatSession, ops sessionUIOps) {
	for {
		switch ops.selectOne("Sessions", sessionMenuItems()) {
		case 0: // Switch session
			if switchSessionInteractive(s, ops) {
				return
			}
		case 1: // New chat
			applySessionChoice(s, "")
			return
		case 2: // Rename session
			renameSessionInteractive(s, ops)
		case 3: // Delete sessions…
			deleteSessionsInteractive(s, ops)
		default: // Cancel / dismissed
			return
		}
	}
}

// listSessionMetas loads the workspace sessions; errors and an empty list are
// reported through the sink and yield ok=false (back to the pre-menu).
func listSessionMetas(s *ChatSession) ([]SessionMeta, bool) {
	metas, err := listSessions(getChatHistoryDir())
	if err != nil {
		s.Sink.WriteText(ui.InfoStyle.Render("Cannot list sessions: " + err.Error()))
		s.Sink.WriteNewline()
		return nil, false
	}
	if len(metas) == 0 {
		s.Sink.WriteText(ui.InfoStyle.Render("No saved sessions for this workspace"))
		s.Sink.WriteNewline()
		return nil, false
	}
	return metas, true
}

// sessionChoices builds the picker labels and the parallel id slice (newest
// first), marking the active session.
func sessionChoices(metas []SessionMeta, currentID string) (labels, ids []string) {
	for _, m := range metas {
		title := m.Title
		if title == "" {
			title = "(untitled)"
		}
		marker := ""
		if m.ID == currentID {
			marker = " (current)"
		}
		labels = append(labels, fmt.Sprintf("%s — %s (%d msgs)%s", title, sessionAge(m.UpdatedAt), m.MsgCount, marker))
		ids = append(ids, m.ID)
	}
	return labels, ids
}

// switchSessionInteractive lets the user pick a session and makes it active. It
// returns true when a switch happened (so the caller leaves the menu); a
// cancelled picker returns false, i.e. back to the pre-menu.
func switchSessionInteractive(s *ChatSession, ops sessionUIOps) bool {
	metas, ok := listSessionMetas(s)
	if !ok {
		return false
	}
	labels, ids := sessionChoices(metas, s.SessionID)
	idx := ops.selectOne("Switch to session", labels)
	if idx < 0 || idx >= len(ids) {
		return false // q/Esc: back to the pre-menu
	}
	applySessionChoice(s, ids[idx])
	return true
}

// renameSessionInteractive renames one session through the picker + a text
// prompt. A cancelled picker or prompt returns to the pre-menu.
func renameSessionInteractive(s *ChatSession, ops sessionUIOps) {
	metas, ok := listSessionMetas(s)
	if !ok {
		return
	}
	labels, ids := sessionChoices(metas, s.SessionID)
	idx := ops.selectOne("Rename which session", labels)
	if idx < 0 || idx >= len(ids) {
		return
	}
	id := ids[idx]
	title, ok := ops.prompt("New title (Enter to confirm, Esc to cancel): ")
	if !ok || title == "" {
		return
	}
	if err := renameChatSession(id, title); err != nil {
		s.Sink.WriteText(ui.InfoStyle.Render("Rename failed: " + err.Error()))
		s.Sink.WriteNewline()
		return
	}
	if id == s.SessionID {
		s.SessionTitle = title
	}
	s.Sink.WriteText(ui.OkStyle.Render("Session renamed"))
	s.Sink.WriteNewline()
}

// deleteSessionsInteractive lets the user tick several sessions and confirm a
// bulk delete. The action row is blocked while nothing is ticked, and the
// deletion always goes through an explicit confirmation.
func deleteSessionsInteractive(s *ChatSession, ops sessionUIOps) {
	metas, ok := listSessionMetas(s)
	if !ok {
		return
	}
	labels, ids := sessionChoices(metas, s.SessionID)
	labels = append(labels, "Delete selected")
	states := make([]bool, len(labels))
	newStates := ops.selectMany("Delete sessions", labels, states, true)
	if len(newStates) == 0 {
		return // cancelled (q/Esc): back to the pre-menu
	}
	var selected []string
	for i, id := range ids {
		if i < len(newStates) && newStates[i] {
			selected = append(selected, id)
		}
	}
	if len(selected) == 0 {
		return
	}
	if ops.selectOne(fmt.Sprintf("Delete %d session(s)?", len(selected)), []string{"No, keep them", "Yes, delete"}) != 1 {
		return
	}
	deleted := 0
	for _, id := range selected {
		if err := deleteChatSession(s, id); err != nil {
			s.Sink.WriteText(ui.InfoStyle.Render("Delete failed: " + err.Error()))
			s.Sink.WriteNewline()
			continue
		}
		deleted++
	}
	s.Sink.WriteText(ui.OkStyle.Render(fmt.Sprintf("Deleted %d session(s)", deleted)))
	s.Sink.WriteNewline()
}
