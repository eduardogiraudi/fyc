package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/plugins"
)

func TestInstalledPluginSkillDiscoveryAndReads(t *testing.T) {
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	workspace, source := t.TempDir(), t.TempDir()
	files := map[string]string{
		"plugin.json":                      `{"$schema":"` + plugins.PluginSchema + `","name":"skill-demo","version":"1.0.0"}`,
		"skills/check/SKILL.md":            "---\nname: check\ndescription: Check this project\n---\nRead references/rules.md before checking.\n",
		"skills/check/references/rules.md": "Plugin rules",
	}
	for name, data := range files {
		p := filepath.Join(source, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := plugins.Prepare(context.Background(), source, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install(p, workspace, true, false); err != nil {
		t.Fatal(err)
	}
	name := "skill-demo--check"
	if desc := DiscoverSkills(workspace)[name]; desc != "Check this project" {
		t.Fatalf("skill not discovered: %q", desc)
	}
	if p := FindSkillPath(name, workspace); p == "" || filepath.Base(p) != "SKILL.md" {
		t.Fatalf("skill path: %q", p)
	}
	if result := executeReadSkillTool(map[string]any{"name": name}, workspace); !strings.Contains(result, "Read references/rules.md") {
		t.Fatalf("skill instructions: %s", result)
	}
	if result := executeReadSkillTool(map[string]any{"name": name, "file": "references/rules.md"}, workspace); !strings.Contains(result, "Plugin rules") {
		t.Fatalf("supporting reference: %s", result)
	}
	if result := executeReadSkillTool(map[string]any{"name": name, "file": "../../../plugin.json"}, workspace); !strings.Contains(result, "error") {
		t.Fatalf("escaped skill directory: %s", result)
	}
}
