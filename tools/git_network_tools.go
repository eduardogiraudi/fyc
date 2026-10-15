package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/config"
)

// ── network git tools (disabled by default) ──────────────────────────────────
//
// fetch/pull/push/clone talk to a remote. They are disabled by default and
// every call is gated by a dedicated network permission; force push requires
// the persistent git.allow_force_push flag, even in auto mode.

// gitRemoteArg resolves the remote to use: explicit argument first, then the
// configured default remote (origin).
func gitRemoteArg(args map[string]interface{}) (string, error) {
	remote := strings.TrimSpace(strArg(args, "remote"))
	if remote == "" {
		remote = config.GetGit().EffectiveDefaultRemote()
	}
	return validateGitRemoteName(remote)
}

// executeGitFetch fetches from a remote.
func executeGitFetch(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	remote, err := gitRemoteArg(args)
	if err != nil {
		return "error: " + err.Error()
	}

	argv := []string{"fetch"}
	if boolArg(args, "prune") {
		argv = append(argv, "--prune")
	}
	argv = append(argv, remote)

	description := fmt.Sprintf("fetch from remote %s", remote)
	if boolArg(args, "prune") {
		description += " (with --prune)"
	}
	if msg := ensureGitTier(ctx, gitActionTier("git_fetch", ""), description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitNetworkTimeout, "", argv).Text
}

// executeGitPull fast-forwards the current branch (`git pull --ff-only`).
func executeGitPull(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	remote, err := gitRemoteArg(args)
	if err != nil {
		return "error: " + err.Error()
	}

	argv := []string{"pull", "--ff-only", remote}
	description := fmt.Sprintf("pull --ff-only from remote %s", remote)
	if branch := strings.TrimSpace(strArg(args, "branch")); branch != "" {
		b, err := validateGitBranchName(branch)
		if err != nil {
			return "error: " + err.Error()
		}
		argv = append(argv, b)
		description += " branch " + b
	}
	if msg := ensureGitTier(ctx, gitActionTier("git_pull", ""), description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitNetworkTimeout, "", argv).Text
}

// executeGitPush pushes to a remote. --force-with-lease is only accepted when
// git.allow_force_push is enabled in the configuration.
func executeGitPush(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	remote, err := gitRemoteArg(args)
	if err != nil {
		return "error: " + err.Error()
	}

	force := boolArg(args, "force_with_lease")
	if force && !config.GetGit().AllowForcePush {
		return "error: force push is disabled. Set git.allow_force_push=true in config.json (or from config → Git Permissions) to allow --force-with-lease; plain --force is never available"
	}

	argv := []string{"push"}
	if force {
		argv = append(argv, "--force-with-lease")
	}
	if boolArg(args, "set_upstream") {
		argv = append(argv, "--set-upstream")
	}
	argv = append(argv, remote)

	description := fmt.Sprintf("push to remote %s", remote)
	if branch := strings.TrimSpace(strArg(args, "branch")); branch != "" {
		b, err := validateGitBranchName(branch)
		if err != nil {
			return "error: " + err.Error()
		}
		argv = append(argv, b)
		description += " branch " + b
	}
	if boolArg(args, "set_upstream") {
		description += " (set upstream)"
	}
	if force {
		description += " with --force-with-lease"
	}
	if msg := ensureGitTier(ctx, gitActionTier("git_push", ""), description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitNetworkTimeout, "", argv).Text
}

// executeGitClone clones a repository into the workspace.
func executeGitClone(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if !GitAvailable() {
		return "error: git is not installed or not on PATH (the git tools require git >= 2.23)"
	}
	url, err := validateGitRemoteURL(strArg(args, "url"))
	if err != nil {
		return "error: " + err.Error()
	}

	dest := "."
	if p := strings.TrimSpace(strArg(args, "destination")); p != "" {
		dest, err = validateGitPath(p, workspaceRoot)
		if err != nil {
			return "error: " + err.Error()
		}
	}
	if dest != "." {
		// git refuses to clone into a non-empty directory: report it clearly
		// instead of letting git fail with a cryptic message.
		abs := filepath.Join(workspaceRoot, dest)
		if entries, err := os.ReadDir(abs); err == nil && len(entries) > 0 {
			return fmt.Sprintf("error: destination %s already exists and is not empty", dest)
		}
	}

	argv := []string{"clone", "--", url}
	if dest != "." {
		argv = append(argv, dest)
	}

	if msg := ensureGitTier(ctx, gitActionTier("git_clone", ""), fmt.Sprintf("clone %s into %s", redactGitOutput(url), dest), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitNetworkTimeout, "", argv)
	InvalidateGitRepoCache()
	return res.Text
}
