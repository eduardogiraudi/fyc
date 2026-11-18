package ui

import (
	"context"
	"fmt"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// preProgramBlock divertes rendered blocks printed before the chat program
// exists (session replay on start-up) away from stdout. The alternate screen is
// entered afterwards and would hide them, so the alt-screen chat seeds them into
// its transcript instead.
var preProgramBlock struct {
	mu sync.Mutex
	fn func(string)
}

// SetPreProgramBlockSink installs (or clears, with nil) the receiver of blocks
// rendered before a chat program is attached.
func SetPreProgramBlockSink(fn func(string)) {
	preProgramBlock.mu.Lock()
	preProgramBlock.fn = fn
	preProgramBlock.mu.Unlock()
}

// PrintRenderedBlock writes an already-rendered block when no program is
// attached: into the alt-screen seed when one is registered, into stdout
// otherwise.
func PrintRenderedBlock(text string) {
	printPreProgramBlock(text, false)
}

// PrintReplayBlock keeps the full source in the seed but clamps plain stdout.
func PrintReplayBlock(text string) {
	printPreProgramBlock(text, true)
}

func printPreProgramBlock(text string, clamp bool) {
	if text == "" {
		return
	}
	preProgramBlock.mu.Lock()
	fn := preProgramBlock.fn
	preProgramBlock.mu.Unlock()
	if fn != nil {
		fn(text)
		return
	}
	if clamp {
		text = ClampBlock(text, TerminalWidth())
	}
	fmt.Println(text)
}

// TerminalOutputSink bridges provider/session output into the long-lived Bubble Tea chat program.
type TerminalOutputSink struct {
	mu             sync.Mutex
	program        *tea.Program
	streamRenderer *MarkdownStreamRenderer
	inputArea      *InputArea
}

func (s *TerminalOutputSink) AttachProgram(p *tea.Program) {
	s.mu.Lock()
	s.program = p
	s.mu.Unlock()
	SetPromptProgram(p)
}

func (s *TerminalOutputSink) send(msg tea.Msg) bool {
	s.mu.Lock()
	p := s.program
	s.mu.Unlock()
	if p == nil {
		return false
	}
	p.Send(msg)
	return true
}

func (s *TerminalOutputSink) ReleaseTerminal() {
	s.mu.Lock()
	p := s.program
	s.mu.Unlock()
	if p != nil {
		_ = p.ReleaseTerminal()
	}
}

func (s *TerminalOutputSink) RestoreTerminal() {
	s.mu.Lock()
	p := s.program
	s.mu.Unlock()
	if p != nil {
		_ = p.RestoreTerminal()
	}
}

func (s *TerminalOutputSink) ReadLine() (string, bool) {
	if s.inputArea == nil {
		return "", false
	}
	return s.inputArea.ReadLine()
}

func NewTerminalOutputSink(history []string, sp SuggestionProvider) *TerminalOutputSink {
	return &TerminalOutputSink{
		streamRenderer: NewMarkdownStreamRenderer(),
		inputArea:      NewInputArea(history, sp),
	}
}

func (s *TerminalOutputSink) WriteText(token string) {
	if token == "" {
		return
	}
	if s.send(StreamTextMsg{Token: token}) {
		return
	}
	s.streamRenderer.Write(token)
}

func (s *TerminalOutputSink) WriteReasoning(delta string) {
	if delta == "" {
		return
	}
	if s.send(StreamReasoningMsg{Delta: delta}) {
		return
	}
}

func (s *TerminalOutputSink) StartSpinner(label string) {
	if s.send(StreamSpinnerStartMsg{Label: label}) {
		return
	}
}

func (s *TerminalOutputSink) StopSpinner() {
	if s.send(StreamSpinnerStopMsg{}) {
		return
	}
}

func (s *TerminalOutputSink) WriteDiffCraft(add, del int) {
	if s.send(StreamDiffCraftMsg{Add: add, Del: del}) {
		return
	}
}

// WriteDiffCraftFor carries the optional file label for the live patch line.
// It implements types.DiffCraftSink; sinks that don't fall back to WriteDiffCraft.
func (s *TerminalOutputSink) WriteDiffCraftFor(label string, add, del int) {
	if s.send(StreamDiffCraftMsg{Label: label, Add: add, Del: del}) {
		return
	}
}

func (s *TerminalOutputSink) FinishDiffCraft() {
	if s.send(StreamDiffCraftDoneMsg{}) {
		return
	}
}

func (s *TerminalOutputSink) WriteInterrupt() {
	if s.send(StreamInterruptMsg{}) {
		return
	}
	s.streamRenderer.Finalize()
	fmt.Println()
}

func (s *TerminalOutputSink) WriteNewline() {
	if s.send(StreamNewlineMsg{}) {
		return
	}
	fmt.Println()
}

func (s *TerminalOutputSink) WriteStats(providerInfo, tokenStats string) {
	s.send(StreamStatsMsg{ProviderInfo: providerInfo, TokenStats: tokenStats})
}

// WriteStatusLine implements types.StatusLineSink: it renders the custom
// status-bar hook line without touching the provider/token stats.
func (s *TerminalOutputSink) WriteStatusLine(text string) {
	s.send(StreamHookLineMsg{Text: text})
}
func (s *TerminalOutputSink) WriteToolOutput(label, res string) {
	if s.send(StreamToolOutputMsg{Label: label, Result: res}) {
		return
	}
	s.streamRenderer.Finalize()
	PrintToolOutput(label, res)
}

// PrintBlock prints an already-rendered block verbatim in the scrollback (see
// StreamPrintMsg). It implements the block sink used by the session replay.
func (s *TerminalOutputSink) PrintBlock(text string) {
	if text == "" {
		return
	}
	if s.send(StreamPrintMsg{Text: text}) {
		return
	}
	PrintRenderedBlock(text)
}

func (s *TerminalOutputSink) BeginStreamAttempt(ctx context.Context) bool {
	reply := make(chan bool, 1)
	if ctx.Err() != nil || !s.send(StreamAttemptBeginMsg{Reply: reply}) {
		return false
	}
	select {
	case supported := <-reply:
		return supported
	case <-ctx.Done():
		// The begin message may already have installed a snapshot.
		s.EndStreamAttempt()
		return false
	}
}

func (s *TerminalOutputSink) RollbackStreamAttempt(ctx context.Context) bool {
	reply := make(chan bool, 1)
	if ctx.Err() != nil || !s.send(StreamAttemptRollbackMsg{Reply: reply}) {
		return false
	}
	select {
	case restored := <-reply:
		return restored
	case <-ctx.Done():
		return false
	}
}

func (s *TerminalOutputSink) EndStreamAttempt() {
	s.send(StreamAttemptEndMsg{})
}

func (s *TerminalOutputSink) Reset() {
	if s.send(StreamResetMsg{}) {
		return
	}
	s.streamRenderer.Clear()
}

func (s *TerminalOutputSink) Finalize() {
	if s.send(StreamFinalizeMsg{}) {
		return
	}
	s.streamRenderer.Finalize()
}

func (s *TerminalOutputSink) PauseForExternalPrompt() {
	PauseForExternalPrompt()
}

func (s *TerminalOutputSink) SelectOption(title string, options []string) int {
	return SelectOption(title, options)
}
func (s *TerminalOutputSink) StartSubagent(id, label string)          {}
func (s *TerminalOutputSink) UpdateSubagent(id, status string)        {}
func (s *TerminalOutputSink) FinishSubagent(id, label, result string) {}
func (s *TerminalOutputSink) StartSubagentBatch(count int)            {}
func (s *TerminalOutputSink) FinishSubagentBatch()                    {}
func (s *TerminalOutputSink) StartSubagentPanel(panel interface{}) {
	s.send(StreamSubagentPanelMsg{Panel: panel})
}
