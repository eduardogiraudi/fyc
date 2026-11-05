package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/types"
)

func TestPruneScreenshotImagesCountsAttachments(t *testing.T) {
	image := func(name string) types.ImageAttachment {
		return types.ImageAttachment{MIMEType: "image/png", Data: name, Name: name}
	}
	history := []types.Message{
		{Role: "user", Content: "task"},
		{Role: "tool", Name: "read_media", ToolCallID: "files", Content: "files", Images: []types.ImageAttachment{image("a"), image("b")}},
		{Role: "assistant", Content: "working"},
		{Role: "tool", Name: "computer", ToolCallID: "shot", Images: []types.ImageAttachment{image("c")}},
		{Role: "tool", Name: "read_media", ToolCallID: "new-files", Content: "new files", Images: []types.ImageAttachment{image("d"), image("e")}},
	}
	before, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	for _, keep := range []int{-1, 0, 1, 2, 3, 4, 5, 10} {
		t.Run(fmt.Sprint(keep), func(t *testing.T) {
			pruned := pruneScreenshotImages(history, keep)
			var names []string
			for i, m := range pruned {
				if m.Role != history[i].Role || m.Name != history[i].Name || m.ToolCallID != history[i].ToolCallID {
					t.Fatal("pruning changed message identity")
				}
				for _, img := range m.Images {
					names = append(names, img.Name)
				}
				if len(m.Images) < len(history[i].Images) {
					if !strings.Contains(m.Content, "image omitted") || !strings.HasPrefix(m.Content, history[i].Content) {
						t.Fatal("pruned media lost its text or placeholder")
					}
				} else if m.Content != history[i].Content {
					t.Fatal("unpruned message text changed")
				}
			}
			all := []string{"a", "b", "c", "d", "e"}
			count := min(max(keep, 0), len(all))
			if len(names) != count || (count > 0 && !reflect.DeepEqual(names, all[len(all)-count:])) {
				t.Fatalf("kept images = %v, want newest %d", names, count)
			}
			after, err := json.Marshal(history)
			if err != nil || string(after) != string(before) {
				t.Fatal("pruning mutated canonical history")
			}
		})
	}
	if len(pruneScreenshotImages(nil, 10)) != 0 {
		t.Fatal("empty history gained messages")
	}
}

func mediaTestHistory() []types.Message {
	images := make([]types.ImageAttachment, 12)
	for i := range images {
		images[i] = types.ImageAttachment{MIMEType: "image/png", Data: fmt.Sprint(i), Width: 1000, Height: 750}
	}
	return []types.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "media", Function: types.ToolCallFunction{Name: "read_media"}}}},
		{Role: "tool", Name: "read_media", ToolCallID: "media", Content: "files", Images: images},
	}
}

func TestMaxMediaUsedForRequestsAndTokenEstimates(t *testing.T) {
	cfg := compactionTestConfig(t, false, false, 8000)
	for _, limit := range []int{0, 2, 1, 20} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			config.SetMaxMedia(limit)
			keep := limit
			if keep <= 0 {
				keep = 10
			}
			history := mediaTestHistory()
			s := &ChatSession{ChatHistory: history}
			want := pruneScreenshotImages(history, keep)
			wantTokens := estimateHistoryTokens(want) + estimateToolsTokens(s.getToolsForAI())
			if got := currentContextTokens(s); got != wantTokens {
				t.Fatalf("estimated context = %d, want %d", got, wantTokens)
			}
			called := false
			p := compactionProviderFunc(func(_ context.Context, messages []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
				called = true
				if !reflect.DeepEqual(messages, want) {
					t.Fatal("request did not use the current configured media limit")
				}
				return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
			})
			streamProviderLoop(s, streamOptions{}, cfg, p, nil)
			if !called || !reflect.DeepEqual(s.ChatHistory[:len(history)], history) || len(history[3].Images) != 12 {
				t.Fatal("request skipped the provider or altered canonical images")
			}
		})
	}
}

func TestMaxMediaUsedForCompactionPlanning(t *testing.T) {
	compactionTestConfig(t, false, false, 8000)
	s := &ChatSession{ChatHistory: mediaTestHistory()}
	config.SetMaxMedia(1)
	low, err := planTurnCompaction(s, 8000)
	if err != nil {
		t.Fatal(err)
	}
	config.SetMaxMedia(10)
	high, err := planTurnCompaction(s, 8000)
	if err != nil {
		t.Fatal(err)
	}
	if len(low.older) != 0 || len(high.older) != 2 {
		t.Fatalf("planning ignored media cost: older groups with limits 1/10 = %d/%d", len(low.older), len(high.older))
	}
	if len(s.ChatHistory[3].Images) != 12 {
		t.Fatal("compaction planning mutated media history")
	}
}
