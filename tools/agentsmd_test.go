package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile writes content to dir/name, creating parent directories.
func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// markRepo drops an empty .git directory so the loader treats dir as the
// repository root (the VCS marker is what bounds the upward walk).
func markRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAgentsMDPrefersOverride(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "regular")
	writeFile(t, dir, "AGENTS.override.md", "override")
	docs := loadAgentsDocs(dir, []string{"CLAUDE.md", "GEMINI.md"}, 32768)
	if len(docs) != 1 || docs[0].Path != "AGENTS.override.md" || docs[0].Content != "override" {
		t.Fatalf("got %+v, want the override file only", docs)
	}
}

func TestAgentsMDBeatsFallback(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "agents")
	writeFile(t, dir, "CLAUDE.md", "claude")
	docs := loadAgentsDocs(dir, []string{"CLAUDE.md", "GEMINI.md"}, 32768)
	if len(docs) != 1 || docs[0].Path != "AGENTS.md" {
		t.Fatalf("got %+v, want AGENTS.md", docs)
	}
}

func TestAgentsMDFallbackOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "GEMINI.md", "gemini")
	writeFile(t, dir, "CLAUDE.md", "claude")
	docs := loadAgentsDocs(dir, []string{"CLAUDE.md", "GEMINI.md"}, 32768)
	if len(docs) != 1 || docs[0].Path != "CLAUDE.md" {
		t.Fatalf("got %+v, want CLAUDE.md (first fallback)", docs)
	}

	dir2 := t.TempDir()
	writeFile(t, dir2, "GEMINI.md", "gemini")
	docs2 := loadAgentsDocs(dir2, []string{"CLAUDE.md", "GEMINI.md"}, 32768)
	if len(docs2) != 1 || docs2[0].Path != "GEMINI.md" {
		t.Fatalf("got %+v, want GEMINI.md", docs2)
	}
}

func TestAgentsMDEmptyFileSkipped(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "AGENTS.md", "   \n")
	writeFile(t, dir, "CLAUDE.md", "claude")
	docs := loadAgentsDocs(dir, []string{"CLAUDE.md", "GEMINI.md"}, 32768)
	if len(docs) != 1 || docs[0].Path != "CLAUDE.md" {
		t.Fatalf("got %+v, want the empty AGENTS.md to be skipped", docs)
	}
}

func TestAgentsMDChainRootToCwd(t *testing.T) {
	repo := t.TempDir()
	markRepo(t, repo)
	sub := filepath.Join(repo, "pkg", "api")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "AGENTS.md", "root rules")
	writeFile(t, sub, "AGENTS.md", "api rules")

	docs := loadAgentsDocs(sub, []string{"CLAUDE.md"}, 32768)
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2: %+v", len(docs), docs)
	}
	if docs[0].Path != "AGENTS.md" || docs[0].Content != "root rules" {
		t.Fatalf("first doc = %+v, want the repo-root one", docs[0])
	}
	if docs[1].Path != filepath.ToSlash(filepath.Join("pkg", "api", "AGENTS.md")) {
		t.Fatalf("second doc path = %q", docs[1].Path)
	}
}

func TestAgentsMDNoVCSDoesNotEscapeRoot(t *testing.T) {
	parent := t.TempDir()
	// A stray AGENTS.md above the "workspace": without a VCS marker it must
	// never be injected.
	writeFile(t, parent, "AGENTS.md", "outside")
	root := filepath.Join(parent, "workspace")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	docs := loadAgentsDocs(root, []string{"CLAUDE.md"}, 32768)
	if len(docs) != 0 {
		t.Fatalf("got %+v, want nothing (no file inside the workspace)", docs)
	}
}

func TestAgentsMDBudgetTruncates(t *testing.T) {
	repo := t.TempDir()
	markRepo(t, repo)
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo, "AGENTS.md", "0123456789") // 10 bytes
	writeFile(t, sub, "AGENTS.md", "abcdefghij")

	docs := loadAgentsDocs(sub, nil, 12)
	if len(docs) != 2 {
		t.Fatalf("got %d docs, want 2", len(docs))
	}
	if docs[0].Content != "0123456789" || docs[0].Truncated {
		t.Fatalf("first doc = %+v, expected full 10 bytes", docs[0])
	}
	if docs[1].Content != "ab" || !docs[1].Truncated {
		t.Fatalf("second doc = %+v, expected truncation to 2 bytes", docs[1])
	}
}

func TestRenderAgentsDocsEmpty(t *testing.T) {
	if got := RenderAgentsDocs(nil); got != "" {
		t.Fatalf("RenderAgentsDocs(nil) = %q, want empty", got)
	}
}

func TestRenderAgentsDocsBlock(t *testing.T) {
	out := RenderAgentsDocs([]AgentsDoc{{Path: "AGENTS.md", Content: "do things\n"}})
	for _, want := range []string{"<project_context>", "# AGENTS.md", "do things", "</project_context>"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered block missing %q:\n%s", want, out)
		}
	}
}
