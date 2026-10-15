package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"fuckyourcli/store"
	"fuckyourcli/types"
)

// This file implements the secret-reference mechanism. config.json stores only
// "${VAR}" / "${VAR:-default}" placeholders for credentials; the concrete
// values live in auth.json (kept out of git by the ignore files dropped next to
// it) or in the process environment. In memory the fields stay *resolved* so
// every read site keeps working unchanged; the placeholders are re-materialised
// on save, on a copy, so the live config is never mutated.
//
// Lookup order is auth.json first, then the environment: the file is the source
// of truth and an env var is only a fallback (see docs/DOCS.md).

const (
	// authFileName is the flat JSON store of secrets, keyed by variable name.
	authFileName = "auth.json"
	// gitignoreFileName and fycignoreFileName are dropped into the config
	// directory (only when absent) so a user who syncs ~/.config to a remote
	// never commits the secret store by accident.
	gitignoreFileName = ".gitignore"
	fycignoreFileName = ".fycignore"
)

// defaultIgnoreBody ignores the secret store and the recovery copies of every
// file that can carry a secret (auth.json and the previous config.json).
const defaultIgnoreBody = authFileName + "\n" +
	authFileName + store.BackupSuffix + "\n" +
	configFileName + store.BackupSuffix + "\n"

// secretPlaceholderRe matches ${VAR} and ${VAR:-default}. Variable names must
// start with a letter or underscore; the default is literal text up to the
// closing brace (no nesting).
var secretPlaceholderRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)

// AuthFilePath returns the location of auth.json. FYC_AUTH_FILE overrides the
// computed location (tests use it together with FYC_CONFIG_DIR).
func AuthFilePath() string {
	if p := os.Getenv("FYC_AUTH_FILE"); p != "" {
		return p
	}
	return filepath.Join(store.ConfigDir(), authFileName)
}

// ensureConfigDefenses drops the ignore files next to config.json. An existing
// file is never touched: the user may have their own rules there.
func ensureConfigDefenses(dir string) {
	for _, name := range []string{gitignoreFileName, fycignoreFileName} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.WriteFile(path, []byte(defaultIgnoreBody), 0o644); err != nil {
			store.Notice("config: cannot write %s: %v", path, err)
		}
	}
}

// loadAuthStore reads auth.json. A missing file is an empty store (corrupt
// false); an unparseable one is reported and returned as empty with corrupt
// true, so callers can refuse to overwrite a file they could not read.
func loadAuthStore() (map[string]string, bool) {
	m := map[string]string{}
	if err := store.ReadJSON(AuthFilePath(), &m); err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, false
		}
		store.Notice("auth: %v", err)
		return map[string]string{}, true
	}
	if m == nil {
		m = map[string]string{}
	}
	return m, false
}

// saveAuthStore persists the secret store (0600, with a .bak recovery copy). An
// empty store is only written when the file already exists, so a fresh install
// never gets an empty auth.json dropped behind it.
func saveAuthStore(m map[string]string) error {
	if len(m) == 0 {
		if _, err := os.Stat(AuthFilePath()); os.IsNotExist(err) {
			return nil
		}
	}
	return store.WriteJSON(AuthFilePath(), m, store.FilePerm, true)
}

// expandPlaceholders substitutes every placeholder in s using lookup. A miss —
// or an empty value when a default is given — resolves to the default, or to ""
// when there is none. Substituted text is never rescanned.
func expandPlaceholders(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return secretPlaceholderRe.ReplaceAllStringFunc(s, func(match string) string {
		m := secretPlaceholderRe.FindStringSubmatch(match)
		name := m[1]
		hasDefault := m[2] != ""
		if v, ok := lookup(name); ok && (v != "" || !hasDefault) {
			return v
		}
		if hasDefault {
			return m[3]
		}
		return ""
	})
}

// isPlaceholder reports whether s carries at least one ${VAR} placeholder.
func isPlaceholder(s string) bool { return secretPlaceholderRe.MatchString(s) }

// solePlaceholder reports whether s is *exactly* one placeholder, returning its
// variable name. Only then can a value changed at runtime be re-pinned to that
// name instead of getting a freshly generated one.
func solePlaceholder(s string) (string, bool) {
	m := secretPlaceholderRe.FindStringSubmatch(s)
	if m == nil || m[0] != s {
		return "", false
	}
	return m[1], true
}

// secretLookup resolves a name with the documented precedence: auth.json wins,
// then the environment.
func secretLookup(auth map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if v, ok := auth[name]; ok {
			return v, true
		}
		return os.LookupEnv(name)
	}
}

// expandConfigSecrets resolves every secret-bearing field in place: the
// credential scalars, each provider API key, and (for MCP) the endpoint,
// arguments, headers and env values.
func expandConfigSecrets(cfg *Config, auth map[string]string) {
	lookup := secretLookup(auth)
	expand := func(s string) string { return expandPlaceholders(s, lookup) }
	cfg.GeminiAPIKey = expand(cfg.GeminiAPIKey)
	cfg.CodexAccessToken = expand(cfg.CodexAccessToken)
	cfg.CodexRefreshToken = expand(cfg.CodexRefreshToken)
	expandProviderKeys(cfg.OpenAICompatibleProviders, expand)
	expandProviderKeys(cfg.OpenAIResponsesCompatibleProviders, expand)
	expandProviderKeys(cfg.AnthropicProviders, expand)
	for i := range cfg.MCPServers {
		s := &cfg.MCPServers[i]
		s.Endpoint = expand(s.Endpoint)
		for j := range s.Args {
			s.Args[j] = expand(s.Args[j])
		}
		for k, v := range s.Headers {
			s.Headers[k] = expand(v)
		}
		for k, v := range s.Env {
			s.Env[k] = expand(v)
		}
	}
}

func expandProviderKeys(list []OpenAICompatibleProviderConfig, expand func(string) string) {
	for i := range list {
		list[i].APIKey = expand(list[i].APIKey)
	}
}

// noticeUnresolvedSecrets logs one notice per distinct ${VAR} (without a
// default) that resolves to nothing, naming the field it appears in, so a
// misconfigured placeholder is visible at startup instead of silently empty.
func noticeUnresolvedSecrets(raw map[string]string, auth map[string]string) {
	lookup := secretLookup(auth)
	seen := map[string]bool{}
	for field, expr := range raw {
		for _, m := range secretPlaceholderRe.FindAllStringSubmatch(expr, -1) {
			name := m[1]
			if m[2] != "" { // explicit ":-default": the fallback is intentional
				continue
			}
			if seen[name] {
				continue
			}
			if v, ok := lookup(name); ok && v != "" {
				continue
			}
			seen[name] = true
			store.Notice("config: %s references ${%s} but it is not set (auth.json or environment)", field, name)
		}
	}
}

// captureRawSecrets records the on-disk expression of every secret-bearing
// field, keyed by a stable field identity. SaveE uses it to tell an untouched
// placeholder from a value the app changed at runtime.
func captureRawSecrets(cfg Config) map[string]string {
	raw := map[string]string{
		"gemini":        cfg.GeminiAPIKey,
		"codex_access":  cfg.CodexAccessToken,
		"codex_refresh": cfg.CodexRefreshToken,
	}
	captureProviderRaw(raw, "openai", cfg.OpenAICompatibleProviders)
	captureProviderRaw(raw, "responses", cfg.OpenAIResponsesCompatibleProviders)
	captureProviderRaw(raw, "anthropic", cfg.AnthropicProviders)
	for _, s := range cfg.MCPServers {
		raw["mcp:"+s.Name+":endpoint"] = s.Endpoint
		for i, a := range s.Args {
			raw[fmt.Sprintf("mcp:%s:arg:%d", s.Name, i)] = a
		}
		for k, v := range s.Headers {
			raw["mcp:"+s.Name+":header:"+k] = v
		}
		for k, v := range s.Env {
			raw["mcp:"+s.Name+":env:"+k] = v
		}
	}
	return raw
}

func captureProviderRaw(raw map[string]string, prefix string, list []OpenAICompatibleProviderConfig) {
	for i, p := range list {
		raw[prefix+":"+providerKey(p.Name, i)] = p.APIKey
	}
}

// providerKey identifies a provider by name, falling back to its index when the
// name is empty (should not happen, but keeps the map key non-degenerate).
func providerKey(name string, idx int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("#%d", idx)
}

// hasInlineSecrets reports whether a loaded config still carries a raw literal
// in a *migratable* secret field, so a save must be forced to move it into
// auth.json. MCP endpoint/args are structural and deliberately not counted:
// they are migrated only when the user opts in with ${VAR}.
func hasInlineSecrets(cfg Config) bool {
	if inlineLiteral(cfg.GeminiAPIKey) || inlineLiteral(cfg.CodexAccessToken) || inlineLiteral(cfg.CodexRefreshToken) {
		return true
	}
	for _, p := range cfg.OpenAICompatibleProviders {
		if inlineLiteral(p.APIKey) {
			return true
		}
	}
	for _, p := range cfg.OpenAIResponsesCompatibleProviders {
		if inlineLiteral(p.APIKey) {
			return true
		}
	}
	for _, p := range cfg.AnthropicProviders {
		if inlineLiteral(p.APIKey) {
			return true
		}
	}
	for _, s := range cfg.MCPServers {
		for k, v := range s.Headers {
			if looksSecretKey(k) && inlineLiteral(v) {
				return true
			}
		}
		for k, v := range s.Env {
			if looksSecretKey(k) && inlineLiteral(v) {
				return true
			}
		}
	}
	return false
}

func inlineLiteral(v string) bool { return v != "" && !isPlaceholder(v) }

// collapseState is the shared scratch state of one collapse pass.
type collapseState struct {
	auth    map[string]string
	used    map[string]bool
	changed *bool
	// frozen disables every write to the auth store, used when auth.json is
	// unreadable (or unwritable): the save then keeps the on-disk expressions
	// instead of emitting a placeholder whose value it could not store.
	frozen bool
}

// move stores value under a freshly generated (collision-free) name and returns
// the placeholder that now stands for it. In frozen mode it keeps the value
// inline, since there is nowhere safe to store it.
func (c *collapseState) move(defName, value string) string {
	if c.frozen {
		return value
	}
	name := uniqueVarName(defName, c.used)
	c.used[name] = true
	if c.auth[name] != value {
		c.auth[name] = value
		*c.changed = true
	}
	return "${" + name + "}"
}

// collapseField decides the persisted expression for one secret field.
//
//   - cur is the live value, raw the on-disk expression captured at load;
//   - migrate=false keeps literal values inline (structural fields: MCP
//     endpoint and args, migrated only through an explicit ${VAR});
//   - an untouched placeholder is kept verbatim and persists nothing, so a
//     value that came from the environment is never copied into auth.json;
//   - a changed value under a sole placeholder is re-pinned to that same name
//     (e.g. a rotated Codex token), otherwise a literal is moved to auth.json.
func collapseField(cur, raw, defName string, migrate bool, c *collapseState) string {
	if cur == "" {
		if isPlaceholder(raw) {
			// The expression resolved to nothing at load (env unset, auth.json
			// absent). Keep it verbatim: a value supplied later, or by a
			// repaired auth.json, must not be silently dropped from config.json.
			if expandPlaceholders(raw, secretLookup(c.auth)) == "" {
				return raw
			}
			// It used to resolve to a value, so the app cleared it on purpose.
			if !c.frozen {
				if name, ok := solePlaceholder(raw); ok {
					if _, exists := c.auth[name]; exists {
						delete(c.auth, name)
						*c.changed = true
					}
				}
			}
		}
		return ""
	}
	if isPlaceholder(raw) {
		if expandPlaceholders(raw, secretLookup(c.auth)) == cur {
			return raw
		}
		if !migrate {
			return cur
		}
		if name, ok := solePlaceholder(raw); ok {
			if c.frozen {
				return cur // cannot re-pin without writing the store
			}
			if c.auth[name] != cur {
				c.auth[name] = cur
				*c.changed = true
			}
			return raw
		}
		return cur // embedded placeholder + changed value: cannot re-pin safely
	}
	if !migrate {
		return cur
	}
	return c.move(defName, cur)
}

// collapseConfigSecrets returns a deep copy of src whose secret fields hold the
// persisted expression (placeholder or inline literal), plus the auth store
// updated with any moved or rotated value and whether it actually changed. The
// live src is never mutated.
func collapseConfigSecrets(src Config, auth map[string]string, frozen bool) (Config, map[string]string, bool) {
	out := src
	out.OpenAICompatibleProviders = append([]OpenAICompatibleProviderConfig(nil), src.OpenAICompatibleProviders...)
	out.OpenAIResponsesCompatibleProviders = append([]OpenAICompatibleProviderConfig(nil), src.OpenAIResponsesCompatibleProviders...)
	out.AnthropicProviders = append([]OpenAICompatibleProviderConfig(nil), src.AnthropicProviders...)
	out.MCPServers = cloneMCPServers(src.MCPServers)

	changed := false
	c := &collapseState{auth: auth, used: referencedVarNames(src.rawSecrets), changed: &changed, frozen: frozen}
	raw := src.rawSecrets

	out.GeminiAPIKey = collapseField(src.GeminiAPIKey, raw["gemini"], "FYC_GEMINI_API_KEY", true, c)
	out.CodexAccessToken = collapseField(src.CodexAccessToken, raw["codex_access"], "FYC_CODEX_ACCESS_TOKEN", true, c)
	out.CodexRefreshToken = collapseField(src.CodexRefreshToken, raw["codex_refresh"], "FYC_CODEX_REFRESH_TOKEN", true, c)

	collapseProviderKeys(out.OpenAICompatibleProviders, "openai", "FYC_OPENAI", raw, c)
	collapseProviderKeys(out.OpenAIResponsesCompatibleProviders, "responses", "FYC_RESPONSES", raw, c)
	collapseProviderKeys(out.AnthropicProviders, "anthropic", "FYC_ANTHROPIC", raw, c)

	collapseMCPServers(out.MCPServers, raw, c)

	return out, auth, changed
}

func collapseProviderKeys(list []OpenAICompatibleProviderConfig, rawPrefix, varPrefix string, raw map[string]string, c *collapseState) {
	for i := range list {
		key := rawPrefix + ":" + providerKey(list[i].Name, i)
		def := varPrefix + "_" + slug(list[i].Name) + "_API_KEY"
		list[i].APIKey = collapseField(list[i].APIKey, raw[key], def, true, c)
	}
}

func collapseMCPServers(list []types.MCPServerConfig, raw map[string]string, c *collapseState) {
	for i := range list {
		s := &list[i]
		n := s.Name
		s.Endpoint = collapseField(s.Endpoint, raw["mcp:"+n+":endpoint"], "FYC_MCP_"+slug(n)+"_ENDPOINT", false, c)
		for j := range s.Args {
			rawKey := fmt.Sprintf("mcp:%s:arg:%d", n, j)
			def := fmt.Sprintf("FYC_MCP_%s_ARG_%d", slug(n), j+1)
			s.Args[j] = collapseField(s.Args[j], raw[rawKey], def, false, c)
		}
		for k := range s.Headers {
			rawKey := "mcp:" + n + ":header:" + k
			def := "FYC_MCP_" + slug(n) + "_" + slug(k)
			s.Headers[k] = collapseField(s.Headers[k], raw[rawKey], def, looksSecretKey(k), c)
		}
		for k := range s.Env {
			rawKey := "mcp:" + n + ":env:" + k
			def := "FYC_MCP_" + slug(n) + "_" + slug(k)
			s.Env[k] = collapseField(s.Env[k], raw[rawKey], def, looksSecretKey(k), c)
		}
	}
}

// cloneMCPServers deep-copies the server list so collapsing the serialization
// copy never touches the live maps.
func cloneMCPServers(list []types.MCPServerConfig) []types.MCPServerConfig {
	out := make([]types.MCPServerConfig, len(list))
	for i, s := range list {
		out[i] = s
		if s.Args != nil {
			out[i].Args = append([]string(nil), s.Args...)
		}
		if s.Headers != nil {
			m := make(map[string]string, len(s.Headers))
			for k, v := range s.Headers {
				m[k] = v
			}
			out[i].Headers = m
		}
		if s.Env != nil {
			m := make(map[string]string, len(s.Env))
			for k, v := range s.Env {
				m[k] = v
			}
			out[i].Env = m
		}
	}
	return out
}

// referencedVarNames collects every variable name already used by a placeholder
// in the loaded config, so a generated name never shadows one of them.
func referencedVarNames(raw map[string]string) map[string]bool {
	used := map[string]bool{}
	for _, v := range raw {
		for _, m := range secretPlaceholderRe.FindAllStringSubmatch(v, -1) {
			used[m[1]] = true
		}
	}
	return used
}

// uniqueVarName returns base, or base_2, base_3… when it is already taken.
func uniqueVarName(base string, used map[string]bool) string {
	if base == "" {
		base = "FYC_SECRET"
	}
	if !used[base] {
		return base
	}
	for n := 2; ; n++ {
		cand := fmt.Sprintf("%s_%d", base, n)
		if !used[cand] {
			return cand
		}
	}
}

// slug upper-cases s and collapses every run of non-alphanumeric characters to
// a single underscore, so a human name becomes a valid env-var segment.
func slug(s string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToUpper(s) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
			underscore = false
		default:
			if !underscore {
				b.WriteByte('_')
				underscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "X"
	}
	return out
}

// secretKeyMarkers are the substrings that make a map key look like a
// credential, so its value is moved to auth.json on migration.
var secretKeyMarkers = []string{
	"token", "secret", "password", "passwd", "credential",
	"authorization", "auth", "cookie", "session", "apikey", "api_key", "api-key",
	"private_key", "access_key",
}

// looksSecretKey reports whether a headers/env key names a credential.
func looksSecretKey(k string) bool {
	lk := strings.ToLower(k)
	for _, m := range secretKeyMarkers {
		if strings.Contains(lk, m) {
			return true
		}
	}
	return false
}
