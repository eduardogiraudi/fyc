package tools

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ── git pathspec / ref validation ────────────────────────────────────────────
//
// Every user-supplied value that ends up in the git argv is validated here.
// Paths are restricted to the workspace (traversal and absolute paths outside
// it are rejected, `.git` internals are never addressable), refs and branch
// names are checked against a conservative character set so an argument can
// never be mistaken for an option (`--` is always used before pathspecs).

var (
	// gitRefSegmentRe matches one side of a rev (or rev range).
	gitRefSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_.~^/@{}:+-]+$`)
	// gitBranchNameRe matches a branch/tag name.
	gitBranchNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./-]*$`)
	// gitStashRefRe matches a stash reference (`stash@{n}`).
	gitStashRefRe = regexp.MustCompile(`^stash@\{\d+\}$`)
	// gitRemoteNameRe matches a remote name.
	gitRemoteNameRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
)

// hasControlChars reports whether s contains characters that must never reach
// the git argv.
func hasControlChars(s string) bool {
	return strings.ContainsAny(s, "\x00\n\r\t")
}

// validateGitPath cleans and validates a repository path supplied by the
// model. The returned path is relative to the workspace and slash-separated.
func validateGitPath(p, workspaceRoot string) (string, error) {
	raw := strings.TrimSpace(p)
	if raw == "" {
		return "", fmt.Errorf("empty path")
	}
	if hasControlChars(raw) {
		return "", fmt.Errorf("invalid characters in path %q", raw)
	}

	clean := filepath.Clean(raw)
	if clean == "." {
		return ".", nil
	}

	if filepath.IsAbs(clean) {
		absRoot, err := filepath.Abs(workspaceRoot)
		if err != nil {
			return "", fmt.Errorf("cannot resolve the workspace root: %w", err)
		}
		if clean != absRoot && !strings.HasPrefix(clean, absRoot+string(filepath.Separator)) {
			return "", fmt.Errorf("access denied: %s is outside the workspace %s", raw, absRoot)
		}
		rel, err := filepath.Rel(absRoot, clean)
		if err != nil {
			return "", fmt.Errorf("cannot compute the relative path: %w", err)
		}
		clean = rel
	}

	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("access denied: path traversal is not allowed (%s)", raw)
	}

	slash := filepath.ToSlash(clean)
	if slash == ".git" || strings.HasPrefix(slash, ".git/") {
		return "", fmt.Errorf("access denied: .git internals are not accessible through git tools")
	}
	return slash, nil
}

// validateGitPaths validates a list of model-supplied paths.
func validateGitPaths(paths []string, workspaceRoot string) ([]string, error) {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		v, err := validateGitPath(p, workspaceRoot)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// gitPathsArg collects a path list from the `paths` array or the single `path`
// argument and validates every entry against the workspace.
func gitPathsArg(args map[string]interface{}, workspaceRoot string) ([]string, error) {
	raw := extractStringList(args, "paths")
	if len(raw) == 0 {
		if p := strings.TrimSpace(strArg(args, "path")); p != "" {
			raw = []string{p}
		}
	}
	return validateGitPaths(raw, workspaceRoot)
}

// validateGitRev validates a revision or a revision range (`a..b`, `a...b`).
func validateGitRev(rev string) (string, error) {
	r := strings.TrimSpace(rev)
	if r == "" {
		return "", fmt.Errorf("empty revision")
	}
	if len(r) > 300 {
		return "", fmt.Errorf("revision too long")
	}
	if strings.HasPrefix(r, "-") || strings.HasPrefix(r, "/") {
		return "", fmt.Errorf("invalid revision %q", rev)
	}
	if hasControlChars(r) || strings.ContainsAny(r, " \"'`$;&|\\<>") {
		return "", fmt.Errorf("invalid characters in revision %q", rev)
	}
	if strings.Contains(r, "..") {
		parts := strings.Split(r, "..")
		if len(parts) > 2 {
			return "", fmt.Errorf("invalid revision range %q", rev)
		}
		empty := 0
		for _, p := range parts {
			if p == "" {
				empty++
				continue
			}
			if !gitRefSegmentRe.MatchString(p) {
				return "", fmt.Errorf("invalid revision %q", rev)
			}
		}
		if empty == len(parts) {
			return "", fmt.Errorf("invalid revision %q", rev)
		}
		return r, nil
	}
	if !gitRefSegmentRe.MatchString(r) {
		return "", fmt.Errorf("invalid revision %q", rev)
	}
	return r, nil
}

// validateGitBranchName validates a branch or tag name.
func validateGitBranchName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("empty branch name")
	}
	if n == "HEAD" {
		return "", fmt.Errorf("refusing to use %q as a branch name", n)
	}
	if len(n) > 200 || hasControlChars(n) {
		return "", fmt.Errorf("invalid branch name %q", name)
	}
	if !gitBranchNameRe.MatchString(n) || strings.Contains(n, "..") ||
		strings.HasSuffix(n, "/") || strings.HasSuffix(n, ".lock") ||
		strings.HasPrefix(n, "-") {
		return "", fmt.Errorf("invalid branch name %q (use letters, digits, '.', '_', '-' and '/')", name)
	}
	return n, nil
}

// validateGitStashRef validates `stash@{n}` references.
func validateGitStashRef(ref string) (string, error) {
	r := strings.TrimSpace(ref)
	if r == "" {
		return "", nil
	}
	if hasControlChars(r) || !gitStashRefRe.MatchString(r) {
		return "", fmt.Errorf("invalid stash reference %q (expected stash@{n})", ref)
	}
	return r, nil
}

// validateGitRemoteName validates a remote name.
func validateGitRemoteName(name string) (string, error) {
	n := strings.TrimSpace(name)
	if n == "" {
		return "", fmt.Errorf("empty remote name")
	}
	if len(n) > 200 || hasControlChars(n) || !gitRemoteNameRe.MatchString(n) {
		return "", fmt.Errorf("invalid remote name %q", name)
	}
	return n, nil
}

// validateGitRemoteURL validates a clone/remote URL. Remote URLs are accepted
// with an explicit scheme (https://, ssh://, file://…) or in scp-like syntax
// (user@host:path); local paths are accepted too, since the user must confirm
// the network permission tier before they are used.
func validateGitRemoteURL(url string) (string, error) {
	u := strings.TrimSpace(url)
	if u == "" {
		return "", fmt.Errorf("empty remote URL")
	}
	if hasControlChars(u) || strings.HasPrefix(u, "-") {
		return "", fmt.Errorf("invalid remote URL %q", url)
	}
	if strings.ContainsAny(u, "\"'`$;&|\\<>") {
		return "", fmt.Errorf("invalid characters in remote URL %q", url)
	}
	if strings.Contains(u, "://") || strings.Contains(u, "@") || strings.Contains(u, ":") || strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") {
		return u, nil
	}
	return "", fmt.Errorf("invalid remote URL %q", url)
}
