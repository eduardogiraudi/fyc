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
	"net/url"
	"sort"
	"strings"
	"time"
)

// CodexProvider implements the Provider interface for OpenAI Codex (Responses API).
type CodexProvider struct {
	AccessToken     string
	Model           string
	Temperature     float64
	ThinkingEnabled bool
	ReasoningEffort string
	// Reasoning is the explicit thinking intent; unset falls back to the legacy
	// toggles above.
	Reasoning reasoning.Intent
	// ReasoningOverride, when non-nil, forces a profile instead of detection.
	ReasoningOverride *reasoning.Profile
	reasoningSet      bool
	// Refresh, when set, is called once if the Responses API rejects the
	// current access token with a 401. It returns a fresh token (the caller is
	// responsible for persisting it); StreamChat then retries the request once.
	Refresh func() (string, error)
}

func NewCodexProvider(accessToken, model string, temperature float64, thinkingEnabled bool, reasoningEffort string) *CodexProvider {
	return &CodexProvider{
		AccessToken:     accessToken,
		Model:           model,
		Temperature:     temperature,
		ThinkingEnabled: thinkingEnabled,
		ReasoningEffort: reasoningEffort,
	}
}

// SetReasoning overrides the legacy boolean toggles with an explicit intent and,
// optionally, a forced profile.
func (p *CodexProvider) SetReasoning(intent reasoning.Intent, override *reasoning.Profile) {
	p.Reasoning = intent
	p.ReasoningOverride = override
	p.reasoningSet = true
}

// CodexCredentials holds OAuth tokens for Codex.
type CodexCredentials struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // Unix timestamp in milliseconds
}

// Codex OAuth constants
const (
	codexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexIssuer   = "https://auth.openai.com"
	codexTokenURL = "https://auth.openai.com/oauth/token"
	codexAuthAPI  = "https://auth.openai.com/api/accounts"
	codexVerifURL = "https://auth.openai.com/codex/device"
)

// codexBaseURL is the Codex API root. It is a variable so tests can point the
// provider at a local server; the endpoints themselves are fixed.
var codexBaseURL = "https://chatgpt.com/backend-api"

// CodexAuthenticate performs the OAuth Device Flow (matching official Codex CLI) and returns credentials.
func CodexAuthenticate() (*CodexCredentials, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	// Step 1: Request user code
	userCodeReq := map[string]interface{}{
		"client_id": codexClientID,
	}
	body, _ := json.Marshal(userCodeReq)
	resp, err := client.Post(codexAuthAPI+"/deviceauth/usercode", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("device code request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("device code request failed (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var userCodeResp struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		IntervalRaw  json.RawMessage `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&userCodeResp); err != nil {
		resp.Body.Close()
		return nil, fmt.Errorf("failed to decode user code response: %w", err)
	}
	resp.Body.Close()

	var interval int
	if len(userCodeResp.IntervalRaw) > 0 {
		// Try parsing as number first, then as string
		if err := json.Unmarshal(userCodeResp.IntervalRaw, &interval); err != nil {
			var intervalStr string
			if err2 := json.Unmarshal(userCodeResp.IntervalRaw, &intervalStr); err2 == nil {
				fmt.Sscanf(intervalStr, "%d", &interval)
			}
		}
	}
	if interval < 1 {
		return nil, fmt.Errorf("invalid polling interval received: %d", interval)
	}

	fmt.Printf("\nTo authenticate with OpenAI Codex:\n")
	fmt.Printf("   Open: %s\n", codexVerifURL)
	fmt.Printf("   Enter the code: %s\n\n", userCodeResp.UserCode)

	// Step 2: Poll for authorization code
	pollBody := map[string]interface{}{
		"device_auth_id": userCodeResp.DeviceAuthID,
		"user_code":      userCodeResp.UserCode,
	}
	pollJSON, _ := json.Marshal(pollBody)

	maxWait := 15 * time.Minute
	start := time.Now()
	var authCode, codeVerifier string

	for time.Since(start) < maxWait {
		time.Sleep(time.Duration(interval) * time.Second)

		pollResp, err := client.Post(codexAuthAPI+"/deviceauth/token", "application/json", bytes.NewReader(pollJSON))
		if err != nil {
			continue
		}
		status := pollResp.StatusCode

		if status == http.StatusOK {
			var success struct {
				AuthorizationCode string `json:"authorization_code"`
				CodeChallenge     string `json:"code_challenge"`
				CodeVerifier      string `json:"code_verifier"`
			}
			if err := json.NewDecoder(pollResp.Body).Decode(&success); err != nil {
				pollResp.Body.Close()
				return nil, fmt.Errorf("failed to decode auth code response: %w", err)
			}
			pollResp.Body.Close()
			authCode = success.AuthorizationCode
			codeVerifier = success.CodeVerifier
			break
		}

		// 403 = still pending
		pollResp.Body.Close()
		if status == http.StatusForbidden || status == http.StatusNotFound {
			continue
		}

		return nil, fmt.Errorf("device auth failed with status %d", status)
	}

	if authCode == "" {
		return nil, fmt.Errorf("device auth timed out after 15 minutes")
	}

	// Step 3: Exchange authorization code for tokens using PKCE
	redirectURI := codexIssuer + "/deviceauth/callback"
	tokenBody := fmt.Sprintf(
		"grant_type=authorization_code&code=%s&redirect_uri=%s&client_id=%s&code_verifier=%s",
		url.QueryEscape(authCode), url.QueryEscape(redirectURI), url.QueryEscape(codexClientID), url.QueryEscape(codeVerifier),
	)

	tokenResp, err := client.Post(codexTokenURL, "application/x-www-form-urlencoded", strings.NewReader(tokenBody))
	if err != nil {
		return nil, fmt.Errorf("token exchange request failed: %w", err)
	}
	defer tokenResp.Body.Close()

	if tokenResp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(tokenResp.Body)
		return nil, fmt.Errorf("token exchange failed (%d): %s", tokenResp.StatusCode, string(bodyBytes))
	}

	var tokenResult struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(tokenResp.Body).Decode(&tokenResult); err != nil {
		return nil, fmt.Errorf("failed to decode token response: %w", err)
	}

	creds := &CodexCredentials{
		AccessToken:  tokenResult.AccessToken,
		RefreshToken: tokenResult.RefreshToken,
		ExpiresAt:    time.Now().UnixMilli() + int64(tokenResult.ExpiresIn)*1000,
	}
	fmt.Println("✓ Codex authentication completed")
	return creds, nil
}

// CodexRefreshToken refreshes the access token (refresh tokens are single-use!).
func CodexRefreshToken(refreshToken string) (*CodexCredentials, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	refreshReq := map[string]interface{}{
		"client_id":     codexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
	}
	body, _ := json.Marshal(refreshReq)

	resp, err := client.Post(codexTokenURL, "application/json", strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("token refresh request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, string(bodyBytes))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed to decode refresh response: %w", err)
	}

	return &CodexCredentials{
		AccessToken:  tokenResp.AccessToken,
		RefreshToken: tokenResp.RefreshToken,
		ExpiresAt:    time.Now().UnixMilli() + int64(tokenResp.ExpiresIn)*1000,
	}, nil
}

// StreamChat streams a chat completion from OpenAI Codex Responses API.
func (p *CodexProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	// Extract the system prompt from messages
	var instructions string
	var chatMessages []types.Message
	for _, msg := range messages {
		if msg.Role == "system" {
			instructions = msg.Content
		} else {
			chatMessages = append(chatMessages, msg)
		}
	}

	if instructions == "" {
		instructions = "You are a helpful assistant."
	}

	input := convertMessagesToCodexInput(chatMessages, p.Model)

	reqBody := map[string]interface{}{
		"model":        p.Model,
		"instructions": instructions,
		"input":        input,
		"stream":       true,
		"store":        false,
	}
	intent := p.Reasoning
	if !p.reasoningSet {
		intent = legacyReasoningIntent(p.ThinkingEnabled, p.ReasoningEffort)
	}
	addResponsesReasoning(reqBody, intent, p.ReasoningOverride, p.Model, "")

	if len(tools) > 0 {
		codexTools := convertToolsToCodex(tools)
		reqBody["tools"] = codexTools
		reqBody["tool_choice"] = "auto"
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("error marshaling request: %w", err)
	}

	url := codexBaseURL + "/codex/responses"

	stream := newStreamRequest(ctx, 240*time.Second)
	defer stream.close()
	httpCtx := stream.ctx

	buildRequest := func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(httpCtx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("OpenAI-Beta", "responses=v1")
		req.Header.Set("User-Agent", "fuckyourcli/1.0")
		return req, nil
	}

	send := func(token string) (*http.Response, error) {
		req, err := buildRequest(token)
		if err != nil {
			return nil, err
		}
		return doWithRetry(httpCtx, nil, req, sink)
	}

	resp, err := send(p.AccessToken)
	if err != nil {
		if ctx.Err() != nil {
			return &types.StreamResult{Interrupted: true}, nil
		}
		return nil, fmt.Errorf("error sending request: %w", err)
	}

	// The access token can expire mid-session (a long tool loop, or a parallel
	// sub-agent outliving it). On a 401 refresh it once and retry, instead of
	// aborting the whole turn.
	if resp.StatusCode == http.StatusUnauthorized && p.Refresh != nil {
		unauthBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		newToken, refreshErr := p.Refresh()
		if refreshErr != nil || newToken == "" {
			reason := "token refresh failed"
			if refreshErr != nil {
				reason = "token refresh failed: " + refreshErr.Error()
			}
			return nil, fmt.Errorf("Codex API error (401): %s (%s)", string(unauthBody), reason)
		}
		p.AccessToken = newToken
		resp, err = send(newToken)
		if err != nil {
			if ctx.Err() != nil {
				return &types.StreamResult{Interrupted: true}, nil
			}
			return nil, fmt.Errorf("error sending request: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Codex API error (%d): %s", resp.StatusCode, string(body))
	}

	return parseCodexStreamResponse(ctx, stream.body(resp.Body), p.ThinkingEnabled, sink)
}

func convertMessagesToCodexInput(messages []types.Message, model string) []map[string]interface{} {
	var input []map[string]interface{}
	for _, msg := range messages {
		switch msg.Role {
		case "user":
			input = append(input, map[string]interface{}{
				"role":    "user",
				"content": msg.Content,
			})
		case "assistant":
			// Replay the reasoning item first (it precedes the assistant output):
			// required to keep reasoning continuity when the server keeps no state.
			// The encrypted state is model-bound, so an item produced by a
			// different model (a hot model switch) is dropped, not replayed.
			if reasoningModelMatches(msg.ReasoningModel, model) {
				input = append(input, decodeReasoningItems(msg.ReasoningItem)...)
			}
			// First emit the assistant text if present
			if msg.Content != "" {
				input = append(input, map[string]interface{}{
					"role":    "assistant",
					"content": msg.Content,
				})
			}
			// Then emit each tool call as a separate item
			for _, tc := range msg.ToolCalls {
				argsStr := tc.Function.ArgumentsRaw
				if argsStr == "" {
					b, _ := json.Marshal(tc.Function.Arguments)
					argsStr = string(b)
				}
				input = append(input, map[string]interface{}{
					"type":      "function_call",
					"call_id":   tc.ID,
					"name":      tc.Function.Name,
					"arguments": argsStr,
				})
			}
		case "tool":
			out := map[string]interface{}{
				"type":    "function_call_output",
				"call_id": msg.ToolCallID,
			}
			if len(msg.Images) > 0 {
				// Responses API: images can be returned as tool outputs by
				// passing a list of content items.
				items := make([]map[string]interface{}, 0, len(msg.Images)+1)
				for _, img := range msg.Images {
					items = append(items, map[string]interface{}{
						"type":      "input_image",
						"image_url": "data:" + img.MIMEType + ";base64," + img.Data,
					})
				}
				if msg.Content != "" {
					items = append(items, map[string]interface{}{
						"type": "input_text",
						"text": msg.Content,
					})
				}
				out["output"] = items
			} else {
				out["output"] = msg.Content
			}
			input = append(input, out)
		}
	}
	return input
}

// reasoningModelMatches reports whether a stored reasoning item may be replayed
// to the given model. An item whose producer is unknown (empty — sessions written
// before the field existed) is replayed as before; otherwise the producer must
// match, because the encrypted state is model-bound and a hot model switch makes
// the server reject the stale item.
func reasoningModelMatches(producedBy, current string) bool {
	return producedBy == "" || current == "" || producedBy == current
}

// decodeReasoningItems parses the opaque ReasoningItem stored on an assistant
// message. New sessions store a JSON array (a turn can carry several reasoning
// items); sessions written by earlier builds stored a single object, so both
// shapes are accepted.
func decodeReasoningItems(raw string) []map[string]interface{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var items []map[string]interface{}
		if err := json.Unmarshal([]byte(raw), &items); err != nil {
			return nil
		}
		return items
	}
	var item map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		return nil
	}
	return []map[string]interface{}{item}
}

func convertToolsToCodex(tools []types.Tool) []map[string]interface{} {
	var codexTools []map[string]interface{}
	for _, tool := range tools {
		properties := make(map[string]interface{})
		required := make([]string, 0)
		for name, prop := range tool.Function.Parameters.Properties {
			properties[name] = prop.ToSchema()
			for _, req := range tool.Function.Parameters.Required {
				if req == name {
					required = append(required, name)
					break
				}
			}
		}
		params := map[string]interface{}{
			"type":       "object",
			"properties": properties,
		}
		if len(required) > 0 {
			params["required"] = required
		}
		codexTools = append(codexTools, map[string]interface{}{
			"type":        "function",
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  params,
		})
	}
	return codexTools
}

// parseCodexStreamResponse parses the Responses API SSE stream.
func parseCodexStreamResponse(ctx context.Context, body io.ReadCloser, thinkingEnabled bool, sink types.OutputSink) (*types.StreamResult, error) {
	result := &types.StreamResult{
		ToolCalls: make([]types.ToolCall, 0),
	}

	sawTerminal := false
	var fullContent strings.Builder
	var fullReasoning strings.Builder
	// reasoningItems keeps every reasoning item of the turn (as decoded maps) so
	// the next turn can replay them verbatim, preserving reasoning continuity
	// without server state. A response may carry several (interleaved thinking
	// across tool calls): all of them are kept, in order.
	var reasoningItems []map[string]interface{}
	// itemReasoning accumulates the text carried by the reasoning items
	// themselves. A backend that sends no reasoning deltas (or a stream whose
	// summary only exists inside the item) must still show its thinking, so
	// this is the fallback the UI sees.
	var itemReasoning strings.Builder
	flushItemReasoning := func() {
		if fullReasoning.Len() == 0 && itemReasoning.Len() > 0 {
			sink.WriteReasoning(itemReasoning.String())
			fullReasoning.WriteString(itemReasoning.String())
		}
	}

	diffCraft := newDiffCraftTracker(sink)
	defer diffCraft.finish()

	type pendingToolCall struct {
		id           string
		callID       string
		name         string
		initialArgs  string
		args         strings.Builder
		finalArgs    string
		resultIndex  int
		lastDiffArgs string
		diffNotified bool
	}
	var order []*pendingToolCall
	toolCallAliases := make(map[string]*pendingToolCall)
	getToolCall := func(itemID, callID, name string) *pendingToolCall {
		key := itemID
		if key == "" {
			key = callID
		}
		if key == "" {
			return nil
		}
		ptc := toolCallAliases[itemID]
		if ptc == nil {
			ptc = toolCallAliases[callID]
		}
		if ptc == nil {
			ptc = &pendingToolCall{id: key, resultIndex: -1}
			order = append(order, ptc)
		}
		if itemID != "" {
			ptc.id = itemID
			toolCallAliases[itemID] = ptc
		}
		if callID != "" {
			ptc.callID = callID
			toolCallAliases[callID] = ptc
		}
		if name != "" {
			ptc.name = name
		}
		return ptc
	}
	// Deduplicate per call so identical arguments on distinct calls still notify.
	updateToolDiff := func(ptc *pendingToolCall, rawArgs string) {
		if ptc == nil || ptc.name != "replace_anchors" {
			return
		}
		if ptc.diffNotified && ptc.lastDiffArgs == rawArgs {
			return
		}
		diffCraft.update(ptc.name, rawArgs)
		ptc.lastDiffArgs = rawArgs
		ptc.diffNotified = true
	}
	// Final snapshots replace streamed fragments, and later snapshots update
	// the same call rather than scheduling another execution.
	finishToolCall := func(ptc *pendingToolCall, finalArgs string) {
		if ptc == nil {
			return
		}
		if finalArgs != "" {
			ptc.finalArgs = finalArgs
		}
		if ptc.name == "" {
			return
		}
		argsStr := ptc.finalArgs
		if argsStr == "" {
			argsStr = ptc.args.String()
		}
		if argsStr == "" {
			argsStr = ptc.initialArgs
		}
		updateToolDiff(ptc, argsStr)
		var parsedArgs map[string]interface{}
		if err := json.Unmarshal([]byte(argsStr), &parsedArgs); err != nil || parsedArgs == nil {
			parsedArgs = make(map[string]interface{})
		}
		id := ptc.callID
		if id == "" {
			id = ptc.id
		}
		call := types.ToolCall{
			ID:   id,
			Type: "function",
			Function: types.ToolCallFunction{
				Name:         ptc.name,
				Arguments:    parsedArgs,
				ArgumentsRaw: argsStr,
			},
		}
		if ptc.resultIndex < 0 {
			ptc.resultIndex = len(result.ToolCalls)
			result.ToolCalls = append(result.ToolCalls, call)
		} else {
			result.ToolCalls[ptc.resultIndex] = call
		}
	}
	finishToolItem := func(item map[string]interface{}) {
		itemID, _ := item["id"].(string)
		callID, _ := item["call_id"].(string)
		name, _ := item["name"].(string)
		args, _ := item["arguments"].(string)
		finishToolCall(getToolCall(itemID, callID, name), args)
	}

	lineCh, stopReader := readStreamLines(ctx, body)
	defer stopReader()

loop:
	for {
		select {
		case <-ctx.Done():
			flushItemReasoning()
			sink.WriteInterrupt()
			result.Content = fullContent.String()
			result.ReasoningContent = fullReasoning.String()
			result.Interrupted = true
			return result, nil

		case sl := <-lineCh:
			if sl.done {
				if sl.err != nil {
					sink.Finalize()
					return nil, fmt.Errorf("responses stream interrupted: %w", sl.err)
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

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			eventType, _ := event["type"].(string)

			switch eventType {
			case "response.output_item.added":
				item, _ := event["item"].(map[string]interface{})
				if item == nil {
					continue
				}
				itemType, _ := item["type"].(string)
				if itemType != "function_call" {
					continue
				}
				itemID, _ := item["id"].(string)
				callID, _ := item["call_id"].(string)
				name, _ := item["name"].(string)
				ptc := getToolCall(itemID, callID, name)
				if ptc == nil {
					continue
				}
				ptc.initialArgs, _ = item["arguments"].(string)
				if name != "" && name != "replace_anchors" {
					sink.StartSpinner("executing " + name + "...")
				}

			case "response.output_text.delta":
				delta, _ := event["delta"].(string)
				if delta != "" {
					fullContent.WriteString(delta)
					sink.WriteText(delta)
				}

			case "response.function_call_arguments.delta":
				itemID, _ := event["item_id"].(string)
				callID, _ := event["call_id"].(string)
				name, _ := event["name"].(string)
				ptc := getToolCall(itemID, callID, name)
				if ptc == nil {
					continue
				}
				if name != "" && name != "replace_anchors" {
					sink.StartSpinner("executing " + name + "...")
				}
				delta, _ := event["delta"].(string)
				ptc.args.WriteString(delta)
				updateToolDiff(ptc, ptc.args.String())

			case "response.function_call_arguments.done":
				itemID, _ := event["item_id"].(string)
				callID, _ := event["call_id"].(string)
				name, _ := event["name"].(string)
				args, _ := event["arguments"].(string)
				finishToolCall(getToolCall(itemID, callID, name), args)

			case "response.output_item.done":
				// A reasoning item is finalized here. Keep its text as a fallback
				// for streams that send no reasoning deltas (the UI must show the
				// thinking even when it only exists inside the item). When
				// response.completed arrives it is authoritative and rebuilds the
				// accumulator, so nothing is counted twice.
				item, _ := event["item"].(map[string]interface{})
				if item != nil {
					switch itemType, _ := item["type"].(string); itemType {
					case "reasoning":
						itemReasoning.WriteString(reasoningItemText(item))
					case "function_call":
						finishToolItem(item)
					}
				}

			case "response.output_text.done":
				// noop

			case "response.reasoning_summary_text.delta", "response.reasoning_summary.delta", "response.reasoning.delta", "response.reasoning_text.delta":
				delta := stringFromEvent(event, "delta")
				if delta != "" {
					sink.WriteReasoning(delta)
					fullReasoning.WriteString(delta)
				}

			case "response.reasoning_summary_text.done", "response.reasoning_summary.done", "response.reasoning.done", "response.reasoning_text.done":
				// Some backends send only the final text, with no deltas: keep it
				// and push it to the UI so the thinking panel is not empty.
				text := stringFromEvent(event, "text")
				if text != "" && fullReasoning.Len() == 0 {
					sink.WriteReasoning(text)
					fullReasoning.WriteString(text)
				}

			case "response.incomplete":
				sawTerminal = true
				result.FinishReason = "incomplete"
				if response, ok := event["response"].(map[string]interface{}); ok {
					if details, ok := response["incomplete_details"].(map[string]interface{}); ok {
						if reason, _ := details["reason"].(string); reason != "" {
							result.FinishReason = reason
						}
					}
				}
			case "response.failed":
				return nil, fmt.Errorf("Responses API failed: %s", data)
			case "response.completed", "response.complete":
				sawTerminal = true
				result.FinishReason = "stop"
				// extract usage from the completed response object
				if respObj, ok := event["response"].(map[string]interface{}); ok {
					if usageObj, ok := respObj["usage"].(map[string]interface{}); ok {
						if inputTokens, ok := usageObj["input_tokens"].(float64); ok && inputTokens > 0 {
							result.PromptTokens = int(inputTokens)
						}
						if outputTokens, ok := usageObj["output_tokens"].(float64); ok && outputTokens > 0 {
							result.CompletionTokens = int(outputTokens)
						}
						// Responses API: cached_tokens is top-level in usage
						if cached, ok := usageObj["cached_tokens"].(float64); ok && cached > 0 {
							result.CachedTokens = int(cached)
						}
						// fallback: some models put it in input_tokens_details
						if result.CachedTokens == 0 {
							if details, ok := usageObj["input_tokens_details"].(map[string]interface{}); ok {
								if cached, ok := details["cached_tokens"].(float64); ok && cached > 0 {
									result.CachedTokens = int(cached)
								}
							}
						}
					}
				}
				// Capture the encrypted reasoning state so the next turn can
				// replay it (only present when encrypted_content was requested).
				// Keep every reasoning item, in order.
				if respObj, ok := event["response"].(map[string]interface{}); ok {
					if output, ok := respObj["output"].([]interface{}); ok {
						reasoningItems = reasoningItems[:0]
						itemReasoning.Reset()
						for _, it := range output {
							m, ok := it.(map[string]interface{})
							if !ok {
								continue
							}
							switch t, _ := m["type"].(string); t {
							case "reasoning":
								reasoningItems = append(reasoningItems, m)
								itemReasoning.WriteString(reasoningItemText(m))
							case "function_call":
								finishToolItem(m)
							}
						}
					}
				}
				// Map iteration must not reorder calls first finalized by this flush.
				for _, ptc := range order {
					finishToolCall(ptc, "")
				}
				break loop

			case "error":
				errMsg, _ := event["message"].(string)
				if errMsg != "" {
					return nil, fmt.Errorf("Codex API error: %s", errMsg)
				}
			}
		}
	}

	flushItemReasoning()
	sink.Finalize()

	result.Content = fullContent.String()
	result.ReasoningContent = fullReasoning.String()
	if len(reasoningItems) > 0 {
		if b, err := json.Marshal(reasoningItems); err == nil {
			result.ReasoningItem = string(b)
		}
	}
	if !sawTerminal {
		return nil, fmt.Errorf("responses stream ended before a terminal event (incomplete response)")
	}
	for _, ptc := range order {
		if ptc.resultIndex < 0 {
			return nil, fmt.Errorf("responses stream ended with an incomplete tool call")
		}
	}
	if err := validateStreamToolCalls(result); err != nil {
		return nil, err
	}
	return result, nil
}

// reasoningItemText extracts the human-readable thinking a reasoning item
// carries: its summary parts, or — when no summary was requested — its raw
// reasoning parts. The Responses API streams that text as delta events, but a
// backend may deliver it only inside the item, so this is the fallback the UI
// displays.
func reasoningItemText(item map[string]interface{}) string {
	for _, key := range []string{"summary", "content"} {
		parts, _ := item[key].([]interface{})
		var text strings.Builder
		for _, raw := range parts {
			part, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if s, _ := part["text"].(string); s != "" {
				text.WriteString(s)
			}
		}
		if text.Len() > 0 {
			return text.String()
		}
	}
	return ""
}

// addResponsesReasoning translates the thinking intent for the Responses API and
// merges the result into reqBody. When thinking is on it also requests the
// encrypted reasoning state, so multi-turn continuity is preserved for the
// providers that support it.
//
// host is the provider base URL: it is what lets a local server (Ollama exposes
// /v1/responses too) be recognised and get the fields its API understands. It
// may be empty (the Codex endpoint does not need it).
func addResponsesReasoning(reqBody map[string]interface{}, intent reasoning.Intent, override *reasoning.Profile, model, host string) {
	profile, _ := reasoning.Lookup(reasoning.OpenAIResponses, host, model, override)
	plan, _ := reasoning.Translate(intent, profile)
	plan.Apply(reqBody)
	provider := reasoning.ProviderFromHost(host)
	if intent.Mode != reasoning.ModeDefault && intent.Mode != reasoning.ModeOff &&
		provider != reasoning.FamilyLocal && provider != reasoning.FamilyOllama {
		// Ollama (local or cloud) does not produce encrypted reasoning state.
		reqBody["include"] = []string{"reasoning.encrypted_content"}
	}
}

func stringFromEvent(event map[string]interface{}, key string) string {
	value, ok := event[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(bytes)
}

// CodexDefaultModels returns default models for Codex.
func CodexDefaultModels() []string {
	return []string{
		"gpt-5.5",
		"gpt-5-codex-mini",
	}
}

// GetCodexModels returns available models for Codex, falling back to defaults on fetch/parse errors.
func GetCodexModels(accessToken string) ([]string, error) {
	models, err := fetchCodexModels(accessToken)
	if err != nil || len(models) == 0 {
		return CodexDefaultModels(), nil
	}
	return models, nil
}

func fetchCodexModels(accessToken string) ([]string, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("codex access token is empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	modelsURL, err := url.Parse(codexBaseURL + "/codex/models")
	if err != nil {
		return nil, fmt.Errorf("error parsing models URL: %w", err)
	}
	clientVersion := resolveCodexClientVersion(ctx)
	query := modelsURL.Query()
	query.Set("client_version", clientVersion)
	modelsURL.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("error creating models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex-cli/"+clientVersion)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error fetching codex models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("codex models API error (%d): %s", resp.StatusCode, string(body))
	}

	var raw interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("failed to decode codex models response: %w", err)
	}

	models := extractCodexModelIDs(raw)
	if len(models) == 0 {
		return nil, fmt.Errorf("codex models response did not contain model ids")
	}
	return models, nil
}

func extractCodexModelIDs(raw interface{}) []string {
	seen := make(map[string]struct{})
	var models []string

	add := func(value interface{}) {
		model, ok := value.(string)
		if !ok || strings.TrimSpace(model) == "" {
			return
		}
		if _, exists := seen[model]; exists {
			return
		}
		seen[model] = struct{}{}
		models = append(models, model)
	}

	var walk func(interface{})
	walk = func(value interface{}) {
		switch v := value.(type) {
		case []interface{}:
			for _, item := range v {
				walk(item)
			}
		case map[string]interface{}:
			// The correct field is "slug" (not "id" or "name")
			for _, key := range []string{"slug", "id", "name", "model"} {
				add(v[key])
			}
			for _, key := range []string{"data", "models"} {
				if child, ok := v[key]; ok {
					walk(child)
				}
			}
		}
	}

	walk(raw)
	sort.Strings(models)
	return models
}
