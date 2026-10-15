package tools

import (
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ── repository detection ─────────────────────────────────────────────────────
//
// The workspace (restricted root) may be the repository root itself or a
// subdirectory of one. Detection runs `git rev-parse --show-toplevel` inside
// the workspace; paths supplied by the model are additionally restricted to
// the workspace (see git_pathspec.go), while repository-wide operations
// (status, log, fetch, …) are allowed on the whole repository.

// gitRepoCacheTTL bounds how often the (relatively cheap) detection runs.
const gitRepoCacheTTL = 20 * time.Second

var gitRepoCache struct {
	sync.Mutex
	key  string
	root string
	ok   bool
	at   time.Time
}

// gitRepoRootFor returns the repository root containing workspaceRoot.
// Results are cached per workspace for gitRepoCacheTTL.
func gitRepoRootFor(workspaceRoot string) (string, bool) {
	key := gitWorkspaceKey(workspaceRoot)

	gitRepoCache.Lock()
	if gitRepoCache.key == key && time.Since(gitRepoCache.at) < gitRepoCacheTTL {
		root, ok := gitRepoCache.root, gitRepoCache.ok
		gitRepoCache.Unlock()
		return root, ok
	}
	gitRepoCache.Unlock()

	root, ok := detectGitRepoRoot(workspaceRoot)

	gitRepoCache.Lock()
	gitRepoCache.key = key
	gitRepoCache.root = root
	gitRepoCache.ok = ok
	gitRepoCache.at = time.Now()
	gitRepoCache.Unlock()

	return root, ok
}

// RefreshGitRepoRoot re-runs detection, bypassing the cache. It is used by the
// background heartbeat so a `git init` (or a repository deletion) is noticed.
func RefreshGitRepoRoot(workspaceRoot string) (string, bool) {
	InvalidateGitRepoCache()
	return gitRepoRootFor(workspaceRoot)
}

// InvalidateGitRepoCache drops the cached detection result.
func InvalidateGitRepoCache() {
	gitRepoCache.Lock()
	gitRepoCache.key = ""
	gitRepoCache.root = ""
	gitRepoCache.ok = false
	gitRepoCache.at = time.Time{}
	gitRepoCache.Unlock()
}

// gitWorkspaceKey normalizes a workspace path for the cache key.
func gitWorkspaceKey(workspaceRoot string) string {
	abs, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return workspaceRoot
	}
	return filepath.Clean(abs)
}

// detectGitRepoRoot runs the actual detection.
func detectGitRepoRoot(workspaceRoot string) (string, bool) {
	res := runGit([]string{"rev-parse", "--show-toplevel"}, gitOptions{
		Dir:     workspaceRoot,
		Timeout: 10 * time.Second,
	})
	if !res.OK() {
		return "", false
	}
	root := strings.TrimSpace(res.Stdout)
	if root == "" {
		return "", false
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return root, true
}
