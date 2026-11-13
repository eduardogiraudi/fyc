package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"syscall"
	"time"

	"fuckyourcli/types"
)

// ErrStreamIdle marks a failure caused by the request-scoped inactivity budget
// expiring before the provider finished the response.
var ErrStreamIdle = errors.New("provider stream idle")

// streamIdleError reports an idle provider without losing the identity of a
// wrapped context.DeadlineExceeded (the compaction fallback and the turn-level
// cancellation checks rely on it) while adding ErrStreamIdle so a resend can be
// told apart from a cancelled turn.
type streamIdleError struct{ idle time.Duration }

func (e *streamIdleError) Error() string {
	return fmt.Sprintf("provider stream idle for %s: %v", e.idle, context.DeadlineExceeded)
}

func (e *streamIdleError) Unwrap() []error {
	return []error{context.DeadlineExceeded, ErrStreamIdle}
}

// MaxStreamResends bounds the extra sampling requests attempted when a request
// fails before producing any output. The budget is deliberately small: a wedged
// backend desynchronizes the conversation when it finally answers, and the user
// is waiting on a spinner the whole time.
const MaxStreamResends = 2

// StreamResendRetryable reports whether a failed sampling request may be
// resent because the failure is connection-level: the provider went idle, or the
// response body died mid-stream. Callers must verify separately that nothing was
// shown to the user yet, since only then does a resend avoid duplicating output.
//
// Failures doWithRetry already retried until exhaustion are excluded: resending
// them here would multiply the attempts behind the user's back.
func StreamResendRetryable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, ErrStreamIdle) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return false
	}
	return isStreamTransportError(err)
}

// isStreamTransportError matches read failures that only the body can produce:
// they arrive after the response was accepted, so the HTTP retry policy never
// saw them.
func isStreamTransportError(err error) bool {
	for _, target := range []error{io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNREFUSED,
		syscall.EPIPE, syscall.ETIMEDOUT, syscall.EHOSTUNREACH, syscall.ENETUNREACH} {
		if errors.Is(err, target) {
			return true
		}
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// StreamResendDelay returns the backoff before the given 1-based resend,
// mirroring the HTTP retry policy so both layers feel the same.
func StreamResendDelay(resend int) time.Duration {
	delay := initialRetryBackoff
	for i := 1; i < resend; i++ {
		delay = nextBackoff(delay)
	}
	return delay
}

// WaitStreamResend shows why the request is being resent and sleeps before the
// next attempt. It reports false when the context was cancelled first.
func WaitStreamResend(ctx context.Context, sink types.OutputSink, resend int, delay time.Duration, reason string) bool {
	return waitWithLabel(ctx, sink, delay, func(d time.Duration) string {
		return fmt.Sprintf("retrying after %s (%d/%d) in %s...", reason, resend, MaxStreamResends, d.Round(100*time.Millisecond))
	})
}

// resendSink records whether a provider has already shown something the user can
// see. Streamed output is committed as completed text by the TUI (and already
// delivered by the AG-UI sink), so only a request that failed silently can be
// resent without duplicating the answer.
type resendSink struct {
	types.OutputSink
	wrote bool
}

func (s *resendSink) WriteText(token string) {
	if token != "" {
		s.wrote = true
	}
	s.OutputSink.WriteText(token)
}

func (s *resendSink) WriteReasoning(delta string) {
	if delta != "" {
		s.wrote = true
	}
	s.OutputSink.WriteReasoning(delta)
}

func (s *resendSink) WriteDiffCraft(add, del int) {
	s.wrote = true
	s.OutputSink.WriteDiffCraft(add, del)
}

// WriteDiffCraftFor keeps the labelled diff-craft path reachable through the
// wrapper: without it every provider would fall back to the plain counter.
func (s *resendSink) WriteDiffCraftFor(label string, add, del int) {
	s.wrote = true
	if ds, ok := s.OutputSink.(types.DiffCraftSink); ok {
		ds.WriteDiffCraftFor(label, add, del)
		return
	}
	s.OutputSink.WriteDiffCraft(add, del)
}

func (s *resendSink) WriteToolOutput(label, res string) {
	s.wrote = true
	s.OutputSink.WriteToolOutput(label, res)
}

type resendProvider struct{ types.Provider }

// WithStreamResend makes StreamChat resend a sampling request that fails before
// producing any visible output (provider idle, connection dropped mid-response),
// up to MaxStreamResends times. A resend is safe only there: the caller's
// history is untouched on failure and no tool has run yet, so nothing is
// replayed or duplicated. A stream that already produced visible output is never
// resent: the caller surfaces the error (the chat loop then offers /retry).
func WithStreamResend(p types.Provider) types.Provider {
	if p == nil {
		return nil
	}
	return &resendProvider{Provider: p}
}

func (p *resendProvider) StreamChat(ctx context.Context, messages []types.Message, tools []types.Tool, sink types.OutputSink) (*types.StreamResult, error) {
	if sink == nil {
		sink = NewSilentOutputSink()
	}
	for resend := 0; ; resend++ {
		attempt := &resendSink{OutputSink: sink}
		result, err := p.Provider.StreamChat(ctx, messages, tools, attempt)
		if err == nil || attempt.wrote || resend == MaxStreamResends || !StreamResendRetryable(ctx, err) {
			return result, err
		}
		reason := "network error"
		if errors.Is(err, ErrStreamIdle) {
			reason = "provider idle"
		}
		if !WaitStreamResend(ctx, sink, resend+1, StreamResendDelay(resend+1), reason) {
			return nil, context.Cause(ctx)
		}
	}
}
