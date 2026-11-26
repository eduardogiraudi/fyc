package plugins

import (
	"fmt"
	"os"
	"path/filepath"

	"fuckyourcli/store"
)

type Entry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Workspace string `json:"workspace,omitempty"`
	Digest    string `json:"sha256"`
}

type registry struct {
	Schema  int     `json:"schema"`
	Plugins []Entry `json:"plugins"`
}

type Installed struct {
	Package *Package
	Entry   Entry
}

func registryPath() string { return filepath.Join(store.ConfigDir(), "plugins.json") }

func readRegistry() (registry, error) {
	r := registry{Schema: 1}
	if err := store.ReadJSON(registryPath(), &r); err != nil && !os.IsNotExist(err) {
		return r, err
	}
	if r.Schema != 1 {
		return r, fmt.Errorf("unsupported plugins registry schema %d", r.Schema)
	}
	return r, nil
}

// resolveWorkspace spells a workspace the way the session does, with symlinks
// resolved, so an install and a later session compare equal even when the user
// reaches the same checkout through a symlinked path.
func resolveWorkspace(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return filepath.Clean(dir)
	}
	if resolved, err := store.ResolvePath(abs); err == nil {
		return resolved
	}
	return abs
}

// sameScope compares two registry scopes: "" is the global one, anything else
// is a workspace and matches by resolved path, so a legacy entry stored under
// a different spelling of the same directory is replaced, not duplicated.
func sameScope(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return store.SamePath(a, b)
}

func Destination(workspace string, global bool, name string) (string, error) {
	if !validID(name) {
		return "", fmt.Errorf("invalid plugin name")
	}
	base := store.ConfigDir()
	if !global {
		base = filepath.Join(resolveWorkspace(workspace), ".fyc")
	}
	return filepath.Abs(filepath.Join(base, "plugins", name))
}

// Install publishes a complete directory and records approval outside the repo.
// A cloned repo cannot approve its own executable MCP configuration.
func Install(p *Package, workspace string, global, replace bool) (string, error) {
	validated, err := parse(p.Files)
	if err != nil {
		return "", err
	}
	p = validated
	workspace = resolveWorkspace(workspace)
	dest, err := Destination(workspace, global, p.Name)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(dest)
	// Check existing ancestors before MkdirAll can follow a scope-escaping link.
	ancestors := []string{parent}
	if !global {
		ancestors = append(ancestors, filepath.Dir(parent))
	}
	for _, ancestor := range ancestors {
		if info, err := os.Lstat(ancestor); err == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("plugin destination contains a symlink or non-directory")
			}
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	if err := store.EnsureDir(parent, store.DirPerm); err != nil {
		return "", err
	}
	lock, err := store.Lock(registryPath() + ".lock")
	if err != nil {
		return "", err
	}
	defer lock.Unlock()
	reg, err := readRegistry()
	if err != nil {
		return "", err
	}
	if info, err := os.Lstat(dest); err == nil {
		if !replace {
			return "", fmt.Errorf("plugin %s already exists; use --replace to review and replace it", p.Name)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("plugin destination is not a regular directory")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	stage, err := os.MkdirTemp(parent, ".install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	for _, name := range sortedKeys(p.Files) {
		f := p.Files[name]
		target := filepath.Join(stage, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), store.DirPerm); err != nil {
			return "", err
		}
		perm := store.FilePerm
		if f.Executable {
			perm = 0o700
		}
		if err := store.WriteFile(target, f.Data, perm); err != nil {
			return "", err
		}
	}
	// Keep the old install intact if publication of the approval record fails.
	backup := stage + ".old"
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, backup); err != nil {
			return "", err
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		_ = os.Rename(backup, dest)
		return "", err
	}
	scope := workspace
	if global {
		scope = ""
	}
	entry := Entry{Name: p.Name, Path: dest, Workspace: scope, Digest: digest(p.Files)}
	next := make([]Entry, 0, len(reg.Plugins)+1)
	for _, old := range reg.Plugins {
		if old.Name != entry.Name || !sameScope(old.Workspace, entry.Workspace) {
			next = append(next, old)
		}
	}
	reg.Plugins = append(next, entry)
	if err := store.WriteJSON(registryPath(), reg, store.FilePerm, false); err != nil {
		_ = os.RemoveAll(dest)
		_ = os.Rename(backup, dest)
		return "", err
	}
	_ = os.RemoveAll(backup)
	return dest, nil
}

// Load validates approved bytes before exposing skills or executable servers.
// Project installations override a global plugin with the same name.
func Load(workspace string) []Installed {
	workspace = resolveWorkspace(workspace)
	reg, err := readRegistry()
	if err != nil {
		store.Notice("plugins: %v", err)
		return nil
	}
	selected := map[string]Entry{}
	for _, e := range reg.Plugins {
		if e.Workspace == "" {
			selected[e.Name] = e
		}
	}
	for _, e := range reg.Plugins {
		if sameScope(e.Workspace, workspace) {
			selected[e.Name] = e
		}
	}
	var out []Installed
	for _, name := range sortedKeys(selected) {
		e := selected[name]
		files, err := snapshot(e.Path, false)
		if err != nil || digest(files) != e.Digest {
			store.Notice("plugins: %s changed or is unreadable; run add --replace to review it again", name)
			continue
		}
		p, err := parse(files)
		if err != nil || p.Name != e.Name {
			store.Notice("plugins: invalid installed plugin %s", name)
			continue
		}
		out = append(out, Installed{p, e})
	}
	return out
}

func (i Installed) DataDir() string {
	scope := "global"
	if i.Entry.Workspace != "" {
		scope = store.WorkspaceKey(i.Entry.Workspace)
	}
	return filepath.Join(store.ConfigDir(), "plugin-data", scope, i.Entry.Name)
}
