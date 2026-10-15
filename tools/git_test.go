package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/types"
	"fuckyourcli/ui"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// gitTestEnv pins the commit identity through the environment so tests never
// depend on the machine's global git configuration.
func gitTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "fyc test")
	t.Setenv("GIT_AUTHOR_EMAIL", "fyc@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "fyc test")
	t.Setenv("GIT_COMMITTER_EMAIL", "fyc@example.invalid")
}

// newGitTestRepo creates a temporary repository with one commit and returns its
// (absolute) path used as the workspace root.
func newGitTestRepo(t *testing.T) string {
	t.Helper()
	gitTestEnv(t)
	if !GitAvailable() {
		t.Skip("git is not available")
	}
	dir := t.TempDir()
	if res := runGit([]string{"init", "-b", "main"}, gitOptions{Dir: dir}); !res.OK() {
		t.Skipf("cannot create a test repository: %s", res.Message())
	}
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write base.txt: %v", err)
	}
	if res := runGit([]string{"add", "--", "base.txt"}, gitOptions{Dir: dir}); !res.OK() {
		t.Fatalf("git add: %s", res.Message())
	}
	if res := runGit([]string{"commit", "--no-gpg-sign", "-m", "initial"}, gitOptions{Dir: dir}); !res.OK() {
		t.Fatalf("git commit: %s", res.Message())
	}
	InvalidateGitRepoCache()
	return dir
}

// allowGitTiers grants every git permission tier for the duration of the test.
func allowGitTiers(t *testing.T) {
	t.Helper()
	config.SetNoAsk(true)
	config.SetGitAllowWrite(true)
	config.SetGitAllowNetwork(true)
	config.SetGitAllowDestructive(true)
	config.SetGitAllowForcePush(true)
	t.Cleanup(func() {
		config.SetNoAsk(false)
		config.SetGitAllowWrite(false)
		config.SetGitAllowNetwork(false)
		config.SetGitAllowDestructive(false)
		config.SetGitAllowForcePush(false)
	})
}

func failIfError(t *testing.T, out string) {
	t.Helper()
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("unexpected error result: %s", out)
	}
}

// writeFakeGit materialises a fake git binary for the current platform: a POSIX
// sh script elsewhere, a cmd batch file on Windows (Go's os/exec runs a .bat by
// handing it to cmd.exe). unixBody/winBody are the platform-specific payloads.
func writeFakeGit(t *testing.T, name, unixBody, winBody string) string {
	t.Helper()
	dir := t.TempDir()
	if runtime.GOOS == "windows" {
		p := filepath.Join(dir, name+".bat")
		if err := os.WriteFile(p, []byte("@echo off\r\n"+winBody+"\r\n"), 0o755); err != nil {
			t.Fatalf("write fake git: %v", err)
		}
		return p
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+unixBody+"\n"), 0o755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	return p
}

// ── registry, tiers and sub-agent policy ─────────────────────────────────────

func TestGitToolRegistry(t *testing.T) {
	names := config.GitToolNames()
	if len(names) == 0 {
		t.Fatal("no git tools registered")
	}
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			t.Fatalf("duplicate git tool %q", n)
		}
		seen[n] = true
		if !strings.HasPrefix(n, "git_") {
			t.Fatalf("git tool %q does not start with git_", n)
		}
		if !IsGitTool(n) {
			t.Fatalf("IsGitTool(%q) = false", n)
		}
		tier, ok := config.GitToolTierFor(n)
		if !ok {
			t.Fatalf("no tier for %q", n)
		}
		// Only read-only tools are enabled by default: every risky tool must
		// be off until the user enables it.
		if got := config.GitToolEnabledByDefault(n); got != (tier == config.GitTierRead) {
			t.Fatalf("GitToolEnabledByDefault(%q) = %v for tier %s", n, got, tier)
		}
	}

	if IsGitTool("git") || IsGitTool("status") || IsGitTool("sh") {
		t.Fatal("IsGitTool matches non-git tools")
	}

	readOnly := config.GitToolGroupNames(config.GitTierRead)
	if len(readOnly) == 0 {
		t.Fatal("no read-only git tools")
	}
	for _, n := range config.GitDisabledByDefaultNames() {
		if config.GitToolEnabledByDefault(n) {
			t.Fatalf("risky tool %q starts enabled", n)
		}
		if tier, ok := config.GitToolTierFor(n); !ok || tier == config.GitTierRead {
			t.Fatalf("tool %q is disabled by default but declared %s", n, tier)
		}
	}
}

func TestGitActionTiers(t *testing.T) {
	cases := []struct {
		tool   string
		action string
		want   config.GitToolTier
	}{
		{"git_status", "", config.GitTierRead},
		{"git_config", "", config.GitTierRead},
		{"git_branch", "list", config.GitTierRead},
		{"git_branch", "delete", config.GitTierWrite},
		{"git_remote", "list", config.GitTierRead},
		{"git_remote", "remove", config.GitTierWrite},
		{"git_stash", "list", config.GitTierRead},
		{"git_stash", "push", config.GitTierWrite},
		{"git_tag", "list", config.GitTierRead},
		{"git_tag", "create", config.GitTierDestructive},
		{"git_push", "", config.GitTierNetwork},
		{"git_reset", "", config.GitTierDestructive},
		{"git_add", "", config.GitTierWrite},
		{"git_unknown", "", config.GitTierDestructive},
	}
	for _, c := range cases {
		if got := config.GitActionTier(c.tool, c.action); got != c.want {
			t.Errorf("GitActionTier(%q, %q) = %s, want %s", c.tool, c.action, got, c.want)
		}
	}
}

func TestSubagentGitToolPolicy(t *testing.T) {
	// mk builds a minimal tool declaration (the real schemas live in package
	// main; this test only cares about the sub-agent policy).
	mk := func(name string) types.Tool {
		return types.Tool{Type: "function", Function: types.ToolFunction{Name: name}}
	}
	all := []types.Tool{mk("git_status")}
	for _, n := range config.GitToolNames() {
		all = append(all, mk(n))
	}
	all = append(all, mk("ls"), mk("computer"))
	for _, n := range config.GitToolNames() {
		if !SubagentToolExcluded(n) {
			t.Errorf("git tool %q is exposed to sub-agents", n)
		}
		if SubagentToolBlockReason(n) == "" {
			t.Errorf("no block reason for git tool %q", n)
		}
	}
	if SubagentToolExcluded("ls") {
		t.Error("ls must stay available to sub-agents")
	}
	if !SubagentToolExcluded("computer") || !SubagentToolExcluded("spawn_subagents") {
		t.Error("computer/spawn_subagents must stay excluded from sub-agents")
	}
	if SubagentToolBlockReason("ls") != "" {
		t.Error("allowed tool must have no block reason")
	}

	filtered := FilterToolsForSubagent(all, nil)
	for _, tool := range filtered {
		if IsGitTool(tool.Function.Name) {
			t.Fatalf("FilterToolsForSubagent kept %q", tool.Function.Name)
		}
	}
	// Explicitly requested git tools are dropped too.
	filtered = FilterToolsForSubagent(all, []string{"ls", "git_commit"})
	if len(filtered) != 1 || filtered[0].Function.Name != "ls" {
		t.Fatalf("FilterToolsForSubagent(allowed) = %#v", filtered)
	}
}

// ── validation ───────────────────────────────────────────────────────────────

func TestGitPathValidation(t *testing.T) {
	ws := t.TempDir()

	if _, err := validateGitPath("../etc/passwd", ws); err == nil {
		t.Error("expected traversal rejection")
	}
	// An absolute path outside the workspace must be rejected. Build it from a
	// second temp dir so it is absolute on every platform: on Windows a bare
	// "/etc/passwd" is drive-relative (`C:\etc\passwd`) and not absolute at all.
	outside := filepath.Join(t.TempDir(), "x.go")
	if _, err := validateGitPath(outside, ws); err == nil {
		t.Error("expected absolute-path-outside-workspace rejection")
	}
	if _, err := validateGitPath(".git/config", ws); err == nil {
		t.Error("expected .git rejection")
	}
	if _, err := validateGitPath("a\x00b", ws); err == nil {
		t.Error("expected control character rejection")
	}

	got, err := validateGitPath("sub/dir/file.go", ws)
	if err != nil || got != "sub/dir/file.go" {
		t.Fatalf("validateGitPath(relative) = %q, %v", got, err)
	}
	got, err = validateGitPath(filepath.Join(ws, "sub", "x.go"), ws)
	if err != nil || got != "sub/x.go" {
		t.Fatalf("validateGitPath(absolute inside) = %q, %v", got, err)
	}
	if got, err := validateGitPath(".", ws); err != nil || got != "." {
		t.Fatalf("validateGitPath(.) = %q, %v", got, err)
	}
}

func TestGitRefValidation(t *testing.T) {
	ok := []string{"HEAD", "HEAD~2", "main", "origin/main", "v1.0.0", "main..HEAD", "HEAD@{1}", "abc123^"}
	for _, r := range ok {
		if _, err := validateGitRev(r); err != nil {
			t.Errorf("validateGitRev(%q) rejected: %v", r, err)
		}
	}
	bad := []string{"", "-x", "/etc", "HEAD; rm -rf /", "a\nb", "a..b..c", ".."}
	for _, r := range bad {
		if _, err := validateGitRev(r); err == nil {
			t.Errorf("validateGitRev(%q) accepted", r)
		}
	}

	if _, err := validateGitBranchName("-b"); err == nil {
		t.Error("branch name -b accepted")
	}
	if _, err := validateGitBranchName("a..b"); err == nil {
		t.Error("branch name a..b accepted")
	}
	if _, err := validateGitBranchName("bad\nname"); err == nil {
		t.Error("branch name with newline accepted")
	}
	if got, err := validateGitBranchName("feature/new-thing"); err != nil || got != "feature/new-thing" {
		t.Fatalf("validateGitBranchName(feature/new-thing) = %q, %v", got, err)
	}

	if _, err := validateGitStashRef("stash@{0}"); err != nil {
		t.Errorf("validateGitStashRef(stash@{0}) rejected: %v", err)
	}
	for _, bad := range []string{"stash@{x}", "stash@{}", "HEAD", "stash@{0; rm}"} {
		if _, err := validateGitStashRef(bad); err == nil {
			t.Errorf("validateGitStashRef(%q) accepted", bad)
		}
	}
}

// ── argument building ────────────────────────────────────────────────────────

func TestGitArgBuilders(t *testing.T) {
	ws := t.TempDir()

	argv, err := gitStatusArgs(map[string]interface{}{}, ws)
	if err != nil {
		t.Fatalf("gitStatusArgs: %v", err)
	}
	if strings.Join(argv, " ") != "status --porcelain=v2 --branch" {
		t.Fatalf("gitStatusArgs = %v", argv)
	}

	// A path that looks like a flag is still passed after --.
	argv, err = gitStatusArgs(map[string]interface{}{"path": "-weird"}, ws)
	if err != nil {
		t.Fatalf("gitStatusArgs(path): %v", err)
	}
	if argv[len(argv)-2] != "--" || argv[len(argv)-1] != "-weird" {
		t.Fatalf("pathspec not after --: %v", argv)
	}

	argv, err = gitDiffArgs(map[string]interface{}{"staged": true, "stat_only": true, "context_lines": float64(5)}, ws)
	if err != nil {
		t.Fatalf("gitDiffArgs: %v", err)
	}
	if strings.Join(argv, " ") != "diff --staged --stat -U5" {
		t.Fatalf("gitDiffArgs = %v", argv)
	}

	if _, err := gitDiffArgs(map[string]interface{}{"staged": true, "rev": "HEAD"}, ws); err == nil {
		t.Error("staged+rev must be rejected")
	}

	argv, err = gitLsFilesArgs(map[string]interface{}{"others": true}, ws)
	if err != nil {
		t.Fatalf("gitLsFilesArgs: %v", err)
	}
	if strings.Join(argv, " ") != "ls-files --others --exclude-standard" {
		t.Fatalf("gitLsFilesArgs = %v", argv)
	}
	// Default: cached only.
	argv, _ = gitLsFilesArgs(map[string]interface{}{}, ws)
	if strings.Join(argv, " ") != "ls-files --cached" {
		t.Fatalf("gitLsFilesArgs default = %v", argv)
	}

	argv, err = gitLogArgs(map[string]interface{}{"count": float64(5)}, ws)
	if err != nil {
		t.Fatalf("gitLogArgs: %v", err)
	}
	if argv[0] != "log" || argv[1] != "-n5" || !strings.Contains(strings.Join(argv, " "), "--oneline") {
		t.Fatalf("gitLogArgs = %v", argv)
	}

	argv, _, err = gitConfigArgs(map[string]interface{}{"key": "user.email"})
	if err != nil {
		t.Fatalf("gitConfigArgs: %v", err)
	}
	if strings.Join(argv, " ") != "config --local --get user.email" {
		t.Fatalf("gitConfigArgs = %v", argv)
	}
	if _, _, err := gitConfigArgs(map[string]interface{}{"scope": "nonsense"}); err == nil {
		t.Error("invalid scope accepted")
	}
	if _, _, err := gitConfigArgs(map[string]interface{}{"key": "--list"}); err == nil {
		t.Error("flag-like config key accepted")
	}
}

func TestParseUnifiedDiffLines(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/f.go b/f.go",
		"index 111..222 100644",
		"--- a/f.go",
		"+++ b/f.go",
		"@@ -1,3 +1,4 @@",
		" package main",
		"-old",
		"+new",
		"+added",
		"",
	}, "\n")

	lines := parseUnifiedDiffLines(diff)
	var added, removed, hunk int
	for _, l := range lines {
		switch l.Kind {
		case ui.DiffAdded:
			added++
		case ui.DiffRemoved:
			removed++
		case ui.DiffHunkHeader:
			hunk++
		}
	}
	if added != 2 || removed != 1 || hunk != 1 {
		t.Fatalf("parseUnifiedDiffLines: added=%d removed=%d hunk=%d", added, removed, hunk)
	}
	if a, r := countDiffLines(lines); a != 2 || r != 1 {
		t.Fatalf("countDiffLines = %d/%d", a, r)
	}
	if parseUnifiedDiffLines("") != nil {
		t.Error("empty diff must yield nil")
	}
}

// ── output hygiene ───────────────────────────────────────────────────────────

func TestGitOutputRedaction(t *testing.T) {
	in := "Clone https://user:s3cr3t@github.com/x/y.git\npassword=abc123\ntoken = qpwoe\nAuthorization: Basic dXNlcjpwYXNz"
	out := redactGitOutput(in)
	for _, secret := range []string{"s3cr3t", "abc123", "qpwoe", "dXNlcjpwYXNz"} {
		if strings.Contains(out, secret) {
			t.Errorf("secret %q leaked in %q", secret, out)
		}
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("no redaction marker in %q", out)
	}
	// The command line is redacted too.
	res := gitResult{Args: []string{"clone", "https://u:p@host/x.git"}}
	if strings.Contains(res.CommandLine(), "u:p@") {
		t.Errorf("command line leaked credentials: %s", res.CommandLine())
	}
}

func TestGitTruncationAndTimeoutWithFakeBinary(t *testing.T) {
	// Fake git that prints far more than the cap.
	big := writeFakeGit(t, "fake-git",
		`awk 'BEGIN{for(i=0;i<60000;i++)print "aaaaaaaaaaaaaaaaaaaa"}'`,
		`for /L %%i in (1,1,60000) do @echo aaaaaaaaaaaaaaaaaaaa`,
	)
	t.Setenv(gitPathEnv, big)

	res := runGit([]string{"status"}, gitOptions{Timeout: 20 * time.Second})
	if !res.Truncated {
		t.Fatalf("expected truncation, got %d bytes", len(res.Stdout))
	}
	if !strings.Contains(res.Output(), "truncated") {
		t.Error("truncation note missing from output")
	}

	// Fake git that hangs: the timeout must kick in.
	slow := writeFakeGit(t, "fake-git-slow", "sleep 5", "ping -n 6 127.0.0.1 >nul")
	t.Setenv(gitPathEnv, slow)
	res = runGit([]string{"status"}, gitOptions{Timeout: 150 * time.Millisecond})
	if !res.TimedOut {
		t.Fatalf("expected timeout, got %+v", res)
	}
	if out := gitResultToTool(res, 150*time.Millisecond).Text; !strings.Contains(out, "timed out") {
		t.Errorf("timeout message missing: %s", out)
	}

	// Fake git that fails: exit status and stderr must be reported.
	fail := writeFakeGit(t, "fake-git-fail", "echo boom >&2\nexit 3", "echo boom 1>&2\r\nexit /b 3")
	t.Setenv(gitPathEnv, fail)
	res = runGit([]string{"status"}, gitOptions{Timeout: 10 * time.Second})
	if res.OK() || res.Exit != 3 {
		t.Fatalf("expected exit 3, got %+v", res)
	}
	out := gitResultToTool(res, time.Second).Text
	if !strings.Contains(out, "exit 3") || !strings.Contains(out, "boom") {
		t.Errorf("failure message incomplete: %s", out)
	}

	// Missing binary.
	t.Setenv(gitPathEnv, filepath.Join(t.TempDir(), "does-not-exist"))
	if GitAvailable() {
		t.Error("GitAvailable must be false for a missing binary")
	}
	res = runGit([]string{"status"}, gitOptions{Timeout: time.Second})
	if res.OK() || res.Err == nil {
		t.Fatalf("expected exec error, got %+v", res)
	}
	if out := gitResultToTool(res, time.Second).Text; !strings.Contains(out, "cannot execute git") {
		t.Errorf("exec error message missing: %s", out)
	}
}

// ── repository detection ─────────────────────────────────────────────────────

func TestGitRepoDetection(t *testing.T) {
	repo := newGitTestRepo(t)
	root, ok := RefreshGitRepoRoot(repo)
	if !ok {
		t.Fatal("repository not detected")
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err == nil && filepath.Base(root) != filepath.Base(resolved) {
		t.Fatalf("unexpected repo root %q for %q", root, resolved)
	}

	// A subdirectory resolves to the same repository root.
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if root2, ok := RefreshGitRepoRoot(sub); !ok || root2 != root {
		t.Fatalf("subdirectory detection = %q, %v (want %q)", root2, ok, root)
	}

	// A directory outside any repository is not detected.
	plain := t.TempDir()
	if _, ok := detectGitRepoRoot(plain); ok {
		t.Skip("temp dir happens to be inside a repository")
	}
	InvalidateGitRepoCache()
	if _, ok := RefreshGitRepoRoot(plain); ok {
		t.Fatal("plain directory detected as a repository")
	}
	if out := executeGitStatus(map[string]interface{}{}, plain); !strings.Contains(out, "not inside a git repository") {
		t.Fatalf("expected repository error, got: %s", out)
	}
}

// ── read tools end to end ────────────────────────────────────────────────────

func TestGitReadTools(t *testing.T) {
	repo := newGitTestRepo(t)
	allowGitTiers(t)

	if err := os.WriteFile(filepath.Join(repo, "modified.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := executeGitStatus(map[string]interface{}{}, repo)
	if !strings.Contains(out, "modified.txt") {
		t.Errorf("git_status output missing the untracked file:\n%s", out)
	}

	out = executeGitLog(map[string]interface{}{"count": float64(1)}, repo)
	if !strings.Contains(out, "initial") {
		t.Errorf("git_log output missing the commit:\n%s", out)
	}

	out = executeGitLsFiles(map[string]interface{}{"others": true}, repo)
	if !strings.Contains(out, "modified.txt") {
		t.Errorf("git_ls_files --others missing the file:\n%s", out)
	}
	out = executeGitLsFiles(map[string]interface{}{"cached": true}, repo)
	if !strings.Contains(out, "base.txt") {
		t.Errorf("git_ls_files --cached missing base.txt:\n%s", out)
	}

	out = executeGitShow(map[string]interface{}{"stat_only": true}, repo)
	if !strings.Contains(out, "base.txt") {
		t.Errorf("git_show --stat missing base.txt:\n%s", out)
	}

	out = executeGitBlame(map[string]interface{}{"path": "base.txt", "max_lines": float64(5)}, repo)
	if !strings.Contains(out, "hello") && !strings.Contains(out, "fyc test") {
		t.Errorf("git_blame output unexpected:\n%s", out)
	}

	out = executeGitConfig(map[string]interface{}{"key": "definitely.not.set"}, repo)
	if !strings.Contains(out, "is not set") {
		t.Errorf("git_config missing key: %s", out)
	}

	// Paths outside the workspace are rejected before git is even called.
	out = executeGitStatus(map[string]interface{}{"path": "../../etc/passwd"}, repo)
	if !strings.Contains(out, "error:") {
		t.Errorf("expected breakout rejection, got: %s", out)
	}
	out = executeGitBlame(map[string]interface{}{}, repo)
	if !strings.Contains(out, "error:") {
		t.Errorf("git_blame without a path must fail: %s", out)
	}
}

// ── write tools and permissions ──────────────────────────────────────────────

func TestGitWriteTools(t *testing.T) {
	repo := newGitTestRepo(t)
	allowGitTiers(t)

	newFile := filepath.Join(repo, "new.txt")
	if err := os.WriteFile(newFile, []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failIfError(t, executeGitAdd(context.Background(), map[string]interface{}{"paths": []interface{}{"new.txt"}}, repo))

	out := executeGitDiff(map[string]interface{}{"staged": true, "name_only": true}, repo)
	if !strings.Contains(out, "new.txt") {
		t.Fatalf("staged file missing from diff: %s", out)
	}

	failIfError(t, executeGitCommit(context.Background(), map[string]interface{}{"message": "add new.txt"}, repo))
	out = executeGitLog(map[string]interface{}{"count": float64(1)}, repo)
	if !strings.Contains(out, "add new.txt") {
		t.Fatalf("commit not found in log: %s", out)
	}

	// Branch listing is read-only; creating needs the write tier (granted here).
	failIfError(t, executeGitBranch(context.Background(), map[string]interface{}{"action": "create", "name": "feature/x"}, repo))
	out = executeGitBranch(context.Background(), map[string]interface{}{"action": "list"}, repo)
	if !strings.Contains(out, "feature/x") {
		t.Fatalf("created branch missing from list: %s", out)
	}

	failIfError(t, executeGitRemote(context.Background(), map[string]interface{}{"action": "add", "name": "upstream", "url": "https://example.invalid/x.git"}, repo))
	out = executeGitRemote(context.Background(), map[string]interface{}{"action": "list"}, repo)
	if !strings.Contains(out, "upstream") {
		t.Fatalf("added remote missing from list: %s", out)
	}

	// Stash push + list.
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("stashed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	failIfError(t, executeGitStash(context.Background(), map[string]interface{}{"action": "push"}, repo))
	out = executeGitStash(context.Background(), map[string]interface{}{"action": "list"}, repo)
	if !strings.Contains(out, "stash@") {
		t.Fatalf("stash list empty: %s", out)
	}
	failIfError(t, executeGitStash(context.Background(), map[string]interface{}{"action": "pop"}, repo))

	// Tag lifecycle.
	failIfError(t, executeGitTag(context.Background(), map[string]interface{}{"action": "create", "name": "v1.0.0"}, repo))
	out = executeGitTag(context.Background(), map[string]interface{}{"action": "list"}, repo)
	if !strings.Contains(out, "v1.0.0") {
		t.Fatalf("tag missing: %s", out)
	}

	// Soft reset back to HEAD (no data loss) then verify the hooks.
	failIfError(t, executeGitReset(context.Background(), map[string]interface{}{"mode": "soft"}, repo))

	// Missing arguments are reported, never passed to git.
	if out := executeGitCommit(context.Background(), map[string]interface{}{}, repo); !strings.Contains(out, "message") {
		t.Errorf("commit without message must fail: %s", out)
	}
	if out := executeGitSwitch(context.Background(), map[string]interface{}{}, repo); !strings.Contains(out, "error:") {
		t.Errorf("switch without branch must fail: %s", out)
	}
	if out := executeGitAdd(context.Background(), map[string]interface{}{}, repo); !strings.Contains(out, "error:") {
		t.Errorf("add without paths must fail: %s", out)
	}
	if out := executeGitStash(context.Background(), map[string]interface{}{"action": "nope"}, repo); !strings.Contains(out, "unknown stash action") {
		t.Errorf("unknown stash action not reported: %s", out)
	}
	if out := executeGitBranch(context.Background(), map[string]interface{}{"action": "nope"}, repo); !strings.Contains(out, "unknown branch action") {
		t.Errorf("unknown branch action not reported: %s", out)
	}
}

func TestGitInitTool(t *testing.T) {
	gitTestEnv(t)
	if !GitAvailable() {
		t.Skip("git is not available")
	}
	allowGitTiers(t)
	dir := t.TempDir()

	if out := executeGitStatus(map[string]interface{}{}, dir); !strings.Contains(out, "not inside a git repository") {
		t.Fatalf("expected no-repo error: %s", out)
	}
	failIfError(t, executeGitInit(context.Background(), map[string]interface{}{"default_branch": "main"}, dir))
	if out := executeGitStatus(map[string]interface{}{}, dir); strings.HasPrefix(out, "error:") {
		t.Fatalf("status failed after init: %s", out)
	}
}

// ── force push / network gating ──────────────────────────────────────────────

func TestGitForcePushRequiresExplicitConfig(t *testing.T) {
	repo := newGitTestRepo(t)
	// Auto mode would normally skip prompts: force push must still be refused.
	config.SetNoAsk(true)
	config.SetGitAllowWrite(true)
	config.SetGitAllowNetwork(true)
	config.SetGitAllowForcePush(false)
	t.Cleanup(func() {
		config.SetNoAsk(false)
		config.SetGitAllowWrite(false)
		config.SetGitAllowNetwork(false)
	})

	out := executeGitPush(context.Background(), map[string]interface{}{"force_with_lease": true, "remote": "origin"}, repo)
	if !strings.Contains(out, "allow_force_push") {
		t.Fatalf("force push was not blocked: %s", out)
	}
}

func TestGitCloneDestinationValidation(t *testing.T) {
	gitTestEnv(t)
	if !GitAvailable() {
		t.Skip("git is not available")
	}
	allowGitTiers(t)
	ws := t.TempDir()

	if out := executeGitClone(context.Background(), map[string]interface{}{}, ws); !strings.Contains(out, "error:") {
		t.Errorf("clone without url must fail: %s", out)
	}
	if out := executeGitClone(context.Background(), map[string]interface{}{"url": "https://example.invalid/x.git", "destination": "../../escape"}, ws); !strings.Contains(out, "error:") {
		t.Errorf("clone outside the workspace must fail: %s", out)
	}
	// Non-empty destination is refused before any network access.
	if err := os.MkdirAll(filepath.Join(ws, "full"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ws, "full", "x"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := executeGitClone(context.Background(), map[string]interface{}{"url": "https://example.invalid/x.git", "destination": "full"}, ws); !strings.Contains(out, "not empty") {
		t.Errorf("non-empty destination not refused: %s", out)
	}
}

// ── permission tier helper ───────────────────────────────────────────────────

func TestGitPermissionTiers(t *testing.T) {
	config.SetNoAsk(false)
	config.SetGitAllowWrite(false)
	config.SetGitAllowNetwork(false)
	config.SetGitAllowDestructive(false)

	// Read-only never prompts.
	if msg := ensureGitTier(context.Background(), config.GitTierRead, "status"); msg != "" {
		t.Errorf("read-only tier prompted: %s", msg)
	}
	// An already-granted tier does not prompt either.
	config.SetGitAllowWrite(true)
	if msg := ensureGitTier(context.Background(), config.GitTierWrite, "add"); msg != "" {
		t.Errorf("granted tier prompted: %s", msg)
	}
	config.SetGitAllowWrite(false)

	// Auto mode skips the interactive prompt (tools are opt-in anyway).
	config.SetNoAsk(true)
	if msg := ensureGitTier(context.Background(), config.GitTierDestructive, "reset --hard"); msg != "" {
		t.Errorf("auto mode prompted: %s", msg)
	}
	config.SetNoAsk(false)

	if !config.GetGit().Allows(config.GitTierRead) {
		t.Error("read-only tier must always be allowed")
	}
}
