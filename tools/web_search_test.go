package tools

import (
	"context"
	"strings"
	"testing"
)

func TestWebSearchContextIsPropagated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := executeWebSearchTool(ctx, map[string]any{"input": "https://example.com"})
	if !strings.Contains(got, "context canceled") {
		t.Fatalf("web_search dropped turn cancellation: %s", got)
	}
}

func TestWebSearchRemainsAvailableWithoutBrowser(t *testing.T) {
	if reason := WebSearchBlockReason(); reason != "" {
		t.Fatal("HTTP path hidden:", reason)
	}
}
