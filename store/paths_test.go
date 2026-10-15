package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestConfigDirHonoursOverride(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", filepath.Join("tmp", "cfg"))
	if got := ConfigDir(); got != filepath.Join("tmp", "cfg") {
		t.Fatalf("ConfigDir() = %q, want overridden value", got)
	}
}

func TestCacheDirHonoursOverride(t *testing.T) {
	t.Setenv("FYC_CACHE_DIR", filepath.Join("tmp", "cache"))
	if got := CacheDir(); got != filepath.Join("tmp", "cache") {
		t.Fatalf("CacheDir() = %q, want overridden value", got)
	}
}

func TestDefaultDirsUseAppName(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", "")
	t.Setenv("FYC_CACHE_DIR", "")
	if AppName != "fyc" {
		t.Fatalf("AppName = %q, want fyc", AppName)
	}
	if got := ConfigDir(); filepath.Base(got) != AppName {
		t.Fatalf("ConfigDir() base = %q, want %q", filepath.Base(got), AppName)
	}
	if got := CacheDir(); filepath.Base(got) != AppName {
		t.Fatalf("CacheDir() base = %q, want %q", filepath.Base(got), AppName)
	}
}

func TestConfigDirUsesPlatformLocation(t *testing.T) {
	home, appData, xdg := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AppData", appData)
	t.Setenv("XDG_CONFIG_HOME", xdg)
	var want string
	switch runtime.GOOS {
	case "windows":
		want = filepath.Join(appData, AppName)
	case "darwin", "ios":
		want = filepath.Join(home, "Library", "Application Support", AppName)
	case "plan9":
		t.Setenv("home", home)
		want = filepath.Join(home, "lib", AppName)
	default:
		want = filepath.Join(xdg, AppName)
	}
	if got := ConfigDir(); got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDirFallsBackWhenXDGConfigHomeIsRelative(t *testing.T) {
	switch runtime.GOOS {
	case "windows", "darwin", "ios", "plan9":
		t.Skip("XDG_CONFIG_HOME is only used on Unix outside macOS")
	}
	home := t.TempDir()
	t.Setenv("FYC_CONFIG_DIR", "")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "relative/config")
	if _, err := os.UserConfigDir(); err == nil {
		t.Fatal("os.UserConfigDir should reject a relative XDG_CONFIG_HOME")
	}
	if got, want := ConfigDir(), filepath.Join(home, ".config", AppName); got != want {
		t.Fatalf("ConfigDir() = %q, want fallback %q", got, want)
	}
}

func TestWorkspaceKeyProperties(t *testing.T) {
	key := WorkspaceKey("/home/eduardo/fyc")
	if len(key) != 16 {
		t.Fatalf("key %q has length %d, want 16", key, len(key))
	}
	for _, r := range key {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Fatalf("key %q is not lowercase hex", key)
		}
	}
	if again := WorkspaceKey("/home/eduardo/fyc"); again != key {
		t.Fatalf("key is not stable: %q vs %q", again, key)
	}
	if other := WorkspaceKey("/home/eduardo/other"); other == key {
		t.Fatalf("distinct paths produced the same key %q", key)
	}
}

// TestWorkspaceKeyMatchesLegacy locks the derivation used by the old
// helpers.getChatHistoryDir so existing chat.json files are still found.
func TestWorkspaceKeyMatchesLegacy(t *testing.T) {
	dir := "/some/project"
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(abs))
	want := hex.EncodeToString(sum[:])[:16]
	if got := WorkspaceKey(dir); got != want {
		t.Fatalf("WorkspaceKey(%q) = %q, want legacy %q", dir, got, want)
	}
}

func TestWorkspaceDirLayout(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "cache")
	t.Setenv("FYC_CACHE_DIR", cache)
	got := WorkspaceDir("/data/proj")
	want := filepath.Join(cache, WorkspaceKey("/data/proj"))
	if got != want {
		t.Fatalf("WorkspaceDir() = %q, want %q", got, want)
	}
}

func TestEnsureDirCreatesAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir, DirPerm); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("%s is not a directory", dir)
	}
	// Windows ignores POSIX mode bits (os.Stat reports 0777 for directories),
	// so only assert the mode on platforms that actually honour it.
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != DirPerm {
			t.Fatalf("perm = %o, want %o", perm, DirPerm)
		}
	}
	if err := EnsureDir(dir, DirPerm); err != nil {
		t.Fatalf("second EnsureDir: %v", err)
	}
}

func TestEnsureDirRejectsFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDir(file, DirPerm); err == nil {
		t.Fatal("EnsureDir on a regular file should fail")
	}
}

func TestEnsureDirDefaultPerm(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "default")
	if err := EnsureDir(dir, 0); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != DirPerm {
			t.Fatalf("perm = %o, want default %o", perm, DirPerm)
		}
	}
}
