package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One root keeps the historical block byte for byte; several roots each get a
// block labelled with their own path, so the model can tell the projects apart.
func TestLoadWorkspaceContextLabelsEveryRoot(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(a, "AGENTS.md"), []byte("rule A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b, "AGENTS.md"), []byte("rule B\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	single, _ := LoadWorkspaceContext([]string{a})
	want, _ := LoadProjectContext(a)
	if single != want {
		t.Fatalf("a single root changed the historical block:\n%s\nwant:\n%s", single, want)
	}

	rendered, summary := LoadWorkspaceContext([]string{a, b})
	if strings.Count(rendered, "<project_context>") != 1 || strings.Count(rendered, "</project_context>") != 1 {
		t.Fatalf("multi-root context must stay inside one block:\n%s", rendered)
	}
	for _, want := range []string{"rule A", "rule B", "# " + a, "# " + b} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("multi-root context does not mention %q:\n%s", want, rendered)
		}
	}
	if !strings.Contains(summary, a) || !strings.Contains(summary, b) {
		t.Fatalf("summary = %q, want both roots", summary)
	}
}

// A root without instructions contributes nothing at all.
func TestLoadWorkspaceContextSkipsRootsWithoutInstructions(t *testing.T) {
	withDocs, empty := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(withDocs, "AGENTS.md"), []byte("only rule\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rendered, _ := LoadWorkspaceContext([]string{withDocs, empty})
	if !strings.Contains(rendered, "only rule") || strings.Contains(rendered, "# "+empty) {
		t.Fatalf("rendered = %q", rendered)
	}
}
