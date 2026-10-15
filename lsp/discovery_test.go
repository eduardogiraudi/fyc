package lsp

import (
	"os"
	"path/filepath"
	"testing"
)

// writeExecutable creates an executable file so isExec accepts it on Unix.
func writeExecutable(t *testing.T, path string) string {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// find returns the discovered entry with the given canonical name.
func find(infos []LSPInfo, name string) (LSPInfo, bool) {
	for _, i := range infos {
		if i.Name == name {
			return i, true
		}
	}
	return LSPInfo{}, false
}

func TestDiscoverBinariesOverride(t *testing.T) {
	dir := t.TempDir()
	gopls := writeExecutable(t, filepath.Join(dir, "my-gopls"))

	infos := Discover(Options{Binaries: map[string]string{"gopls": gopls}})
	got, ok := find(infos, "gopls")
	if !ok {
		t.Fatalf("gopls not discovered; got %+v", infos)
	}
	if got.Binary != gopls {
		t.Errorf("Binary = %q; want the pinned %q", got.Binary, gopls)
	}
	if got.Source != "config" {
		t.Errorf("Source = %q; want \"config\"", got.Source)
	}
}

// An explicit override must win even when the same server exists on PATH/editor
// caches. We cannot guarantee gopls is installed here, so the assertion is
// simply that the pin is honoured whenever gopls is reported at all.
func TestDiscoverBinariesOverrideBeatsSearchPath(t *testing.T) {
	dir := t.TempDir()
	pinned := writeExecutable(t, filepath.Join(dir, "pinned-gopls"))
	other := t.TempDir()
	writeExecutable(t, filepath.Join(other, "gopls"))

	infos := Discover(Options{
		SearchPaths: []string{other},
		Binaries:    map[string]string{"gopls": pinned},
	})
	got, ok := find(infos, "gopls")
	if !ok {
		t.Fatalf("gopls not discovered; got %+v", infos)
	}
	if got.Binary != pinned {
		t.Errorf("Binary = %q; want the pinned %q (Binaries must beat SearchPaths)", got.Binary, pinned)
	}
}

func TestDiscoverSearchPath(t *testing.T) {
	dir := t.TempDir()
	gopls := writeExecutable(t, filepath.Join(dir, "gopls"))

	infos := Discover(Options{SearchPaths: []string{dir}})
	got, ok := find(infos, "gopls")
	if !ok {
		t.Fatalf("gopls not discovered; got %+v", infos)
	}
	if got.Binary != gopls {
		t.Errorf("Binary = %q; want %q", got.Binary, gopls)
	}
	if got.Source != "config" {
		t.Errorf("Source = %q; want \"config\"", got.Source)
	}
}

func TestDiscoverDisabled(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "gopls"))

	infos := Discover(Options{
		SearchPaths: []string{dir},
		Disabled:    []string{"gopls"},
	})
	if _, ok := find(infos, "gopls"); ok {
		t.Error("gopls was discovered despite being disabled")
	}
}

// A non-existent override path must not crash nor shadow the other sources: it
// simply falls through.
func TestDiscoverBadOverrideFallsThrough(t *testing.T) {
	dir := t.TempDir()
	gopls := writeExecutable(t, filepath.Join(dir, "gopls"))

	infos := Discover(Options{
		SearchPaths: []string{dir},
		Binaries:    map[string]string{"gopls": filepath.Join(dir, "does-not-exist")},
	})
	got, ok := find(infos, "gopls")
	if !ok {
		t.Fatalf("gopls not discovered; got %+v", infos)
	}
	if got.Binary != gopls {
		t.Errorf("Binary = %q; want the search-path %q", got.Binary, gopls)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	cases := map[string]string{
		"~":            home,
		"~/.local/bin": filepath.Join(home, ".local", "bin"),
		"/abs/path":    "/abs/path",
		"relative":     "relative",
		"":             "",
	}
	for in, want := range cases {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestExpandHomePaths(t *testing.T) {
	got := expandHomePaths([]string{"  /a  ", "", "   ", "~"})
	if len(got) != 2 || got[0] != "/a" {
		t.Fatalf("expandHomePaths = %#v; want [/a, <home>]", got)
	}
}
