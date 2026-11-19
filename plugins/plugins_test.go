package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func fixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"plugin.json":                        `{"$schema":"` + PluginSchema + `","name":"demo","version":"1.0.0","description":"Demo"}`,
		"skills/review/SKILL.md":             "---\nname: review\ndescription: >-\n  Review code\n  safely\n---\nRead references/policy.md first.\n",
		"skills/review/references/policy.md": "Policy",
		"mcp.json":                           `{"$schema":"` + MCPSchema + `","mcpServers":{"docs":{"type":"streamable-http","url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${DEMO_TOKEN}"}},"local":{"type":"stdio","command":"./scripts/server","args":["${PLUGIN_ROOT}/data"],"cwd":"./scripts","env":{"MODE":"read"}}}}`,
		"scripts/server":                     "#!/bin/sh\nexit 1\n",
	}
	for name, data := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func prepareFixture(t *testing.T) *Package {
	t.Helper()
	p, err := Prepare(context.Background(), fixture(t), "", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPrepareAndMCPTranslation(t *testing.T) {
	p := prepareFixture(t)
	if p.Name != "demo" || len(p.Skills) != 1 || p.Skills[0].Name != "demo--review" || p.Skills[0].Description != "Review code safely" {
		t.Fatalf("unexpected package: %+v", p)
	}
	root, data := t.TempDir(), t.TempDir()
	servers, err := p.MCPServers(root, data, func(v string) (string, error) {
		return strings.ReplaceAll(v, "${DEMO_TOKEN}", "token"), nil
	})
	if err != nil || len(servers) != 2 {
		t.Fatalf("servers: %v %v", servers, err)
	}
	if servers[0].Transport != "streamableHttp" || servers[0].Headers["Authorization"] != "Bearer token" {
		t.Fatalf("HTTP translation: %+v", servers[0])
	}
	local := servers[1]
	if local.Command != filepath.Join(root, "scripts/server") || local.WorkingDirectory != filepath.Join(root, "scripts") || local.Env["PLUGIN_DATA"] != data || local.Args[0] != root+"/data" {
		t.Fatalf("stdio translation: %+v", local)
	}
	b, err := json.Marshal(local)
	if err != nil || bytes.Contains(b, []byte("WorkingDirectory")) || bytes.Contains(b, []byte("working_directory")) {
		t.Fatalf("runtime cwd leaked into JSON: %s %v", b, err)
	}
}

func TestInstallScopeSnapshotReplacementAndTampering(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	workspace, other := t.TempDir(), t.TempDir()
	source := fixture(t)
	p, err := Prepare(context.Background(), source, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// Changes after preview do not change the bytes that get installed.
	if err := os.WriteFile(filepath.Join(source, "scripts/server"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	dest, err := Install(p, workspace, false, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "scripts/server"))
	if err != nil || string(got) == "changed" {
		t.Fatalf("snapshot not installed: %q %v", got, err)
	}
	if len(Load(workspace)) != 1 || len(Load(other)) != 0 {
		t.Fatal("project plugin leaked across workspaces")
	}
	if _, err := Install(p, workspace, false, false); err == nil {
		t.Fatal("overwrote plugin without --replace")
	}
	if err := os.WriteFile(filepath.Join(dest, "scripts/server"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(Load(workspace)) != 0 {
		t.Fatal("tampered executable remained enabled")
	}
	if _, err := Install(p, workspace, false, true); err != nil {
		t.Fatal(err)
	}
	if len(Load(workspace)) != 1 {
		t.Fatal("replacement did not renew approval")
	}
	if _, err := Install(p, other, true, false); err != nil {
		t.Fatal(err)
	}
	if len(Load(workspace)) != 1 || len(Load(other)) != 1 {
		t.Fatal("global visibility/project override failed")
	}
	if Load(workspace)[0].Entry.Workspace != workspace || Load(other)[0].Entry.Workspace != "" {
		t.Fatal("wrong plugin precedence")
	}
	if err := os.MkdirAll(filepath.Join(dest, ".git/hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, ".git/hooks/injected"), []byte("injected"), 0o600); err != nil {
		t.Fatal(err)
	}
	if len(Load(workspace)) != 0 {
		t.Fatal("hidden executable files escaped integrity validation")
	}
}

func TestUnapprovedCloneAndSymlinks(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	workspace := t.TempDir()
	p := prepareFixture(t)
	dest, _ := Destination(workspace, false, p.Name)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dest, "plugin.json"), p.Files["plugin.json"].Data, 0o600); err != nil {
		t.Fatal(err)
	}
	if len(Load(workspace)) != 0 {
		t.Fatal("unapproved cloned plugin was loaded")
	}
	if runtime.GOOS == "windows" {
		return
	}
	other := t.TempDir()
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(other, ".fyc")); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(p, other, false, false); err == nil {
		t.Fatal("followed project scope symlink")
	}
	entries, err := os.ReadDir(escape)
	if err != nil || len(entries) != 0 {
		t.Fatalf("modified escaped directory: %v %v", entries, err)
	}
	local := fixture(t)
	if err := os.Symlink("plugin.json", filepath.Join(local, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Prepare(context.Background(), local, "", ""); err == nil {
		t.Fatal("accepted package symlink")
	}
}

func zipFixture(t *testing.T, names []string, mode os.FileMode) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, name := range names {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(mode)
		f, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("contents")); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestArchiveValidation(t *testing.T) {
	for _, names := range [][]string{{"../escape"}, {"/absolute"}, {`C:\escape`}, {"a/../../escape"}, {"a", "a"}, {"A", "a"}} {
		if _, err := unzip(zipFixture(t, names, 0o600), false); err == nil {
			t.Fatalf("accepted unsafe archive: %v", names)
		}
	}
	if _, err := unzip(zipFixture(t, []string{"link"}, os.ModeSymlink|0o777), false); err == nil {
		t.Fatal("accepted ZIP symlink")
	}
	if _, err := unzip(zipFixture(t, []string{"root/a", "other/b"}, 0o600), true); err == nil {
		t.Fatal("accepted multiple GitHub roots")
	}
	files, err := unzip(zipFixture(t, []string{"repo-ref/skills/review/SKILL.md"}, 0o700), true)
	if err != nil || !files["skills/review/SKILL.md"].Executable {
		t.Fatalf("root stripping/executable preservation: %v %v", files, err)
	}
	if _, err := readLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("accepted oversized content")
	}
}

func TestSourceAndSubdir(t *testing.T) {
	for _, source := range []string{"owner/repo", "https://github.com/owner/repo.git"} {
		got, strip, err := sourceURL(source, "feature/one")
		if err != nil || !strip || got != "https://codeload.github.com/owner/repo/zip/feature%2Fone" {
			t.Fatalf("source URL: %s %v %v", got, strip, err)
		}
	}
	for _, source := range []string{"http://example.com/a.zip", "https://user:secret@example.com/a.zip", "not-a-directory"} {
		if _, _, err := sourceURL(source, ""); err == nil {
			t.Fatalf("accepted source %q", source)
		}
	}
	if _, err := Prepare(context.Background(), fixture(t), "main", ""); err == nil {
		t.Fatal("silently ignored local --ref")
	}
	root := fixture(t)
	if _, err := Prepare(context.Background(), root, "", "../escape"); err == nil {
		t.Fatal("accepted unsafe subdir")
	}
}

func TestInvalidManifestsAndMCP(t *testing.T) {
	base := prepareFixture(t)
	for _, body := range []string{
		`{"$schema":"` + MCPSchema + `","mcpServers":{"x":{"type":"stdio","command":"x","cwd":"./../escape"}}}`,
		`{"$schema":"` + MCPSchema + `","mcpServers":{"x":{"type":"stdio","command":"x","env":{"PLUGIN_ROOT":"bad"}}}}`,
		`{"$schema":"` + MCPSchema + `","mcpServers":{"x":{"type":"streamable-http","url":"https://example.com","oauth":{}}}}`,
		`{"$schema":"` + MCPSchema + `","mcpServers":{"x":{"type":"streamable-http","url":"file:///etc/passwd"}}}`,
		`{"$schema":"wrong","mcpServers":{}}`,
	} {
		files := map[string]File{}
		for k, v := range base.Files {
			files[k] = v
		}
		files["mcp.json"] = File{Data: []byte(body)}
		if _, err := parse(files); err == nil {
			t.Fatalf("accepted invalid MCP: %s", body)
		}
	}
	a := map[string]File{"a": {Data: []byte("x")}, "b": {Data: []byte("y")}}
	b := map[string]File{"b": {Data: []byte("y")}, "a": {Data: []byte("x")}}
	if !reflect.DeepEqual(a, b) || digest(a) != digest(b) {
		t.Fatal("digest depends on map order")
	}
}
