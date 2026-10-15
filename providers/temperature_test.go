package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"fuckyourcli/reasoning"
	"fuckyourcli/types"
)

func TestStreamChatTemperaturePolicy(t *testing.T) {
	cases := []struct {
		name   string
		create func() Provider
		nested bool
	}{
		{"chat", func() Provider {
			return NewOpenAICompatibleProvider("https://example.test/v1", "", "model", "test", 0.7, false, "")
		}, false},
		{"responses", func() Provider {
			return NewOpenAIResponsesCompatibleProvider("https://example.test/v1", "", "model", "test", 0.7, false, "")
		}, false},
		{"anthropic", func() Provider { return NewAnthropicProvider("https://example.test", "", "model", "test", 0.7) }, false},
		{"gemini", func() Provider { return NewGeminiProvider("test-key", "model", 0.7) }, true},
	}
	previous := providerHTTPClient
	t.Cleanup(func() { providerHTTPClient = previous })
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, forbids := range []bool{false, true} {
				for _, mode := range []reasoning.Mode{reasoning.ModeDefault, reasoning.ModeOff, reasoning.ModeEffort} {
					var body map[string]any
					providerHTTPClient = &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
						if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
							return nil, err
						}
						stream := "data: [DONE]\n\n"
						if tc.name == "anthropic" {
							stream = "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(stream))}, nil
					})}
					p := tc.create()
					profile := &reasoning.Profile{ForbidsTemperature: forbids, Always: map[string]any{"testMarker": true}}
					if tc.nested {
						profile.Always["generationConfig.thinkingConfig.includeThoughts"] = true
					}
					p.(interface {
						SetReasoning(reasoning.Intent, *reasoning.Profile)
					}).SetReasoning(reasoning.Intent{Mode: mode, Effort: "high"}, profile)
					if _, err := p.StreamChat(context.Background(), []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink()); err != nil {
						t.Fatal(err)
					}
					if body["testMarker"] != true {
						t.Fatalf("reasoning plan was not applied: %v", body)
					}
					generation := body
					if tc.nested {
						generation = body["generationConfig"].(map[string]any)
						thinking := generation["thinkingConfig"].(map[string]any)
						if thinking["includeThoughts"] != true {
							t.Fatalf("omission lost Gemini thinking config: %v", body)
						}
					}
					value, sent := generation["temperature"]
					if sent == forbids || (sent && value != 0.7) {
						t.Fatalf("mode=%v forbids=%v: temperature=%v sent=%v", mode, forbids, value, sent)
					}
				}
			}
		})
	}
}
