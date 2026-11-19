package config

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"fuckyourcli/plugins"
	"fuckyourcli/types"
)

func TestSessionPluginMCPConfiguration(t *testing.T) {
	old := config
	t.Cleanup(func() { config = old })
	config = defaults()
	t.Setenv("FYC_CONFIG_DIR", t.TempDir())
	t.Setenv("FYC_AUTH_FILE", filepath.Join(t.TempDir(), "auth.json"))
	t.Setenv("SESSION_PLUGIN_TOKEN", "")
	workspace, source := t.TempDir(), t.TempDir()
	manifest := `{"$schema":"` + plugins.PluginSchema + `","name":"mcp-demo","version":"1"}`
	mcp := `{"$schema":"` + plugins.MCPSchema + `","mcpServers":{"docs":{"type":"streamable-http","url":"https://example.com/mcp","headers":{"Authorization":"Bearer ${SESSION_PLUGIN_TOKEN}"}}}}`
	for name, body := range map[string]string{"plugin.json": manifest, "mcp.json": mcp} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := plugins.Prepare(context.Background(), source, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.Install(p, workspace, false, false); err != nil {
		t.Fatal(err)
	}
	if list := SessionMCPServers(workspace); len(list) != 0 {
		t.Fatalf("started plugin without credentials: %+v", list)
	}
	if err := SavePluginSecret("SESSION_PLUGIN_TOKEN", "stored-token"); err != nil {
		t.Fatal(err)
	}
	list := SessionMCPServers(workspace)
	if len(list) != 1 || list[0].Name != "mcp-demo--docs" || list[0].Transport != "streamableHttp" || list[0].Headers["Authorization"] != "Bearer stored-token" {
		t.Fatalf("plugin MCP configuration: %+v", list)
	}
	if len(Get().MCPServers) != 0 {
		t.Fatal("plugin servers leaked into persisted config")
	}
	config.MCPServers = []types.MCPServerConfig{{Name: "mcp-demo--docs", Command: "explicit", Disabled: true}}
	list = SessionMCPServers(workspace)
	if len(list) != 1 || list[0].Command != "explicit" || !list[0].Disabled {
		t.Fatalf("explicit MCP override lost: %+v", list)
	}
}
