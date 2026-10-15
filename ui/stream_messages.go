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
type StreamToolOutputMsg struct{ Label, Result string }
type StreamInterruptMsg struct{}
type StreamNewlineMsg struct{}

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

// StreamPauseMsg asks the chat model to flush all transient state and release
// the terminal. Ready is closed by the model once ReleaseTerminal has been
// called, so the sender can block on it to guarantee the stdin handoff is
// complete before launching a nested tea.Program.
type StreamPauseMsg struct{ Ready chan struct{} }

// mark itself as unpaused and re-render the input prompt.
type StreamResumeMsg struct{}

type SubagentTickMsg struct{}
type StreamSubagentPanelMsg struct{ Panel interface{} }
