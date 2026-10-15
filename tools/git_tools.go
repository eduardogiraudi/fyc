package tools

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

// ── git tool helpers ─────────────────────────────────────────────────────────
//
// Git tool names, tiers and the default-disabled set live in config/git.go
// (single source of truth). This file maps a tool + action pair to its tier
// and implements the permission prompts of the three-level model.

// IsGitTool reports whether name is one of the git_* tools.
func IsGitTool(name string) bool { return config.IsGitTool(name) }

// GitToolUsableOutsideRepo reports whether a git tool can run without an
// existing repository: only the bootstrap tools (git_init, git_clone) can.
// Everything else stays hidden until the workspace is inside a repository.
func GitToolUsableOutsideRepo(name string) bool {
	return name == "git_init" || name == "git_clone"
}

// gitActionTier returns the permission tier of a tool action.
func gitActionTier(name, action string) config.GitToolTier {
	return config.GitActionTier(name, action)
}

// gitSetupError verifies that git can run at all and that the workspace is
// inside a repository. It returns "" when the environment is usable.
func gitSetupError(workspaceRoot string) string {
	if !GitAvailable() {
		return "error: git is not installed or not on PATH (the git tools require git >= 2.23)"
	}
	if _, ok := gitRepoRootFor(workspaceRoot); !ok {
		return "error: the workspace is not inside a git repository — enable git_init (Enable/Disable Tools) to create one, or start fuckyourcli inside a repository"
	}
	return ""
}

// ensureGitTier enforces the permission model for one git action. It returns
// "" when the action may run, otherwise an error message for the model.
//
//   - read-only actions never prompt;
//   - a tier already granted in the configuration runs without prompting;
//   - in auto mode (/auto) the interactive prompts are skipped, but only tools
//     the user enabled can reach this point (every mutating git tool starts
//     disabled), and force push still requires git.allow_force_push;
//   - otherwise the user is asked, and "Allow always" persists the grant.
//
// details is optional extra context shown by the "See details" option; callers
// pass gitCommandDetails(...) with the exact argv they are about to run.
func ensureGitTier(ctx context.Context, tier config.GitToolTier, description string, details ...types.DetailLine) string {
	if tier == config.GitTierRead {
		return ""
	}
	cfg := config.Get()
	if cfg.Git.Allows(tier) {
		return ""
	}
	if cfg.NoAsk {
		return ""
	}
	return askGitTier(ctx, tier, description, details)
}

// askGitTier shows the permission prompt for a tier. Destructive operations
// put "Cancel" first so a bare Enter refuses the operation.
func askGitTier(ctx context.Context, tier config.GitToolTier, description string, details []types.DetailLine) string {
	title := fmt.Sprintf("Git %s permission:\n%s", tier.String(), description)

	var options []string
	allowIdx, denyIdx := 0, 1
	if tier == config.GitTierDestructive {
		options = []string{"Cancel", "Confirm destructive operation", "Allow always", "Deny with suggestion"}
		allowIdx, denyIdx = 1, 0
	} else {
		options = []string{"Allow", "Deny", "Allow always", "Deny with suggestion"}
	}
	persistIdx := 2
	denySuggIdx := len(options) - 1

	req := InteractionRequest{
		Kind:            KindApproval,
		Title:           title,
		Options:         options,
		AllowIndex:      allowIdx,
		DenyIndex:       denyIdx,
		SuggestionIndex: denySuggIdx,
		SeeDetailsLabel: "See details",
		DetailsTitle:    "Git " + tier.String() + " permission",
		Details:         gitPermissionDetails(tier, description, details),
	}
	resp, err := interact(ctx, req)
	if err != nil {
		return "error: user denied the git operation"
	}

	switch resp.Choice {
	case allowIdx:
		return ""
	case persistIdx:
		persistGitTierAllowed(tier)
		return ""
	case denySuggIdx:
		if s := strings.TrimSpace(resp.Text); s != "" {
			return "error: user denied the git operation. Suggestion: " + s
		}
		return "error: user denied the git operation"
	default:
		return "error: user denied the git operation"
	}
}

// gitPermissionDetails renders the "See details" content of a git permission
// prompt: the tier, the action, and (when the caller provides it) the exact
// invocation that will run, already typed for the viewer.
func gitPermissionDetails(tier config.GitToolTier, description string, extra []types.DetailLine) []types.DetailLine {
	out := []types.DetailLine{
		{Text: "Tier: " + tier.String()},
		{Text: "Action: " + description},
	}
	if len(extra) > 0 {
		out = append(out, types.DetailLine{})
		out = append(out, extra...)
	}
	return out
}

// gitCommandDetails renders the workspace root and the exact git invocation a
// permission covers. The argv is the same slice the executor runs, so what the
// user reads is exactly what happens. The lines are typed: the viewer highlights
// the invocation with the shell lexer and colours the workspace path.
func gitCommandDetails(workspaceRoot string, argv []string) []types.DetailLine {
	parts := make([]string, len(argv))
	for i, a := range argv {
		// Redact credentials: an argument can be a remote URL carrying userinfo
		// (git clone/add/set-url), and it must never reach the screen or the
		// terminal scrollback with a token in it.
		parts[i] = quoteGitArg(redactGitOutput(a))
	}
	return []types.DetailLine{
		{Kind: types.DetailSection, Text: "Workspace:"},
		{Kind: types.DetailPath, Text: "  " + workspaceRoot},
		{},
		{Kind: types.DetailSection, Text: "Command:"},
		{Kind: types.DetailCommand, Text: "  git " + strings.Join(parts, " ")},
	}
}

// quoteGitArg quotes an argument for display when it contains characters that
// would otherwise be ambiguous in the rendered command line.
func quoteGitArg(a string) string {
	if a == "" {
		return `""`
	}
	if strings.ContainsAny(a, " \t\n\"'\\$`") {
		return strconv.Quote(a)
	}
	return a
}

// confirmGitTyped asks the user to type a word to confirm a dangerous action
// (strengthened confirmation, used by `git_reset` with mode=hard).
func confirmGitTyped(ctx context.Context, description, word string) string {
	if config.Get().NoAsk {
		return ""
	}
	resp, err := interact(ctx, InteractionRequest{
		Kind:   KindTypedConfirm,
		Title:  description,
		Expect: word,
	})
	if err != nil || strings.TrimSpace(resp.Text) != word {
		return "error: confirmation not provided, the operation was aborted"
	}
	return ""
}

// persistGitTierAllowed records a git tier grant in the shared configuration
// unless persistence is disabled (AG-UI server: grants are per-thread and live
// in the interaction broker).
func persistGitTierAllowed(tier config.GitToolTier) {
	if !permissionPersistenceEnabled() {
		return
	}
	config.SetGitTierAllowed(tier, true)
	config.Save()
}

// persistGitAllowWrite records the "always allow commits" grant in the shared
// configuration unless persistence is disabled (AG-UI server).
func persistGitAllowWrite() {
	if !permissionPersistenceEnabled() {
		return
	}
	config.SetGitAllowWrite(true)
	config.Save()
}
