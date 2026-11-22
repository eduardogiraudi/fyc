package ui

import (
	"context"
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

// SeedBlock is one entry of the alt-screen transcript seed, in the order the
// blocks were produced: text already rendered, or a tool result the chat keeps
// expandable (so a resumed chat shows the same clickable marker as the live
// one).
type SeedBlock struct {
	Text string
	Tool *ToolOutput
	// Turn marks a turn boundary: the block itself prints nothing, it only tells
	// the seeded transcript where a turn ended (see TurnSink).
	Turn bool
}

// preProgramBlock divertes rendered blocks printed before the chat program
// exists (session replay on start-up) away from stdout. The alternate screen is
// entered afterwards and would hide them, so the alt-screen chat seeds them into
// its transcript instead.
var preProgramBlock struct {
	mu sync.Mutex
	fn func(SeedBlock)
}

// SetPreProgramBlockSink installs (or clears, with nil) the receiver of blocks
// rendered before a chat program is attached.
func SetPreProgramBlockSink(fn func(SeedBlock)) {
	preProgramBlock.mu.Lock()
	preProgramBlock.fn = fn
	preProgramBlock.mu.Unlock()
}

// PrintRenderedBlock writes an already-rendered block when no program is
// attached: into the alt-screen seed when one is registered, into stdout
// otherwise.
func PrintRenderedBlock(text string) {
	printPreProgramBlock(SeedBlock{Text: text}, false)
}

// PrintReplayBlock keeps the full source in the seed but clamps plain stdout.
func PrintReplayBlock(text string) {
	printPreProgramBlock(SeedBlock{Text: text}, true)
}

// PrintToolBlock writes an expandable tool result when no program is attached:
// into the alt-screen seed when one is registered, into stdout as its collapsed
// rendering otherwise (there is nothing to click in a scrollback).
func PrintToolBlock(tool *ToolOutput) {
	if tool == nil {
		return
	}
	printPreProgramBlock(SeedBlock{Tool: tool}, true)
}

// PrintTurnBoundary records a turn boundary for a transcript that does not exist
// yet (session replay on start-up): the block carries no text and prints nothing
// on stdout, where turn history has no meaning.
func PrintTurnBoundary() {
	preProgramBlock.mu.Lock()
	fn := preProgramBlock.fn
	preProgramBlock.mu.Unlock()
	if fn != nil {
		fn(SeedBlock{Turn: true})
	}
}

// TurnSink is implemented by the sink of the running alt-screen chat: it keeps
// the transcript's turn boundaries aligned with the conversation, so a rewound
// turn can retract exactly the rows it produced.
type TurnSink interface {
	// CloseTurn records where the turn that just finished ended.
	CloseTurn()
	// AlignTurns drops the boundaries of turns that no longer exist (compaction,
	// /clear, a different session).
	AlignTurns(turns int)
	// RewindTurns retracts the transcript back to the start of a 0-based turn,
	// reporting how many rows disappeared. ok is false when the running frame
	// keeps no transcript of its own (the inline REPL, the AG-UI server) or does
	// not answer in time.
	RewindTurns(ctx context.Context, turn int) (rows int, ok bool)
}

func printPreProgramBlock(block SeedBlock, clamp bool) {
	text := block.Text
	if text == "" && block.Tool == nil {
		return
	}
	preProgramBlock.mu.Lock()
	fn := preProgramBlock.fn
	preProgramBlock.mu.Unlock()
	if fn != nil {
		fn(block)
		return
	}
	if block.Tool != nil {
		rows, _ := block.Tool.Rows(TerminalWidth())
		text = strings.Join(rows, "\n")
	} else if clamp {
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
	s.WriteToolOutputDetail(label, res, "")
}

// WriteToolOutputDetail implements types.ToolDetailSink: detail is the
// human-facing part of the result (e.g. the changed lines of an anchored edit)
// and never reaches the model.
func (s *TerminalOutputSink) WriteToolOutputDetail(label, res, detail string) {
	if s.send(StreamToolOutputMsg{Label: label, Result: res, Detail: detail}) {
		return
	}
	s.streamRenderer.Finalize()
	PrintToolOutput(label, res)
}

// PrintToolBlock prints an expandable tool result (see ToolOutput): the
// alt-screen transcript keeps the marker clickable, and before the program
// exists the block waits in the seed so a resumed chat shows it too.
func (s *TerminalOutputSink) PrintToolBlock(tool *ToolOutput) {
	if tool == nil {
		return
	}
	if s.send(StreamToolBlockMsg{Tool: tool}) {
		return
	}
	PrintToolBlock(tool)
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

// CloseTurn implements TurnSink: the boundary of the finished turn stays in the
// seed when the program is not attached yet, exactly like the blocks around it.
func (s *TerminalOutputSink) CloseTurn() {
	if s.send(StreamTurnCloseMsg{}) {
		return
	}
	PrintTurnBoundary()
}

// AlignTurns implements TurnSink. It only means something inside a running
// transcript, so without a program there is nothing to do.
func (s *TerminalOutputSink) AlignTurns(turns int) {
	s.send(StreamTurnAlignMsg{Turns: turns})
}

// RewindTurns implements TurnSink. The answer comes from the running frame, so
// a session without a transcript of its own (inline REPL, AG-UI) reports ok
// false instead of pretending the rows are gone.
func (s *TerminalOutputSink) RewindTurns(ctx context.Context, turn int) (int, bool) {
	reply := make(chan int, 1)
	if ctx.Err() != nil || !s.send(StreamTurnRewindMsg{Turn: turn, Reply: reply}) {
		return 0, false
	}
	select {
	case rows := <-reply:
		return rows, rows >= 0
	case <-ctx.Done():
		return 0, false
	}
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
func (s *TerminalOutputSink) SelectSearchableOption(title string, options []string) int {
	return SelectSearchableOption(title, options)
}
func (s *TerminalOutputSink) StartSubagent(id, label string)          {}
func (s *TerminalOutputSink) UpdateSubagent(id, status string)        {}
func (s *TerminalOutputSink) FinishSubagent(id, label, result string) {}
func (s *TerminalOutputSink) StartSubagentBatch(count int)            {}
func (s *TerminalOutputSink) FinishSubagentBatch()                    {}
func (s *TerminalOutputSink) StartSubagentPanel(panel interface{}) {
	s.send(StreamSubagentPanelMsg{Panel: panel})
}
