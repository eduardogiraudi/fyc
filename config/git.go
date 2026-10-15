package config

import "strings"

// ── Git tool registry ────────────────────────────────────────────────────────
//
// Git tools are grouped into four permission tiers:
//
//	read-only    → inspection, no prompt
//	local write  → mutates the local repository
//	network      → talks to a remote (fetch/pull/push/clone)
//	destructive  → can discard work or rewrite history
//
// Only the read-only tools are enabled by default. Every tool that can mutate
// the repository starts disabled and must be enabled explicitly from the
// Enable/Disable Tools menu (/tools) or from config.json, because a tool that
// only exists while enabled cannot be reached through /auto. Sub-agents never
// receive git tools at all (see tools/subagents_policy.go).
//
// This file is the single source of truth for the git tool names: the tool
// declarations in globals.go, the /tools menu, the default disabled set and
// the permission checks all read from here.

// GitToolTier classifies how risky a git operation is.
type GitToolTier int

const (
	// GitTierRead is read-only inspection: no permission prompt.
	GitTierRead GitToolTier = iota
	// GitTierWrite mutates the local repository.
	GitTierWrite
	// GitTierNetwork talks to a remote.
	GitTierNetwork
	// GitTierDestructive can discard work or rewrite history.
	GitTierDestructive
)

// String returns a human-readable tier name (used in prompts and messages).
func (t GitToolTier) String() string {
	switch t {
	case GitTierRead:
		return "read-only"
	case GitTierWrite:
		return "local write"
	case GitTierNetwork:
		return "network"
	case GitTierDestructive:
		return "destructive"
	}
	return "unknown"
}

var (
	gitReadToolNames = []string{
		"git_status", "git_diff", "git_log", "git_show", "git_blame",
		"git_ls_files", "git_config",
	}
	gitWriteToolNames = []string{
		"git_add", "git_commit", "git_switch", "git_restore",
		"git_stash", "git_init", "git_branch", "git_remote",
	}
	gitNetworkToolNames = []string{
		"git_fetch", "git_pull", "git_push", "git_clone",
	}
	gitDestructiveToolNames = []string{
		"git_reset", "git_merge", "git_rebase", "git_cherry_pick",
		"git_revert", "git_tag",
	}

	// gitToolTiers maps every git tool to its default tier.
	gitToolTiers = buildGitToolTiers()
)

func buildGitToolTiers() map[string]GitToolTier {
	m := make(map[string]GitToolTier, 32)
	for _, n := range gitReadToolNames {
		m[n] = GitTierRead
	}
	for _, n := range gitWriteToolNames {
		m[n] = GitTierWrite
	}
	for _, n := range gitNetworkToolNames {
		m[n] = GitTierNetwork
	}
	for _, n := range gitDestructiveToolNames {
		m[n] = GitTierDestructive
	}
	return m
}

// GitToolNames returns every git tool name (read-only first, then write,
// network and destructive). The order is stable and used by the /tools menu.
func GitToolNames() []string {
	out := make([]string, 0, len(gitToolTiers))
	out = append(out, gitReadToolNames...)
	out = append(out, gitWriteToolNames...)
	out = append(out, gitNetworkToolNames...)
	out = append(out, gitDestructiveToolNames...)
	return out
}

// GitToolGroupNames returns the tool names of one tier.
func GitToolGroupNames(tier GitToolTier) []string {
	switch tier {
	case GitTierRead:
		return append([]string(nil), gitReadToolNames...)
	case GitTierWrite:
		return append([]string(nil), gitWriteToolNames...)
	case GitTierNetwork:
		return append([]string(nil), gitNetworkToolNames...)
	case GitTierDestructive:
		return append([]string(nil), gitDestructiveToolNames...)
	}
	return nil
}

// IsGitTool reports whether name is one of the git_* tools.
func IsGitTool(name string) bool {
	_, ok := gitToolTiers[name]
	return ok
}

// GitToolTierFor returns the default tier of a git tool.
func GitToolTierFor(name string) (GitToolTier, bool) {
	t, ok := gitToolTiers[name]
	return t, ok
}

// GitToolEnabledByDefault reports whether a git tool is exposed when no saved
// tool state exists. Only read-only tools are.
func GitToolEnabledByDefault(name string) bool {
	t, ok := gitToolTiers[name]
	return ok && t == GitTierRead
}

// GitDisabledByDefaultNames returns the git tools that start disabled.
func GitDisabledByDefaultNames() []string {
	var out []string
	for _, n := range GitToolNames() {
		if !GitToolEnabledByDefault(n) {
			out = append(out, n)
		}
	}
	return out
}

// GitActionTier returns the tier of a single action of a git tool. Tools that
// mix a read-only default action with mutating ones (git_branch, git_remote,
// git_stash, git_tag) are read-only for their `list` action and inherit the
// tier of the tool for everything else.
func GitActionTier(name, action string) GitToolTier {
	action = strings.ToLower(strings.TrimSpace(action))
	switch name {
	case "git_branch", "git_remote", "git_stash":
		if action == "" || action == "list" {
			return GitTierRead
		}
		return GitTierWrite
	case "git_tag":
		if action == "" || action == "list" {
			return GitTierRead
		}
		return GitTierDestructive
	}
	if t, ok := gitToolTiers[name]; ok {
		return t
	}
	// Unknown tool: be strict.
	return GitTierDestructive
}

// ── Permission model ─────────────────────────────────────────────────────────

// GitConfig persists the git permission model. Everything defaults to false:
// the user must explicitly grant a tier before a mutating git tool can run.
type GitConfig struct {
	// AllowWrite grants local repository mutations.
	AllowWrite bool `json:"allow_write"`
	// AllowNetwork grants fetch/pull/push/clone.
	AllowNetwork bool `json:"allow_network"`
	// AllowDestructive grants reset/merge/rebase/cherry-pick/revert/tag.
	AllowDestructive bool `json:"allow_destructive"`
	// AllowForcePush grants --force-with-lease on git_push. It is required even
	// in auto mode (/auto, fuckyourcli auto) and defaults to false.
	AllowForcePush bool `json:"allow_force_push"`
	// DefaultRemote is used when a network tool has no explicit remote.
	DefaultRemote string `json:"default_remote,omitempty"`
}

// EffectiveDefaultRemote returns the configured default remote, falling back
// to "origin".
func (g GitConfig) EffectiveDefaultRemote() string {
	if r := strings.TrimSpace(g.DefaultRemote); r != "" {
		return r
	}
	return "origin"
}

// Allows reports whether a tier is granted by the configuration.
func (g GitConfig) Allows(tier GitToolTier) bool {
	switch tier {
	case GitTierRead:
		return true
	case GitTierWrite:
		return g.AllowWrite
	case GitTierNetwork:
		return g.AllowNetwork
	case GitTierDestructive:
		return g.AllowDestructive
	}
	return false
}

// GetGit returns the git configuration.
func GetGit() GitConfig {
	return config.Git
}

// SetGitAllowWrite grants/revokes local repository mutations.
func SetGitAllowWrite(allow bool) {
	config.Git.AllowWrite = allow
}

// SetGitAllowNetwork grants/revokes network git operations.
func SetGitAllowNetwork(allow bool) {
	config.Git.AllowNetwork = allow
}

// SetGitAllowDestructive grants/revokes destructive git operations.
func SetGitAllowDestructive(allow bool) {
	config.Git.AllowDestructive = allow
}

// SetGitAllowForcePush grants/revokes --force-with-lease on git_push.
func SetGitAllowForcePush(allow bool) {
	config.Git.AllowForcePush = allow
}

// SetGitDefaultRemote sets the default remote used by network tools.
func SetGitDefaultRemote(remote string) {
	config.Git.DefaultRemote = strings.TrimSpace(remote)
}

// SetGitTierAllowed persists the grant for a tier (used by "Allow always").
func SetGitTierAllowed(tier GitToolTier, allow bool) {
	switch tier {
	case GitTierWrite:
		SetGitAllowWrite(allow)
	case GitTierNetwork:
		SetGitAllowNetwork(allow)
	case GitTierDestructive:
		SetGitAllowDestructive(allow)
	}
}
