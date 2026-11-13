package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"fuckyourcli/providers"
	"fuckyourcli/types"
)

type streamFunc func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error)

func (f streamFunc) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	return f(ctx, messages, tools, sink)
}

// A sub-agent has no /retry: a request that dies before producing any output is
// resent by the provider decorator the sub-agent loop uses.
func TestSubagentStreamResendsSilentFailure(t *testing.T) {
	calls := 0
	p := streamFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
		calls++
		if calls == 1 {
			return nil, fmt.Errorf("stream interrupted: %w", providers.ErrStreamIdle)
		}
		return &types.StreamResult{Content: "done", FinishReason: "stop"}, nil
	})

	result, err := subagentStream(p, context.Background(), nil, nil, providers.NewSilentOutputSink())
	if err != nil || result == nil || result.Content != "done" {
		t.Fatalf("sub-agent turn did not recover: %v %+v", err, result)
	}
	if calls != 2 {
		t.Fatalf("want 2 sampling requests, got %d", calls)
	}
}

// A partially consumed sub-agent stream is never replayed, and a cancelled
// sub-agent context never triggers a resend.
func TestSubagentStreamDoesNotReplayVisibleOutput(t *testing.T) {
	calls := 0
	p := streamFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
		calls++
		sink.WriteReasoning("thinking")
		return nil, fmt.Errorf("stream interrupted: %w", providers.ErrStreamIdle)
	})

	if _, err := subagentStream(p, context.Background(), nil, nil, providers.NewSilentOutputSink()); err == nil {
		t.Fatal("failure was swallowed")
	}
	if calls != 1 {
		t.Fatalf("visible output was replayed: %d requests", calls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls = 0
	networkErr := streamFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, _ types.OutputSink) (*types.StreamResult, error) {
		calls++
		return nil, errors.New("read tcp: connection reset by peer")
	})
	if _, err := subagentStream(networkErr, ctx, nil, nil, providers.NewSilentOutputSink()); err == nil {
		t.Fatal("cancelled sub-agent reported success")
	}
	if calls != 1 {
		t.Fatalf("cancelled sub-agent was resent: %d requests", calls)
	}
}
