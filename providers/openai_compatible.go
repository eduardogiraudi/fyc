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
	"sort"
	"strings"
	"time"
)

// OpenAICompatibleProvider implements the Provider interface for OpenAI-compatible APIs
type OpenAICompatibleProvider struct {
	BaseURL     string
	APIKey      string
	Model       string
	Name        string
	Temperature float64
	// ThinkingEnabled and ReasoningEffort are the legacy toggles. When no
	// explicit Intent is set (see SetReasoning) they are translated into one.
	ThinkingEnabled bool
	ReasoningEffort string
	// Reasoning is the explicit thinking intent. It wins over the legacy
	// booleans once SetReasoning has been called.
	Reasoning reasoning.Intent
	// ReasoningOverride, when non-nil, forces a profile instead of detection.
	ReasoningOverride *reasoning.Profile
	reasoningSet      bool
}

// NewOpenAICompatibleProvider creates a new OpenAI-compatible provider
func NewOpenAICompatibleProvider(baseURL, apiKey, model, name string, temperature float64, thinkingEnabled bool, reasoningEffort string) *OpenAICompatibleProvider {
	return &OpenAICompatibleProvider{
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
func (p *OpenAICompatibleProvider) SetReasoning(intent reasoning.Intent, override *reasoning.Profile) {
	p.Reasoning = intent
	p.ReasoningOverride = override
	p.reasoningSet = true
}

// OpenAI API structures
type OpenAIContent struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIMessage struct {
	Role             string `json:"role"`
	Content          string `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Reasoning and Thinking are the other names the same piece of text travels
	// under (see reasoningDelta): they are only ever read from a response.
	Reasoning  string           `json:"reasoning,omitempty"`
	Thinking   string           `json:"thinking,omitempty"`
	Name       string           `json:"name,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// reasoningDelta returns the reasoning text carried by one streamed delta.
// There is no single field name in the wild: DeepSeek and most local servers
// we support use "reasoning_content", some OpenAI-compatible gateways use
// "reasoning", and Ollama's native protocol calls it "thinking".
func (m OpenAIMessage) reasoningDelta() string {
	for _, v := range []string{m.ReasoningContent, m.Reasoning, m.Thinking} {
		if v != "" {
			return v
		}
	}
	return ""
}

type OpenAIToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type OpenAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type OpenAIRequest struct {
	Model         string          `json:"model"`
	Messages      []OpenAIMessage `json:"messages"`
	Stream        bool            `json:"stream"`
	StreamOptions *StreamOptions  `json:"stream_options,omitempty"`
	Tools         []OpenAITool    `json:"tools,omitempty"`
	//MaxTokens       int             `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	CachePrompt *bool   `json:"cache_prompt,omitempty"` // llama.cpp: reuse KV cache across requests
}

type OpenAIResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int           `json:"index"`
		Delta        OpenAIMessage `json:"delta"`
		FinishReason string        `json:"finish_reason"`
		Message      OpenAIMessage `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		TotalTokens         int `json:"total_tokens"`
		PromptTokensDetails *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details,omitempty"`
	} `json:"usage,omitempty"`
}

// StreamChat streams a chat completion from OpenAI-compatible API.
// If ctx is cancelled during generation, it interrupts the stream and
// returns what has been received so far with Interrupted=true.
func (p *OpenAICompatibleProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	openaiMessages := convertMessagesToOpenAI(messages)
	openaiTools := convertToolsToOpenAI(tools)

	reqBody := OpenAIRequest{
		Model:       p.Model,
		Messages:    openaiMessages,
		Stream:      true,
		Temperature: p.Temperature,
		//MaxTokens:   8192,
	}

	// Thinking/reasoning: resolve the profile for this endpoint+model and
	// translate the user intent into wire fields. An unrecognised model sends
	// nothing (the defensive default).
	intent := p.Reasoning
	if !p.reasoningSet {
		intent = legacyReasoningIntent(p.ThinkingEnabled, p.ReasoningEffort)
	}
	profile, _ := reasoning.Lookup(reasoning.ChatCompletions, p.BaseURL, p.Model, p.ReasoningOverride)
	plan, _ := reasoning.Translate(intent, profile)
	if len(openaiTools) > 0 {
		reqBody.Tools = openaiTools
	}

	reqBody.StreamOptions = &StreamOptions{IncludeUsage: true}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}
	jsonData, err = applyReasoningPlan(jsonData, plan)
	if err != nil {
		return nil, fmt.Errorf("error applying reasoning plan: %w", err)
	}

	url := p.BaseURL
	if !strings.HasSuffix(url, "/chat/completions") {
		if strings.HasSuffix(url, "/") {
			url = url + "chat/completions"
		} else {
			url = url + "/chat/completions"
		}
	}

	stream := newStreamRequest(ctx, 240*time.Second)
	defer stream.close()
	httpCtx := stream.ctx

	req, err := http.NewRequestWithContext(httpCtx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
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
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	return parseOpenAIStreamResponse(ctx, stream.body(resp.Body), p.ThinkingEnabled, sink)
}

func convertMessagesToOpenAI(messages []types.Message) []OpenAIMessage {
	var openaiMessages []OpenAIMessage

	for _, msg := range messages {
		openaiMsg := OpenAIMessage{
			Role:    msg.Role,
			Content: msg.Content,
		}

		// Chat Completions tool messages cannot carry images. If history
		// contains image attachments (e.g. screenshots taken with another
		// provider), replace them with a text placeholder instead of failing.
		if len(msg.Images) > 0 {
			const note = "[image omitted: the OpenAI Completions API does not support images in tool results]"
			if openaiMsg.Content == "" {
				openaiMsg.Content = note
			} else {
				openaiMsg.Content += "\n" + note
			}
		}

		if msg.Role == "assistant" && msg.ReasoningContent != "" {
			openaiMsg.ReasoningContent = msg.ReasoningContent
		}

		if msg.Name != "" {
			openaiMsg.Name = msg.Name
		}

		if msg.ToolCallID != "" {
			openaiMsg.ToolCallID = msg.ToolCallID
		}

		if len(msg.ToolCalls) > 0 {
			openaiMsg.ToolCalls = make([]OpenAIToolCall, len(msg.ToolCalls))
			for i, tc := range msg.ToolCalls {
				argsStr := tc.Function.ArgumentsRaw
				if argsStr == "" {
					b, _ := json.Marshal(tc.Function.Arguments)
					argsStr = string(b)
				}
				openaiMsg.ToolCalls[i] = OpenAIToolCall{
					ID:   tc.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      tc.Function.Name,
						Arguments: argsStr,
					},
				}
			}
		}

		openaiMessages = append(openaiMessages, openaiMsg)
	}

	return openaiMessages
}

type orderedProp struct {
	name   string
	schema interface{}
}

func convertToolsToOpenAI(tools []types.Tool) []OpenAITool {
	var openaiTools []OpenAITool

	for _, tool := range tools {
		// Collect property names sorted — deterministic JSON serialization for prefix caching
		propNames := make([]string, 0, len(tool.Function.Parameters.Properties))
		for name := range tool.Function.Parameters.Properties {
			propNames = append(propNames, name)
		}
		sort.Strings(propNames)

		orderedProps := make([]orderedProp, 0, len(propNames))
		for _, name := range propNames {
			prop := tool.Function.Parameters.Properties[name]
			orderedProps = append(orderedProps, orderedProp{name, prop.ToSchema()})
		}

		// Marshal properties as a JSON object with sorted keys
		propertiesJSON := marshalOrderedProperties(orderedProps)

		// Build required list sorted
		required := make([]string, 0)
		for _, req := range tool.Function.Parameters.Required {
			required = append(required, req)
		}
		sort.Strings(required)

		params := map[string]interface{}{
			"type":       "object",
			"properties": json.RawMessage(propertiesJSON),
		}
		if len(required) > 0 {
			params["required"] = required
		}

		openaiTools = append(openaiTools, OpenAITool{
			Type: "function",
			Function: OpenAIFunction{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  params,
			},
		})
	}

	return openaiTools
}

// marshalOrderedProperties serializes ordered name+schema pairs into a JSON object
// with keys in the provided order, guaranteeing a stable byte sequence for prefix caching.
func marshalOrderedProperties(props []orderedProp) string {
	var b strings.Builder
	b.WriteString("{")
	for i, p := range props {
		if i > 0 {
			b.WriteString(",")
		}
		keyBytes, _ := json.Marshal(p.name)
		valBytes, _ := json.Marshal(p.schema)
		b.Write(keyBytes)
		b.WriteString(":")
		b.Write(valBytes)
	}
	b.WriteString("}")
	return b.String()
}

// parseOpenAIStreamResponse reads the SSE stream and respects context cancellation.
func parseOpenAIStreamResponse(ctx context.Context, body io.ReadCloser, thinkingEnabled bool, sink types.OutputSink) (*types.StreamResult, error) {
	result := &types.StreamResult{
		ToolCalls: make([]types.ToolCall, 0),
	}
	var reasoningBuffer strings.Builder
	var fullReasoning strings.Builder

	sawTerminal := false
	allowUnmarkedEOF := true
	var fullContent strings.Builder

	toolCallBuilders := make(map[int]*types.ToolCall)
	argBuilders := make(map[int]*strings.Builder)

	diffCraft := newDiffCraftTracker(sink)
	defer diffCraft.finish()

	flushToolCalls := func() {
		for idx, tcb := range toolCallBuilders {
			if tcb != nil && tcb.Function.Name != "" {
				raw := argBuilders[idx].String()
				var parsedArgs map[string]interface{}
				if err := json.Unmarshal([]byte(raw), &parsedArgs); err == nil {
					tcb.Function.Arguments = parsedArgs
				}
				tcb.Function.ArgumentsRaw = raw
				result.ToolCalls = append(result.ToolCalls, *tcb)
			}
		}
		toolCallBuilders = make(map[int]*types.ToolCall)
		argBuilders = make(map[int]*strings.Builder)
	}

	lineCh, stopReader := readStreamLines(ctx, body)
	defer stopReader()

loop:
	for {
		select {
		case <-ctx.Done():
			sink.WriteInterrupt()
			result.Content = fullContent.String()
			result.ReasoningContent = fullReasoning.String()
			result.Interrupted = true
			return result, nil

		case sl := <-lineCh:
			if sl.done {
				if sl.err != nil {
					sink.Finalize()
					return nil, fmt.Errorf("chat completions stream interrupted: %w", sl.err)
				}
				break loop
			}

			line := sl.text
			if line == "" {
				continue
			}
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				sawTerminal = true
				break loop
			}

			var response OpenAIResponse
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				allowUnmarkedEOF = false
				continue
			}

			for _, choice := range response.Choices {
				if choice.Delta.Content != "" {
					fullContent.WriteString(choice.Delta.Content)
					sink.WriteText(choice.Delta.Content)
				}

				if delta := choice.Delta.reasoningDelta(); delta != "" {
					reasoningBuffer.WriteString(delta)
					fullReasoning.WriteString(delta)
					sink.WriteReasoning(delta)

					buf := reasoningBuffer.String()
					if strings.Contains(buf, "<tool_call>") {
						allowUnmarkedEOF = false
						if strings.Contains(buf, "</tool_call>") {
							prevLen := len(result.ToolCalls)
							parseXMLToolCalls(buf, result)
							result.FinishReason = "tool_calls"
							reasoningBuffer.Reset()
							for i := prevLen; i < len(result.ToolCalls); i++ {
								tc := result.ToolCalls[i]
								if tc.Function.Name == "replace_anchors" {
									if raw, err := json.Marshal(tc.Function.Arguments); err == nil {
										diffCraft.update(tc.Function.Name, string(raw))
									}
								}
							}
						} else {
							name := extractAttr(buf, "<function=", ">")
							if name != "" {
								if name != "replace_anchors" {
									sink.StartSpinner("executing " + name + "...")
								}
							}
						}
					}
				}

				if len(choice.Delta.ToolCalls) > 0 {
					allowUnmarkedEOF = false
					for _, tc := range choice.Delta.ToolCalls {
						if tc.Function.Name != "" && tc.Function.Name != "replace_anchors" {
							sink.StartSpinner("executing " + tc.Function.Name + "...")
						}
					}
				}

				for _, tc := range choice.Delta.ToolCalls {
					idx := tc.Index

					if toolCallBuilders[idx] == nil {
						toolCallBuilders[idx] = &types.ToolCall{
							ID:   tc.ID,
							Type: tc.Type,
							Function: types.ToolCallFunction{
								Name:      tc.Function.Name,
								Arguments: make(map[string]interface{}),
							},
						}
						argBuilders[idx] = &strings.Builder{}
					}

					if tc.ID != "" {
						toolCallBuilders[idx].ID = tc.ID
					}
					if tc.Function.Name != "" {
						toolCallBuilders[idx].Function.Name = tc.Function.Name
					}
					if tc.Function.Arguments != "" {
						argBuilders[idx].WriteString(tc.Function.Arguments)
						diffCraft.update(toolCallBuilders[idx].Function.Name, argBuilders[idx].String())
					}
				}

				if choice.FinishReason != "" {
					sawTerminal = true
					// XML calls discovered in reasoning still need a tool round trip
					// even when the server reports an ordinary text stop.
					if len(result.ToolCalls) == 0 || choice.FinishReason != "stop" {
						result.FinishReason = choice.FinishReason
					}
					if choice.FinishReason == "tool_calls" {
						flushToolCalls()
					}
				}
			}

			if response.Usage != nil {
				result.PromptTokens = response.Usage.PromptTokens
				result.CompletionTokens = response.Usage.CompletionTokens
				if response.Usage.PromptTokensDetails != nil {
					result.CachedTokens = response.Usage.PromptTokensDetails.CachedTokens
				}
			}
		}
	}

	sink.Finalize()
	result.Content = fullContent.String()
	result.ReasoningContent = fullReasoning.String()
	// Some compatible servers omit terminal metadata on text-only responses.
	if !sawTerminal && (!allowUnmarkedEOF || strings.TrimSpace(result.Content) == "") {
		return nil, fmt.Errorf("chat completions stream ended before a terminal event (incomplete response)")
	}
	if len(toolCallBuilders) != 0 {
		return nil, fmt.Errorf("chat completions stream ended with an incomplete tool call")
	}
	if err := validateStreamToolCalls(result); err != nil {
		return nil, err
	}
	return result, nil
}

// GetOpenAICompatibleModels fetches available models from an OpenAI-compatible API
func GetOpenAICompatibleModels(baseURL, apiKey string) ([]string, error) {
	url := baseURL
	if !strings.HasSuffix(url, "/models") {
		if strings.HasSuffix(url, "/") {
			url = url + "models"
		} else {
			url = url + "/models"
		}
	}

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}

	var models []string
	for _, model := range data.Data {
		models = append(models, model.ID)
	}

	return models, nil
}

func parseXMLToolCalls(xml string, result *types.StreamResult) {
	blocks := extractBetween(xml, "<tool_call>", "</tool_call>")

	for _, b := range blocks {
		name := extractAttr(b, "<function=", ">")
		if name == "" {
			continue
		}

		argsMap := make(map[string]interface{})
		remaining := b
		for {
			paramTagStart := strings.Index(remaining, "<parameter=")
			if paramTagStart == -1 {
				break
			}
			gtPos := strings.Index(remaining[paramTagStart:], ">")
			if gtPos == -1 {
				break
			}
			paramKey := remaining[paramTagStart+len("<parameter=") : paramTagStart+gtPos]
			paramKey = strings.TrimSpace(paramKey)

			afterOpen := remaining[paramTagStart+gtPos+1:]
			closeTag := "</parameter>"
			closePos := strings.Index(afterOpen, closeTag)
			if closePos == -1 {
				break
			}
			paramValue := strings.TrimSpace(afterOpen[:closePos])
			if paramKey != "" {
				argsMap[paramKey] = paramValue
			}
			remaining = afterOpen[closePos+len(closeTag):]
		}

		result.ToolCalls = append(result.ToolCalls, types.ToolCall{
			ID:   "xml_call",
			Type: "function",
			Function: types.ToolCallFunction{
				Name:      name,
				Arguments: argsMap,
			},
		})
	}
}

func extractBetween(s, start, end string) []string {
	var res []string
	for {
		i := strings.Index(s, start)
		if i == -1 {
			break
		}
		s = s[i+len(start):]
		j := strings.Index(s, end)
		if j == -1 {
			break
		}
		res = append(res, s[:j])
		s = s[j+len(end):]
	}
	return res
}

func extractBetweenSingle(s, start, end string) string {
	i := strings.Index(s, start)
	if i == -1 {
		return ""
	}
	s = s[i+len(start):]
	j := strings.Index(s, end)
	if j == -1 {
		return ""
	}
	return s[:j]
}

func extractAttr(s, start, end string) string {
	return strings.Trim(extractBetweenSingle(s, start, end), " ")
}
