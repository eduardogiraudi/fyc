//go:build darwin

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox-exec matches the canonical path of a vnode, so the profile must carry
// resolved paths: $TMPDIR is /var/folders/… → /private/var/…. With the symlinked
// spelling the workspace rule never fired and a sandboxed command could not write
// the very directory it was allowed to write.
func TestSBProfileUsesResolvedPaths(t *testing.T) {
	real := t.TempDir()
	resolvedReal, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	link := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	profile := buildSBProfile(sandboxConfig{RestrictedRoot: link, AllowedRW: []string{link}})

	if strings.Contains(profile, link+"\"") {
		t.Fatalf("the profile kept the symlinked spelling %s:\n%s", link, profile)
	}
	if !strings.Contains(profile, resolvedReal) {
		t.Fatalf("the profile does not allow the resolved workspace %s:\n%s", resolvedReal, profile)
	}
	if got := sbPath(filepath.Join(link, "not-created-yet", "x")); !strings.HasPrefix(got, resolvedReal) {
		t.Fatalf("sbPath of a missing path = %q, want it under %q", got, resolvedReal)
	}
}
