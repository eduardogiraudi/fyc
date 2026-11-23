package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"fuckyourcli/store"
)

// maxSavedWorkspaces caps the remembered root sets: the least recently used
// entries are pruned so config.json cannot grow without bound.
const maxSavedWorkspaces = 50

// SavedWorkspace is a set of roots the user allowed together and asked to keep
// (see /cd). Roots[0] is the workspace it was created from; every entry is
// absolute and symlink-resolved.
type SavedWorkspace struct {
	Name      string    `json:"name"`
	Roots     []string  `json:"roots"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// ResolveDir spells dir the way store.ResolvePath does, so two spellings of the
// same directory (macOS /var → /private/var, a symlinked project) compare equal.
func ResolveDir(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	if resolved, err := store.ResolvePath(abs); err == nil {
		return resolved
	}
	return abs
}

// NormalizeRoots makes every root absolute and symlink-resolved, drops
// duplicates and roots nested inside another allowed one: a nested root grants
// nothing extra and only duplicates the sandbox binds, the AGENTS.md chain and
// the undo journal.
func NormalizeRoots(primary string, extra []string) []string {
	out := make([]string, 0, len(extra)+1)
	if p := ResolveDir(primary); p != "" {
		out = append(out, p)
	}
	for _, r := range extra {
		root := ResolveDir(r)
		if root == "" || insideAny(out, root) {
			continue
		}
		out = append(out, root)
	}
	return out
}

// containsPath reports whether parent is child itself or one of its ancestors.
func containsPath(parent, child string) bool {
	return child == parent || strings.HasPrefix(child, parent+string(filepath.Separator))
}

// insideAny reports whether dir is one of roots or lies inside one of them.
func insideAny(roots []string, dir string) bool {
	for _, root := range roots {
		if dir == root || strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AddableRoot validates a directory the user wants to allow and returns its
// resolved spelling. The filesystem root, the user's home and fyc's own config
// directory are refused: allowing them would expose every secret on the machine
// (or hand the model the very store that holds the API keys) in one keystroke.
func AddableRoot(dir string) (string, error) {
	root := ResolveDir(dir)
	if root == "" {
		return "", fmt.Errorf("empty path")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("%s: %w", dir, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}
	// The filesystem root is the volume's own root: on Windows it is spelled
	// `C:\` (or a UNC share root), never a bare separator, so comparing with
	// string(filepath.Separator) alone would accept the whole drive.
	if root == string(filepath.Separator) || root == filepath.VolumeName(root)+string(filepath.Separator) {
		return "", fmt.Errorf("refusing to allow the filesystem root")
	}
	// A folder that CONTAINS the home directory, or fyc's configuration
	// directory, grants them just as well as allowing them directly: through
	// /home or the parent of ~/.config the model would still read ~/.ssh and the
	// API keys in auth.json. Both directions are refused, so neither a parent nor
	// the folder itself can slip through.
	if home, herr := os.UserHomeDir(); herr == nil && home != "" {
		if resolved := ResolveDir(home); resolved != "" && containsPath(root, resolved) {
			return "", fmt.Errorf("refusing to allow %s: it contains the whole home directory (%s); allow the project folders inside it instead", root, resolved)
		}
	}
	cfgDir := ResolveDir(store.ConfigDir())
	if cfgDir != "" && (containsPath(root, cfgDir) || containsPath(cfgDir, root)) {
		return "", fmt.Errorf("refusing to allow %s: it contains fyc's configuration (%s), which holds the API keys", root, cfgDir)
	}
	return root, nil
}

// SavedWorkspaces returns the remembered root sets, most recently used first.
func SavedWorkspaces() []SavedWorkspace {
	return append([]SavedWorkspace(nil), config.Workspaces...)
}

// WorkspacesContaining lists the saved workspaces that allow dir: dir itself or
// a directory inside one of their roots.
func WorkspacesContaining(dir string) []SavedWorkspace {
	resolved := ResolveDir(dir)
	if resolved == "" {
		return nil
	}
	var out []SavedWorkspace
	for _, w := range config.Workspaces {
		if insideAny(w.Roots, resolved) || hasRootContaining(w.Roots, resolved) {
			out = append(out, w)
		}
	}
	return out
}

// hasRootContaining reports whether one of roots is an ancestor of dir.
func hasRootContaining(roots []string, dir string) bool {
	for _, root := range roots {
		if strings.HasPrefix(dir, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// UpsertSavedWorkspace remembers roots under name, replacing an entry with the
// same name, the same primary root or the same root set.
func UpsertSavedWorkspace(name string, roots []string) SavedWorkspace {
	if len(roots) == 0 {
		return SavedWorkspace{}
	}
	normalized := NormalizeRoots(roots[0], roots[1:])
	if len(normalized) == 0 {
		return SavedWorkspace{}
	}
	if strings.TrimSpace(name) == "" {
		name = DerivedWorkspaceName(normalized)
	}
	entry := SavedWorkspace{Name: name, Roots: normalized, UpdatedAt: time.Now().UTC()}

	next := make([]SavedWorkspace, 0, len(config.Workspaces)+1)
	replaced := false
	for _, w := range config.Workspaces {
		samePrimary := len(w.Roots) > 0 && ResolveDir(w.Roots[0]) == normalized[0]
		if w.Name == name || samePrimary || sameRootSet(w.Roots, normalized) {
			if !replaced {
				next = append(next, entry)
				replaced = true
			}
			continue
		}
		next = append(next, w)
	}
	if !replaced {
		next = append(next, entry)
	}
	// Most recently used first, then the cap.
	sort.SliceStable(next, func(i, j int) bool { return next[i].UpdatedAt.After(next[j].UpdatedAt) })
	if len(next) > maxSavedWorkspaces {
		next = next[:maxSavedWorkspaces]
	}
	config.Workspaces = next
	return entry
}

// DeleteSavedWorkspace forgets the workspace called name. It reports whether an
// entry was removed.
func DeleteSavedWorkspace(name string) bool {
	next := make([]SavedWorkspace, 0, len(config.Workspaces))
	removed := false
	for _, w := range config.Workspaces {
		if w.Name == name {
			removed = true
			continue
		}
		next = append(next, w)
	}
	if removed {
		if len(next) == 0 {
			next = nil
		}
		config.Workspaces = next
	}
	return removed
}

// DerivedWorkspaceName is the default name of a saved workspace: the base name
// of its first root, made unique against the other entries.
func DerivedWorkspaceName(roots []string) string {
	base := "workspace"
	if len(roots) > 0 {
		if b := filepath.Base(roots[0]); b != "" && b != string(filepath.Separator) {
			base = b
		}
	}
	taken := func(name string) bool {
		for _, w := range config.Workspaces {
			if w.Name == name {
				return true
			}
		}
		return false
	}
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken(candidate) {
			return candidate
		}
	}
}

// sameRootSet reports whether a and b name the same directories.
func sameRootSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if ResolveDir(a[i]) != ResolveDir(b[i]) {
			return false
		}
	}
	return true
}
