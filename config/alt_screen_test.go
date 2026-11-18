package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The alt-screen chat is opt-in: an older document (and a fresh default) must
// keep the inline REPL, while an explicit "alt_screen": true survives a reload.
func TestAltScreenIsOptInAndPersists(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":13,"model":"m"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	if AltScreenEnabled() {
		t.Fatal("alt screen enabled by default")
	}
	SetAltScreen(true)
	if err := SaveE(); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatal(err)
	}
	if !AltScreenEnabled() {
		t.Fatal("alt screen did not survive a reload")
	}
}
