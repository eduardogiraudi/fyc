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

const anthropicVersion = "2023-06-01"

// AnthropicProvider implements the Provider interface for Anthropic Messages API.
type AnthropicProvider struct {
	BaseURL     string
	APIKey      string
	Model       string
	Name        string
	Temperature float64
	MaxTokens   int
	// Reasoning is the explicit thinking intent. Unset keeps the previous
	// behaviour: no thinking fields are sent at all (the defensive default).
	Reasoning reasoning.Intent
	// ReasoningOverride, when non-nil, forces a profile instead of detection.
	ReasoningOverride *reasoning.Profile
	reasoningSet      bool
}

// NewAnthropicProvider creates a new Anthropic Messages API provider.
func NewAnthropicProvider(baseURL, apiKey, model, name string, temperature float64) *AnthropicProvider {
	return &AnthropicProvider{
		BaseURL:     baseURL,
		APIKey:      apiKey,
		Model:       model,
		Name:        name,
		Temperature: temperature,
		MaxTokens:   65536,
	}
}

// SetReasoning overrides the default thinking intent and, optionally, forces a
// profile.
func (p *AnthropicProvider) SetReasoning(intent reasoning.Intent, override *reasoning.Profile) {
	p.Reasoning = intent
	p.ReasoningOverride = override
	p.reasoningSet = true
}

type anthropicContentBlock struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	// Input is a pointer so that a present-but-empty object ({}) is not
	// dropped by omitempty: Anthropic requires "input" on every tool_use
	// block, even when the tool takes no arguments.
	Input     *map[string]interface{} `json:"input,omitempty"`
	ToolUseID string                  `json:"tool_use_id,omitempty"`
	Content   interface{}             `json:"content,omitempty"`
	Source    *anthropicImageSource   `json:"source,omitempty"`
}

// anthropicImageSource is the base64 image source used inside content blocks
// (e.g. images returned by tools within a tool_result).
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type anthropicRequest struct {
	Model       string             `json:"model"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	ToolChoice  map[string]string  `json:"tool_choice,omitempty"`
	Stream      bool               `json:"stream"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
}

type anthropicStreamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type  string                 `json:"type"`
		ID    string                 `json:"id"`
		Name  string                 `json:"name"`
		Input map[string]interface{} `json:"input"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		PartialJSON string `json:"partial_json"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// StreamChat streams a response from Anthropic Messages API.
func (p *AnthropicProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	system, anthropicMessages := convertMessagesToAnthropic(messages)
	reqBody := anthropicRequest{
		Model:       p.Model,
		System:      system,
		Messages:    anthropicMessages,
		Stream:      true,
		MaxTokens:   p.MaxTokens,
		Temperature: p.Temperature,
	}
	if len(tools) > 0 {
		reqBody.Tools = convertToolsToAnthropic(tools)
		reqBody.ToolChoice = map[string]string{"type": "auto"}
	}

	// Thinking/reasoning: translate the intent into the Messages API shape
	// (thinking block + budget, or output_config.effort on newer models). An
	// unresolved intent keeps the previous behaviour: no thinking fields.
	intent := p.Reasoning
	if !p.reasoningSet {
		intent = reasoning.Intent{Mode: reasoning.ModeDefault}
	}
	profile, _ := reasoning.Lookup(reasoning.AnthropicMessages, "", p.Model, p.ReasoningOverride)
	plan, _ := reasoning.Translate(intent, profile)
	// The Messages API requires budget_tokens < max_tokens (the thinking budget
	// counts toward max_tokens). The default cap is generous, but a user or
	// catalog budget can exceed it, so raise the cap rather than let the API
	// reject the whole request with a 400.
	reqBody.MaxTokens = anthropicMaxTokens(reqBody.MaxTokens, plan)

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}
	jsonData, err = applyReasoningPlan(jsonData, plan)
	if err != nil {
		return nil, fmt.Errorf("error applying reasoning plan: %w", err)
	}

	url := strings.TrimRight(p.BaseURL, "/") + "/messages"
	stream := newStreamRequest(ctx, 240*time.Second)
	defer stream.close()
	httpCtx := stream.ctx

	req, err := http.NewRequestWithContext(httpCtx, http.MethodPost, url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("anthropic-version", anthropicVersion)
	if p.APIKey != "" {
		req.Header.Set("x-api-key", p.APIKey)
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
		return nil, fmt.Errorf("Anthropic API error (%d): %s", resp.StatusCode, string(body))
	}

	return parseAnthropicStreamResponse(ctx, stream.body(resp.Body), sink)
}

// anthropicMaxTokens returns max_tokens, raised when needed so that a thinking
// budget carried by the plan stays strictly below it (the Messages API requires
// budget_tokens < max_tokens) with room for the answer to follow the thinking.
func anthropicMaxTokens(maxTokens int, plan reasoning.Plan) int {
	budget := anthropicPlanBudget(plan)
	if budget <= 0 || budget < maxTokens {
		return maxTokens
	}
	// Thinking tokens count toward max_tokens, so the answer needs room past the
	// budget: at least 4k, or half the previous cap when that is larger. The bare
	// budget+1 would leave the reply a single token.
	room := maxTokens / 2
	if room < 4096 {
		room = 4096
	}
	return budget + room
}

// anthropicPlanBudget returns the thinking.budget_tokens the resolved plan will
// set, or 0 when the plan does not carry a thinking budget.
func anthropicPlanBudget(plan reasoning.Plan) int {
	thinking, ok := plan.Set["thinking"].(map[string]any)
	if !ok {
		return 0
	}
	switch v := thinking["budget_tokens"].(type) {
	case int:
		return v
	case float64:
		return int(v)
	}
	return 0
}

func convertMessagesToAnthropic(messages []types.Message) (string, []anthropicMessage) {
	var systemParts []string
	var result []anthropicMessage
	// lastWasToolResult tracks whether the most recently appended message is a
	// user message holding only tool_result blocks. Consecutive "tool" messages
	// (parallel tool calls) must be coalesced into a single user message:
	// Anthropic requires every tool_use block to be answered by the tool_result
	// blocks of the message immediately following the assistant turn.
	lastWasToolResult := false

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			if msg.Content != "" {
				systemParts = append(systemParts, msg.Content)
			}
		case "user":
			lastWasToolResult = false
			if msg.Content != "" {
				result = append(result, anthropicMessage{Role: "user", Content: []anthropicContentBlock{{Type: "text", Text: msg.Content}}})
			}
		case "assistant":
			lastWasToolResult = false
			blocks := make([]anthropicContentBlock, 0)
			// A thinking block is only accepted together with the signature that
			// proves Claude produced it: without one the Messages API rejects the
			// whole request ("thinking.signature: Field required"). The signature
			// is empty when the history was recorded by another provider (a hot
			// provider/model switch) or by a build/gateway that dropped the
			// signature_delta event, so drop the block instead of failing every
			// following request. Omitting an earlier turn's thinking is allowed;
			// only the current turn's blocks must be passed back.
			if msg.ReasoningContent != "" && msg.ReasoningSignature != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "thinking", Thinking: msg.ReasoningContent, Signature: msg.ReasoningSignature})
			}
			if msg.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: msg.Content})
			}
			for _, tc := range msg.ToolCalls {
				input := tc.Function.Arguments
				if input == nil && tc.Function.ArgumentsRaw != "" {
					_ = json.Unmarshal([]byte(tc.Function.ArgumentsRaw), &input)
				}
				if input == nil {
					input = make(map[string]interface{})
				}
				blocks = append(blocks, anthropicContentBlock{Type: "tool_use", ID: tc.ID, Name: tc.Function.Name, Input: &input})
			}
			if len(blocks) > 0 {
				result = append(result, anthropicMessage{Role: "assistant", Content: blocks})
			}
		case "tool":
			toolUseID := msg.ToolCallID
			if toolUseID == "" {
				toolUseID = msg.Name
			}
			content := make([]anthropicContentBlock, 0, len(msg.Images)+1)
			for _, img := range msg.Images {
				content = append(content, anthropicContentBlock{
					Type: "image",
					Source: &anthropicImageSource{
						Type:      "base64",
						MediaType: img.MIMEType,
						Data:      img.Data,
					},
				})
			}
			// Only emit a text block when there is actual text. An empty text
			// block marshals to {"type":"text"} (Text carries omitempty) and
			// Anthropic rejects it with "missing field `text`". Tools that
			// produce no output (e.g. a silent sh command) leave Content empty.
			if msg.Content != "" {
				content = append(content, anthropicContentBlock{Type: "text", Text: msg.Content})
			}
			block := anthropicContentBlock{Type: "tool_result", ToolUseID: toolUseID}
			// When there is neither text nor images, omit "content" entirely:
			// the field is optional for tool_result, and an empty array is a
			// less portable request than a missing field.
			if len(content) > 0 {
				block.Content = content
			}
			if lastWasToolResult && len(result) > 0 {
				last := &result[len(result)-1]
				last.Content = append(last.Content, block)
			} else {
				result = append(result, anthropicMessage{Role: "user", Content: []anthropicContentBlock{block}})
				lastWasToolResult = true
			}
		}
	}

	return strings.Join(systemParts, "\n\n"), result
}

func convertToolsToAnthropic(tools []types.Tool) []anthropicTool {
	anthropicTools := make([]anthropicTool, 0, len(tools))
	for _, tool := range tools {
		properties := make(map[string]interface{})
		for name, prop := range tool.Function.Parameters.Properties {
			properties[name] = prop.ToSchema()
		}
		schema := map[string]interface{}{
			"type":       "object",
			"properties": properties,
		}
		if len(tool.Function.Parameters.Required) > 0 {
			schema["required"] = tool.Function.Parameters.Required
		}
		anthropicTools = append(anthropicTools, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: schema,
		})
	}
	return anthropicTools
}

func parseAnthropicStreamResponse(ctx context.Context, body io.ReadCloser, sink types.OutputSink) (*types.StreamResult, error) {
	result := &types.StreamResult{ToolCalls: make([]types.ToolCall, 0)}
	lineCh, stopReader := readStreamLines(ctx, body)
	defer stopReader()

	diffCraft := newDiffCraftTracker(sink)
	defer diffCraft.finish()

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var reasoningSignature string
	toolCalls := make(map[int]*types.ToolCall)
	argBuilders := make(map[int]*strings.Builder)
	// sawMessageStop tracks the terminal event of the stream: when it is missing
	// the response was cut off (network drop, proxy idle timeout, http deadline,
	// oversized line) and must not be reported as a completed turn.
	sawMessageStop := false
	// stopReason is the last message_delta stop_reason (Anthropic only reports it
	// there, never in the content events).
	stopReason := ""

	flushToolCall := func(idx int) {
		tc := toolCalls[idx]
		if tc == nil || tc.Function.Name == "" {
			return
		}
		raw := argBuilders[idx].String()
		var parsed map[string]interface{}
		if raw != "" {
			_ = json.Unmarshal([]byte(raw), &parsed)
		}
		if parsed == nil {
			parsed = make(map[string]interface{})
		}
		tc.Function.Arguments = parsed
		tc.Function.ArgumentsRaw = raw
		result.ToolCalls = append(result.ToolCalls, *tc)
		delete(toolCalls, idx)
		delete(argBuilders, idx)
	}

	var dataBuf strings.Builder

loop:
	for {
		select {
		case <-ctx.Done():
			sink.WriteInterrupt()
			result.Content = fullContent.String()
			result.ReasoningContent = fullReasoning.String()
			result.ReasoningSignature = reasoningSignature
			result.Interrupted = true
			return result, nil

		case sl := <-lineCh:
			if sl.done {
				if sl.err != nil {
					sink.Finalize()
					return nil, fmt.Errorf("anthropic stream interrupted before message_stop: %w", sl.err)
				}
				break loop
			}
			line := sl.text
			if strings.HasPrefix(line, "data: ") {
				dataBuf.WriteString(strings.TrimPrefix(line, "data: "))
				continue
			}
			if line != "" {
				continue
			}
			data := strings.TrimSpace(dataBuf.String())
			dataBuf.Reset()
			if data == "" {
				continue
			}

			var event anthropicStreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			switch event.Type {
			case "message_start":
				result.PromptTokens = event.Message.Usage.InputTokens
				result.CompletionTokens = event.Message.Usage.OutputTokens
				if event.Message.Usage.CacheReadInputTokens > 0 {
					result.CachedTokens = event.Message.Usage.CacheReadInputTokens
				}
			case "content_block_start":
				if event.ContentBlock.Type == "tool_use" {
					toolCalls[event.Index] = &types.ToolCall{
						ID:   event.ContentBlock.ID,
						Type: "function",
						Function: types.ToolCallFunction{
							Name:      event.ContentBlock.Name,
							Arguments: make(map[string]interface{}),
						},
					}
					argBuilders[event.Index] = &strings.Builder{}
					if event.ContentBlock.Name != "" && event.ContentBlock.Name != "replace_anchors" {
						sink.StartSpinner("executing " + event.ContentBlock.Name + "...")
					}
				}
			case "content_block_delta":
				if event.Delta.Text != "" {
					fullContent.WriteString(event.Delta.Text)
					sink.WriteText(event.Delta.Text)
				}
				if event.Delta.Thinking != "" {
					fullReasoning.WriteString(event.Delta.Thinking)
					sink.WriteReasoning(event.Delta.Thinking)
				}
				if event.Delta.Signature != "" {
					reasoningSignature = event.Delta.Signature
				}
				if event.Delta.PartialJSON != "" {
					if argBuilders[event.Index] == nil {
						argBuilders[event.Index] = &strings.Builder{}
					}
					argBuilders[event.Index].WriteString(event.Delta.PartialJSON)
					if tc := toolCalls[event.Index]; tc != nil {
						diffCraft.update(tc.Function.Name, argBuilders[event.Index].String())
					}
				}
			case "content_block_stop":
				flushToolCall(event.Index)
			case "message_delta":
				stopReason = event.Delta.StopReason
				if event.Delta.StopReason == "tool_use" {
					result.FinishReason = "tool_calls"
				} else if event.Delta.StopReason != "" {
					result.FinishReason = event.Delta.StopReason
				}
				if event.Usage.InputTokens > 0 {
					result.PromptTokens = event.Usage.InputTokens
				}
				if event.Usage.OutputTokens > 0 {
					result.CompletionTokens = event.Usage.OutputTokens
				}
				if event.Usage.CacheReadInputTokens > 0 {
					result.CachedTokens = event.Usage.CacheReadInputTokens
				}
			case "error":
				return nil, fmt.Errorf("Anthropic stream error %s: %s", event.Error.Type, event.Error.Message)
			case "message_stop":
				sawMessageStop = true
				break loop
			}
		}
	}

	sink.Finalize()
	result.Content = fullContent.String()
	result.ReasoningContent = fullReasoning.String()
	result.ReasoningSignature = reasoningSignature

	// A stream that ends without message_stop is a truncated response. Without
	// this check the empty FinishReason made the caller treat it as a normally
	// completed turn, so the CLI silently returned to the prompt mid-answer.
	if !sawMessageStop {
		return nil, fmt.Errorf("anthropic stream ended before message_stop (incomplete response, %d chars of text received)", fullContent.Len())
	}

	// stop_reason values that mean "the model produced nothing usable".
	switch stopReason {
	case "refusal":
		if result.Content == "" && len(result.ToolCalls) == 0 {
			return nil, fmt.Errorf("Claude refused to answer this request (stop_reason: refusal)")
		}
	case "pause_turn":
		if len(result.ToolCalls) == 0 {
			return nil, fmt.Errorf("Claude paused the turn (stop_reason: pause_turn) and the turn cannot be resumed by this client")
		}
	}

	if len(toolCalls) != 0 {
		return nil, fmt.Errorf("anthropic stream ended with an incomplete tool call")
	}
	if err := validateStreamToolCalls(result); err != nil {
		return nil, err
	}
	return result, nil
}

// AnthropicDefaultModels returns a conservative fallback model list.
func AnthropicDefaultModels() []string {
	return []string{
		"claude-sonnet-4-5",
		"claude-opus-4-5",
		"claude-haiku-4-5",
		"claude-sonnet-4-20250514",
		"claude-opus-4-20250514",
		"claude-3-haiku-20240307",
	}
}

// GetAnthropicModels fetches available Anthropic models.
func GetAnthropicModels(baseURL, apiKey string) ([]string, error) {
	url := strings.TrimRight(baseURL, "/") + "/models"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	if apiKey != "" {
		req.Header.Set("x-api-key", apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("models API error (%d): %s", resp.StatusCode, string(body))
	}

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(data.Data))
	for _, model := range data.Data {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}
	return models, nil
}
