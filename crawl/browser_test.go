package crawl

import (
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/config"
)

// TestDetectBrowserPrefersConfiguredPaths pins the discovery priority: a
// browser_paths entry wins over the built-in candidates.
func TestDetectBrowserPrefersConfiguredPaths(t *testing.T) {
	// Isolate from a host/CI that exports FYC_BROWSER_PATH: the env override
	// wins over browser_paths, so a stray value would shadow the configured one.
	t.Setenv("FYC_BROWSER_PATH", "")
	dir := t.TempDir()
	fake := filepath.Join(dir, "my-browser")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { config.SetBrowserPaths(nil) })
	config.SetBrowserPaths([]string{fake})

	got, err := DetectBrowser()
	if err != nil {
		t.Fatalf("DetectBrowser: %v", err)
	}
	if got != fake {
		t.Fatalf("DetectBrowser = %q, want configured %q", got, fake)
	}
	if !BrowserAvailable() {
		t.Error("BrowserAvailable must be true when a configured path exists")
	}
}

// TestDetectBrowserEnvOverrideWins pins FYC_BROWSER_PATH above browser_paths.
func TestDetectBrowserEnvOverrideWins(t *testing.T) {
	dir := t.TempDir()
	configured := filepath.Join(dir, "configured")
	override := filepath.Join(dir, "override")
	for _, p := range []string{configured, override} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { config.SetBrowserPaths(nil) })
	config.SetBrowserPaths([]string{configured})
	t.Setenv("FYC_BROWSER_PATH", override)

	got, err := DetectBrowser()
	if err != nil {
		t.Fatalf("DetectBrowser: %v", err)
	}
	if got != override {
		t.Fatalf("DetectBrowser = %q, want env override %q", got, override)
	}
}
