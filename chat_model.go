package main

import (
	"fmt"
	"strings"

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
		m.input = m.input.SetSubmitDisabled(m.generating)
		updated, cmd := m.input.UpdateComponent(msg)
		m.input = updated
		cmds = append(cmds, cmd)

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
		cmds = append(cmds, m.printCmd(ui.FormatToolOutput(msg.Label, msg.Result, m.width, 2)))

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
	}
	return m, tea.Sequence(nonNil(cmds)...)
}

func (m chatModel) View() string {
	if m.quitting {
		return ""
	}
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

	if m.generating && !m.suppressGeneratingMsg {
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
	parts = append(parts, m.input.SetSubmitDisabled(m.generating).View())
	// The view is the only thing Bubble Tea truncates for us; clamping it keeps
	// a stray over-wide part (a hook line, a panel row) from wrapping and
	// shifting the whole frame.
	return ui.ClampBlock(strings.TrimRight(strings.Join(parts, "\n"), "\n"), m.width)
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

// printCmd queues a block for the scrollback. Bubble Tea writes printed lines
// verbatim — unlike the view, they are never truncated — so every line must
// already fit the terminal: one over-wide line soft-wraps, the renderer's line
// count stops matching the screen and the next repaint (status bar, input)
// lands on the wrong rows.
func (m *chatModel) printCmd(s string) tea.Cmd {
	return tea.Println(ui.ClampBlock(s, m.width))
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
