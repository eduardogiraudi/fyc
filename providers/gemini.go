package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"fuckyourcli/reasoning"
	"fuckyourcli/types"
)

// GeminiProvider implements the Provider interface for Google Gemini API
type GeminiProvider struct {
	APIKey            string
	Model             string
	ContextSize       int
	Temperature       float64
	SystemInstruction string
	// Reasoning is the explicit thinking intent. Unset keeps the defensive
	// default: request thought output and let the server pick the depth.
	Reasoning reasoning.Intent
	// ReasoningOverride, when non-nil, forces a profile instead of detection.
	ReasoningOverride *reasoning.Profile
	reasoningSet      bool
}

// NewGeminiProvider creates a new Gemini provider
func NewGeminiProvider(apiKey, model string, temperature float64) *GeminiProvider {
	return &GeminiProvider{
		APIKey:      apiKey,
		Model:       model,
		Temperature: temperature,
	}
}

// SetReasoning overrides the default thinking intent and, optionally, forces a
// profile.
func (p *GeminiProvider) SetReasoning(intent reasoning.Intent, override *reasoning.Profile) {
	p.Reasoning = intent
	p.ReasoningOverride = override
	p.reasoningSet = true
}

// Gemini native API structures
type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
	Role  string       `json:"role,omitempty"`
}

type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *GeminiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
}

// GeminiInlineData carries base64 media nested inside a function response
// (multimodal function response, Gemini 3 series only).
type GeminiInlineData struct {
	MimeType    string `json:"mimeType"`
	Data        string `json:"data"`
	DisplayName string `json:"displayName,omitempty"`
}

type GeminiFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args"`
}

type GeminiFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
	// Parts carries multimodal content (e.g. screenshots from tools) for Gemini 3.
	Parts []GeminiPart `json:"parts,omitempty"`
}

// geminiModelSupportsImages reports whether the model accepts multimodal
// function responses (images nested in functionResponse parts).
func geminiModelSupportsImages(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gemini-3")
}

type GeminiFunctionDeclaration struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type GeminiTool struct {
	FunctionDeclarations []GeminiFunctionDeclaration `json:"functionDeclarations"`
}

type GeminiRequest struct {
	Contents          []GeminiContent        `json:"contents"`
	SystemInstruction *GeminiContent         `json:"system_instruction,omitempty"`
	Tools             []GeminiTool           `json:"tools,omitempty"`
	ToolConfig        map[string]interface{} `json:"toolConfig,omitempty"`
	GenerationConfig  map[string]interface{} `json:"generationConfig,omitempty"`
}

type GeminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []struct {
				Text         string              `json:"text"`
				FunctionCall *GeminiFunctionCall `json:"functionCall"`
			} `json:"parts"`
			Role string `json:"role"`
		} `json:"content"`
		FinishReason string `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata struct {
		PromptTokens     int `json:"promptTokenCount"`
		CandidatesTokens int `json:"candidatesTokenCount"`
	} `json:"usageMetadata"`
}

// StreamResult is defined here for the providers package

// StreamChat streams a chat completion from Gemini API.
// If ctx is cancelled during generation, it interrupts the stream and
// returns what has been received so far with Interrupted=true.
func (p *GeminiProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	contents := convertMessagesToGeminiNative(messages, geminiModelSupportsImages(p.Model))
	geminiTools := convertToolsToGeminiNative(tools)

	reqBody := GeminiRequest{
		Contents: contents,
		GenerationConfig: map[string]interface{}{
			"temperature": p.Temperature,
		},
	}

	// Thinking: the Gemini profile always requests thought output
	// (includeThoughts = true); the depth comes from the resolved intent. An
	// unresolved intent keeps the server default.
	intent := p.Reasoning
	if !p.reasoningSet {
		intent = reasoning.Intent{Mode: reasoning.ModeDefault}
	}
	profile, _ := reasoning.Lookup(reasoning.GeminiGenerateContent, "", p.Model, p.ReasoningOverride)
	plan, _ := reasoning.Translate(intent, profile)

	if p.SystemInstruction != "" {
		reqBody.SystemInstruction = &GeminiContent{
			Parts: []GeminiPart{
				{Text: p.SystemInstruction},
			},
			Role: "user",
		}
	}

	if len(geminiTools) > 0 {
		reqBody.Tools = geminiTools
		reqBody.ToolConfig = map[string]interface{}{
			"functionCallingConfig": map[string]interface{}{
				"mode": "AUTO",
			},
		}
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}
	jsonData, err = applyReasoningPlan(jsonData, plan)
	if err != nil {
		return nil, fmt.Errorf("error applying reasoning plan: %w", err)
	}

	url := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", p.Model, p.APIKey)

	stream := newStreamRequest(ctx, 120*time.Second)
	defer stream.close()
	httpCtx := stream.ctx

	req, err := http.NewRequestWithContext(httpCtx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := doWithRetry(httpCtx, nil, req, sink)
	if err != nil {
		// If the context has been cancelled, it is not a fatal error
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

	return parseGeminiStreamResponse(ctx, stream.body(resp.Body), sink)
}

func convertMessagesToGeminiNative(messages []types.Message, supportsImages bool) []GeminiContent {
	var contents []GeminiContent

	for _, msg := range messages {
		var parts []GeminiPart

		switch msg.Role {
		case "system":
			// Gemini doesn't support "system" role in contents array;
			// inject system instructions as a user message at the start
			parts = append(parts, GeminiPart{Text: msg.Content})
			contents = append(contents, GeminiContent{Parts: parts, Role: "user"})

		case "user":
			parts = append(parts, GeminiPart{Text: msg.Content})
			contents = append(contents, GeminiContent{Parts: parts, Role: "user"})

		case "assistant":
			// Re-emit reasoning/thought parts exactly as the model returned them,
			// including their thoughtSignature. Gemini 3 requires this to maintain
			// reasoning state across turns; omitting it causes 400 errors on tool-call
			// turns and loses reasoning context on text-only turns.
			if msg.ReasoningContent != "" {
				parts = append(parts, GeminiPart{
					Text:             msg.ReasoningContent,
					Thought:          true,
					ThoughtSignature: msg.ReasoningSignature,
				})
			}

			if len(msg.ToolCalls) > 0 {
				for _, tc := range msg.ToolCalls {
					parts = append(parts, GeminiPart{
						FunctionCall: &GeminiFunctionCall{
							Name: tc.Function.Name,
							Args: tc.Function.Arguments,
						},
						ThoughtSignature: tc.ThoughtSignature,
					})
				}
			} else if msg.Content != "" {
				// For text-only responses, Gemini 3 attaches thoughtSignature to the
				// last text part. Re-attach it here if we have one and no reasoning
				// part already consumed the signature.
				sig := ""
				if msg.ReasoningContent == "" {
					sig = msg.ReasoningSignature
				}
				parts = append(parts, GeminiPart{Text: msg.Content, ThoughtSignature: sig})
			}

			if len(parts) > 0 {
				contents = append(contents, GeminiContent{Parts: parts, Role: "model"})
			}

		case "tool":
			fr := &GeminiFunctionResponse{
				Name: msg.Name,
				Response: map[string]interface{}{
					"result": msg.Content,
				},
			}
			if len(msg.Images) > 0 {
				if supportsImages {
					for i, img := range msg.Images {
						name := img.Name
						if name == "" {
							ext := "jpg"
							if img.MIMEType == "image/png" {
								ext = "png"
							}
							name = fmt.Sprintf("image-%d.%s", i+1, ext)
						}
						fr.Parts = append(fr.Parts, GeminiPart{InlineData: &GeminiInlineData{
							MimeType:    img.MIMEType,
							Data:        img.Data,
							DisplayName: name,
						}})
					}
				} else {
					fr.Response["result"] = msg.Content + "\n[screenshot omitted: the current Gemini model does not support images in function responses]"
				}
			}
			parts = append(parts, GeminiPart{FunctionResponse: fr})
			// Gemini v1beta only accepts "user" or "model" as Content.role.
			// Function responses must be sent on a "user" turn, right after the
			// "model" turn that contained the matching functionCall. Using
			// "function" (an OpenAI-style role) makes the API reject the request
			// with 400 INVALID_ARGUMENT "Request contains an invalid argument.".
			contents = append(contents, GeminiContent{Parts: parts, Role: "user"})
		}
	}

	return contents
}

func convertToolsToGeminiNative(tools []types.Tool) []GeminiTool {
	if len(tools) == 0 {
		return nil
	}

	functionDeclarations := make([]GeminiFunctionDeclaration, 0)

	for _, tool := range tools {
		params := make(map[string]interface{})

		if tool.Function.Parameters.Type == "object" {
			params["type"] = "object"

			properties := make(map[string]interface{})
			required := make([]string, 0)

			for name, prop := range tool.Function.Parameters.Properties {
				propSchema := prop.ToSchema()
				properties[name] = propSchema

				for _, req := range tool.Function.Parameters.Required {
					if req == name {
						required = append(required, name)
						break
					}
				}
			}

			if len(properties) > 0 {
				params["properties"] = properties
			}

			if len(required) > 0 {
				params["required"] = required
			}
		}

		var finalParams map[string]interface{}
		if len(params) > 0 {
			finalParams = params
		}

		functionDeclarations = append(functionDeclarations, GeminiFunctionDeclaration{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  finalParams,
		})
	}

	return []GeminiTool{{FunctionDeclarations: functionDeclarations}}
}

// parseGeminiStreamResponse reads the SSE stream and respects context cancellation.
func parseGeminiStreamResponse(ctx context.Context, body io.ReadCloser, sink types.OutputSink) (*types.StreamResult, error) {
	result := &types.StreamResult{
		ToolCalls: make([]types.ToolCall, 0),
	}

	sawTerminal := false
	var fullContent strings.Builder
	var fullReasoning strings.Builder
	// lastTextThoughtSig tracks the thoughtSignature on the final text part of a
	// non-tool-call response. Gemini 3 attaches it to preserve reasoning state;
	// we store it so the caller can round-trip it in the next turn's history.
	var lastTextThoughtSig string

	diffCraft := newDiffCraftTracker(sink)
	defer diffCraft.finish()

	lineCh, stopReader := readStreamLines(ctx, body)
	defer stopReader()

loop:
	for {
		select {
		case <-ctx.Done():
			sink.WriteInterrupt()
			result.Content = fullContent.String()
			result.ReasoningContent = fullReasoning.String()
			result.ReasoningSignature = lastTextThoughtSig
			result.Interrupted = true
			return result, nil

		case sl := <-lineCh:
			if sl.done {
				if sl.err != nil {
					sink.Finalize()
					return nil, fmt.Errorf("gemini stream interrupted: %w", sl.err)
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

			var response map[string]interface{}
			if err := json.Unmarshal([]byte(data), &response); err != nil {
				continue
			}

			if feedback, ok := response["promptFeedback"].(map[string]interface{}); ok {
				reason, _ := feedback["blockReason"].(string)
				if reason == "" || reason == "BLOCK_REASON_UNSPECIFIED" {
					if geminiSafetyBlocked(feedback) {
						reason = "SAFETY"
					} else {
						reason = ""
					}
				}
				if reason != "" {
					sink.Finalize()
					return nil, fmt.Errorf("gemini prompt blocked: %s", reason)
				}
			}

			candidates, ok := response["candidates"].([]interface{})
			if !ok || len(candidates) == 0 {
				continue
			}

			for _, cand := range candidates {
				candidate, ok := cand.(map[string]interface{})
				if !ok {
					continue
				}

				if reason, _ := candidate["finishReason"].(string); reason != "" {
					sawTerminal = true
					result.FinishReason = strings.ToLower(reason)
				} else if geminiSafetyBlocked(candidate) {
					sink.Finalize()
					return nil, fmt.Errorf("gemini response blocked: SAFETY")
				}
				content, ok := candidate["content"].(map[string]interface{})
				if !ok {
					continue
				}

				parts, ok := content["parts"].([]interface{})
				if !ok {
					continue
				}

				for _, part := range parts {
					partMap, ok := part.(map[string]interface{})
					if !ok {
						continue
					}

					if text, ok := partMap["text"].(string); ok && text != "" {
						sig, _ := partMap["thoughtSignature"].(string)
						if thought, _ := partMap["thought"].(bool); thought {
							sink.WriteReasoning(text)
							fullReasoning.WriteString(text)
							// Save the reasoning part's signature; it will be attached
							// to the thought part when rebuilding history.
							if sig != "" {
								lastTextThoughtSig = sig
							}
						} else {
							sink.WriteText(text)
							fullContent.WriteString(text)
							// For plain text parts (no tool calls), Gemini 3 places the
							// thoughtSignature on the last text part of the response.
							if sig != "" {
								lastTextThoughtSig = sig
							}
						}
					}

					if funcCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
						name, _ := funcCall["name"].(string)
						args, _ := funcCall["args"].(map[string]interface{})
						thoughtSig, _ := partMap["thoughtSignature"].(string)

						if args == nil {
							args = make(map[string]interface{})
						}

						if name == "replace_anchors" {
							if raw, err := json.Marshal(args); err == nil {
								diffCraft.update(name, string(raw))
							}
						} else {
							sink.StartSpinner("executing " + name + "...")
						}

						result.ToolCalls = append(result.ToolCalls, types.ToolCall{
							Function: types.ToolCallFunction{
								Name:      name,
								Arguments: args,
							},
							ThoughtSignature: thoughtSig,
						})
					}
				}
			}

			if usage, ok := response["usageMetadata"].(map[string]interface{}); ok {
				if promptTokens, ok := usage["promptTokenCount"].(float64); ok && promptTokens > 0 {
					result.PromptTokens = int(promptTokens)
				}
				if candidatesTokens, ok := usage["candidatesTokenCount"].(float64); ok && candidatesTokens > 0 {
					result.CompletionTokens = int(candidatesTokens)
				}
				// keep the highest value seen across chunks (implicit caching sends it on multiple chunks)
				if cachedTokens, ok := usage["cachedContentTokenCount"].(float64); ok && int(cachedTokens) > result.CachedTokens {
					result.CachedTokens = int(cachedTokens)
				}
			}
		}
	}

	sink.Finalize()
	result.Content = fullContent.String()
	result.ReasoningContent = fullReasoning.String()
	result.ReasoningSignature = lastTextThoughtSig
	if !sawTerminal {
		return nil, fmt.Errorf("gemini stream ended before a terminal event (incomplete response)")
	}
	return result, nil
}

func geminiSafetyBlocked(object map[string]interface{}) bool {
	ratings, _ := object["safetyRatings"].([]interface{})
	for _, value := range ratings {
		if rating, ok := value.(map[string]interface{}); ok {
			if blocked, _ := rating["blocked"].(bool); blocked {
				return true
			}
		}
	}
	return false
}
