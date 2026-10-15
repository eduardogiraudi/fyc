package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"fuckyourcli/config"
	"fuckyourcli/ui"
)

// ── local write git tools (disabled by default) ──────────────────────────────
//
// Every tool in this file mutates the repository, so all of them start
// disabled and each call is additionally gated by the local-write permission.

// executeGitAdd stages paths (`git add`).
func executeGitAdd(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	all := boolArg(args, "all")
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	if !all && len(paths) == 0 {
		return "error: provide 'paths' (or 'path') or set all=true"
	}

	target := strings.Join(paths, ", ")
	if all {
		if target != "" {
			target = "all changes, " + target
		} else {
			target = "all changes in the repository"
		}
	}
	argv := []string{"add"}
	if all {
		argv = append(argv, "--all")
	}
	if len(paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, paths...)
	}

	if msg := ensureGitTier(ctx, gitActionTier("git_add", ""), gitActionDescription("stage", target), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// executeGitCommit creates a commit. The message is passed on stdin (`-F -`),
// so no shell or temporary file is involved; the staged summary is shown in
// the permission prompt together with a diff viewer.
func executeGitCommit(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	message := strings.TrimSpace(strArg(args, "message"))
	if message == "" {
		return "error: 'message' is required"
	}
	all := boolArg(args, "all")
	amend := boolArg(args, "amend")

	if err := gitCommitPermission(ctx, message, all, workspaceRoot); err != "" {
		return err
	}

	argv := []string{"commit"}
	if all {
		argv = append(argv, "--all")
	}
	if amend {
		argv = append(argv, "--amend")
	}
	// The message is passed on stdin: no shell, no temp files.
	argv = append(argv, "--file", "-")
	return gitToolRun(workspaceRoot, gitDefaultTimeout, message, argv).Text
}

// gitCommitPermission shows the commit prompt: `--stat` of the staged changes
// plus an option to open the full staged diff in the viewer.
func gitCommitPermission(ctx context.Context, message string, all bool, workspaceRoot string) string {
	cfg := config.Get()
	if cfg.Git.AllowWrite || cfg.NoAsk {
		return ""
	}

	statRes := runGit([]string{"diff", "--staged", "--stat"}, gitOptions{Dir: workspaceRoot, Timeout: gitDefaultTimeout})
	diffRes := runGit([]string{"diff", "--staged"}, gitOptions{Dir: workspaceRoot, Timeout: gitDefaultTimeout})

	stat := strings.TrimSpace(statRes.Stdout)
	if all {
		stat += "\n(--all: unstaged modifications of tracked files are included)"
	}
	if stat == "" {
		stat = "(nothing staged)"
	}

	firstLine := message
	if i := strings.IndexByte(firstLine, '\n'); i >= 0 {
		firstLine = firstLine[:i]
	}
	title := ui.FileStyle.Render("Commit: " + truncateForDisplay(firstLine, 80))

	diffLines := parseUnifiedDiffLines(diffRes.Stdout)
	added, removed := countDiffLines(diffLines)
	stats := formatDiffStats(added, removed)

	options := []string{"Commit", "Cancel", "Allow always", "Deny with suggestion"}
	denySuggIdx := len(options) - 1
	req := InteractionRequest{
		Kind:            KindApproval,
		Title:           title + "\n" + stat + "\n",
		StyledTitle:     true,
		Options:         options,
		AllowIndex:      0,
		DenyIndex:       1,
		SuggestionIndex: denySuggIdx,
		SeeChangesLabel: "See staged changes",
		Diff:            diffLines,
		DiffStats:       stats,
		DiffTitle:       title,
	}
	resp, err := interact(ctx, req)
	if err != nil {
		return "error: user denied the commit"
	}

	switch resp.Choice {
	case 0:
		return ""
	case 2:
		persistGitAllowWrite()
		return ""
	case denySuggIdx:
		if s := strings.TrimSpace(resp.Text); s != "" {
			return "error: user denied the commit. Suggestion: " + s
		}
		return "error: user denied the commit"
	default:
		return "error: user denied the commit"
	}
}

// countDiffLines counts added/removed lines in parsed diff lines.
func countDiffLines(lines []ui.DiffLine) (added, removed int) {
	for _, l := range lines {
		switch l.Kind {
		case ui.DiffAdded:
			added++
		case ui.DiffRemoved:
			removed++
		}
	}
	return
}

var gitHunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// parseUnifiedDiffLines converts a unified diff (as produced by `git diff`)
// into ui.DiffLine values for the terminal diff viewer.
func parseUnifiedDiffLines(diff string) []ui.DiffLine {
	if strings.TrimSpace(diff) == "" {
		return nil
	}
	raw := strings.Split(diff, "\n")
	lines := make([]ui.DiffLine, 0, len(raw))
	oldNum, newNum := 0, 0

	for i, l := range raw {
		if i == len(raw)-1 && l == "" {
			continue // trailing newline
		}
		switch {
		case strings.HasPrefix(l, "@@"):
			lines = append(lines, ui.DiffLine{Kind: ui.DiffHunkHeader, Content: l})
			if m := gitHunkRe.FindStringSubmatch(l); m != nil {
				oldNum, _ = strconv.Atoi(m[1])
				newNum, _ = strconv.Atoi(m[2])
			}
		case isUnifiedDiffHeader(l):
			// File headers (diff --git, index, ---, +++, mode lines…): shown as
			// context, and detected before the +/- cases below so that
			// "--- a/f" and "+++ b/f" are not counted as removed/added lines.
			lines = append(lines, ui.DiffLine{Kind: ui.DiffContext, Content: l})
		case strings.HasPrefix(l, "+"):
			lines = append(lines, ui.DiffLine{Kind: ui.DiffAdded, Content: l[1:], LineNum: newNum})
			newNum++
		case strings.HasPrefix(l, "-"):
			lines = append(lines, ui.DiffLine{Kind: ui.DiffRemoved, Content: l[1:], LineNum: oldNum})
			oldNum++
		case strings.HasPrefix(l, " "):
			lines = append(lines, ui.DiffLine{Kind: ui.DiffContext, Content: l[1:], LineNum: oldNum})
			oldNum++
			newNum++
		default:
			// Anything else (e.g. a "\ No newline at end of file" marker) is
			// shown as context without consuming line numbers.
			lines = append(lines, ui.DiffLine{Kind: ui.DiffContext, Content: l})
		}
	}
	return lines
}

// unifiedDiffHeaderPrefixes lists the metadata lines of a unified diff that are
// neither context nor added/removed content.
var unifiedDiffHeaderPrefixes = []string{
	"diff --git ", "index ", "--- ", "+++ ", "new file mode ", "deleted file mode ",
	"old mode ", "new mode ", "similarity index ", "rename from ", "rename to ",
	"copy from ", "copy to ", "Binary files ",
}

// isUnifiedDiffHeader reports whether a diff line is file metadata.
func isUnifiedDiffHeader(line string) bool {
	for _, p := range unifiedDiffHeaderPrefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// executeGitSwitch switches branch (creating it with create=true).
func executeGitSwitch(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	branch, err := validateGitBranchName(strArg(args, "branch"))
	if err != nil {
		return "error: " + err.Error()
	}
	create := boolArg(args, "create")

	verb := "switch to branch"
	if create {
		verb = "create and switch to branch"
	}
	var argv []string
	if gitSupportsSwitch() {
		argv = []string{"switch"}
		if create {
			argv = append(argv, "--create")
		}
		argv = append(argv, branch)
	} else {
		// git < 2.23 fallback
		argv = []string{"checkout"}
		if create {
			argv = append(argv, "-b")
		}
		argv = append(argv, branch)
	}

	if msg := ensureGitTier(ctx, gitActionTier("git_switch", ""), gitActionDescription(verb, branch), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// executeGitRestore restores paths from the index or from a revision.
func executeGitRestore(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(paths) == 0 {
		return "error: 'paths' (or 'path') is required"
	}
	staged := boolArg(args, "staged")
	source := strings.TrimSpace(strArg(args, "source"))
	if source != "" {
		v, err := validateGitRev(source)
		if err != nil {
			return "error: " + err.Error()
		}
		source = v
	}

	target := strings.Join(paths, ", ")
	verb := "discard local changes in"
	if staged {
		if source != "" {
			verb = "reset the index entries of"
		} else {
			verb = "unstage"
		}
	} else if source != "" {
		verb = fmt.Sprintf("overwrite local changes of %q", source)
	}
	argv := []string{"restore"}
	if staged {
		argv = append(argv, "--staged")
	}
	if source != "" {
		argv = append(argv, "--source", source)
	}
	argv = append(argv, "--")
	argv = append(argv, paths...)

	if msg := ensureGitTier(ctx, gitActionTier("git_restore", ""), gitActionDescription(verb, target), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// executeGitStash manages the stash (list/push/pop/apply/drop).
func executeGitStash(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
	if action == "" {
		action = "list"
	}

	ref, err := validateGitStashRef(strArg(args, "ref"))
	if err != nil {
		return "error: " + err.Error()
	}
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}

	var argv []string
	switch action {
	case "list":
		argv = []string{"stash", "list"}
		if ref != "" {
			argv = append(argv, ref)
		}
	case "push":
		argv = []string{"stash", "push"}
		if msg := strings.TrimSpace(strArg(args, "message")); msg != "" {
			if hasControlChars(msg) {
				return "error: invalid characters in 'message'"
			}
			argv = append(argv, "--message", msg)
		}
		if len(paths) > 0 {
			argv = append(argv, "--")
			argv = append(argv, paths...)
		}
	case "pop", "apply", "drop":
		argv = []string{"stash", action}
		if ref != "" {
			argv = append(argv, ref)
		}
		if action != "drop" && len(paths) > 0 {
			return fmt.Sprintf("error: 'paths' is not supported for stash %s", action)
		}
	default:
		return fmt.Sprintf("error: unknown stash action %q (use list, push, pop, apply or drop)", action)
	}

	description := "stash " + action
	if ref != "" {
		description += " " + ref
	}
	if msg := ensureGitTier(ctx, gitActionTier("git_stash", action), gitActionDescription(description, strings.Join(paths, ", ")), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// executeGitInit initializes a repository.
func executeGitInit(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if !GitAvailable() {
		return "error: git is not installed or not on PATH (the git tools require git >= 2.23)"
	}
	branch := strings.TrimSpace(strArg(args, "default_branch"))
	if branch != "" {
		v, err := validateGitBranchName(branch)
		if err != nil {
			return "error: " + err.Error()
		}
		branch = v
	}

	argv := []string{"init"}
	target := "current workspace"
	if branch != "" {
		argv = append(argv, "--initial-branch", branch)
	}
	if p := strings.TrimSpace(strArg(args, "path")); p != "" {
		v, err := validateGitPath(p, workspaceRoot)
		if err != nil {
			return "error: " + err.Error()
		}
		argv = append(argv, v)
		target = v
	}

	if msg := ensureGitTier(ctx, gitActionTier("git_init", ""), gitActionDescription("initialize a git repository in", target), gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv)
	InvalidateGitRepoCache()
	return res.Text
}

// executeGitBranch lists, creates, renames or deletes branches.
func executeGitBranch(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	action := strings.ToLower(strings.TrimSpace(strArg(args, "action")))
	if action == "" {
		action = "list"
	}

	tier := gitActionTier("git_branch", action)
	var argv []string
	var description string

	switch action {
	case "list":
		argv = []string{"branch", "--list", "--no-color"}
	case "create":
		name, err := validateGitBranchName(strArg(args, "name"))
		if err != nil {
			return "error: " + err.Error()
		}
		start := strings.TrimSpace(strArg(args, "rev"))
		argv = []string{"branch", name}
		if start != "" {
			v, err := validateGitRev(start)
			if err != nil {
				return "error: " + err.Error()
			}
			argv = append(argv, v)
			description = fmt.Sprintf("create branch %s at %s", name, v)
		} else {
			description = "create branch " + name
		}
	case "rename":
		name, err := validateGitBranchName(strArg(args, "name"))
		if err != nil {
			return "error: " + err.Error()
		}
		newName, err := validateGitBranchName(strArg(args, "new_name"))
		if err != nil {
			return "error: " + err.Error()
		}
		argv = []string{"branch", "--move", name, newName}
		description = fmt.Sprintf("rename branch %s to %s", name, newName)
	case "delete":
		name, err := validateGitBranchName(strArg(args, "name"))
		if err != nil {
			return "error: " + err.Error()
		}
		flag := "-d"
		if boolArg(args, "force") {
			flag = "-D"
			tier = config.GitTierDestructive // force delete may drop unmerged commits
		}
		argv = []string{"branch", flag, name}
		description = "delete branch " + name
	default:
		return fmt.Sprintf("error: unknown branch action %q (use list, create, rename or delete)", action)
	}

	if msg := ensureGitTier(ctx, tier, description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	res := gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv)
	if action != "list" {
		InvalidateGitRepoCache()
	}
	return res.Text
}

// executeGitRemote lists, adds, updates or removes remotes.
func executeGitRemote(ctx context.Context, args map[string]interface{}, workspaceRoot string) string {
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
		argv = []string{"remote", "-v"}
	case "add":
		name, err := validateGitRemoteName(strArg(args, "name"))
		if err != nil {
			return "error: " + err.Error()
		}
		url, err := validateGitRemoteURL(strArg(args, "url"))
		if err != nil {
			return "error: " + err.Error()
		}
		argv = []string{"remote", "add", name, url}
		description = fmt.Sprintf("add remote %s -> %s", name, redactGitOutput(url))
	case "set-url":
		name, err := validateGitRemoteName(strArg(args, "name"))
		if err != nil {
			return "error: " + err.Error()
		}
		url, err := validateGitRemoteURL(strArg(args, "url"))
		if err != nil {
			return "error: " + err.Error()
		}
		argv = []string{"remote", "set-url", name, url}
		description = fmt.Sprintf("change remote %s to %s", name, redactGitOutput(url))
	case "remove":
		name, err := validateGitRemoteName(strArg(args, "name"))
		if err != nil {
			return "error: " + err.Error()
		}
		argv = []string{"remote", "remove", name}
		description = "remove remote " + name
	default:
		return fmt.Sprintf("error: unknown remote action %q (use list, add, set-url or remove)", action)
	}

	if msg := ensureGitTier(ctx, gitActionTier("git_remote", action), description, gitCommandDetails(workspaceRoot, argv)...); msg != "" {
		return msg
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}
