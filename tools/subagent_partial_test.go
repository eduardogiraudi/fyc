package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fuckyourcli/config"
	"fuckyourcli/providers"
	"fuckyourcli/types"
)

func withWrapUpProvider(t *testing.T, fn streamFunc) {
	t.Helper()
	prev := SubagentProviderFactory
	SubagentProviderFactory = func(config.Config, config.CompactorChoice, string) (types.Provider, error) { return fn, nil }
	t.Cleanup(func() { SubagentProviderFactory = prev })
}

var wrapChain = []config.CompactorChoice{{Current: true, Kind: config.CompactorOpenAI, Provider: "P", Model: "m"}}

func TestSubagentStoppedAsksModelForPartialResult(t *testing.T) {
	var gotTools []types.Tool
	var last types.Message
	withWrapUpProvider(t, func(_ context.Context, msgs []types.Message, tools []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
		gotTools = tools
		last = msgs[len(msgs)-1]
		return &types.StreamResult{Content: "Status: partial. Read a.go.", FinishReason: "stop"}, nil
	})
	defs := []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "read"}}}
	out := subagentStopped(context.Background(), config.Config{}, wrapChain, 0, "sys",
		[]types.Message{{Role: "system", Content: "sys"}}, defs, providers.NewSilentOutputSink(),
		"reached the iteration limit (3)", []string{"read a.go"}, "")
	if !strings.Contains(out, "reached the iteration limit (3)") || !strings.Contains(out, "Status: partial. Read a.go.") {
		t.Fatalf("partial result missing: %q", out)
	}
	if len(gotTools) != 1 {
		t.Fatal("tool definitions must stay in the wrap-up request: some providers reject tool calls in the history without them")
	}
	if last.Role != "user" || !strings.Contains(last.Content, "Do not call any tool") {
		t.Fatalf("wrap-up instruction not sent last: %+v", last)
	}
}

func TestSubagentStoppedFallsBackToDigest(t *testing.T) {
	withWrapUpProvider(t, func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		return nil, errors.New("invalid api key")
	})
	out := subagentStopped(context.Background(), config.Config{}, wrapChain, 0, "sys", nil, nil,
		providers.NewSilentOutputSink(), "reached the time limit (5m0s)", []string{"read a.go", "searched for foo"}, "I found foo in a.go")
	for _, want := range []string{"time limit", "wrap-up request failed", "- read a.go", "- searched for foo", "I found foo in a.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("digest lacks %q:\n%s", want, out)
		}
	}
}

func TestSubagentStoppedDoesNotRunAfterInterruption(t *testing.T) {
	calls := 0
	withWrapUpProvider(t, func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
		calls++
		return &types.StreamResult{Content: "x"}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := subagentStopped(ctx, config.Config{}, wrapChain, 0, "sys", nil, nil, providers.NewSilentOutputSink(), "r", nil, "")
	if out != "subagent interrupted" || calls != 0 {
		t.Fatalf("out=%q calls=%d: a user interruption must not trigger a wrap-up", out, calls)
	}
}

func TestSubagentDigestBoundsAndEmpty(t *testing.T) {
	if subagentDigest(nil, "  ") != "" {
		t.Fatal("nothing to report must give an empty digest")
	}
	steps := make([]string, maxDigestSteps+5)
	for i := range steps {
		steps[i] = "step"
	}
	if d := subagentDigest(steps, ""); !strings.Contains(d, "5 earlier steps omitted") {
		t.Fatalf("long digests must be bounded: %s", d)
	}
}
