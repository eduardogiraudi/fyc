package agui

import (
	"encoding/json"
	"strings"

	"fuckyourcli/types"
)

// RunAgentInput is the request body of POST /.
//
// Only the fields used by the backend are decoded; the rest (parentRunId, state,
// tools, context, forwardedProps, …) are tolerated and ignored. Resume carries
// the answers to the interrupts of a previous run (level-2 HITL).
type RunAgentInput struct {
	ThreadID       string          `json:"threadId"`
	RunID          string          `json:"runId"`
	ParentRunID    string          `json:"parentRunId,omitempty"`
	State          json.RawMessage `json:"state,omitempty"`
	Messages       []InputMessage  `json:"messages"`
	Tools          json.RawMessage `json:"tools,omitempty"`
	Context        json.RawMessage `json:"context,omitempty"`
	ForwardedProps json.RawMessage `json:"forwardedProps,omitempty"`
	// Resume answers the interrupts a previous run on the same thread left open.
	Resume []ResumeEntry `json:"resume,omitempty"`
}

// Interrupt resume statuses.
const (
	// ResumeStatusResolved means the client answered the interrupt; Payload
	// carries the answer (a denial is expressed inside the payload).
	ResumeStatusResolved = "resolved"
	// ResumeStatusCancelled means the client abandoned without answering.
	ResumeStatusCancelled = "cancelled"
)

// ResumeEntry is one item of RunAgentInput.resume.
type ResumeEntry struct {
	InterruptID string          `json:"interruptId"`
	Status      string          `json:"status"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	Metadata    json.RawMessage `json:"metadata,omitempty"`
}

// InputMessage is one entry of RunAgentInput.messages.
type InputMessage struct {
	ID         string          `json:"id,omitempty"`
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	ToolCalls  []InputToolCall `json:"toolCalls,omitempty"`
}

// InputToolCall is an assistant tool call entry.
type InputToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type,omitempty"`
	Function InputToolFunction `json:"function"`
}

// InputToolFunction is the function descriptor of an InputToolCall.
type InputToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// contentPart models one element of a content-parts array.
type contentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
}

// MessagesFromInput converts the AG-UI conversation history into the internal
// message representation.
//
// `system`/`developer` messages are dropped: the fuckyourcli system prompt is
// authoritative (see README §AG-UI). Unparseable tool-call arguments are kept
// verbatim in ArgumentsRaw.
func MessagesFromInput(in []InputMessage) []types.Message {
	out := make([]types.Message, 0, len(in))
	for _, m := range in {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" || role == "developer" {
			continue
		}
		content, images := parseContent(m.Content)
		msg := types.Message{
			Role:       role,
			Content:    content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			Images:     images,
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]types.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, types.ToolCall{
					ID:   tc.ID,
					Type: normalizeToolCallType(tc.Type),
					Function: types.ToolCallFunction{
						Name:         tc.Function.Name,
						Arguments:    parseArguments(tc.Function.Arguments),
						ArgumentsRaw: tc.Function.Arguments,
					},
				})
			}
		}
		out = append(out, msg)
	}
	return out
}

func normalizeToolCallType(t string) string {
	if strings.TrimSpace(t) == "" {
		return "function"
	}
	return t
}

// parseArguments decodes a JSON object of tool-call arguments. On failure it
// returns an empty (non-nil) map and relies on ArgumentsRaw.
func parseArguments(raw string) map[string]interface{} {
	args := map[string]interface{}{}
	if strings.TrimSpace(raw) == "" {
		return args
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return map[string]interface{}{}
	}
	return args
}

// parseContent accepts either a plain string or an array of content parts
// (text and binary). Binary parts become ImageAttachment values.
func parseContent(raw json.RawMessage) (string, []types.ImageAttachment) {
	if len(raw) == 0 {
		return "", nil
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}

	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil
	}

	var sb strings.Builder
	var images []types.ImageAttachment
	for _, p := range parts {
		switch strings.ToLower(p.Type) {
		case "text":
			sb.WriteString(p.Text)
		case "binary", "image", "image_url":
			if p.Data != "" {
				images = append(images, types.ImageAttachment{MIMEType: p.MimeType, Data: p.Data})
			}
		}
	}
	return sb.String(), images
}

// SnapshotFromMessages converts the internal conversation state into the
// MESSAGES_SNAPSHOT representation. It is emitted at an interrupt boundary, so
// the client can replay the context of a resumed run.
//
// Message ids are generated (the internal representation has none) and only the
// fields the protocol needs are carried: role, text content, tool call id,
// tool calls and tool name. System/developer messages are omitted so the
// authoritative fyc system prompt never leaves the server; images are not part
// of the snapshot.
func SnapshotFromMessages(msgs []types.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		// The fyc system prompt is authoritative and stays server-side: emitting it
		// in the snapshot would leak it to the client. Client-supplied
		// system/developer messages are already dropped by MessagesFromInput.
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		msg := Message{
			ID:         NewID("msg"),
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
		}
		// ToolMessageSchema requires a toolCallId. The history keeps the id only
		// for the providers that expose it (Gemini does not) and a tool call left
		// pending by an interrupt may lack one, so a synthetic id is generated:
		// without it the client rejects the whole MESSAGES_SNAPSHOT.
		if msg.Role == "tool" && msg.ToolCallID == "" {
			msg.ToolCallID = NewID("call")
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]InputToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = NewID("call")
				}
				msg.ToolCalls = append(msg.ToolCalls, InputToolCall{
					ID:   id,
					Type: normalizeToolCallType(tc.Type),
					Function: InputToolFunction{
						Name:      tc.Function.Name,
						Arguments: toolCallArguments(tc.Function),
					},
				})
			}
		}
		out = append(out, msg)
	}
	return out
}

// toolCallArguments returns the raw JSON string of a tool call's arguments,
// falling back to the decoded map when the provider did not keep the raw form.
func toolCallArguments(fn types.ToolCallFunction) string {
	if strings.TrimSpace(fn.ArgumentsRaw) != "" {
		return fn.ArgumentsRaw
	}
	if len(fn.Arguments) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(fn.Arguments)
	if err != nil {
		return "{}"
	}
	return string(raw)
}
