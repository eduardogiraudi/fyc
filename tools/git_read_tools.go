package tools

import (
	"fmt"
	"strings"
)

// ── read-only git tools (enabled by default) ─────────────────────────────────

// gitStatusArgs builds `git status --porcelain=v2 --branch [-- path]`.
func gitStatusArgs(args map[string]interface{}, workspaceRoot string) ([]string, error) {
	argv := []string{"status", "--porcelain=v2", "--branch"}
	if p := strings.TrimSpace(strArg(args, "path")); p != "" {
		v, err := validateGitPath(p, workspaceRoot)
		if err != nil {
			return nil, err
		}
		argv = append(argv, "--", v)
	}
	return argv, nil
}

func executeGitStatus(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, err := gitStatusArgs(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// gitDiffArgs builds `git diff [--staged] [--stat|--name-only] [-U<n>] [rev] [-- paths]`.
func gitDiffArgs(args map[string]interface{}, workspaceRoot string) ([]string, error) {
	staged := boolArg(args, "staged")
	rev := strings.TrimSpace(strArg(args, "rev"))
	if staged && rev != "" {
		return nil, fmt.Errorf("staged and rev are mutually exclusive")
	}

	argv := []string{"diff"}
	if staged {
		argv = append(argv, "--staged")
	}
	switch {
	case boolArg(args, "name_only"):
		argv = append(argv, "--name-only")
	case boolArg(args, "stat_only"):
		argv = append(argv, "--stat")
	}
	if n := intArg(args, "context_lines"); n > 0 {
		if n > 100 {
			n = 100
		}
		argv = append(argv, fmt.Sprintf("-U%d", n))
	}
	if rev != "" {
		v, err := validateGitRev(rev)
		if err != nil {
			return nil, err
		}
		argv = append(argv, v)
	}
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, paths...)
	}
	return argv, nil
}

func executeGitDiff(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, err := gitDiffArgs(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// gitLogArgs builds `git log -n<count> [--oneline] [--stat] [filters] [-- paths]`.
func gitLogArgs(args map[string]interface{}, workspaceRoot string) ([]string, error) {
	count := intArg(args, "count")
	if count <= 0 {
		count = 20
	}
	if count > gitMaxLines {
		count = gitMaxLines
	}

	argv := []string{"log", fmt.Sprintf("-n%d", count), "--decorate=short"}
	if boolArgDefault(args, "oneline", true) {
		argv = append(argv, "--oneline")
	}
	if boolArg(args, "stat") {
		argv = append(argv, "--stat")
	}
	for _, f := range []struct{ key, flag string }{
		{"author", "--author="},
		{"grep", "--grep="},
		{"since", "--since="},
		{"until", "--until="},
	} {
		v := strings.TrimSpace(strArg(args, f.key))
		if v == "" {
			continue
		}
		if hasControlChars(v) {
			return nil, fmt.Errorf("invalid characters in '%s'", f.key)
		}
		argv = append(argv, f.flag+v)
	}
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, paths...)
	}
	return argv, nil
}

func executeGitLog(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, err := gitLogArgs(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// gitShowArgs builds `git show [--stat] [rev] [-- paths]`.
func gitShowArgs(args map[string]interface{}, workspaceRoot string) ([]string, error) {
	rev := strings.TrimSpace(strArg(args, "rev"))
	if rev == "" {
		rev = "HEAD"
	} else {
		v, err := validateGitRev(rev)
		if err != nil {
			return nil, err
		}
		rev = v
	}

	argv := []string{"show"}
	if boolArg(args, "stat_only") {
		argv = append(argv, "--stat")
	}
	argv = append(argv, rev)
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, paths...)
	}
	return argv, nil
}

func executeGitShow(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, err := gitShowArgs(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// gitBlameArgs builds `git blame [-L start,end] -- path`.
func gitBlameArgs(args map[string]interface{}, workspaceRoot string) ([]string, string, error) {
	p := strings.TrimSpace(strArg(args, "path"))
	if p == "" {
		return nil, "", fmt.Errorf("'path' is required")
	}
	v, err := validateGitPath(p, workspaceRoot)
	if err != nil {
		return nil, "", err
	}

	argv := []string{"blame"}
	start, end := intArg(args, "start"), intArg(args, "end")
	if start > 0 || end > 0 {
		if start <= 0 {
			start = 1
		}
		if end <= 0 {
			end = start
		}
		if end < start {
			return nil, "", fmt.Errorf("'end' must be greater than or equal to 'start'")
		}
		argv = append(argv, fmt.Sprintf("-L%d,%d", start, end))
	}
	argv = append(argv, "--", v)
	return argv, v, nil
}

func executeGitBlame(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, _, err := gitBlameArgs(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	max := intArg(args, "max_lines")
	if max <= 0 {
		max = 200
	}
	if max > gitMaxLines {
		max = gitMaxLines
	}

	res := runGit(argv, gitOptions{Dir: workspaceRoot, Timeout: gitDefaultTimeout})
	if !res.OK() {
		return gitResultToTool(res, gitDefaultTimeout).Text
	}
	return truncateGitLines(res.Output(), max)
}

// gitLsFilesArgs builds `git ls-files [flags] [-- paths]`.
func gitLsFilesArgs(args map[string]interface{}, workspaceRoot string) ([]string, error) {
	cached := boolArg(args, "cached")
	others := boolArg(args, "others")
	ignored := boolArg(args, "ignored")
	modified := boolArg(args, "modified")
	deleted := boolArg(args, "deleted")
	if !cached && !others && !ignored && !modified && !deleted {
		cached = true
	}

	argv := []string{"ls-files"}
	if cached {
		argv = append(argv, "--cached")
	}
	if others {
		argv = append(argv, "--others")
	}
	if ignored {
		argv = append(argv, "--ignored")
	}
	if modified {
		argv = append(argv, "--modified")
	}
	if deleted {
		argv = append(argv, "--deleted")
	}
	if others || ignored {
		argv = append(argv, "--exclude-standard")
	}
	paths, err := gitPathsArg(args, workspaceRoot)
	if err != nil {
		return nil, err
	}
	if len(paths) > 0 {
		argv = append(argv, "--")
		argv = append(argv, paths...)
	}
	return argv, nil
}

func executeGitLsFiles(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, err := gitLsFilesArgs(args, workspaceRoot)
	if err != nil {
		return "error: " + err.Error()
	}
	return gitToolRun(workspaceRoot, gitDefaultTimeout, "", argv).Text
}

// gitConfigArgs builds a read-only `git config` invocation. The default scope
// is the repository (`--local`) so the global configuration (which may hold
// credentials) is only read on explicit request.
func gitConfigArgs(args map[string]interface{}) ([]string, string, error) {
	key := strings.TrimSpace(strArg(args, "key"))
	scope := strings.ToLower(strings.TrimSpace(strArg(args, "scope")))
	if scope == "" {
		scope = "local"
	}

	argv := []string{"config"}
	switch scope {
	case "local", "global", "system", "worktree":
		argv = append(argv, "--"+scope)
	case "effective":
		// no scope flag: git resolves system → global → local
	default:
		return nil, "", fmt.Errorf("invalid scope %q (use local, global, system, worktree or effective)", scope)
	}

	if key == "" {
		argv = append(argv, "--list")
		return argv, "", nil
	}
	if hasControlChars(key) || strings.HasPrefix(key, "-") {
		return nil, "", fmt.Errorf("invalid key %q", key)
	}
	argv = append(argv, "--get", key)
	return argv, key, nil
}

func executeGitConfig(args map[string]interface{}, workspaceRoot string) string {
	if msg := gitSetupError(workspaceRoot); msg != "" {
		return msg
	}
	argv, key, err := gitConfigArgs(args)
	if err != nil {
		return "error: " + err.Error()
	}

	res := runGit(argv, gitOptions{Dir: workspaceRoot, Timeout: gitDefaultTimeout})
	if key != "" && res.Exit == 1 && strings.TrimSpace(res.Stdout) == "" {
		return fmt.Sprintf("%s is not set", key)
	}
	return gitResultToTool(res, gitDefaultTimeout).Text
}

// gitActionDescription builds the human-readable line shown in permission
// prompts and error messages.
func gitActionDescription(verb, target string) string {
	if strings.TrimSpace(target) == "" {
		return verb
	}
	return verb + ": " + target
}
