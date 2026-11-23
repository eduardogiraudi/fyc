package ui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func pickerIn(t *testing.T, dir string) dirPickerModel {
	t.Helper()
	m := newDirPickerModel("pick", dir, nil)
	// Run the initial directory read, exactly like the program would.
	updated, _ := m.Update(m.Init()())
	return updated.(dirPickerModel)
}

func TestDirPickerSpaceSelectsTheOpenFolder(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	m := pickerIn(t, dir)

	final, cmd := m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if cmd == nil {
		t.Fatal("space must finish the picker")
	}
	got := final.(dirPickerModel)
	if !got.ok || got.value != m.picker.CurrentDirectory {
		t.Fatalf("space selected %q (ok=%v), want the open folder %q", got.value, got.ok, m.picker.CurrentDirectory)
	}
}

func TestDirPickerEscCancels(t *testing.T) {
	m := pickerIn(t, t.TempDir())
	final, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc must finish the picker")
	}
	got := final.(dirPickerModel)
	if got.ok || got.value != "" {
		t.Fatalf("esc returned a selection: %q (ok=%v)", got.value, got.ok)
	}
}

func TestDirPickerFallsBackToHomeForAnInvalidStart(t *testing.T) {
	m := newDirPickerModel("pick", filepath.Join(t.TempDir(), "missing"), nil)
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home directory here")
	}
	if filepath.Clean(m.picker.CurrentDirectory) != filepath.Clean(home) {
		t.Fatalf("start directory = %q, want the home directory", m.picker.CurrentDirectory)
	}
}
