package ui

import "fuckyourcli/types"

// Compile-time guarantee: the TUI sink carries the optional live-patch label.
var _ types.DiffCraftSink = (*TerminalOutputSink)(nil)

// ... and the human-facing detail of a tool result (the changed lines of a patch).
var _ types.ToolDetailSink = (*TerminalOutputSink)(nil)
