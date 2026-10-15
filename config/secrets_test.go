package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/store"
)

// readRawConfig decodes the on-disk config.json for structural assertions.
func readRawConfig(t *testing.T, dir string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := store.ReadJSON(filepath.Join(dir, configFileName), &m); err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	return m
}

// readAuth decodes the on-disk auth.json secret store.
func readAuth(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	if err := store.ReadJSON(filepath.Join(dir, authFileName), &m); err != nil {
		if os.IsNotExist(err) {
			return m
		}
		t.Fatalf("read auth.json: %v", err)
	}
	return m
}

func TestExpandPlaceholders(t *testing.T) {
	lookup := func(name string) (string, bool) {
		src := map[string]string{"SET": "value", "EMPTY": ""}
		v, ok := src[name]
		return v, ok
	}
	cases := []struct{ in, want string }{
		{"${SET}", "value"},
		{"${MISSING}", ""},
		{"${MISSING:-fallback}", "fallback"},
		{"${EMPTY:-fallback}", "fallback"},
		{"${EMPTY}", ""},
		{"${SET:-fallback}", "value"},
		{"${MISSING:-}", ""},
		{"pre ${SET} post", "pre value post"},
		{"Bearer ${MISSING:-none}", "Bearer none"},
		{"no placeholder $5 and $VAR", "no placeholder $5 and $VAR"},
		{"${UPPER_123:-x}", "x"},
	}
	for _, c := range cases {
		if got := expandPlaceholders(c.in, lookup); got != c.want {
			t.Errorf("expandPlaceholders(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestExpandPlaceholdersNoRescan pins the documented rule: substituted text is
// never re-scanned.
func TestExpandPlaceholdersNoRescan(t *testing.T) {
	lookup := func(name string) (string, bool) { return "literal ${OTHER}", true }
	if got := expandPlaceholders("${X}", lookup); got != "literal ${OTHER}" {
		t.Fatalf("substituted value was rescanned: %q", got)
	}
}

func TestSolePlaceholder(t *testing.T) {
	if n, ok := solePlaceholder("${X}"); !ok || n != "X" {
		t.Fatalf("solePlaceholder(${X}) = %q,%v", n, ok)
	}
	if n, ok := solePlaceholder("${X:-y}"); !ok || n != "X" {
		t.Fatalf("solePlaceholder(${X:-y}) = %q,%v", n, ok)
	}
	if _, ok := solePlaceholder("a${X}"); ok {
		t.Fatal("embedded placeholder must not count as sole")
	}
	if _, ok := solePlaceholder("plain"); ok {
		t.Fatal("a literal must not count as sole")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"OpenAI":    "OPENAI",
		"My Server": "MY_SERVER",
		"x-api-key": "X_API_KEY",
		"gh!":       "GH",
		"":          "X",
		"###":       "X",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniqueVarName(t *testing.T) {
	used := map[string]bool{"FYC_X": true, "FYC_X_2": true}
	if got := uniqueVarName("FYC_X", used); got != "FYC_X_3" {
		t.Fatalf("uniqueVarName collision = %q, want FYC_X_3", got)
	}
	if got := uniqueVarName("FYC_Y", used); got != "FYC_Y" {
		t.Fatalf("uniqueVarName free = %q, want FYC_Y", got)
	}
}

// TestEnsureConfigDefenses checks the ignore files are created next to the
// config and that an existing one is never overwritten.
func TestEnsureConfigDefenses(t *testing.T) {
	dir := withConfigDir(t)
	if err := LoadE(); err != nil { // missing config.json: still seeds the ignore files
		t.Fatalf("LoadE: %v", err)
	}
	for _, name := range []string{gitignoreFileName, fycignoreFileName} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s not created: %v", name, err)
		}
		if string(data) != defaultIgnoreBody {
			t.Fatalf("%s = %q, want %q", name, data, defaultIgnoreBody)
		}
	}
	custom := []byte("# mine\nauth.json\n")
	if err := os.WriteFile(filepath.Join(dir, gitignoreFileName), custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("second LoadE: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, gitignoreFileName))
	if string(got) != string(custom) {
		t.Fatalf("existing .gitignore was modified: %q", got)
	}
}

func TestDefaultIgnoreBodyCoversSecrets(t *testing.T) {
	for _, want := range []string{authFileName, authFileName + store.BackupSuffix, configFileName + store.BackupSuffix} {
		if !strings.Contains(defaultIgnoreBody, want) {
			t.Fatalf("default ignore body misses %q: %q", want, defaultIgnoreBody)
		}
	}
}

func TestLoadResolvesSecretFromAuthJSON(t *testing.T) {
	dir := withConfigDir(t)
	cfg := `{"schema":7,"provider":"gemini","model":"m","gemini_api_key":"${FYC_TEST_KEY}"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, authFileName), []byte(`{"FYC_TEST_KEY":"from-auth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "from-auth" {
		t.Fatalf("GeminiAPIKey = %q, want from-auth", config.GeminiAPIKey)
	}
}

// TestAuthJSONWinsOverEnv pins the documented precedence: the file, not the
// environment, is the source of truth.
func TestAuthJSONWinsOverEnv(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":7,"gemini_api_key":"${FYC_TEST_KEY}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, authFileName), []byte(`{"FYC_TEST_KEY":"from-auth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYC_TEST_KEY", "from-env")
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "from-auth" {
		t.Fatalf("GeminiAPIKey = %q, want from-auth (auth.json wins)", config.GeminiAPIKey)
	}
}

func TestEnvFallbackWhenAuthMissing(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":7,"gemini_api_key":"${FYC_TEST_KEY}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYC_TEST_KEY", "from-env")
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "from-env" {
		t.Fatalf("GeminiAPIKey = %q, want from-env", config.GeminiAPIKey)
	}
}

func TestLoadPlaceholderDefault(t *testing.T) {
	dir := withConfigDir(t)
	cfg := `{"schema":7,"gemini_api_key":"${FYC_DEFINITELY_UNSET:-fallback-key}"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "fallback-key" {
		t.Fatalf("GeminiAPIKey = %q, want fallback-key", config.GeminiAPIKey)
	}
}

// TestSaveMovesInlineSecretToAuthJSON is the core migration: a plain-text key
// leaves config.json (replaced by a placeholder) and lands in auth.json, while
// the live in-memory value is left resolved.
func TestSaveMovesInlineSecretToAuthJSON(t *testing.T) {
	dir := withConfigDir(t)
	config.GeminiAPIKey = "sk-inline"
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}

	if got := readRawConfig(t, dir)["gemini_api_key"]; got != "${FYC_GEMINI_API_KEY}" {
		t.Fatalf("persisted gemini_api_key = %v, want the placeholder", got)
	}
	if got := readAuth(t, dir)["FYC_GEMINI_API_KEY"]; got != "sk-inline" {
		t.Fatalf("auth.json FYC_GEMINI_API_KEY = %q, want sk-inline", got)
	}
	if config.GeminiAPIKey != "sk-inline" {
		t.Fatalf("in-memory value was mutated: %q", config.GeminiAPIKey)
	}

	// A fresh load resolves the placeholder back to the secret.
	config = defaults()
	if err := LoadE(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if config.GeminiAPIKey != "sk-inline" {
		t.Fatalf("reloaded GeminiAPIKey = %q, want sk-inline", config.GeminiAPIKey)
	}
}

// TestSaveDoesNotPersistEnvSourcedSecret: a value that came from the
// environment through an untouched placeholder must never be copied into
// auth.json.
func TestSaveDoesNotPersistEnvSourcedSecret(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":7,"gemini_api_key":"${FYC_ENV_ONLY}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYC_ENV_ONLY", "env-secret")
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	// Nudge a save with an unrelated change.
	config.Temperature = 0.5
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}
	if _, ok := readAuth(t, dir)["FYC_ENV_ONLY"]; ok {
		t.Fatal("env-sourced secret was persisted into auth.json")
	}
	if got := readRawConfig(t, dir)["gemini_api_key"]; got != "${FYC_ENV_ONLY}" {
		t.Fatalf("placeholder not preserved: %v", got)
	}
}

// TestCodexRotationReusesPlaceholder: a value changed at runtime (token
// rotation) is persisted under the very same variable name the config already
// references, without renaming it.
func TestCodexRotationReusesPlaceholder(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":7,"codex_access_token":"${FYC_CODEX_ACCESS_TOKEN}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, authFileName), []byte(`{"FYC_CODEX_ACCESS_TOKEN":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.CodexAccessToken != "old" {
		t.Fatalf("load = %q, want old", config.CodexAccessToken)
	}
	config.CodexAccessToken = "new" // simulated rotation
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}
	if got := readAuth(t, dir)["FYC_CODEX_ACCESS_TOKEN"]; got != "new" {
		t.Fatalf("auth.json token = %q, want new", got)
	}
	if got := readRawConfig(t, dir)["codex_access_token"]; got != "${FYC_CODEX_ACCESS_TOKEN}" {
		t.Fatalf("placeholder renamed: %v", got)
	}
}

// TestMCPServersMigratedAndExpanded covers the MCP story: placeholders resolve
// everywhere, credential headers/env move to auth.json, and structural fields
// (endpoint, args, non-secret headers) stay inline.
func TestMCPServersMigratedAndExpanded(t *testing.T) {
	dir := withConfigDir(t)
	cfg := `{"schema":7,"mcp_servers":[
		{"name":"gh","transport":"streamableHttp","endpoint":"https://api.example/mcp",
		 "headers":{"Authorization":"Bearer tok","Accept":"application/json"}},
		{"name":"tool","transport":"stdio","command":"run","args":["--token","${FYC_MCP_TOKEN}","--flag"],
		 "env":{"API_TOKEN":"sk-env","PATH":"/bin"}}
	]}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, authFileName), []byte(`{"FYC_MCP_TOKEN":"argtok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil { // forced save: inline credentials detected
		t.Fatalf("LoadE: %v", err)
	}

	// In-memory values are resolved, so the MCP manager keeps working.
	if got := config.MCPServers[1].Args[1]; got != "argtok" {
		t.Fatalf("arg placeholder not resolved: %q", got)
	}

	raw := readRawConfig(t, dir)
	gh := raw["mcp_servers"].([]any)[0].(map[string]any)
	ghHeaders := gh["headers"].(map[string]any)
	if s, _ := ghHeaders["Authorization"].(string); !isPlaceholder(s) {
		t.Fatalf("Authorization was not moved to a placeholder: %v", ghHeaders["Authorization"])
	}
	if ghHeaders["Accept"] != "application/json" {
		t.Fatalf("non-secret header must stay inline: %v", ghHeaders["Accept"])
	}
	if gh["endpoint"] != "https://api.example/mcp" {
		t.Fatalf("endpoint must stay inline: %v", gh["endpoint"])
	}

	tool := raw["mcp_servers"].([]any)[1].(map[string]any)
	toolEnv := tool["env"].(map[string]any)
	if s, _ := toolEnv["API_TOKEN"].(string); !isPlaceholder(s) {
		t.Fatalf("env API_TOKEN was not moved to a placeholder: %v", toolEnv["API_TOKEN"])
	}
	if toolEnv["PATH"] != "/bin" {
		t.Fatalf("non-secret env must stay inline: %v", toolEnv["PATH"])
	}
	if args := tool["args"].([]any); args[1] != "${FYC_MCP_TOKEN}" || args[0] != "--token" || args[2] != "--flag" {
		t.Fatalf("args mutated unexpectedly: %v", args)
	}

	auth := readAuth(t, dir)
	if auth["FYC_MCP_GH_AUTHORIZATION"] != "Bearer tok" {
		t.Fatalf("header secret missing from auth.json: %v", auth)
	}
	if auth["FYC_MCP_TOOL_API_TOKEN"] != "sk-env" {
		t.Fatalf("env secret missing from auth.json: %v", auth)
	}
}

func TestCorruptAuthJSONFallsBackToEmpty(t *testing.T) {
	dir := withConfigDir(t)
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(`{"schema":7,"gemini_api_key":"${K}"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, authFileName), []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE must tolerate a corrupt auth.json, got %v", err)
	}
	if config.GeminiAPIKey != "" {
		t.Fatalf("GeminiAPIKey = %q, want empty", config.GeminiAPIKey)
	}
}

// TestLoadMigratesInlineSecretOnSchemaBump covers the upgrade path: an older
// config.json (schema < 7) with a plain-text key is rewritten on load, the
// secret moving to auth.json and the file carrying the current schema.
func TestLoadMigratesInlineSecretOnSchemaBump(t *testing.T) {
	dir := withConfigDir(t)
	legacy := `{"schema":6,"provider":"gemini","model":"gemini-2.0-flash","gemini_api_key":"sk-legacy"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "sk-legacy" {
		t.Fatalf("in-memory key = %q, want sk-legacy", config.GeminiAPIKey)
	}
	if got := readRawConfig(t, dir)["gemini_api_key"]; got != "${FYC_GEMINI_API_KEY}" {
		t.Fatalf("config.json gemini_api_key = %v, want the placeholder", got)
	}
	if got := readRawConfig(t, dir)["schema"]; got != float64(CurrentSchemaVersion) {
		t.Fatalf("schema = %v, want %d", got, CurrentSchemaVersion)
	}
	if got := readAuth(t, dir)["FYC_GEMINI_API_KEY"]; got != "sk-legacy" {
		t.Fatalf("auth.json FYC_GEMINI_API_KEY = %q, want sk-legacy", got)
	}
}

// TestSaveKeepsUnresolvedPlaceholder pins the case where config.json ships a
// ${VAR} placeholder whose value is unavailable at load (env unset, auth.json
// absent): an unrelated save must keep the expression, not drop it.
func TestSaveKeepsUnresolvedPlaceholder(t *testing.T) {
	dir := withConfigDir(t)
	os.Unsetenv("FYC_ABSENT_KEY_PROBE")
	cfg := `{"schema":7,"gemini_api_key":"${FYC_ABSENT_KEY_PROBE}"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "" {
		t.Fatalf("expected empty resolution, got %q", config.GeminiAPIKey)
	}
	config.Temperature = 0.5 // unrelated change forces a save
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}
	if got := readRawConfig(t, dir)["gemini_api_key"]; got != "${FYC_ABSENT_KEY_PROBE}" {
		t.Fatalf("placeholder dropped: gemini_api_key = %v", got)
	}
}

// TestSaveWithCorruptAuthKeepsPlaceholders: an unparseable auth.json must not be
// overwritten and must not cost the placeholders in config.json.
func TestSaveWithCorruptAuthKeepsPlaceholders(t *testing.T) {
	dir := withConfigDir(t)
	cfg := `{"schema":7,"gemini_api_key":"${FYC_KEEP_A}","codex_access_token":"${FYC_KEEP_B}"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	corrupt := "{ not json at all"
	if err := os.WriteFile(filepath.Join(dir, authFileName), []byte(corrupt), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FYC_KEEP_A", "resolved-a")
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	if config.GeminiAPIKey != "resolved-a" {
		t.Fatalf("env fallback not applied: %q", config.GeminiAPIKey)
	}
	config.Temperature = 0.5
	if err := SaveE(); err != nil {
		t.Fatalf("SaveE: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, authFileName))
	if err != nil {
		t.Fatalf("auth.json vanished: %v", err)
	}
	if string(got) != corrupt {
		t.Fatalf("corrupt auth.json was overwritten: %q", got)
	}
	raw := readRawConfig(t, dir)
	if raw["gemini_api_key"] != "${FYC_KEEP_A}" {
		t.Fatalf("gemini placeholder lost: %v", raw["gemini_api_key"])
	}
	if raw["codex_access_token"] != "${FYC_KEEP_B}" {
		t.Fatalf("codex placeholder lost: %v", raw["codex_access_token"])
	}
}

// TestUnresolvedPlaceholderIsNoticed checks the once-per-variable load notice
// naming the variable and the field it came from.
func TestUnresolvedPlaceholderIsNoticed(t *testing.T) {
	dir := withConfigDir(t)
	var notices []string
	store.SetLogger(func(format string, args ...any) {
		notices = append(notices, fmt.Sprintf(format, args...))
	})
	os.Unsetenv("FYC_NOTICE_PROBE")
	cfg := `{"schema":7,"gemini_api_key":"${FYC_NOTICE_PROBE}"}`
	if err := os.WriteFile(filepath.Join(dir, configFileName), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := LoadE(); err != nil {
		t.Fatalf("LoadE: %v", err)
	}
	for _, n := range notices {
		if strings.Contains(n, "FYC_NOTICE_PROBE") && strings.Contains(n, "gemini") {
			return
		}
	}
	t.Fatalf("no notice for the unresolved placeholder, got %v", notices)
}
