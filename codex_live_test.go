package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/providers"
	"fuckyourcli/reasoning"
	"fuckyourcli/types"
)

// TestLiveCodexEncryptedReasoningReplay is the live check of Appendice A.12: it
// runs against the real Codex Responses API and verifies the multi-turn
// reasoning replay that the unit tests only approximate with synthetic JSON.
//
// It is skipped unless FYC_LIVE_CODEX=1, because it needs valid Codex
// credentials (config.json) and spends tokens:
//
//	FYC_LIVE_CODEX=1 go test -run TestLiveCodexEncryptedReasoningReplay -v .
//
// What it proves, in order:
//  1. with an active reasoning mode the request asks for
//     include=["reasoning.encrypted_content"] and the API answers with a
//     reasoning item carrying a non-empty encrypted blob;
//  2. replaying that item on the next turn is accepted (no 4xx) and the turn
//     completes with an answer;
//  3. the replayed request really carries the blob (captured from the wire, not
//     assumed from the code path);
//  4. (informational) a tampered blob is rejected — or silently ignored.
func TestLiveCodexEncryptedReasoningReplay(t *testing.T) {
	if os.Getenv("FYC_LIVE_CODEX") != "1" {
		t.Skip("set FYC_LIVE_CODEX=1 to run against the live Codex API (spends tokens)")
	}
	config.LoadE()
	cfg := config.Get()
	model := cfg.CodexModel
	if v := os.Getenv("FYC_CODEX_MODEL"); v != "" {
		model = v
	}
	effort := "low"
	if v := os.Getenv("FYC_CODEX_EFFORT"); v != "" {
		effort = v
	}
	t.Logf("live Codex check: model=%q effort=%q (override with FYC_CODEX_MODEL / FYC_CODEX_EFFORT)", model, effort)
	token, err := config.CodexFreshAccessToken()
	if err != nil {
		t.Skipf("no usable Codex credentials (%v): run 'fuckyourcli config' first", err)
	}

	// Every outgoing body is captured, so "the blob was replayed" is observed on
	// the wire instead of inferred from reading the code.
	wire := captureCodexRequests(t)

	newProvider := func() *providers.CodexProvider {
		p := providers.NewCodexProvider(token, model, 0, false, "")
		// An active mode is what makes the provider request the encrypted state.
		p.SetReasoning(reasoning.Intent{Mode: reasoning.ModeEffort, Effort: effort}, nil)
		p.Refresh = config.CodexForceRefresh
		return p
	}
	sink := providers.NewSilentOutputSink()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	history := []types.Message{{Role: "user", Content: "Think briefly, then answer in one short sentence: is 2^10 larger than 10^3?"}}

	// ── Turn 1: the API must hand back an encrypted reasoning item ────────────
	t1, err := newProvider().StreamChat(ctx, history, nil, sink)
	if err != nil {
		if strings.Contains(err.Error(), "model_not_found") {
			t.Fatalf("turn 1 failed: %v\nhint: this account has no access to %q — retry with FYC_CODEX_MODEL=<model you can use>", err, model)
		}
		t.Fatalf("turn 1 failed: %v", err)
	}
	t.Logf("turn 1: content=%q reasoning_chars=%d item_chars=%d", clipLog(t1.Content), len(t1.ReasoningContent), len(t1.ReasoningItem))
	if !bodyHasInclude(wire.last(), "reasoning.encrypted_content") {
		t.Fatalf("turn 1 did not request reasoning.encrypted_content: %s", clipLog(string(wire.last())))
	}
	blob := encryptedReasoningContent(t1.ReasoningItem)
	if blob == "" {
		t.Fatalf("no encrypted reasoning item in the response: %s\n"+
			"hint: an item only comes back if the model actually reasoned — check output_tokens_details.reasoning_tokens with TestLiveCodexWireDump, then retry with a model/effort that thinks (e.g. FYC_CODEX_MODEL=gpt-5.5 FYC_CODEX_EFFORT=medium)",
			clipLog(t1.ReasoningItem))
	}
	t.Logf("turn 1: encrypted reasoning blob received (%d chars, item id %s)", len(blob), reasoningItemID(t1.ReasoningItem))

	// ── Turn 2: replay the item verbatim ────────────────────────────────────
	history = append(history,
		types.Message{Role: "assistant", Content: t1.Content, ReasoningContent: t1.ReasoningContent, ReasoningItem: t1.ReasoningItem},
		types.Message{Role: "user", Content: "Now reply with exactly one word: OK"},
	)
	before := wire.count()
	t2, err := newProvider().StreamChat(ctx, history, nil, sink)
	if err != nil {
		t.Fatalf("turn 2 (replay) failed: %v", err)
	}
	if strings.TrimSpace(t2.Content) == "" {
		t.Fatal("turn 2 returned no content")
	}
	t.Logf("turn 2: content=%q", clipLog(t2.Content))

	replayed := encryptedReasoningItems(wire.after(before))
	if len(replayed) == 0 {
		t.Fatal("the replayed request carried no reasoning item (the blob was dropped before the wire)")
	}
	if replayed[0] != blob {
		t.Fatalf("the replayed blob differs from the one received:\n got %s\nwant %s", clipLog(replayed[0]), clipLog(blob))
	}
	t.Logf("turn 2: the request replayed the same blob (%d chars) and the API accepted it", len(replayed[0]))

	// ── Turn 3 (informational): tampered blob ───────────────────────────────
	tampered := tamperBlob(t1.ReasoningItem)
	if tampered == "" {
		t.Log("turn 3 skipped: could not tamper the item")
		return
	}
	history = append(history,
		types.Message{Role: "assistant", Content: t1.Content, ReasoningItem: tampered},
		types.Message{Role: "user", Content: "Reply with exactly one word: OK"},
	)
	if _, err := newProvider().StreamChat(ctx, history, nil, sink); err != nil {
		t.Logf("turn 3: a tampered blob was rejected, as expected: %v", err)
	} else {
		t.Log("turn 3: a tampered blob was accepted — the API does not validate its contents")
	}
}

// TestLiveCodexWireDump answers "why is there no reasoning item?" from the wire:
// it lists the Codex models the token can use, shows the profile fyc currently
// detects for the model, then dumps one raw request/response exchange (SSE event
// types, output item types and usage).
//
// Same gates as the replay test; FYC_CODEX_EFFORT sets the effort sent (default
// "medium"):
//
//	FYC_LIVE_CODEX=1 FYC_CODEX_MODEL=codex-auto-review go test -run TestLiveCodexWireDump -v .
func TestLiveCodexWireDump(t *testing.T) {
	if os.Getenv("FYC_LIVE_CODEX") != "1" {
		t.Skip("set FYC_LIVE_CODEX=1 to run against the live Codex API (spends tokens)")
	}
	config.LoadE()
	cfg := config.Get()
	token, err := config.CodexFreshAccessToken()
	if err != nil {
		t.Skipf("no usable Codex credentials (%v): run 'fuckyourcli config' first", err)
	}

	if models, err := providers.GetCodexModels(token); err != nil {
		t.Logf("models listing failed: %v", err)
	} else {
		t.Logf("models available to this account: %v", models)
	}

	model := cfg.CodexModel
	if v := os.Getenv("FYC_CODEX_MODEL"); v != "" {
		model = v
	}
	effort := "medium"
	if v := os.Getenv("FYC_CODEX_EFFORT"); v != "" {
		effort = v
	}

	// What fyc would send today for this model, from the built-in tables.
	profile, det := reasoning.Lookup(reasoning.OpenAIResponses, "", model, nil)
	t.Logf("detected: family=%q confident=%v effort_path=%q alongside=%v include_requested=%v",
		det.Family, det.Confident, profile.EffortPath, profile.AlongsideControl,
		profile.EffortPath != "" || profile.BudgetPath != "")

	body := map[string]any{
		"model":        model,
		"instructions": "You are a helpful assistant.",
		"input": []any{map[string]any{
			"role":    "user",
			"content": "Think briefly, then answer in one short sentence: is 2^10 larger than 10^3?",
		}},
		"stream":    true,
		"store":     false,
		"include":   []string{"reasoning.encrypted_content"},
		"reasoning": map[string]any{"effort": effort, "summary": "auto"},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("sending: %s", clipLog(string(raw)))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=v1")
	req.Header.Set("User-Agent", "fuckyourcli/1.0")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("HTTP %d: %s", resp.StatusCode, clipLog(string(raw)))
	}

	events := map[string]int{}
	var items []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var ev map[string]any
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		kind, _ := ev["type"].(string)
		events[kind]++
		if item, ok := ev["item"].(map[string]any); ok {
			items = append(items, fmt.Sprintf("%s:%v(encrypted=%v)", kind, item["type"], item["encrypted_content"] != nil))
		}
		if kind == "response.completed" {
			if r, ok := ev["response"].(map[string]any); ok {
				if output, ok := r["output"].([]any); ok {
					for _, it := range output {
						m, _ := it.(map[string]any)
						items = append(items, fmt.Sprintf("completed.output:%v(id=%v,encrypted=%v)", m["type"], m["id"], m["encrypted_content"] != nil))
					}
				}
				t.Logf("usage: %v", clipLog(fmt.Sprint(r["usage"])))
				// The reasoning tokens tell whether the model reasoned at all: with
				// zero, no reasoning item (and no summary) can come back, and the
				// replay check has nothing to replay.
				if usage, ok := r["usage"].(map[string]any); ok {
					if details, ok := usage["output_tokens_details"].(map[string]any); ok {
						t.Logf("reasoning tokens: %v (0 means the model did not think in this exchange)", details["reasoning_tokens"])
					}
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Logf("stream read error: %v", err)
	}
	t.Logf("output items: %v", items)
	t.Logf("event counts: %v", events)
	if events["response.reasoning_summary_text.delta"]+events["response.reasoning_text.delta"] > 0 {
		t.Log("the model reasoned (reasoning deltas received)")
	} else {
		t.Log("no reasoning deltas in this exchange")
	}
}

// ── wire capture ────────────────────────────────────────────────────────────

// captureTransport records the body of every request made through
// http.DefaultTransport (the client doWithRetry falls back to).
type captureTransport struct {
	inner http.RoundTripper
	mu    sync.Mutex
	seen  [][]byte
}

func (c *captureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		body, err := io.ReadAll(r.Body)
		r.Body.Close()
		if err == nil {
			c.mu.Lock()
			c.seen = append(c.seen, body)
			c.mu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
	}
	return c.inner.RoundTrip(r)
}

func (c *captureTransport) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.seen)
}

// after returns the bodies captured from index start onwards.
func (c *captureTransport) after(start int) [][]byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if start >= len(c.seen) {
		return nil
	}
	return c.seen[start:]
}

// last returns the most recent captured body.
func (c *captureTransport) last() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		return nil
	}
	return c.seen[len(c.seen)-1]
}

func captureCodexRequests(t *testing.T) *captureTransport {
	t.Helper()
	prev := http.DefaultTransport
	c := &captureTransport{inner: prev}
	http.DefaultTransport = c
	t.Cleanup(func() { http.DefaultTransport = prev })
	return c
}

// ── request/response helpers ────────────────────────────────────────────────

// bodyHasInclude reports whether the outgoing request asked for one include.
func bodyHasInclude(body []byte, want string) bool {
	var m map[string]any
	if json.Unmarshal(body, &m) != nil {
		return false
	}
	arr, _ := m["include"].([]any)
	for _, v := range arr {
		if s, ok := v.(string); ok && s == want {
			return true
		}
	}
	return false
}

// encryptedReasoningItems collects the encrypted blobs carried by the reasoning
// items of a request body (the Responses "input" array).
func encryptedReasoningItems(bodies [][]byte) []string {
	var out []string
	for _, body := range bodies {
		var m map[string]any
		if json.Unmarshal(body, &m) != nil {
			continue
		}
		input, _ := m["input"].([]any)
		for _, raw := range input {
			item, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if kind, _ := item["type"].(string); kind != "reasoning" {
				continue
			}
			if s, _ := item["encrypted_content"].(string); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func encryptedReasoningContent(item string) string {
	blobs := encryptedReasoningItems([][]byte{[]byte(item)})
	if len(blobs) == 0 {
		return ""
	}
	return blobs[0]
}

func reasoningItemID(item string) string {
	var m map[string]any
	if json.Unmarshal([]byte(item), &m) != nil {
		return "?"
	}
	id, _ := m["id"].(string)
	return id
}

// tamperBlob corrupts the encrypted content so the server can no longer
// decrypt it, keeping the item otherwise well-formed.
func tamperBlob(item string) string {
	var m map[string]any
	if json.Unmarshal([]byte(item), &m) != nil {
		return ""
	}
	blob, _ := m["encrypted_content"].(string)
	if blob == "" {
		return ""
	}
	flipped := []byte(blob)
	flipped[0] ^= 0x01
	flipped[len(flipped)/2] ^= 0x01
	m["encrypted_content"] = string(flipped)
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return string(out)
}

func clipLog(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 200 {
		return fmt.Sprintf("%s…(%d chars)", s[:200], len(s))
	}
	return s
}
