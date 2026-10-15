package main

import (
	"testing"

	"fuckyourcli/types"
)

func TestAppendAssistantStreamResultSkipsEmpty(t *testing.T) {
	for _, result := range []*types.StreamResult{nil, {}, {Interrupted: true}, {FinishReason: "stop"}} {
		s := &ChatSession{ChatHistory: []types.Message{{Role: "user", Content: "hello"}}}
		appendAssistantStreamResult(s, result, true, "probe")
		if len(s.ChatHistory) != 1 {
			t.Fatalf("empty assistant persisted: %+v", s.ChatHistory)
		}
	}
}

func TestAppendAssistantStreamResultKeepsUsefulContent(t *testing.T) {
	for _, result := range []*types.StreamResult{
		{Content: "partial", Interrupted: true},
		{ReasoningContent: "thinking", ReasoningSignature: "sig"},
		{ReasoningItem: `[{"type":"reasoning"}]`},
		{ToolCalls: []types.ToolCall{{ID: "call_1", Function: types.ToolCallFunction{Name: "sh", Arguments: map[string]any{"command": "echo ok"}}}}},
	} {
		s := &ChatSession{}
		appendAssistantStreamResult(s, result, true, "probe")
		if len(s.ChatHistory) != 1 {
			t.Fatal("useful assistant discarded")
		}
		msg := s.ChatHistory[0]
		if msg.Content != result.Content || msg.ReasoningContent != result.ReasoningContent || msg.ReasoningSignature != result.ReasoningSignature || msg.ReasoningItem != result.ReasoningItem || len(msg.ToolCalls) != len(result.ToolCalls) {
			t.Fatalf("assistant data lost: %+v", msg)
		}
		if result.ReasoningItem != "" && msg.ReasoningModel != "probe" {
			t.Fatal("reasoning model lost")
		}
	}
}
