package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fuckyourcli/logger"
	"fuckyourcli/types"
)

func providerLogEvents(t *testing.T, session string) []logger.Event {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(os.Getenv("FYC_CACHE_DIR"), "logs", session, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var events []logger.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e logger.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	return events
}

func TestLoggingAllProviderTransportsAndRetries(t *testing.T) {
	for _, kind := range []string{"openai_compatible", "openai_responses_compatible", "anthropic", "gemini", "codex"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("FYC_CACHE_DIR", t.TempDir())
			t.Setenv("FYC_LOG_DEBUG", "")
			calls := 0
			old := providerHTTPClient
			providerHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After-Ms": {"0"}}, Body: io.NopCloser(strings.NewReader("private body"))}, nil
				}
				stream := ""
				switch kind {
				case "openai_compatible":
					stream = `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`
				case "anthropic":
					stream = `{"type":"content_block_delta","delta":{"text":"ok"}}`
				case "gemini":
					stream = `{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`
				default:
					stream = `{"type":"response.output_text.delta","delta":"ok"}`
				}
				if kind == "anthropic" {
					stream += "\n\ndata: {\"type\":\"message_stop\"}"
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: " + stream + "\n\ndata: [DONE]\n\n"))}, nil
			})}
			t.Cleanup(func() { providerHTTPClient = old })
			var p types.Provider
			switch kind {
			case "openai_compatible":
				p = NewOpenAICompatibleProvider("https://example.test", "private-key", "model", "local", 0, false, "")
			case "openai_responses_compatible":
				p = NewOpenAIResponsesCompatibleProvider("https://example.test", "private-key", "model", "local", 0, false, "")
			case "anthropic":
				p = NewAnthropicProvider("https://example.test", "private-key", "model", "local", 0)
			case "gemini":
				p = NewGeminiProvider("private-key", "model", 0)
			case "codex":
				p = NewCodexProvider("private-key", "model", 0, false, "")
			}
			ctx := logger.WithSession(context.Background(), "transport")
			result, err := WithLogging(p).StreamChat(ctx, []types.Message{{Role: "user", Content: "private prompt"}}, nil, NewSilentOutputSink())
			if err != nil || result == nil || result.Content != "ok" || calls != 2 {
				t.Fatalf("provider call: %+v, %v, calls=%d", result, err, calls)
			}
			es := providerLogEvents(t, "transport")
			if len(es) != 4 || es[1].HTTPStatus != 429 || es[1].Attempt != 1 || es[2].HTTPStatus != 200 || es[2].Attempt != 2 {
				t.Fatalf("missing retry diagnostics: %+v", es)
			}
			for _, e := range es {
				if e.RequestID != es[0].RequestID || e.Provider != kind || e.Model != "model" || e.Role != "main" {
					t.Fatalf("correlation lost: %+v", e)
				}
			}
			data, _ := json.Marshal(es)
			if strings.Contains(string(data), "private") || strings.Contains(string(data), "example.test") {
				t.Fatal("private HTTP data leaked into diagnostics")
			}
		})
	}
}

func TestCodexLoggingRedactsRefreshedCredentials(t *testing.T) {
	t.Setenv("FYC_CACHE_DIR", t.TempDir())
	t.Setenv("FYC_LOG_DEBUG", "1")
	old := providerHTTPClient
	calls := 0
	providerHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.Header.Get("Authorization") != "Bearer original-credential" {
				t.Error("original token missing")
			}
			return &http.Response{StatusCode: 401, Body: io.NopCloser(strings.NewReader("expired"))}, nil
		}
		if r.Header.Get("Authorization") != "Bearer refreshed-credential" {
			t.Error("refreshed token missing")
		}
		stream := fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\ndata: [DONE]\n\n", "original-credential refreshed-credential")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(stream))}, nil
	})}
	t.Cleanup(func() { providerHTTPClient = old })
	p := NewCodexProvider("original-credential", "model", 0, false, "")
	p.Refresh = func() (string, error) { return "refreshed-credential", nil }
	result, err := WithLogging(p).StreamChat(logger.WithSession(context.Background(), "refresh"), nil, nil, NewSilentOutputSink())
	if err != nil || result == nil || result.Content != "original-credential refreshed-credential" || calls != 2 {
		t.Fatalf("refresh changed: %+v %v", result, err)
	}
	es := providerLogEvents(t, "refresh")
	if len(es) != 4 || es[1].HTTPStatus != 401 || es[1].Attempt != 1 || es[2].HTTPStatus != 200 || es[2].Attempt != 2 {
		t.Fatalf("attempt numbers reset after refresh: %+v", es)
	}
	data, err := os.ReadFile(filepath.Join(os.Getenv("FYC_CACHE_DIR"), "logs", "refresh", "responses", es[0].RequestID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "credential") || !strings.Contains(string(data), "[REDACTED]") {
		t.Fatalf("credentials not redacted: %s", data)
	}
}

func TestCodexLoggingAttemptsSpanRetriesAndRefresh(t *testing.T) {
	t.Setenv("FYC_CACHE_DIR", t.TempDir())
	t.Setenv("FYC_LOG_DEBUG", "")
	old := providerHTTPClient
	calls, refreshes := 0, 0
	statuses := []int{429, 401, 503, 200}
	providerHTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		status := statuses[calls%len(statuses)]
		calls++
		body := "private error body"
		if status == 200 {
			body = "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n"
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Retry-After-Ms": {"0"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	t.Cleanup(func() { providerHTTPClient = old })
	p := NewCodexProvider("credential", "model", 0, false, "")
	p.Refresh = func() (string, error) {
		refreshes++
		return "refreshed-credential", nil
	}
	wrapped := WithLogging(p)
	ctx := logger.WithSession(context.Background(), "retry-refresh")
	for turn := 0; turn < 2; turn++ {
		result, err := wrapped.StreamChat(ctx, nil, nil, NewSilentOutputSink())
		if err != nil || result == nil || result.Content != "ok" {
			t.Fatalf("provider result changed: %+v %v", result, err)
		}
	}
	if calls != 8 || refreshes != 2 {
		t.Fatalf("retry behavior changed: calls=%d refreshes=%d", calls, refreshes)
	}
	es := providerLogEvents(t, "retry-refresh")
	if len(es) != 12 || es[0].RequestID == es[6].RequestID {
		t.Fatalf("logical requests not distinct: %+v", es)
	}
	for turn := 0; turn < 2; turn++ {
		for i, status := range statuses {
			e := es[turn*6+i+1]
			if e.Type != "request_http" || e.Attempt != i+1 || e.HTTPStatus != status || e.RequestID != es[turn*6].RequestID {
				t.Fatalf("bad attempt numbering/correlation: %+v", e)
			}
		}
	}
}
