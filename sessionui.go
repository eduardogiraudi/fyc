package main

import (
	"fmt"
	"strings"
	"time"

	"fuckyourcli/tools"
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
	// The turn records of the previous conversation (anchors, workspace, turn
	// boundaries) describe turns this session does not have.
	s.resetTurnSnapshots()
	s.realignTurnState(0)
	// The loaded conversation starts as plain chat: drop the runtime mode and
	// its injection flag so nothing dangles.
	s.persistentModeInjected = ""
	s.CurrentMode = nil
	updateSystemPrompt(s)
	s.SessionID = sf.ID
	s.SessionCreatedAt = sf.CreatedAt
	s.SessionTitle = sessionTitle(msgs)
	replaySessionHistory(s)
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
	s.resetTurnSnapshots()
	s.realignTurnState(0)
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

// blockPrinter is implemented by sinks that can print an already-rendered block
// verbatim into the scrollback (the TUI one does). History replay is not a
// markdown stream: pushing it through the stream renderer would re-wrap and
// re-highlight text that was already rendered, and split it at block
// boundaries that no longer exist.
type blockPrinter interface{ PrintBlock(text string) }

// writeSessionBlock prints one replayed history block. Without a block-capable
// sink (plain stdout before the TUI starts, the AG-UI sink) the block is
// printed directly, clamped so a line can never soft-wrap the terminal.
func writeSessionBlock(s *ChatSession, block string) {
	if strings.TrimSpace(block) == "" {
		return
	}
	if bp, ok := s.Sink.(blockPrinter); ok {
		bp.PrintBlock(block)
		return
	}
	ui.PrintReplayBlock(block)
}

// replaySessionHistory redraws a resumed conversation: without it a loaded
// session opened on an empty terminal where the model had the whole history but
// the user could see nothing. Every block is reproduced the way the live turn
// printed it — tool calls with their result, truncated to the same rows and
// with the same "… N more lines hidden" marker — so a resumed chat reads like
// the one that was live.
func replaySessionHistory(s *ChatSession) {
	if s == nil || len(s.ChatHistory) == 0 {
		return
	}

	replayed := 0
	for _, m := range s.ChatHistory {
		switch m.Role {
		case "user":
			if replayablePrompt(m.Content) != "" {
				replayed++
			}
		case "assistant":
			replayed++
		}
	}
	if replayed == 0 {
		return
	}

	// The width drives both the wrapping and the truncation of a tool result,
	// so it must be the real terminal width (the TUI clamps to it as well).
	width := ui.TerminalWidth()
	writeSessionBlock(s, ui.InfoStyle.Render(fmt.Sprintf("↺ resumed chat · %d previous messages", replayed)))

	// pending holds the tool calls announced by the last assistant message that
	// have not met their result yet: a call still pending when another message
	// starts belongs to an interrupted turn and is replayed as a bare label,
	// like the spinner the live turn left behind.
	var pending []types.ToolCall
	flushPending := func() {
		for _, tc := range pending {
			lbl := tools.DescribeTool(tc.Function.Name, tc.Function.Arguments)
			writeSessionBlock(s, fmt.Sprintf("%s %s", ui.ToolStyle.Render("☉"), ui.InfoStyle.Render(lbl.Done)))
		}
		pending = pending[:0]
	}

	// The conversation being replayed replaces whatever the transcript was
	// showing: its turn boundaries are the only ones left, and they are rebuilt
	// from the history as the blocks go by (see chatTurns).
	alignTurns(s, 0)
	turns := chatTurns(s.ChatHistory)
	turnStarts := map[int]bool{}
	for i, t := range turns {
		if i > 0 && t.PromptIndex >= 0 {
			turnStarts[t.PromptIndex] = true
		}
	}

	for i, m := range s.ChatHistory {
		switch m.Role {
		case "user":
			flushPending()
			if strings.HasPrefix(m.Content, compactionSummaryPrefix) {
				writeSessionBlock(s, ui.TipStyle.Render("▣ compacted context summary"))
				continue
			}
			if prompt := replayablePrompt(m.Content); prompt != "" {
				if turnStarts[i] {
					closeTurn(s)
				}
				writeSessionBlock(s, ui.RenderSubmittedPrompt(prompt))
			}
		case "assistant":
			flushPending()
			if strings.TrimSpace(m.Content) != "" {
				writeSessionBlock(s, ui.GetStreamRenderer().RenderMarkdown(m.Content))
			}
			pending = append(pending, m.ToolCalls...)
		case "tool":
			call, _ := takePendingCall(&pending, m)
			writeSessionToolResult(s, m, call, width)
		}
	}
	flushPending()
	if len(turns) > 0 {
		closeTurn(s)
	}
}

// toolBlockPrinter is implemented by sinks that keep a tool result expandable
// instead of printing it once: a resumed chat then shows the same clickable
// marker the live one had.
type toolBlockPrinter interface{ PrintToolBlock(*ui.ToolOutput) }

// writeSessionToolResult replays a stored tool result exactly like the live TUI:
// an expandable block when the sink can keep it, the rendered text otherwise.
func writeSessionToolResult(s *ChatSession, message types.Message, call types.ToolCall, width int) {
	name := call.Function.Name
	if name == "" {
		name = message.Name
	}
	label := tools.DescribeTool(name, call.Function.Arguments)
	done := label.Done
	if types.ToolResultFailed(message.Content) {
		done = label.Failed
	}
	tool := ui.NewToolOutput(done, message.Content, message.ToolDetail)
	switch sink := s.Sink.(type) {
	case toolBlockPrinter:
		sink.PrintToolBlock(tool)
		return
	case nil:
		// Start-up replay runs before the chat has a sink: the block still goes
		// to the alt-screen seed, so the resumed chat keeps the marker.
		ui.PrintToolBlock(tool)
		return
	}
	writeSessionBlock(s, ui.FormatToolOutput(done, message.Content, width, ui.ToolOutputRows))
}

// takePendingCall pops the call a tool message answers: by call id when the
// transport assigned one, otherwise the first announced call with the same tool
// name (sessions written before call ids were persisted have none).
func takePendingCall(pending *[]types.ToolCall, m types.Message) (types.ToolCall, bool) {
	queue := *pending
	idx := -1
	if m.ToolCallID != "" {
		for i, tc := range queue {
			if tc.ID == m.ToolCallID {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		for i, tc := range queue {
			if tc.Function.Name == m.Name {
				idx = i
				break
			}
		}
	}
	if idx < 0 {
		return types.ToolCall{}, false
	}
	call := queue[idx]
	*pending = append(queue[:idx:idx], queue[idx+1:]...)
	return call, true
}

// replayablePrompt returns the prompt to redraw, or "" for the entries that are
// bookkeeping rather than something the user typed (mode instruction
// injections, empty turns).
func replayablePrompt(content string) string {
	if strings.HasPrefix(content, "[INSTRUCTIONS]: ") {
		return ""
	}
	return strings.TrimSpace(content)
}
