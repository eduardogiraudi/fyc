package tools

import (
	"context"
	"fmt"
	"strings"

	"fuckyourcli/config"
)

// ── destructive git tools (disabled by default) ──────────────────────────────
//
// These operations can discard work or rewrite history. They are disabled by
// default, every call is gated by the destructive permission (whose prompt
// defaults to Cancel), and `git reset --hard` additionally requires a typed
// confirmation.

// gitRevsArg collects a revision list from `revs` (array) or `rev` (string).
func gitRevsArg(args map[string]interface{}) ([]string, error) {
	raw := extractStringList(args, "revs")
	if len(raw) == 0 {
		if r := strings.TrimSpace(strArg(args, "rev")); r != "" {
			raw = []string{r}
		}
	}
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		v, err := validateGitRev(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// executeGitReset resets the index/worktree (soft, mixed or hard).
func executeGitReset(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	mode := strings.ToLower(strings.TrimSpace(strArg(args, "mode")))
	if mode == "" {
		mode = "mixed"
	}
	switch mode {
	case "soft", "mixed", "hard":
	default:
		return fmt.Sprintf("error: invalid mode %q (use soft, mixed or hard)", mode)
	}

	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(paths) > 0 && mode != "mixed" {
		return fmt.Sprintf("error: paths can only be used with mode=mixed (got %s)", mode)
	}

	rev := strings.TrimSpace(strArg(args, "rev"))
	if rev != "" {
		v, err := validateGitRev(rev)
		if err != nil {
			return "error: " + err.Error()
		}
		rev = v
	}

	description := "git reset --" + mode
	if rev != "" {
		description += " " + rev
	}
	if len(paths) > 0 {
		description += " -- " + strings.Join(paths, ", ")
	}
	if mode == "hard" {
		description += " (discards all local changes and untracked-file content in the working tree)"
	}

	argv := []string{"reset", "--" + mode}
	if rev != "" {
		argv = append(argv, rev)
	}
	if len(paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, paths...)
	}

	if msg := ensureGitTier(ctx, config.GitTierDestructive, description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	if mode == "hard" {
		if msg := confirmGitTyped(ctx, description, "RESET"); msg != "" {
			return msg
		}
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// executeGitMerge merges a branch into the current one (or aborts a merge).
func executeGitMerge(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	if boolArg(args, "abort") {
		if msg := ensureGitTier(ctx, config.GitTierDestructive, "abort the in-progress merge", gitCommandDetails(workspaceRoot, []string{"merge", "--abort"})...); msg != "" {
			return msg
		}
		return gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"merge", "--abort"}).Text
	}

	branch, err := validateGitBranchName(strArg(args, "branch"))
	if err != nil {
		return "error: 'branch' is required: " + err.Error()
	}
	if msg := ensureGitTier(ctx, config.GitTierDestructive, "merge branch "+branch+" into the current branch", gitCommandDetails(workspaceRoot, []string{"merge", "--no-edit", branch})...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"merge", "--no-edit", branch})
	InvalidateGitRepoCache()
	return res.Text
}

// executeGitRebase rebases the current branch (interactive rebase is never
// offered). Also handles --continue / --abort.
func executeGitRebase(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	if boolArg(args, "abort") {
		if msg := ensureGitTier(ctx, config.GitTierDestructive, "abort the in-progress rebase", gitCommandDetails(workspaceRoot, []string{"rebase", "--abort"})...); msg != "" {
			return msg
		}
		return gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"rebase", "--abort"}).Text
	}
	if boolArg(args, "continue") {
		if msg := ensureGitTier(ctx, config.GitTierDestructive, "continue the in-progress rebase", gitCommandDetails(workspaceRoot, []string{"rebase", "--continue"})...); msg != "" {
			return msg
		}
		return gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"rebase", "--continue"}).Text
	}

	upstream, err := validateGitRev(strArg(args, "upstream"))
	if err != nil {
		return "error: 'upstream' is required: " + err.Error()
	}
	if msg := ensureGitTier(ctx, config.GitTierDestructive, "rebase the current branch onto "+upstream, gitCommandDetails(workspaceRoot, []string{"rebase", upstream})...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"rebase", upstream})
	InvalidateGitRepoCache()
	return res.Text
}

// executeGitCherryPick applies commits onto the current branch.
func executeGitCherryPick(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	if boolArg(args, "abort") {
		if msg := ensureGitTier(ctx, config.GitTierDestructive, "abort the in-progress cherry-pick", gitCommandDetails(workspaceRoot, []string{"cherry-pick", "--abort"})...); msg != "" {
			return msg
		}
		return gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"cherry-pick", "--abort"}).Text
	}

	revs, err := gitRevsArg(args)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(revs) == 0 {
		return "error: 'revs' (or 'rev') is required"
	}
	argv := append([]string{"cherry-pick"}, revs...)
	if msg := ensureGitTier(ctx, config.GitTierDestructive, "cherry-pick "+strings.Join(revs, " "), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv)
	InvalidateGitRepoCache()
	return res.Text
}

// executeGitRevert reverts commits (never opens an editor).
func executeGitRevert(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	if boolArg(args, "abort") {
		if msg := ensureGitTier(ctx, config.GitTierDestructive, "abort the in-progress revert", gitCommandDetails(workspaceRoot, []string{"revert", "--abort"})...); msg != "" {
			return msg
		}
		return gitToolRun(workspaceRoot, gitDefaultTimeout, "", []string{"revert", "--abort"}).Text
	}

	revs, err := gitRevsArg(args)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(revs) == 0 {
		return "error: 'revs' (or 'rev') is required"
	}
	argv := []string{"revert", "--no-edit"}
	argv = append(argv, revs...)

	if msg := ensureGitTier(ctx, config.GitTierDestructive, "revert "+strings.Join(revs, " "), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv)
	InvalidateGitRepoCache()
	return res.Text
}

// executeGitTag lists, creates or deletes tags.
func executeGitTag(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
	if action == "" {
		action = "list"
	}

	var argv []string
	var description string
	switch action {
	case "list":
		argv = []string{"tag", "--list"}
	case "create":
		name, err := validateGitBranchName(strArg(args, "name"))
		if err != nil {
			return "error: 'name' is required: " + err.Error()
		}
		message := strings.TrimSpace(strArg(args, "message"))
		if message != "" && hasControlChars(message) {
			return "error: invalid characters in 'message'"
		}
		argv = []string{"tag"}
		if message != "" {
			argv = append(argv, "--annotate", "--message", message)
		}
		argv = append(argv, name)
		description = "create tag " + name
		if rev := strings.TrimSpace(strArg(args, "rev")); rev != "" {
			v, err := validateGitRev(rev)
			if err != nil {
				return "error: " + err.Error()
			}
			argv = append(argv, v)
			description += " at " + v
		}
	case "delete":
		name, err := validateGitBranchName(strArg(args, "name"))
		if err != nil {
			return "error: 'name' is required: " + err.Error()
		}
		argv = []string{"tag", "--delete", name}
		description = "delete tag " + name
	default:
		return fmt.Sprintf("error: unknown tag action %q (use list, create or delete)", action)
	}

	if msg := ensureGitTier(ctx, gitActionTier("git_tag", action), description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}
