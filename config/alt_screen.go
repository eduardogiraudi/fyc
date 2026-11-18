package config

// AltScreenEnabled reports whether the chat UI runs in the terminal's alternate
// screen buffer (full-screen TUI with an in-app scrollable transcript) instead
// of the default inline REPL, whose output stays in the terminal scrollback.
// Default: false.
func AltScreenEnabled() bool {
	return config.AltScreen
}

// SetAltScreen enables or disables the alt-screen chat UI.
func SetAltScreen(enabled bool) {
	config.AltScreen = enabled
}
