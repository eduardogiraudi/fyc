//go:build windows

package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// A pin written without the launcher extension must still resolve: resolveExec
// probes .cmd/.exe/.bat, exactly like execIn does for the SearchPaths.
func TestDiscoverBinariesOverrideWindowsExtension(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "gopls.exe")
	if err := os.WriteFile(exe, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	infos := Discover(Options{Binaries: map[string]string{"gopls": filepath.Join(dir, "gopls")}})
	got, ok := find(infos, "gopls")
	if !ok {
		t.Fatalf("gopls not discovered; got %+v", infos)
	}
	if got.Binary != exe {
		t.Errorf("Binary = %q; want %q", got.Binary, exe)
	}
	if got.Source != "config" {
		t.Errorf("Source = %q; want \"config\"", got.Source)
	}
}
