package main

import (
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/tools"
)

// TestGitToolsDeclaredAndDefaultDisabled checks the integration between the
// tool declarations (globals.go), the git registry (config/git.go) and the
// enablement defaults: only read-only git tools may be enabled by default.
func TestGitToolsDeclaredAndDefaultDisabled(t *testing.T) {
	declared := map[string]bool{}
	for _, tool := range availableTools {
		if declared[tool.Function.Name] {
			t.Fatalf("tool %q declared twice", tool.Function.Name)
		}
		declared[tool.Function.Name] = true
	}

	names := config.GitToolNames()
	if len(names) == 0 {
		t.Fatal("no git tool registered")
	}
	for _, name := range names {
		if !declared[name] {
			t.Errorf("git tool %q is not declared in availableTools", name)
		}
		if !tools.IsGitTool(name) {
			t.Errorf("tools.IsGitTool(%q) = false", name)
		}
		if got, want := defaultToolEnabled(name), config.GitToolEnabledByDefault(name); got != want {
			t.Errorf("defaultToolEnabled(%q) = %v, want %v", name, got, want)
		}
	}

	// Risky tools stay off until the user enables them.
	for _, name := range []string{"git_add", "git_commit", "git_switch", "git_branch", "git_fetch", "git_push", "git_clone", "git_reset", "git_merge", "git_tag"} {
		if defaultToolEnabled(name) {
			t.Errorf("risky git tool %q is enabled by default", name)
		}
	}
	// Read-only tools are on.
	for _, name := range []string{"git_status", "git_diff", "git_log", "git_show", "git_blame", "git_ls_files", "git_config"} {
		if !defaultToolEnabled(name) {
			t.Errorf("read-only git tool %q is disabled by default", name)
		}
	}
}

// TestGetToolsForAIHidesGitTools verifies the two visibility layers: the
// default-off tool states and the runtime auto-hide flag.
func TestGetToolsForAIHidesGitTools(t *testing.T) {
	session := &ChatSession{AvailableTools: availableTools}
	// Simulate a workspace inside a repository: in production this flag is set
	// by refreshGitToolAvailability at startup and by the heartbeat.
	session.gitInRepo.Store(true)

	seen := map[string]bool{}
	for _, tool := range session.getToolsForAI() {
		seen[tool.Function.Name] = true
	}
	if !seen["git_status"] || !seen["git_log"] {
		t.Error("read-only git tools should be offered by default")
	}
	for _, name := range []string{"git_add", "git_commit", "git_push", "git_reset", "git_clone"} {
		if seen[name] {
			t.Errorf("risky git tool %q offered without being enabled", name)
		}
	}

	// Auto-hidden (git unavailable): every git tool disappears, even the
	// read-only and the bootstrap ones.
	session.GitToolsAutoDisabled.Store(true)
	for _, tool := range session.getToolsForAI() {
		if tools.IsGitTool(tool.Function.Name) {
			t.Fatalf("git tool %q offered while auto-disabled", tool.Function.Name)
		}
	}
}

// TestGitToolHiddenOutsideRepo covers the bootstrap exception: outside a
// repository only git_init/git_clone remain visible.
func TestGitToolHiddenOutsideRepo(t *testing.T) {
	session := &ChatSession{}
	session.GitToolsAutoDisabled.Store(false)
	session.gitInRepo.Store(false)

	if !session.gitToolHidden("git_status") {
		t.Error("git_status must be hidden outside a repository")
	}
	if session.gitToolHidden("git_init") || session.gitToolHidden("git_clone") {
		t.Error("git_init/git_clone must stay available outside a repository")
	}

	session.gitInRepo.Store(true)
	if session.gitToolHidden("git_status") {
		t.Error("git_status must be visible inside a repository")
	}

	session.GitToolsAutoDisabled.Store(true)
	if !session.gitToolHidden("git_init") {
		t.Error("everything must be hidden while git is unavailable")
	}
	if session.gitToolsVisible() {
		t.Error("gitToolsVisible must be false while auto-disabled")
	}
}

// TestSystemPromptGitRuleOnlyActiveTools ensures the git rule block in the
// system prompt names only the git tools actually exposed to the model: the
// disabled ones (hidden from getToolsForAI) must not be advertised.
func TestSystemPromptGitRuleOnlyActiveTools(t *testing.T) {
	// Default state: only the read-only git tools are active.
	for _, name := range config.GitToolNames() {
		config.SetToolEnabled(name, config.GitToolEnabledByDefault(name))
	}
	session := &ChatSession{AvailableTools: availableTools}
	session.GitToolsAutoDisabled.Store(false)
	session.gitInRepo.Store(true)

	rule := gitRuleLine(buildSystemPrompt("Wednesday, September 16, 2026", "", session))
	if rule == "" {
		t.Fatal("git rule missing while read-only tools are active")
	}
	for _, name := range config.GitDisabledByDefaultNames() {
		if strings.Contains(rule, name) {
			t.Errorf("rule advertises disabled tool %q: %s", name, rule)
		}
	}
	for _, name := range []string{"git_status", "git_config"} {
		if !strings.Contains(rule, name) {
			t.Errorf("rule omits active tool %q: %s", name, rule)
		}
	}

	// Enabling a mutating tool must surface it under the right gate.
	config.SetToolEnabled("git_commit", true)
	rule = gitRuleLine(buildSystemPrompt("Wednesday, September 16, 2026", "", session))
	if !strings.Contains(rule, "git_commit") || !strings.Contains(rule, "local-write") {
		t.Errorf("rule must list git_commit as local-write: %s", rule)
	}

	// Outside a repository the rule disappears entirely.
	session.gitInRepo.Store(false)
	if r := gitRuleLine(buildSystemPrompt("Wednesday, September 16, 2026", "", session)); r != "" {
		t.Errorf("git rule must be absent outside a repository: %s", r)
	}
}

// gitRuleLine returns the git rule line from a system prompt, or "".
func gitRuleLine(prompt string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.Contains(line, "Git: always use the git_* tools") {
			return line
		}
	}
	return ""
}
