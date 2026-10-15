package ui

import "fuckyourcli/types"

// Compile-time guarantee: the TUI sink carries the optional live-patch label.
var _ types.DiffCraftSink = (*TerminalOutputSink)(nil)
