package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"fuckyourcli/store"
)

// Workspace is the set of directories one tool call may touch. Primary is the
// directory relative paths resolve against and where `sh` runs (the process
// cwd); Extra holds the additional roots the user allowed with /cd, reachable
// only through absolute paths. An empty Extra is the historical single-root
// workspace, so nothing changes until /cd is used.
type Workspace struct {
	Primary string
	Extra   []string
}

// NewWorkspace builds a workspace from a primary root and any number of extra
// roots. Every root is made absolute and symlink-resolved, and roots that
// duplicate or nest inside an earlier one are dropped: a nested root grants
// nothing extra and would only duplicate sandbox binds, the AGENTS.md chain and
// the undo journal.
func NewWorkspace(primary string, extra []string) Workspace {
	w := Workspace{Primary: resolveRoot(primary)}
	for _, r := range extra {
		root := resolveRoot(r)
		if root == "" || nestedIn(w.Roots(), root) {
			continue
		}
		w.Extra = append(w.Extra, root)
	}
	return w
}

// resolveRoot makes p absolute and resolves its symlinks when it exists.
func resolveRoot(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		abs = filepath.Clean(p)
	}
	if resolved, err := store.ResolvePath(abs); err == nil {
		return resolved
	}
	return abs
}

// nestedIn reports whether dir is equal to or inside one of roots.
func nestedIn(roots []string, dir string) bool {
	for _, root := range roots {
		if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// Roots returns every allowed root, primary first, with symlinks resolved: a
// root built by hand (a struct literal, the single-root compatibility wrapper)
// must answer the same containment questions a normalised one does.
func (w Workspace) Roots() []string {
	out := make([]string, 0, len(w.Extra)+1)
	seen := make(map[string]bool, len(w.Extra)+1)
	for _, r := range append([]string{w.Primary}, w.Extra...) {
		root := resolveRoot(r)
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		out = append(out, root)
	}
	return out
}

// lexicalRoot returns the allowed root p is spelled inside, without resolving
// p's own symlinks. It is what turns a failed resolution into a precise error
// ("resolves outside" vs "is outside").
func (w Workspace) lexicalRoot(p string) (string, bool) {
	clean := filepath.Clean(p)
	if !filepath.IsAbs(clean) {
		primary := w.primaryRoot()
		if primary == "" {
			return "", false
		}
		return primary, true
	}
	for _, root := range w.Roots() {
		rel, err := filepath.Rel(root, clean)
		if err == nil && !escapesRoot(rel) {
			return root, true
		}
	}
	return "", false
}

// IsEmpty reports whether the workspace has no root at all.
func (w Workspace) IsEmpty() bool { return w.primaryRoot() == "" }

// primaryRoot is the resolved primary root ("" for a zero Workspace).
func (w Workspace) primaryRoot() string {
	if w.Primary == "" {
		return ""
	}
	return resolveRoot(w.Primary)
}

// resolve maps requestedPath onto the root that owns it and the name inside
// that root. A relative path always resolves against the primary root; an
// absolute path is accepted when it is inside any allowed root, in the order the
// user added them. The name handed back keeps its final component as spelled
// (see relPathInRoot), so delete and rename address a symlink itself.
func (w Workspace) resolve(requestedPath string, ig *IgnoreManager) (root, rel string, err error) {
	if requestedPath == "" {
		return "", "", fmt.Errorf("empty path")
	}
	primary := w.primaryRoot()
	if primary == "" {
		return "", "", fmt.Errorf("no workspace root")
	}

	clean := filepath.Clean(requestedPath)
	if !filepath.IsAbs(clean) {
		if escapesRoot(clean) {
			return "", "", fmt.Errorf("access denied: path traversal not allowed")
		}
		root, rel = primary, clean
	} else {
		found := false
		for _, candidate := range w.Roots() {
			r, rerr := relPathInRoot(requestedPath, candidate)
			if rerr != nil {
				continue
			}
			root, rel, found = candidate, r, true
			break
		}
		if !found {
			return "", "", fmt.Errorf("access denied: %s is outside the workspace (%s)",
				requestedPath, strings.Join(w.Roots(), ", "))
		}
	}

	if ig != nil && ig.isIgnored(filepath.Join(root, rel), root) {
		return "", "", fmt.Errorf("access denied: %s is listed in an ignore file", requestedPath)
	}
	return root, rel, nil
}

// openRoot resolves requestedPath and opens the owning root. The caller closes
// the returned *os.Root; rootDir is the absolute root it was opened on and rel
// the name inside it.
func (w Workspace) openRoot(requestedPath string, ig *IgnoreManager) (r *os.Root, rootDir, rel string, err error) {
	rootDir, rel, err = w.resolve(requestedPath, ig)
	if err != nil {
		return nil, "", "", err
	}
	r, err = os.OpenRoot(rootDir)
	if err != nil {
		return nil, "", "", fmt.Errorf("error opening root: %w", err)
	}
	return r, rootDir, rel, nil
}

// realPath resolves requestedPath and then every symlink component of it,
// refusing anything that lands outside the owning root. Tools that hand an
// absolute path back to the plain OS (fastwalk, LSP servers, git) need it.
func (w Workspace) realPath(requestedPath string) (string, error) {
	root, rel, err := w.resolve(requestedPath, nil)
	if err != nil {
		return "", err
	}
	return realPathWithinRoot(filepath.Join(root, rel), root)
}

// bindPath resolves args[key] against the workspace and returns a copy of args
// whose value is rewritten as a name inside the owning root, plus that root.
// Tools that take a single path keep their single-root implementation and are
// rebinding-proof: a missing or empty argument is left to the tool, which
// reports it (or applies its own default) exactly as before.
func bindPath(args map[string]any, key string, ws Workspace, ig *IgnoreManager) (map[string]any, string, error) {
	value, ok := args[key].(string)
	if !ok || value == "" {
		return args, ws.primaryRoot(), nil
	}
	root, rel, err := ws.resolve(value, ig)
	if err != nil {
		return args, "", err
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	out[key] = rel
	return out, root, nil
}

// gitWorkspaceRoot picks the root a git call runs in: the one owning the first
// explicit path argument, otherwise the primary root. Repository-wide
// operations on every root are handled by gitReadOnWorkspace.
func gitWorkspaceRoot(args map[string]any, ws Workspace) string {
	if p, _ := args["path"].(string); strings.TrimSpace(p) != "" {
		if root, _, err := ws.resolve(p, nil); err == nil {
			return root
		}
	}
	for _, p := range extractStringList(args, "paths") {
		if strings.TrimSpace(p) == "" {
			continue
		}
		if root, _, err := ws.resolve(p, nil); err == nil {
			return root
		}
	}
	return ws.primaryRoot()
}

// validateRelInRoot validates a name that was derived from an already resolved
// path (rename's new_name) and must stay inside the same root.
func validateRelInRoot(rel, root string, ig *IgnoreManager) (string, error) {
	clean := filepath.Clean(rel)
	if escapesRoot(clean) {
		return "", fmt.Errorf("access denied: path traversal not allowed")
	}
	if ig != nil && ig.isIgnored(filepath.Join(root, clean), root) {
		return "", fmt.Errorf("access denied: %s is listed in an ignore file", rel)
	}
	return clean, nil
}
