package main

import (
	"context"
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

type cancelToolSink struct {
	recordSink
	cancel context.CancelFunc
	tools  int
}

func (s *cancelToolSink) WriteToolOutput(string, string) {
	s.tools++
	s.cancel()
}

func TestToolCallIDsSurviveAbortedBatch(t *testing.T) {
	for _, includeIDs := range []bool{false, true} {
		t.Run(map[bool]string{false: "Gemini", true: "other providers"}[includeIDs], func(t *testing.T) {
			calls := []types.ToolCall{compactionCall(""), compactionCall(""), compactionCall("provider-id")}
			s := &ChatSession{RestrictedRoot: t.TempDir(), ChatHistory: []types.Message{{Role: "assistant", ToolCalls: calls}}}
			ctx := newGenerationContext(s)
			defer finishGeneration(s)
			sink := &cancelToolSink{cancel: s.GenerationCancel}
			handleToolCalls(s, sink, calls, includeIDs)
			if ctx.Err() == nil || sink.tools != 1 || len(s.ChatHistory) != 4 {
				t.Fatal("cancellation failed to stop the batch")
			}
			seen := map[string]bool{}
			for i, call := range s.ChatHistory[0].ToolCalls {
				result := s.ChatHistory[i+1]
				if call.ID == "" || seen[call.ID] || result.ToolCallID != call.ID {
					t.Fatal("aborted batch lost stable call/result IDs")
				}
				seen[call.ID] = true
				if i > 0 && result.Content != errToolAborted {
					t.Fatal("an aborted tool was executed")
				}
			}
			if s.ChatHistory[0].ToolCalls[2].ID != "provider-id" {
				t.Fatal("provider-supplied ID was rewritten")
			}
		})
	}
}
