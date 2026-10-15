package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"fuckyourcli/config"
)

// statusHookTimeout bounds a single status-bar hook invocation. It is kept
// short: the hook feeds the status bar and must never stall the interface.
const statusHookTimeout = 8 * time.Second

// statusHookMaxRunes caps the rendered hook line so a runaway command cannot
// blow up the status bar.
const statusHookMaxRunes = 200

// StatusHookRunner runs the active status-bar hook and remembers its last
// successful value. Error handling is deliberate and documented here:
//
//   - a failing command (non-zero exit), a timeout or empty output never
//     overwrites the remembered value ("keep the last useful value");
//   - when there is no remembered value yet (e.g. the very first turn fails)
//     nothing is rendered, so the status bar simply stays without the line;
//   - switching provider resets the remembered value, so a stale value from
//     another provider is never attributed to the new one.
//
// Errors are intentionally never surfaced in the bar. A hook author who wants
// feedback can write to a log file from the command itself; fyc stays silent on
// purpose.
type StatusHookRunner struct {
	mu       sync.Mutex
	lastLine string
	lastKey  string
	hasValue bool
}

// Refresh resolves the hook for the active provider and runs it. show reports
// whether a line must be rendered; line is the text to show.
func (r *StatusHookRunner) Refresh(root string) (line string, show bool) {
	h := config.ActiveStatusHook()
	key := config.CurrentProviderKey()

	r.mu.Lock()
	if key != r.lastKey {
		// Provider changed: a value produced for another provider is stale.
		r.lastLine, r.hasValue, r.lastKey = "", false, key
	}
	r.mu.Unlock()

	if !h.Enabled || strings.TrimSpace(h.Command) == "" {
		r.mu.Lock()
		r.lastLine, r.hasValue = "", false
		r.mu.Unlock()
		return "", false
	}

	out, err := runStatusHookCmd(h, root)

	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil || strings.TrimSpace(out) == "" {
		// Error, timeout or empty output: keep the last useful value.
		if r.hasValue {
			return r.lastLine, true
		}
		return "", false
	}
	r.lastLine, r.hasValue = out, true
	return out, true
}

// Reset forgets the remembered value (used when the configuration changes).
func (r *StatusHookRunner) Reset() {
	r.mu.Lock()
	r.lastLine, r.hasValue = "", false
	r.mu.Unlock()
}

// runStatusHookCmd executes the hook and returns its first non-empty stdout
// line. stderr is discarded: a failing hook must stay silent.
func runStatusHookCmd(h config.StatusBarHook, root string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statusHookTimeout)
	defer cancel()

	cfg := config.Get()
	key := config.CurrentProviderKey()
	baseURL, secret := config.ActiveProviderEndpoint()

	cmd := shellCommand(ctx, h.Command)
	if root != "" {
		cmd.Dir = root
	}
	env := append(os.Environ(),
		"FYC_HOOK=status_bar",
		"FYC_PROVIDER_KEY="+key,
		"FYC_PROVIDER_TYPE="+string(cfg.Provider),
		"FYC_PROVIDER="+config.GetCurrentProviderInfo(),
		"FYC_MODEL="+cfg.Model,
		"FYC_BASE_URL="+baseURL,
	)
	// The API key/token is exported only when the hook explicitly opts in.
	if h.IncludeSecrets {
		env = append(env, "FYC_API_KEY="+secret)
	}
	cmd.Env = env

	payload, _ := json.Marshal(map[string]string{
		"provider":      config.GetCurrentProviderInfo(),
		"provider_key":  key,
		"provider_type": string(cfg.Provider),
		"model":         cfg.Model,
		"base_url":      baseURL,
	})
	cmd.Stdin = bytes.NewReader(payload)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	return clampRunes(firstNonEmptyLine(buf.String()), statusHookMaxRunes), nil
}

// firstNonEmptyLine returns the first non-blank line, trimmed.
func firstNonEmptyLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimRight(line, "\r"); strings.TrimSpace(t) != "" {
			return strings.TrimSpace(t)
		}
	}
	return ""
}

// clampRunes truncates s to at most n runes, appending an ellipsis when cut.
func clampRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
