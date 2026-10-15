package agui

import (
	"encoding/json"
	"testing"

	"fuckyourcli/types"
)

func TestMessagesFromInputDropsSystemAndMapsRoles(t *testing.T) {
	raw := []byte(`[
		{"id":"1","role":"system","content":"ignore me"},
		{"id":"2","role":"developer","content":"ignore me too"},
		{"id":"3","role":"user","content":"hello"},
		{"id":"4","role":"assistant","content":"hi","toolCalls":[{"id":"c1","type":"function","function":{"name":"read","arguments":"{\"path\":\"a.go\"}"}}]},
		{"id":"5","role":"tool","toolCallId":"c1","content":"file content"}
	]`)
	var in []InputMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	msgs := MessagesFromInput(in)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages (system/developer dropped), got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("unexpected first message: %+v", msgs[0])
	}
	if len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("expected one tool call, got %+v", msgs[1].ToolCalls)
	}
	tc := msgs[1].ToolCalls[0]
	if tc.ID != "c1" || tc.Function.Name != "read" || tc.Function.Arguments["path"] != "a.go" {
		t.Fatalf("unexpected tool call mapping: %+v", tc)
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "c1" {
		t.Fatalf("unexpected tool message: %+v", msgs[2])
	}
}

func TestMessagesFromInputContentParts(t *testing.T) {
	raw := []byte(`[{"role":"user","content":[
		{"type":"text","text":"look at this"},
		{"type":"binary","mimeType":"image/png","data":"AAAA"}
	]}]`)
	var in []InputMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := MessagesFromInput(in)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Content != "look at this" {
		t.Fatalf("unexpected text: %q", msgs[0].Content)
	}
	if len(msgs[0].Images) != 1 || msgs[0].Images[0].MIMEType != "image/png" {
		t.Fatalf("unexpected images: %+v", msgs[0].Images)
	}
}

func TestParseArgumentsInvalidFallsBackToRaw(t *testing.T) {
	raw := []byte(`[{"role":"assistant","toolCalls":[{"id":"c1","function":{"name":"x","arguments":"not json"}}]}]`)
	var in []InputMessage
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := MessagesFromInput(in)
	tc := msgs[0].ToolCalls[0]
	if len(tc.Function.Arguments) != 0 {
		t.Fatalf("expected empty args on invalid JSON, got %+v", tc.Function.Arguments)
	}
	if tc.Function.ArgumentsRaw != "not json" {
		t.Fatalf("expected raw args preserved, got %q", tc.Function.ArgumentsRaw)
	}
}

func TestNewIDUnique(t *testing.T) {
	a, b := NewID("msg"), NewID("msg")
	if a == b {
		t.Fatalf("expected unique ids, got %q twice", a)
	}
	if len(a) < 6 || a[:4] != "msg_" {
		t.Fatalf("unexpected id format: %q", a)
	}
}

// TestSnapshotFromMessagesKeepsRequiredFields covers the fields MessageSchema
// makes mandatory: `id` on every message, `content` on user/tool and
// `toolCallId` on tool messages. A client validating the event rejects the whole
// MESSAGES_SNAPSHOT when one of them is missing. System messages are dropped:
// the authoritative fyc system prompt must never reach the client.
func TestSnapshotFromMessagesKeepsRequiredFields(t *testing.T) {
	msgs := SnapshotFromMessages([]types.Message{
		{Role: "system", Content: "sys"}, // dropped: must not leak in the snapshot
		{Role: "user", Content: ""},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "c1", Function: types.ToolCallFunction{Name: "read", ArgumentsRaw: "{}"}}}},
		{Role: "tool", Content: "output"}, // no ToolCallID (e.g. Gemini history)
	})

	raw, err := json.Marshal(NewMessagesSnapshot(msgs))
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var ev struct {
		Messages []map[string]interface{} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if len(ev.Messages) != 3 {
		t.Fatalf("expected 3 messages (system dropped), got %d", len(ev.Messages))
	}
	for _, m := range ev.Messages {
		if m["role"] == "system" {
			t.Fatalf("system message leaked into the snapshot: %+v", m)
		}
	}
	for i, m := range ev.Messages {
		if id, _ := m["id"].(string); id == "" {
			t.Fatalf("message %d is missing the required id: %+v", i, m)
		}
		if _, ok := m["content"]; !ok {
			t.Fatalf("message %d is missing the required content: %+v", i, m)
		}
		if m["role"] == "tool" {
			if id, _ := m["toolCallId"].(string); id == "" {
				t.Fatalf("tool message %d is missing the required toolCallId: %+v", i, m)
			}
		}
	}
}
