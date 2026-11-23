package config

import (
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/store"
)

func TestNormalizeRootsDropsNestedAndDuplicates(t *testing.T) {
	primary := t.TempDir()
	nested := filepath.Join(primary, "sub")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	roots := NormalizeRoots(primary, []string{nested, other, other})
	if len(roots) != 2 || roots[0] != ResolveDir(primary) || roots[1] != ResolveDir(other) {
		t.Fatalf("roots = %v, want the primary and the unrelated root", roots)
	}
}

func TestAddableRootRefusesBroadAndSensitiveFolders(t *testing.T) {
	cfgDir := withConfigDir(t)
	if _, err := AddableRoot(string(filepath.Separator)); err == nil {
		t.Fatal("the filesystem root was accepted")
	}
	if _, err := AddableRoot(cfgDir); err == nil {
		t.Fatal("the configuration directory was accepted")
	}
	home := t.TempDir()
	setHomeDir(t, home)
	if _, err := AddableRoot(home); err == nil {
		t.Fatal("the whole home directory was accepted")
	}
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := AddableRoot(project); err != nil || got != ResolveDir(project) {
		t.Fatalf("AddableRoot(project) = %q, %v", got, err)
	}
}

// setHomeDir points the variable os.UserHomeDir reads at dir: %USERPROFILE% on
// Windows, $HOME everywhere else. Setting only HOME silently does nothing there,
// and a test that then expects the home check to fire would pass for the wrong
// reason (or fail on the next assertion).
func setHomeDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

// A folder that contains the home directory or the configuration directory is
// as dangerous as the folder itself: /home or ~/.config still expose ~/.ssh and
// the API keys in auth.json.
func TestAddableRootRefusesFoldersThatContainHomeOrConfig(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "user")
	cfgDir := filepath.Join(home, ".config", "fyc")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "auth.json"), []byte(`{"OPENAI_API_KEY":"sk-test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	setHomeDir(t, home)
	t.Setenv("FYC_CONFIG_DIR", cfgDir)

	for _, p := range []string{home, cfgDir, filepath.Join(home, ".config"), base} {
		if got, err := AddableRoot(p); err == nil {
			t.Errorf("AddableRoot(%q) = %q, want a refusal: it contains the home or the config", p, got)
		}
	}
	// A project folder inside the home directory is still allowed: that is the
	// whole point of /cd.
	project := filepath.Join(home, "code", "app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := AddableRoot(project); err != nil {
		t.Errorf("AddableRoot(project) = %v, want it allowed", err)
	} else if got != ResolveDir(project) {
		t.Errorf("AddableRoot(project) = %q", got)
	}
}

func TestSavedWorkspacesRoundTrip(t *testing.T) {
	withConfigDir(t)
	primary, other := t.TempDir(), t.TempDir()

	entry := UpsertSavedWorkspace("", []string{primary, other})
	if entry.Name == "" || len(entry.Roots) != 2 {
		t.Fatalf("entry = %+v", entry)
	}
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}

	config = defaults()
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	groups := WorkspacesContaining(filepath.Join(primary, "nested"))
	if len(groups) != 1 || len(groups[0].Roots) != 2 {
		t.Fatalf("WorkspacesContaining = %+v", groups)
	}
	// A folder outside every saved root matches nothing.
	if groups := WorkspacesContaining(t.TempDir()); len(groups) != 0 {
		t.Fatalf("unrelated folder matched %+v", groups)
	}
	if !DeleteSavedWorkspace(entry.Name) {
		t.Fatal("DeleteSavedWorkspace reported no removal")
	}
	if len(SavedWorkspaces()) != 0 {
		t.Fatalf("a deleted workspace is still listed: %+v", SavedWorkspaces())
	}
}

func TestUpsertReplacesTheSamePrimaryRoot(t *testing.T) {
	withConfigDir(t)
	primary, a, b := t.TempDir(), t.TempDir(), t.TempDir()

	UpsertSavedWorkspace("first", []string{primary, a})
	UpsertSavedWorkspace("second", []string{primary, b})
	groups := SavedWorkspaces()
	if len(groups) != 1 {
		t.Fatalf("groups = %+v, want the same primary root to replace the entry", groups)
	}
	if groups[0].Name != "second" || len(groups[0].Roots) != 2 || groups[0].Roots[1] != ResolveDir(b) {
		t.Fatalf("groups[0] = %+v", groups[0])
	}
}

func TestSavedWorkspacesMigrationFrom16(t *testing.T) {
	dir := withConfigDir(t)
	path := filepath.Join(dir, configFileName)
	if err := store.WriteFile(path, []byte(`{"schema":16,"provider":"openai_compatible","model":"test"}`), store.FilePerm); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.Schema != CurrentSchemaVersion || len(config.Workspaces) != 0 {
		t.Fatalf("config = %+v, want schema %d and no workspace", config, CurrentSchemaVersion)
	}
}
