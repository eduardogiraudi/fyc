package tools

import (
	"context"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// TestGitBootstrapToolsOutsideRepo documents which git tools stay available
// when the workspace is not (yet) a repository: only the bootstrap ones.
// Everything else depends on repository metadata and must stay hidden.
func TestGitBootstrapToolsOutsideRepo(t *testing.T) {
	for _, name := range config.GitToolNames() {
		want := name == "git_init" || name == "git_clone"
		if got := GitToolUsableOutsideRepo(name); got != want {
			t.Errorf("GitToolUsableOutsideRepo(%q) = %v, want %v", name, got, want)
		}
	}
	if GitToolUsableOutsideRepo("ls") || GitToolUsableOutsideRepo("sh") {
		t.Error("non-git tools must not be reported as git bootstrap tools")
	}
}

// TestGitCommandDetailsRedactsCredentials guards the "See details" viewer: an
// argv carrying a remote URL with userinfo (git clone/add/set-url) must never
// print the credential, neither on screen nor in the terminal scrollback.
func TestGitCommandDetailsRedactsCredentials(t *testing.T) {
	got := strings.Join(types.DetailLinesText(gitCommandDetails("/ws", []string{"clone", "--", "https://user:s3cr3t@github.com/o/r.git"})), "\n")
	if strings.Contains(got, "s3cr3t") {
		t.Fatalf("credentials leaked into the details: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected a redacted URL, got %q", got)
	}

	got = strings.Join(types.DetailLinesText(gitCommandDetails("/ws", []string{"remote", "add", "origin", "https://ghp_token123@host/x"})), "\n")
	if strings.Contains(got, "ghp_token123") {
		t.Fatalf("token leaked into the details: %q", got)
	}
}

// TestGitRemoteAddPromptRedactsURL guards the whole prompt path end to end: the
// title shown on screen and sent on the AG-UI wire, plus the "See details"
// payload, must never print a credential from a remote URL with userinfo.
func TestGitRemoteAddPromptRedactsURL(t *testing.T) {
	repo := newGitTestRepo(t) // skips when git is unavailable

	prevNoAsk := config.Get().NoAsk
	config.SetNoAsk(false)
	config.SetGitAllowWrite(false)
	t.Cleanup(func() {
		config.SetNoAsk(prevNoAsk)
		config.SetGitAllowWrite(false)
	})

	spy := &captureInteractor{}
	SetInteractor(spy)
	t.Cleanup(func() { SetInteractor(nil) })

	url := "https://user:s3cr3t@github.com/o/r.git"
	out := executeGitRemote(context.Background(), map[string]interface{}{
		"action": "add",
		"name":   "origin",
		"url":    url,
	}, repo)
	if !strings.Contains(out, "denied") {
		t.Fatalf("expected the prompt to deny the operation, got %q", out)
	}
	if spy.req.Title == "" {
		t.Fatal("no permission prompt was raised")
	}

	for _, s := range append([]string{spy.req.Title}, types.DetailLinesText(spy.req.Details)...) {
		if strings.Contains(s, "s3cr3t") {
			t.Fatalf("credential leaked into the prompt: %q", s)
		}
	}
	if joined := strings.Join(types.DetailLinesText(spy.req.Details), "\n"); !strings.Contains(joined, "[REDACTED]") {
		t.Fatalf("expected the redacted URL in the details, got:\n%s", joined)
	}
}
