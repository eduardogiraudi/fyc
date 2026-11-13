package main

import (
	"context"
	"strings"
	"testing"

	"fuckyourcli/types"
)

func TestCompactionTranscriptIsCompactAndCapsLargeResults(t *testing.T) {
	compactionTestConfig(t, true, true, 8000)
	huge := "HEAD" + strings.Repeat("middle ", 5000) + "TAIL"
	history := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "working", ReasoningContent: "secret thoughts", ReasoningItem: "opaque-state", ReasoningSignature: "sig-bytes"},
	}
	history = append(history, toolPair("big", "sh", map[string]interface{}{"command": "make"}, huge)...)
	s := &ChatSession{ChatHistory: history}

	_, _, _, err := compactContextWith(context.Background(), s, false, 8000, func(_ context.Context, messages []types.Message, _ *ChatSession) (compactionResult, error) {
		transcript := messages[len(messages)-1].Content
		for _, banned := range []string{"secret thoughts", "opaque-state", "sig-bytes", "reasoning_content", "tool_call_id"} {
			if strings.Contains(transcript, banned) {
				t.Fatalf("transcript leaks %q", banned)
			}
		}
		if !strings.Contains(transcript, "[user]\ntask") || !strings.Contains(transcript, "[tool call: sh]") || !strings.Contains(transcript, "[tool result: sh]") {
			t.Fatalf("role-tagged blocks missing:\n%s", transcript)
		}
		if !strings.Contains(transcript, "HEAD") || !strings.Contains(transcript, "TAIL") ||
			!strings.Contains(transcript, "tokens omitted from this transcript") || len(transcript) > 20000 {
			t.Fatalf("oversized tool result not capped to head+tail (len %d)", len(transcript))
		}
		return compactionResult{Summary: "summary"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.ChatHistory[len(s.ChatHistory)-1].Content; got != compactionAckMessage {
		t.Fatalf("unexpected history tail: %q", got)
	}
}

func TestCompactionMergesPreviousSummary(t *testing.T) {
	compactionTestConfig(t, true, true, 8000)
	history := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: compactionSummaryPrefix + "RULE: never touch vendor/"},
		{Role: "assistant", Content: compactionAckMessage},
		{Role: "user", Content: "next task"},
		{Role: "assistant", Content: "did it"},
	}
	s := &ChatSession{ChatHistory: history}

	_, _, _, err := compactContextWith(context.Background(), s, false, 8000, func(_ context.Context, messages []types.Message, _ *ChatSession) (compactionResult, error) {
		if !strings.Contains(messages[0].Content, "merged summary") {
			t.Fatal("prompt does not ask for a merged summary")
		}
		input := messages[1].Content
		if strings.Count(input, "RULE: never touch vendor/") != 1 || strings.Contains(input, compactionSummaryPrefix) ||
			strings.Contains(input, compactionAckMessage) || !strings.Contains(input, "next task") {
			t.Fatalf("previous summary not handed over as a distinct input:\n%s", input)
		}
		return compactionResult{Summary: "merged"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.ChatHistory[1].Content != compactionSummaryPrefix+"merged" {
		t.Fatalf("merged summary not committed: %+v", s.ChatHistory)
	}
}

func TestCompactionSkipsWhenOnlyAPreviousSummaryExists(t *testing.T) {
	compactionTestConfig(t, true, true, 8000)
	history := []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: compactionSummaryPrefix + "summary"},
		{Role: "assistant", Content: compactionAckMessage},
	}
	s := &ChatSession{ChatHistory: history}
	called := false

	before, after, _, err := compactContextWith(context.Background(), s, false, 8000, func(context.Context, []types.Message, *ChatSession) (compactionResult, error) {
		called = true
		return compactionResult{Summary: "again"}, nil
	})
	if err != nil || called || before != after || len(s.ChatHistory) != len(history) {
		t.Fatalf("a lone summary must not be re-summarized: called=%v %d->%d err=%v", called, before, after, err)
	}
}

func TestEstimateTokensCountsWideCharactersPerRune(t *testing.T) {
	for text, want := range map[string]int{"日本語": 3, "abcd日": 2, "abcde": 2} {
		if got := estimateTokens(text); got != want {
			t.Errorf("estimateTokens(%q) = %d, want %d", text, got, want)
		}
	}
}
