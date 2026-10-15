//go:build windows

package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// TestZedPathWindows pins the Windows layout: Zed stores its user data under
// %LOCALAPPDATA%\Zed (https://zed.dev/docs/reference/cli), so the servers it
// downloads live in %LOCALAPPDATA%\Zed\languages\<binary>.
func TestZedPathWindows(t *testing.T) {
	local := t.TempDir()
	roaming := t.TempDir()
	t.Setenv("LOCALAPPDATA", local)
	t.Setenv("APPDATA", roaming)

	want := filepath.Join(local, "Zed", "languages", "gopls", "gopls_0.22.0.exe")
	if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(want, []byte("stub"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := zedPath("", "gopls", nil); got != want {
		t.Errorf("zedPath = %q; want %q", got, want)
	}
}
