package main

import (
	"fmt"
	"strings"
	"time"

	"fuckyourcli/ui"

	tea "github.com/charmbracelet/bubbletea"
)

type generationDoneMsg struct{}

type chatModel struct {
	session *ChatSession
	sink    *ui.TerminalOutputSink
	input   ui.InputModel

	width  int
	height int

	generating            bool
	suppressGeneratingMsg bool
	quitting              bool

	reasoning ui.ReasoningView

	spinnerLabel string
	// activeSpinnerLabel is the label of the tool call currently running. Unlike
	// spinnerLabel (what is drawn) it survives a pause/resume cycle: a permission
	// prompt releases the terminal and clears the transient lines, and on resume
	// the spinner is restored so a long tool (e.g. sh) stays visible while it runs.
	activeSpinnerLabel string
	spinnerFrame       int
	diffActive         bool
	diffAdd            int
	diffDel            int
	diffLabel          string

	textBuf string

	spinnerTickScheduled bool
	statsProviderInfo    string
	statsTokenText       string
	statsHookText        string
	subagentPanel        *ui.SubagentPanel

	// transcript is non-nil only in alt-screen mode (config alt_screen): the
	// completed output is kept here, in the running program, because the
	// alternate screen has no scrollback to hold it.
	transcript *transcriptState

	// modal is an interactive screen (a permission selector, a settings menu, a
	// typed confirmation) hosted inside the frame instead of in a nested
	// program, which in alt-screen mode would drop the user out of the TUI. Nil
	// when the inline REPL runs it as a nested program or when nothing is open.
	modal tea.Model
	// modalReply hands the finished model back to the goroutine waiting for it
	// (see ui.runInApp); nil when no modal is open.
	modalReply chan tea.Model

	// copyNotice is the one-line confirmation of the last drag-selection copy,
	// empty when there is nothing to say.
	copyNotice    string
	copyNoticeErr bool
}

func newChatModel(session *ChatSession, sink *ui.TerminalOutputSink, history []string, sp ui.SuggestionProvider) chatModel {
	return chatModel{
		session:           session,
		sink:              sink,
		input:             ui.NewInputModel(history, sp),
		width:             80,
		height:            24,
		reasoning:         ui.NewReasoningView(80, 7),
		statsProviderInfo: currentProviderLine(),
	}
}

// enableAltScreen switches the chat to the full-screen transcript: completed
// output goes into transcript instead of the terminal scrollback. seed holds the
// blocks printed before the program started (banner, tips, start-up notices),
// which the alternate screen would otherwise hide.
func (m *chatModel) enableAltScreen(seed []string) {
	m.transcript = &transcriptState{}
	for _, block := range seed {
		m.transcript.Append(block)
	}
}

func (m chatModel) Init() tea.Cmd {
	// Start the status-bar hook once the program is running: sending to a
	// Program before Run panics, so main cannot do it before prog.Run().
	return func() tea.Msg {
		startStatusHook(m.session)
		return nil
	}
}

func (m chatModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// A hosted modal owns the keyboard and the mouse while it is open: only the
	// chat's own stream traffic (spinner, stats, hook lines) keeps flowing
	// underneath it.
	if cmd, handled := m.modalUpdate(msg); handled {
		return m, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.reasoning.Resize(msg.Width, reasoningHeight(msg.Height))
		// The markdown singleton is created at whatever width was current when
		// it was first used: without this sync a resize keeps every rendered
		// line at the old width and they overflow the (now narrower) terminal.
		ui.GetStreamRenderer().SetWidth(msg.Width)
		updated, cmd := m.input.UpdateComponent(msg)
		m.input = updated
		cmds = append(cmds, cmd)
		if m.modal != nil {
			var modalCmd tea.Cmd
			m.modal, modalCmd = m.modal.Update(msg)
			cmds = append(cmds, m.wrapModalCmd(modalCmd))
		}

	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			if m.generating {
				// Interrupt the turn: cancelling its context aborts the provider
				// stream AND stops a tool that is running (e.g. sh), without
				// quitting the program. Only an idle ctrl+c quits.
				m.session.GenerationCancelMu.Lock()
				if m.session.GenerationCancel != nil {
					m.session.GenerationCancel()
				}
				m.session.GenerationCancelMu.Unlock()
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		}
		if m.handleScrollKey(msg) {
			return m, nil
		}
		m.input = m.input.SetSubmitDisabled(m.generating)
		updated, cmd := m.input.UpdateComponent(msg)
		m.input = updated
		cmds = append(cmds, cmd)

	case tea.MouseMsg:
		// Alt-screen mode owns its scrollback, so the wheel belongs to the
		// transcript. Without mouse reporting the terminal would send arrow
		// keys instead and the input would walk its history. In the inline REPL
		// the terminal keeps the mouse for itself on purpose.
		if m.transcript == nil {
			break
		}
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			m.transcript.ScrollUp(mouseScrollRows)
		case tea.MouseButtonWheelDown:
			m.transcript.ScrollDown(mouseScrollRows)
		}
		if msg.Button == tea.MouseButtonLeft || msg.Action == tea.MouseActionRelease {
			switch msg.Action {
			case tea.MouseActionPress:
				m.transcript.SelectionStart(msg.Y, msg.X)
			case tea.MouseActionMotion:
				m.transcript.SelectionExtend(msg.Y, msg.X)
			case tea.MouseActionRelease:
				// The terminal has no clipboard of its own in alt screen, so a
				// selection is only useful if it is copied on release.
				text := m.transcript.SelectionText()
				if text == "" {
					m.transcript.SelectionClear()
					break
				}
				cmds = append(cmds, copySelectionCmd(text))
			}
		}
		return m, tea.Sequence(nonNil(cmds)...)
	case ui.InputEnterIdleMsg:
		updated, cmd := m.input.UpdateComponent(msg)
		m.input = updated
		cmds = append(cmds, cmd)

	case ui.InputSubmitMsg:
		input := strings.TrimRight(msg.Value, "\n")
		if strings.TrimSpace(input) == "" || m.generating {
			return m, nil
		}
		if input == "exit" || input == "/exit" {
			m.quitting = true
			return m, tea.Quit
		}
		m.generating = true
		// A new turn is the reader's own action: jump back to the live output
		// instead of leaving them on the rows they had scrolled to.
		if m.transcript != nil {
			m.transcript.Follow()
		}
		trimmed := strings.TrimSpace(input)
		m.suppressGeneratingMsg = trimmed == "/tools" || strings.HasPrefix(trimmed, "/tools ") ||
			trimmed == "/provider" || strings.HasPrefix(trimmed, "/provider ") ||
			trimmed == "/model" || strings.HasPrefix(trimmed, "/model ")
		m.input = m.input.ResetForNext()
		cmds = append(cmds, m.printCmd(ui.RenderSubmittedPrompt(input)))
		cmds = append(cmds, m.printCmd(ui.SeparatorStyle.Render(strings.Repeat("─", max(20, m.width-1)))))
		cmds = append(cmds, func() tea.Msg {
			m.session.ProcessInput(input)
			return generationDoneMsg{}
		})

	case ui.InputCancelMsg:
		m.quitting = true
		return m, tea.Quit

	case generationDoneMsg:
		m.generating = false
		m.suppressGeneratingMsg = false
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		m.clearTransient()
		m.statsProviderInfo = currentProviderLine()

	case ui.StreamResetMsg:
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		m.clearTransient()

	case ui.StreamTextMsg:
		cmds = append(cmds, m.finishReasoningCmd())
		m.spinnerLabel = ""
		m.textBuf += msg.Token
		cmds = append(cmds, m.flushCompletedTextCmds()...)

	case ui.StreamReasoningMsg:
		cmd := m.reasoning.Append(msg.Delta)
		cmds = append(cmds, cmd)

	case ui.ReasoningTickMsg:
		cmds = append(cmds, m.reasoning.Tick())

	case ui.StreamSpinnerStartMsg:
		cmds = append(cmds, m.finishReasoningCmd())
		m.spinnerLabel = msg.Label
		m.activeSpinnerLabel = msg.Label
		cmds = append(cmds, m.scheduleSpinnerTick())

	case ui.StreamSpinnerStopMsg:
		m.spinnerLabel = ""
		m.activeSpinnerLabel = ""

	case ui.SpinnerTickMsg:
		m.spinnerTickScheduled = false
		if m.subagentPanel != nil {
			m.subagentPanel.Tick()
			cmds = append(cmds, m.scheduleSpinnerTick())
		} else if m.spinnerLabel != "" || m.diffActive {
			m.spinnerFrame++
			cmds = append(cmds, m.scheduleSpinnerTick())
		}

	case ui.StreamDiffCraftMsg:
		cmds = append(cmds, m.finishReasoningCmd())
		m.spinnerLabel = ""
		m.diffActive = true
		m.diffAdd = msg.Add
		m.diffDel = msg.Del
		m.diffLabel = msg.Label
		cmds = append(cmds, m.scheduleSpinnerTick())

	case ui.StreamDiffCraftDoneMsg:
		m.diffActive = false

	case ui.StreamToolOutputMsg:
		m.textBuf = strings.TrimRight(m.textBuf, "\n\r ")
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		m.clearTransient()
		m.subagentPanel = nil
		cmds = append(cmds, m.printCmd(ui.FormatToolOutput(msg.Label, msg.Result, m.width, ui.ToolOutputRows)))

	case ui.StreamInterruptMsg:
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		m.clearTransient()
		cmds = append(cmds, m.printCmd(""))

	case ui.StreamNewlineMsg:
		m.textBuf += "\n"

	case ui.StreamStatsMsg:
		if msg.ProviderInfo != "" {
			m.statsProviderInfo = msg.ProviderInfo
		}
		if msg.TokenStats != "" {
			m.statsTokenText = msg.TokenStats
		}

	case ui.StreamHookLineMsg:
		// A background refresh of the status-bar hook: an empty text clears the
		// line (e.g. the hook was disabled), a non-empty one replaces it.
		m.statsHookText = msg.Text

	case ui.StreamFinalizeMsg:
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		m.clearTransient()

	case ui.StreamPauseMsg:
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		m.clearTransient()
		if msg.Ready != nil {
			cmds = append(cmds, func() tea.Msg {
				close(msg.Ready)
				return nil
			})
		}

	case ui.StreamPrintMsg:
		// A block that is already rendered (session replay): nothing may be
		// waiting in the markdown buffer, and the text must not be re-rendered.
		cmds = append(cmds, m.flushTextCmd())
		cmds = append(cmds, m.finishReasoningCmd())
		if strings.TrimSpace(msg.Text) != "" {
			cmds = append(cmds, m.printCmd(msg.Text))
		}
	case ui.StreamResumeMsg:
		// A nested program (permission prompt, questionary, ...) released the
		// terminal and cleared the transient lines: if a tool is still running,
		// bring its spinner back so the wait stays visible while it executes.
		if m.activeSpinnerLabel != "" {
			m.spinnerLabel = m.activeSpinnerLabel
			cmds = append(cmds, m.scheduleSpinnerTick())
		}
	case ui.StreamSubagentPanelMsg:
		if p, ok := msg.Panel.(*ui.SubagentPanel); ok {
			m.subagentPanel = p
		}
	case clipboardCopiedMsg:
		switch {
		case msg.err != nil:
			m.copyNotice = "copy failed: " + msg.err.Error()
			m.copyNoticeErr = true
		case msg.source == "osc52":
			// OSC 52 is fire-and-forget: the write proves nothing, and VTE
			// terminals drop the sequence without a word, so this branch must
			// never read as a confirmation when the terminal cannot honour it.
			if !ui.OSC52TerminalSupport() {
				m.copyNotice = fmt.Sprintf("not copied: this terminal ignores OSC 52 (%d line(s))", msg.lines)
				m.copyNoticeErr = true
				break
			}
			m.copyNotice = fmt.Sprintf("copied %d line(s) via OSC 52 · unconfirmed", msg.lines)
			m.copyNoticeErr = false
		default:
			m.copyNotice = fmt.Sprintf("copied %d line(s) to the clipboard", msg.lines)
			m.copyNoticeErr = false
		}
		cmds = append(cmds, copyNoticeExpired())
	case copyNoticeExpiredMsg:
		m.copyNotice = ""
		m.copyNoticeErr = false
	case ui.StreamModelMsg:
		// An interactive screen asked for while the chat owns the terminal (a
		// permission confirmation, /tools, /sessions, a typed confirm): host it
		// in the frame instead of a nested program, which would leave the
		// alternate screen, and answer through the message's reply channel.
		m.modal = msg.Model
		m.modalReply = msg.Reply
		var cmd tea.Cmd
		m.modal, cmd = m.modal.Update(m.modalSize())
		cmds = append(cmds, m.wrapModalCmd(cmd), m.wrapModalCmd(m.modal.Init()))
	case modalDoneMsg:
		if m.modalReply != nil {
			m.modalReply <- m.modal
			m.modalReply = nil
		}
		m.modal = nil
	}
	return m, tea.Sequence(nonNil(cmds)...)
}

func (m chatModel) View() string {
	if m.quitting {
		return ""
	}
	if m.transcript != nil {
		return m.altScreenView()
	}
	// The view is the only thing Bubble Tea truncates for us; clamping it keeps
	// a stray over-wide part (a hook line, a panel row) from wrapping and
	// shifting the whole frame.
	return ui.ClampBlock(strings.TrimRight(strings.Join(m.liveParts(), "\n"), "\n"), m.width)
}

// liveParts is everything drawn below the completed output: in-progress
// reasoning, the live text tail, the status bar and the input. In the inline
// REPL it is the whole view (the transcript lives in the scrollback); in
// alt-screen mode it is the frame pinned to the bottom.
func (m chatModel) liveParts() []string {
	var parts []string
	if m.reasoning.Active() {
		parts = append(parts, m.reasoning.View())
	}
	if m.subagentPanel != nil && !m.subagentPanel.AllDone() {
		parts = append(parts, m.subagentPanel.View(m.width))
	}
	if m.spinnerLabel != "" {
		parts = append(parts, fmt.Sprintf("%s %s", ui.SpinnerFrame(m.spinnerFrame), m.spinnerLabel))
	}
	if m.diffActive {
		label := m.diffLabel
		if label == "" {
			label = "patching file"
		}
		parts = append(parts, fmt.Sprintf("%s %s  %s  %s", ui.SpinnerFrame(m.spinnerFrame), label, ui.AddStyle.Render(fmt.Sprintf("+%d", m.diffAdd)), ui.DeleteStyle.Render(fmt.Sprintf("-%d", m.diffDel))))
	}
	if strings.TrimSpace(m.textBuf) != "" {
		lines := strings.Split(m.textBuf, "\n")
		visible := lines
		if len(lines) > 5 {
			visible = lines[len(lines)-5:]
		}
		tail := strings.Join(visible, "\n")
		parts = append(parts, ui.DimStyle.Render(tail))
	}

	// The hint is only true while the input is the thing on screen: an open
	// modal owns enter and ctrl+c.
	if m.generating && !m.suppressGeneratingMsg && m.modal == nil {
		parts = append(parts, ui.DimStyle.Render("generating… enter disabled, ctrl+c to interrupt"))
	}
	if m.statsProviderInfo != "" {
		parts = append(parts, ui.InfoStyle.Render(m.statsProviderInfo))
	}
	if m.statsTokenText != "" {
		parts = append(parts, ui.InfoStyle.Render(m.statsTokenText))
	}
	if m.statsHookText != "" {
		parts = append(parts, ui.HookStyle.Render(m.statsHookText))
	}
	if m.copyNotice != "" {
		style := ui.DimStyle
		if m.copyNoticeErr {
			style = ui.DeleteStyle
		}
		parts = append(parts, style.Render(m.copyNotice))
	}
	// An open modal takes the place of the input: same frame, no nested
	// program, so a confirmation or a menu never leaves the alt screen.
	if m.modal != nil {
		parts = append(parts, ui.ClampBlock(m.modal.View(), m.width))
	} else {
		parts = append(parts, m.input.SetSubmitDisabled(m.generating).View())
	}
	return parts
}

// altScreenView lays out the full-screen frame: the transcript on top, the live
// area pinned to the bottom, and the free rows between them. The frame is
// clamped to exactly the terminal height, so the renderer never has to scroll
// the alt screen (which would shift the whole UI).
func (m chatModel) altScreenView() string {
	live := ui.ClampBlock(strings.TrimRight(strings.Join(m.liveParts(), "\n"), "\n"), m.width)
	liveRows := strings.Split(live, "\n")

	height := m.height
	if height <= 0 {
		height = 24
	}
	budget := height - len(liveRows)
	indicator := ""
	if budget > 3 && m.transcript.ScrolledUp() {
		// The hint needs a row of its own, between the transcript and the input.
		budget--
		indicator = ui.DimStyle.Render(fmt.Sprintf("↓ %d rows below · ctrl+end to follow", m.transcript.offset))
	}

	var rows []string
	if budget > 0 {
		// Rows were clamped at the width they were rendered with: a terminal
		// that got narrower would otherwise soft-wrap them and the frame would
		// no longer match the screen.
		if body := ui.ClampBlock(m.transcript.View(budget), m.width); body != "" {
			rows = append(rows, strings.Split(body, "\n")...)
		}
	}
	indicatorRows := 0
	if indicator != "" {
		indicatorRows = 1
	}
	if pad := height - len(rows) - len(liveRows) - indicatorRows; pad > 0 {
		rows = append(rows, make([]string, pad)...)
	}
	if indicator != "" {
		rows = append(rows, indicator)
	}
	rows = append(rows, liveRows...)
	// A live area taller than the screen (a long suggestion list) still has to
	// show the input: keep the tail.
	if len(rows) > height {
		rows = rows[len(rows)-height:]
	}
	return strings.Join(rows, "\n")
}

// clipboardCopiedMsg carries the outcome of a drag-selection copy.
type clipboardCopiedMsg struct {
	lines  int
	source string
	err    error
}

// copyNoticeExpiredMsg clears the copy confirmation after a few seconds.
type copyNoticeExpiredMsg struct{}

// copyNoticeTTL is how long the copy confirmation stays on screen.
const copyNoticeTTL = 3 * time.Second

// copyText is the clipboard writer; tests replace it to inspect what a drag
// selection hands over.
var copyText = ui.CopyToClipboard

func copySelectionCmd(text string) tea.Cmd {
	return func() tea.Msg {
		source, err := copyText(text)
		return clipboardCopiedMsg{lines: strings.Count(text, "\n") + 1, source: source, err: err}
	}
}

func copyNoticeExpired() tea.Cmd {
	return tea.Tick(copyNoticeTTL, func(time.Time) tea.Msg { return copyNoticeExpiredMsg{} })
}

// modalDoneMsg reports that the hosted modal finished: its reply waits to be
// handed back and the frame goes back to the input.
type modalDoneMsg struct{}

// modalUpdate feeds msg to the hosted modal. It reports handled=false for the
// chat's own stream traffic, which keeps repainting the frame underneath, and
// for the window size, which the chat handles before forwarding it.
func (m *chatModel) modalUpdate(msg tea.Msg) (tea.Cmd, bool) {
	if m.modal == nil {
		return nil, false
	}
	if _, isSize := msg.(tea.WindowSizeMsg); isSize || isChatOwnedMsg(msg) {
		return nil, false
	}
	var cmd tea.Cmd
	m.modal, cmd = m.modal.Update(msg)
	return m.wrapModalCmd(cmd), true
}

// wrapModalCmd keeps the hosted modal from quitting the program: a tea.Quit it
// returns (the way every interactive screen signals "I am done") becomes a
// modalDoneMsg, so only the modal closes and the chat keeps running.
func (m *chatModel) wrapModalCmd(cmd tea.Cmd) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			wrapped := make(tea.BatchMsg, 0, len(batch))
			for _, c := range batch {
				if c != nil {
					wrapped = append(wrapped, m.wrapModalCmd(c))
				}
			}
			return wrapped
		}
		if _, quit := msg.(tea.QuitMsg); quit {
			return modalDoneMsg{}
		}
		return msg
	}
}

// modalSize is the room the frame can give to a hosted modal: the terminal,
// minus the status rows the chat keeps drawing below it.
func (m chatModel) modalSize() tea.WindowSizeMsg {
	height := m.height
	if height <= 0 {
		height = 24
	}
	return tea.WindowSizeMsg{Width: m.width, Height: max(4, height-2)}
}

// isChatOwnedMsg reports whether msg belongs to the chat itself and must not be
// forwarded to a hosted modal: the stream, the arrival of a new modal and a
// modal that finished.
func isChatOwnedMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case modalDoneMsg, ui.StreamModelMsg, clipboardCopiedMsg, copyNoticeExpiredMsg:
		return true
	}
	return isChatStreamMsg(msg)
}

// isChatStreamMsg reports whether msg belongs to the chat's own stream: those
// keep updating the frame even while a modal owns the keyboard.
func isChatStreamMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case ui.StreamResetMsg, ui.StreamTextMsg, ui.StreamReasoningMsg, ui.ReasoningTickMsg,
		ui.StreamSpinnerStartMsg, ui.StreamSpinnerStopMsg, ui.SpinnerTickMsg,
		ui.StreamDiffCraftMsg, ui.StreamDiffCraftDoneMsg, ui.StreamToolOutputMsg,
		ui.StreamInterruptMsg, ui.StreamNewlineMsg, ui.StreamStatsMsg,
		ui.StreamHookLineMsg, ui.StreamFinalizeMsg, ui.StreamPauseMsg,
		ui.StreamPrintMsg, ui.StreamResumeMsg, ui.StreamSubagentPanelMsg,
		generationDoneMsg:
		return true
	}
	return false
}

func (m *chatModel) flushCompletedTextCmds() []tea.Cmd {
	boundary := ui.LastCompletedBlockBoundary(m.textBuf)
	if boundary <= 0 {
		return nil
	}

	completed := m.textBuf[:boundary]
	m.textBuf = m.textBuf[boundary:]

	if strings.TrimSpace(completed) == "" {
		return nil
	}

	rendered := ui.GetStreamRenderer().RenderMarkdown(completed)
	return []tea.Cmd{m.printCmd(strings.TrimRight(rendered, "\n"))}
}

func (m *chatModel) flushTextCmd() tea.Cmd {
	if m.textBuf == "" {
		return nil
	}
	text := m.textBuf
	m.textBuf = ""
	// Whitespace-only buffers (e.g. the blank separator queued between tool
	// calls) must not be flushed: rendering them would print a stray blank line.
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if strings.Contains(text, "\x1b[") {
		return m.printCmd(text)
	}
	rendered := ui.GetStreamRenderer().RenderMarkdown(text)
	return m.printCmd(strings.TrimRight(rendered, "\n"))
}

func (m *chatModel) finishReasoningCmd() tea.Cmd {
	label := m.reasoning.Finish()
	if label == "" {
		return nil
	}
	return m.printCmd(fmt.Sprintf("%s %s", ui.OkStyle.Render("❖"), ui.InfoStyle.Render(label)))
}

func (m *chatModel) printCmd(s string) tea.Cmd {
	block := ui.ClampBlock(s, m.width)
	// Alt-screen mode owns its own scrollback (see transcriptState): there is no
	// terminal output to print into, and the view repaints from the transcript.
	if m.transcript != nil {
		m.transcript.Append(block)
		return nil
	}
	return tea.Println(block)
}

// handleScrollKey consumes the transcript navigation keys and reports whether
// the key belonged to them, so the input never sees it. In the inline REPL the
// terminal does the scrolling, so every key goes to the input.
func (m *chatModel) handleScrollKey(msg tea.KeyMsg) bool {
	if m.transcript == nil {
		return false
	}
	switch msg.Type {
	case tea.KeyPgUp:
		m.transcript.ScrollUp(m.transcript.PageSize())
	case tea.KeyPgDown:
		m.transcript.ScrollDown(m.transcript.PageSize())
	case tea.KeyShiftUp:
		m.transcript.ScrollUp(1)
	case tea.KeyShiftDown:
		m.transcript.ScrollDown(1)
	case tea.KeyCtrlHome:
		m.transcript.ScrollTop()
	case tea.KeyCtrlEnd:
		m.transcript.Follow()
	default:
		return false
	}
	return true
}

func (m *chatModel) clearTransient() {
	m.spinnerLabel = ""
	m.diffActive = false
	m.diffAdd = 0
	m.diffDel = 0
	m.diffLabel = ""
}

func (m *chatModel) scheduleSpinnerTick() tea.Cmd {
	if m.spinnerTickScheduled {
		return nil
	}
	m.spinnerTickScheduled = true
	return ui.SpinnerTickCmd()
}

func reasoningHeight(termHeight int) int {
	if termHeight <= 0 {
		return 7
	}
	h := termHeight / 3
	if h < 5 {
		h = 5
	}
	if h > 10 {
		h = 10
	}
	return h
}

func nonNil(cmds []tea.Cmd) []tea.Cmd {
	out := make([]tea.Cmd, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd != nil {
			out = append(out, cmd)
		}
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
