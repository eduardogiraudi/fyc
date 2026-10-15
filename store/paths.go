// Package store is the shared persistence foundation for fuckyourcli.
//
// It is the single source of truth for the on-disk locations used by the
// application. It writes files atomically (temp file + fsync + rename), reads
// versioned JSON envelopes through a migration chain with a ".bak" recovery
// path, and serializes concurrent writers with advisory file locks.
//
// Errors are returned, never swallowed: callers decide whether a failure is
// fatal, worth a notice (see SetLogger), or recoverable. The package has no
// dependencies beyond the standard library and golang.org/x/sys (advisory
// locks on Windows).
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// AppName is the on-disk namespace shared by every persisted surface.
const AppName = "fyc"

// Directory and file permission modes. Directories that may hold secrets are
// restricted to the owner (0700), files to 0600.
const (
	DirPerm  os.FileMode = 0o700
	FilePerm os.FileMode = 0o600
)

// ConfigDir returns the directory holding configuration, skills and user prompts:
// ~/.config/fyc on Linux (honouring $XDG_CONFIG_HOME),
// ~/Library/Application Support/fyc on macOS, and %APPDATA%\fyc on Windows.
// The FYC_CONFIG_DIR environment variable overrides the computed location.
func ConfigDir() string {
	if dir := os.Getenv("FYC_CONFIG_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, AppName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", AppName)
	}
	return AppName
}

// CacheDir returns the application-wide cache directory:
// ${XDG_CACHE_HOME:-~/.cache}/fyc on Linux.
// The FYC_CACHE_DIR environment variable overrides the computed location.
func CacheDir() string {
	if dir := os.Getenv("FYC_CACHE_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, AppName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", AppName)
	}
	return filepath.Join(".cache", AppName)
}

// WorkspaceKey is the stable 16-hex-character identifier of a workspace
// directory, derived from its absolute, cleaned path. It is byte-for-byte
// compatible with the historical chat-history directory name so previously
// saved chats keep being found after the migration.
func WorkspaceKey(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = filepath.Clean(dir)
	}
	sum := sha256.Sum256([]byte(abs))
	return hex.EncodeToString(sum[:])[:16]
}

// WorkspaceDir returns the per-workspace cache directory for dir.
func WorkspaceDir(dir string) string {
	return filepath.Join(CacheDir(), WorkspaceKey(dir))
}

// CurrentWorkspaceDir returns the per-workspace cache directory of the process
// working directory.
func CurrentWorkspaceDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return WorkspaceDir(cwd), nil
}

// EnsureDir creates dir (and its parents) with perm when it does not exist and
// returns nil when it already is a directory. perm defaults to DirPerm when
// zero. An existing directory keeps its current permissions: they are never
// silently loosened or tightened.
func EnsureDir(dir string, perm os.FileMode) error {
	info, err := os.Stat(dir)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("store: %s exists and is not a directory", dir)
		}
		return nil
	case os.IsNotExist(err):
		if perm == 0 {
			perm = DirPerm
		}
		if err := os.MkdirAll(dir, perm); err != nil {
			return fmt.Errorf("store: mkdir %s: %w", dir, err)
		}
		// MkdirAll is subject to the process umask; make the leaf explicit.
		return os.Chmod(dir, perm)
	default:
		return err
	}
}
