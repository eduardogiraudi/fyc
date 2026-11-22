package store

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolvePath resolves the symlinks of the deepest existing ancestor of p and
// re-appends the components that do not exist yet, so a path can be prepared
// before its parent directories are created. The result is always absolute.
//
// A path that exists but does not resolve (a dangling or looping symlink) is an
// error: writing through it would land wherever it points, not where the caller
// checked. Reachability matters because macOS spellings of the same directory
// differ — $TMPDIR and /tmp live under /var → /private/var — and comparing a
// resolved path with an unresolved one never matches.
func ResolvePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	// Always absolute: a relative answer would compare unequal to the absolute
	// paths it is checked against (and "." would mean the workspace root).
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	rest := ""
	cur := abs
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			return filepath.Join(resolved, rest), nil
		}
		if _, lerr := os.Lstat(cur); lerr == nil {
			return "", fmt.Errorf("cannot resolve %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", err
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}
