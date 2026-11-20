package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The alt-screen chat is the default: a document without the key (and a fresh
// config) must run the full-screen TUI, while an explicit "alt_screen": false
// selects the inline REPL and survives a reload.
func TestAltScreenIsDefaultAndOptOutPersists(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":13,"model":"m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	if !AltScreenEnabled() {
		t.Fatal("alt screen not enabled by default")
	}
	SetAltScreen(false)
	if err := SaveE(); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	if AltScreenEnabled() {
		t.Fatal("alt screen opt-out did not survive a reload")
	}
}
