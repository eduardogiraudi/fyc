package ui

// Stream/control messages sent from TerminalOutputSink to the long-lived chat Tea program.
type StreamTextMsg struct{ Token string }
type StreamReasoningMsg struct{ Delta string }
type StreamSpinnerStartMsg struct{ Label string }
type StreamSpinnerStopMsg struct{}
type StreamDiffCraftMsg struct {
	Label    string
	Add, Del int
}
type StreamDiffCraftDoneMsg struct{}

// StreamToolOutputMsg carries a finished tool call. Detail is the human-facing
// part of the result (never sent to the model) and may be empty.
type StreamToolOutputMsg struct{ Label, Result, Detail string }

// StreamToolBlockMsg carries a tool result the alt-screen chat keeps expandable
// in its transcript instead of printing once (see ToolOutput).
type StreamToolBlockMsg struct{ Tool *ToolOutput }
type StreamInterruptMsg struct{}
type StreamNewlineMsg struct{}

// StreamPrintMsg asks the chat model to print an already-rendered block
// verbatim in the scrollback. Session replay uses it: history text must land as
// completed output, never through the markdown stream renderer (which would
// re-wrap and re-highlight what was already rendered).
type StreamPrintMsg struct{ Text string }

// StreamStatsMsg carries provider info and token stats to be rendered above the input.
type StreamStatsMsg struct {
	ProviderInfo string
	TokenStats   string
}

// StreamHookLineMsg carries only a refreshed status-bar hook line. It is sent
// by the background refresh without disturbing the provider/token stats.
type StreamHookLineMsg struct{ Text string }
type StreamResetMsg struct{}
type StreamFinalizeMsg struct{}

// Attempt replies acknowledge rollback support in the actual running frame.
type StreamAttemptBeginMsg struct{ Reply chan bool }
type StreamAttemptRollbackMsg struct{ Reply chan bool }
type StreamAttemptEndMsg struct{}

// Turn messages keep the transcript's turn boundaries aligned with the history,
// so /undo can retract exactly the rows of the turns it discards. A boundary is
// recorded when a turn ends (CloseTurn) and only becomes a real split once the
// next turn starts on top of it, which is what keeps the count equal to the
// history's.
type StreamTurnCloseMsg struct{}

// StreamTurnRewindMsg asks the frame to retract the transcript back to the start
// of a 0-based turn. Reply receives the number of rows removed, or -1 when the
// running frame keeps no transcript of its own (inline REPL, AG-UI server).
type StreamTurnRewindMsg struct {
	Turn  int
	Reply chan int
}

// StreamTurnAlignMsg reports that the history now holds turns turns (compaction
// summarized the older ones away): only the boundaries of the surviving turns
// are kept.
type StreamTurnAlignMsg struct{ Turns int }

// StreamPauseMsg asks the chat model to flush all transient state and release
// the terminal. Ready is closed by the model once ReleaseTerminal has been
// called, so the sender can block on it to guarantee the stdin handoff is
// complete before launching a nested tea.Program.
type StreamPauseMsg struct{ Ready chan struct{} }

// mark itself as unpaused and re-render the input prompt.
type StreamResumeMsg struct{}

type SubagentTickMsg struct{}
type StreamSubagentPanelMsg struct{ Panel interface{} }
