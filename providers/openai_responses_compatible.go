package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"fuckyourcli/reasoning"
	"fuckyourcli/types"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIResponsesCompatibleProvider implements the Provider interface for OpenAI Responses API-compatible endpoints.
type OpenAIResponsesCompatibleProvider struct {
	BaseURL         string
	APIKey          string
	Model           string
	Name            string
	Temperature     float64
	ThinkingEnabled bool
	ReasoningEffort string
	// Reasoning is the explicit thinking intent; unset falls back to the legacy
	// toggles above.
	Reasoning reasoning.Intent
	// ReasoningOverride, when non-nil, forces a profile instead of detection.
	ReasoningOverride *reasoning.Profile
	reasoningSet      bool
}

// NewOpenAIResponsesCompatibleProvider creates a new OpenAI Responses API-compatible provider.
func NewOpenAIResponsesCompatibleProvider(baseURL, apiKey, model, name string, temperature float64, thinkingEnabled bool, reasoningEffort string) *OpenAIResponsesCompatibleProvider {
	return &OpenAIResponsesCompatibleProvider{
		BaseURL:         baseURL,
		APIKey:          apiKey,
		Model:           model,
		Name:            name,
		Temperature:     temperature,
		ThinkingEnabled: thinkingEnabled,
		ReasoningEffort: reasoningEffort,
	}
}

// SetReasoning overrides the legacy boolean toggles with an explicit intent and,
// optionally, a forced profile.
func (p *OpenAIResponsesCompatibleProvider) SetReasoning(intent reasoning.Intent, override *reasoning.Profile) {
	p.Reasoning = intent
	p.ReasoningOverride = override
	p.reasoningSet = true
}

// StreamChat streams a chat completion from a Responses API-compatible endpoint.
func (p *OpenAIResponsesCompatibleProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	var instructions string
	var chatMessages []types.Message
	for _, msg := range messages {
		if msg.Role == "system" {
			if instructions == "" {
				instructions = msg.Content
			} else {
				instructions += "\n\n" + msg.Content
			}
			continue
		}
		chatMessages = append(chatMessages, msg)
	}
	if instructions == "" {
		instructions = "You are a helpful assistant."
	}

	reqBody := map[string]interface{}{
		"model":        p.Model,
		"instructions": instructions,
		"input":        convertMessagesToCodexInput(chatMessages, p.Model),
		"stream":       true,
		"store":        false,
	}
	intent := p.Reasoning
	if !p.reasoningSet {
		intent = legacyReasoningIntent(p.ThinkingEnabled, p.ReasoningEffort)
	}
	if p.Temperature > 0 {
		reqBody["temperature"] = p.Temperature
	}
	addResponsesReasoning(reqBody, intent, p.ReasoningOverride, p.Model, p.BaseURL)
	if len(tools) > 0 {
		reqBody["tools"] = convertToolsToCodex(tools)
		reqBody["tool_choice"] = "auto"
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}

	url := p.BaseURL
	if !strings.HasSuffix(url, "/responses") {
		if strings.HasSuffix(url, "/") {
			url += "responses"
		} else {
			url += "/responses"
		}
	}

	stream := newStreamRequest(ctx, 240*time.Second)
	defer stream.close()
	httpCtx := stream.ctx

	req, err := http.NewRequestWithContext(httpCtx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}

	resp, err := doWithRetry(httpCtx, nil, req, sink)
	if err != nil {
		if ctx.Err() != nil {
			return &types.StreamResult{Interrupted: true}, nil
		}
		return nil, fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Responses API error (%d): %s", resp.StatusCode, string(body))
	}

	return parseCodexStreamResponse(ctx, stream.body(resp.Body), p.ThinkingEnabled, sink)
}

// GetOpenAIResponsesCompatibleModels fetches available models from a Responses API-compatible provider.
func GetOpenAIResponsesCompatibleModels(baseURL, apiKey string) ([]string, error) {
	return GetOpenAICompatibleModels(baseURL, apiKey)
}
