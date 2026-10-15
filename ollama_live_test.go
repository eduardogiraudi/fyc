package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"fuckyourcli/config"
	"fuckyourcli/reasoning"
)

// TestLiveOllamaWireDump prints what a local Ollama actually returns for a
// thinking request, so "I see no thinking" can be attributed to the request, to
// the endpoint or to the parser instead of being guessed.
//
//	FYC_LIVE_OLLAMA=1 go test -run TestLiveOllamaWireDump -v .
//
// The URL and the model come from the active provider; override them with
// FYC_OLLAMA_URL / FYC_OLLAMA_MODEL. It sends four small requests:
//
//	POST /v1/chat/completions  reasoning_effort=high
//	POST /v1/chat/completions  think=true            (native field, often ignored here)
//	POST /v1/responses         reasoning.effort=high
//	POST /v1/responses         think=true            (Ollama extension)
func TestLiveOllamaWireDump(t *testing.T) {
	if os.Getenv("FYC_LIVE_OLLAMA") != "1" {
		t.Skip("set FYC_LIVE_OLLAMA=1 to talk to the local Ollama server")
	}
	config.LoadE()
	base, key := config.ActiveProviderEndpoint()
	model := config.Get().Model
	if v := os.Getenv("FYC_OLLAMA_URL"); v != "" {
		base = v
	}
	if v := os.Getenv("FYC_OLLAMA_MODEL"); v != "" {
		model = v
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		t.Fatal("no base URL: switch to the Ollama provider, or pass FYC_OLLAMA_URL (e.g. http://localhost:11434/v1)")
	}
	if model == "" {
		t.Fatal("no model: pass FYC_OLLAMA_MODEL (e.g. gemma4:31b-cloud)")
	}
	// Guard: the active provider is whatever the user last used, and firing these
	// probes at a paid endpoint would be both useless and rude.
	wantExplicit := os.Getenv("FYC_OLLAMA_URL") != ""
	if !wantExplicit && reasoning.ProviderFromHost(hostFromURL(base)) != reasoning.FamilyLocal {
		t.Fatalf("the active provider is not local (%s): pass FYC_OLLAMA_URL=http://localhost:11434/v1", base)
	}
	// A base without the OpenAI prefix is tolerated: /v1 is what Ollama exposes.
	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		base += "/v1"
	}
	t.Logf("ollama: url=%s model=%s", base, model)

	const prompt = "Think briefly, then answer in one short sentence: is 2^10 larger than 10^3?"
	chat := []any{map[string]any{"role": "user", "content": prompt}}

	postStreamingRequest(t, "chat/completions + reasoning_effort", base+"/chat/completions", key, map[string]any{
		"model": model, "stream": true, "messages": chat, "reasoning_effort": "high",
	})
	postStreamingRequest(t, "chat/completions + think", base+"/chat/completions", key, map[string]any{
		"model": model, "stream": true, "messages": chat, "think": true,
	})
	postStreamingRequest(t, "responses + reasoning.effort", base+"/responses", key, map[string]any{
		"model": model, "stream": true, "input": prompt, "reasoning": map[string]any{"effort": "high"},
	})
	postStreamingRequest(t, "responses + think", base+"/responses", key, map[string]any{
		"model": model, "stream": true, "input": prompt, "think": true,
	})
}

// postStreamingRequest sends one streaming request and logs the raw frames: the first lines plus
// the union of the keys found in each delta, which is what tells a thinking
// trace apart from the answer.
func postStreamingRequest(t *testing.T, label, url, key string, body map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("%s: marshal: %v", label, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: request: %v", label, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}

	t.Logf("── %s", label)
	t.Logf("   request: %s", clipLog(string(raw)))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("   request failed: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(resp.Body)
		t.Logf("   HTTP %d: %s", resp.StatusCode, clipLog(string(errBody)))
		return
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	deltaKeys := map[string]bool{}
	shown := 0
	total := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		total++
		if shown < 12 {
			t.Logf("   %s", clipLog(data))
			shown++
		}
		for k := range streamFrameKeys(data) {
			deltaKeys[k] = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Logf("   stream read error: %v", err)
	}
	t.Logf("   frames=%d, payload keys=%v", total, sortedSet(deltaKeys))
	hasTrace := false
	for k := range deltaKeys {
		if strings.Contains(k, "reasoning") || strings.Contains(k, "thinking") {
			hasTrace = true
		}
	}
	if hasTrace {
		t.Logf("   ✓ this variant returned a thinking trace")
	} else {
		t.Logf("   ✗ no thinking trace in this variant")
	}
}

// streamFrameKeys collects the interesting keys of one SSE frame: for a chat
// completion the delta keys, for a Responses event the event type.
func streamFrameKeys(data string) map[string]bool {
	out := map[string]bool{}
	var frame map[string]any
	if json.Unmarshal([]byte(data), &frame) != nil {
		return out
	}
	if kind, ok := frame["type"].(string); ok {
		out[kind] = true
	}
	choices, _ := frame["choices"].([]any)
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		for _, holder := range []string{"delta", "message"} {
			if d, ok := choice[holder].(map[string]any); ok {
				for k, v := range d {
					if s, isStr := v.(string); isStr && s == "" {
						continue
					}
					out[holder+"."+k] = true
				}
			}
		}
	}
	return out
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
