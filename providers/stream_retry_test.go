package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"fuckyourcli/types"
)

// labelSink captures the spinner labels shown while a resend waits.
type labelSink struct {
	SilentOutputSink
	labels []string
}

func (s *labelSink) StartSpinner(label string) { s.labels = append(s.labels, label) }

func TestStreamIdleErrorKeepsBothIdentities(t *testing.T) {
	err := error(&streamIdleError{idle: 30 * time.Second})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle error must stay a DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, ErrStreamIdle) {
		t.Fatalf("idle error must be matched by ErrStreamIdle: %v", err)
	}
	if got := err.Error(); got != "provider stream idle for 30s: context deadline exceeded" {
		t.Fatalf("unexpected message: %q", got)
	}
}

func TestStreamResendRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"idle", fmt.Errorf("chat completions stream interrupted: %w", &streamIdleError{idle: time.Second}), true},
		{"connection reset", fmt.Errorf("stream interrupted: %w", syscall.ECONNRESET), true},
		{"unexpected eof", fmt.Errorf("stream interrupted: %w", io.ErrUnexpectedEOF), true},
		// doWithRetry already exhausted its own budget for these.
		{"dial failure", &url.Error{Op: "Post", URL: "https://example.test", Err: syscall.ECONNREFUSED}, false},
		{"protocol", errors.New("chat completions stream ended before a terminal event (incomplete response)"), false},
		{"http", errors.New("API error (400): bad request"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StreamResendRetryable(context.Background(), tc.err); got != tc.want {
				t.Fatalf("StreamResendRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if StreamResendRetryable(cancelled, syscall.ECONNRESET) {
		t.Fatal("a cancelled turn must never be resent")
	}
}

func TestStreamResendDelayMirrorsHTTPPolicy(t *testing.T) {
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, maxRetryBackoff, maxRetryBackoff}
	for i, d := range want {
		if got := StreamResendDelay(i + 1); got != d {
			t.Fatalf("StreamResendDelay(%d) = %s, want %s", i+1, got, d)
		}
	}
}

func TestWaitStreamResendShowsReasonAndHonorsCancel(t *testing.T) {
	sink := &labelSink{}
	if !WaitStreamResend(context.Background(), sink, 1, 2*time.Millisecond, "provider idle") {
		t.Fatal("resend wait aborted without a cancellation")
	}
	notice := fmt.Sprintf("(1/%d)", MaxStreamResends)
	if len(sink.labels) != 1 || !strings.Contains(sink.labels[0], "provider idle") || !strings.Contains(sink.labels[0], notice) {
		t.Fatalf("missing retry notice: %v", sink.labels)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if WaitStreamResend(ctx, sink, 1, time.Second, "provider idle") {
		t.Fatal("cancelled wait reported success")
	}
}

// craftSink records the labelled diff-craft path (an optional OutputSink
// extension) to verify the tracking wrapper does not hide it.
type craftSink struct {
	SilentOutputSink
	labels []string
}

func (s *craftSink) WriteDiffCraftFor(label string, add, del int) {
	s.labels = append(s.labels, label)
}

// countSink counts the visible output calls so the wrapper cannot duplicate or
// swallow them.
type countSink struct {
	SilentOutputSink
	texts, reasonings, tools, crafts int
}

func (s *countSink) WriteText(string)               { s.texts++ }
func (s *countSink) WriteReasoning(string)          { s.reasonings++ }
func (s *countSink) WriteToolOutput(string, string) { s.tools++ }
func (s *countSink) WriteDiffCraft(int, int)        { s.crafts++ }

func TestResendSinkTracksOutputAndKeepsDiffCraft(t *testing.T) {
	inner := &craftSink{}
	wrapped := &resendSink{OutputSink: inner}
	if _, ok := any(wrapped).(types.DiffCraftSink); !ok {
		t.Fatal("wrapper must keep the diff-craft extension reachable")
	}
	wrapped.WriteText("")
	if wrapped.wrote {
		t.Fatal("an empty token is not visible output")
	}
	wrapped.WriteText(" ")
	if !wrapped.wrote {
		t.Fatal("a streamed token must count as visible output")
	}
	wrapped.WriteDiffCraftFor("patching file a.go", 1, 2)
	if len(inner.labels) != 1 || inner.labels[0] != "patching file a.go" {
		t.Fatalf("labelled diff-craft lost: %v", inner.labels)
	}
	plain := &countSink{}
	plainWrap := &resendSink{OutputSink: plain}
	plainWrap.WriteDiffCraftFor("x", 1, 1)
	if plain.crafts != 1 || !plainWrap.wrote {
		t.Fatalf("plain diff-craft fallback lost: %+v", plain)
	}

	// Fresh wrappers: `wrapped` above is already marked by WriteText.
	fresh := &resendSink{OutputSink: &craftSink{}}
	fresh.WriteDiffCraftFor("x", 1, 1)
	if !fresh.wrote {
		t.Fatal("labelled diff craft must count as visible output")
	}
	plainFresh := &resendSink{OutputSink: &countSink{}}
	plainFresh.WriteDiffCraft(1, 1)
	if !plainFresh.wrote {
		t.Fatal("plain diff craft must count as visible output")
	}

	counting := &countSink{}
	countingWrapper := &resendSink{OutputSink: counting}
	countingWrapper.WriteText("tok")
	countingWrapper.WriteReasoning("thinking")
	countingWrapper.WriteToolOutput("ls", "out")
	if counting.texts != 1 || counting.reasonings != 1 || counting.tools != 1 {
		t.Fatalf("output must be forwarded exactly once: %+v", counting)
	}
	if !countingWrapper.wrote {
		t.Fatal("forwarded output must be recorded as visible")
	}
}

func TestResendSinkRecordsReasoningAndToolOutput(t *testing.T) {
	reasoning := &resendSink{OutputSink: &SilentOutputSink{}}
	reasoning.WriteReasoning("thinking")
	if !reasoning.wrote {
		t.Fatal("reasoning is visible output")
	}
	tool := &resendSink{OutputSink: &SilentOutputSink{}}
	tool.WriteToolOutput("ls", "out")
	if !tool.wrote {
		t.Fatal("tool output is visible output")
	}
}

// streamFunc adapts a function to types.Provider for decorator tests.
type streamFunc func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error)

func (f streamFunc) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	return f(ctx, messages, tools, sink)
}

// The decorator must not resend what a resend cannot fix, and must never hide a
// partially consumed stream. Both cases stop at the first attempt (no waits).
func TestWithStreamResendSkipsUnsafeRetries(t *testing.T) {
	cases := []struct {
		name    string
		fail    func(types.OutputSink) error
		nilSink bool
	}{
		{"protocol error", func(types.OutputSink) error { return errors.New("stream ended before a terminal event") }, false},
		{"api error", func(types.OutputSink) error { return errors.New("API error (400): bad request") }, false},
		{"visible output", func(sink types.OutputSink) error {
			sink.WriteText("partial")
			return fmt.Errorf("interrupted: %w", &streamIdleError{idle: time.Second})
		}, false},
		{"nil sink", func(types.OutputSink) error { return errors.New("API error (500)") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := WithStreamResend(streamFunc(func(_ context.Context, _ []types.Message, _ []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
				calls++
				return nil, tc.fail(sink)
			}))
			sink := types.OutputSink(&SilentOutputSink{})
			if tc.nilSink {
				sink = nil
			}
			if _, err := p.StreamChat(context.Background(), nil, nil, sink); err == nil {
				t.Fatal("failure was swallowed")
			}
			if calls != 1 {
				t.Fatalf("unsafe retry: %d attempts", calls)
			}
		})
	}
}
