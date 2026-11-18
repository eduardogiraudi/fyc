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

// ErrStreamTransient marks a failure that happened inside an already accepted
// stream but says nothing about the request itself: the provider reported an
// overload/internal error as an in-band event, or the stream was cut off before
// its terminal event. Like an idle provider, it is safe to resend while nothing
// has been shown to the user.
var ErrStreamTransient = errors.New("provider stream failed transiently")

// ErrStreamRateLimit marks an in-band rate limit, which follows the HTTP 429
// retry budget instead of the smaller budget for other stream failures.
var ErrStreamRateLimit = errors.New("provider stream rate limited")

// transientStreamError tags err as ErrStreamTransient without changing its
// message, so logs and the error shown to the user stay exactly as before.
type transientStreamError struct{ err error }

func (e *transientStreamError) Error() string   { return e.err.Error() }
func (e *transientStreamError) Unwrap() []error { return []error{e.err, ErrStreamTransient} }

// transientStream wraps err so StreamResendRetryable accepts it.
func transientStream(err error) error {
	if err == nil {
		return nil
	}
	return &transientStreamError{err: err}
}

type rateLimitStreamError struct{ err error }

func (e *rateLimitStreamError) Error() string { return e.err.Error() }
func (e *rateLimitStreamError) Unwrap() []error {
	return []error{e.err, ErrStreamRateLimit, ErrStreamTransient}
}

func rateLimitedStream(err error) error {
	if err == nil {
		return nil
	}
	return &rateLimitStreamError{err: err}
}

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

// MaxStreamResends bounds extra sampling requests after transient failures.
// Each resend is a full sampling request, so keep this below the HTTP budget.
const MaxStreamResends = 3

// StreamResendRetryable reports whether a failed sampling request may be
// resent because the failure is connection-level: the provider went idle, or the
// response body died mid-stream. Callers must separately ensure any visible
// output can be retracted before resending.
//
// Failures doWithRetry already retried until exhaustion are excluded: resending
// them here would multiply the attempts behind the user's back.
func StreamResendRetryable(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	if errors.Is(err, ErrStreamIdle) || errors.Is(err, ErrStreamTransient) {
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

// StreamResendDelay returns the base backoff before the given 1-based resend,
// mirroring the HTTP retry policy so both layers feel the same. The caller
// applies jitter.
func StreamResendDelay(resend int) time.Duration {
	delay := initialRetryBackoff
	for i := 1; i < resend; i++ {
		delay = nextBackoff(delay)
	}
	return delay
}

// WaitStreamResend shows why the request is being resent and sleeps before the
// next attempt, counting the remaining time down on the sink. It reports false
// when the context was cancelled first.
func WaitStreamResend(ctx context.Context, sink types.OutputSink, resend int, delay time.Duration, reason string) bool {
	return waitWithLabel(ctx, sink, delay, func(d time.Duration) string {
		return fmt.Sprintf("retrying after %s (%d/%d) in %s...", reason, resend, MaxStreamResends, formatWait(d))
	})
}

// resendSink tracks visible output; only retractable frames may replay it.
type resendSink struct {
	types.OutputSink
	wrote      bool
	toolOutput bool
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
	s.toolOutput = true
	s.OutputSink.WriteToolOutput(label, res)
}

type resendProvider struct{ types.Provider }

// WithStreamResend retries transient sampling failures without touching history
// or executing tools. Visible output requires a sink that can retract the
// failed attempt (the alt-screen TUI); REPL and AG-UI keep manual retry.
// Ordinary failures use MaxStreamResends; in-band rate limits use the HTTP budget.
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
	rollback, supportsRollback := sink.(types.StreamAttemptSink)
	if supportsRollback {
		supportsRollback = rollback.BeginStreamAttempt(ctx)
		if supportsRollback {
			defer rollback.EndStreamAttempt()
		}
	}
	for attemptNumber := 1; ; attemptNumber++ {
		attempt := &resendSink{OutputSink: sink}
		result, err := p.Provider.StreamChat(ctx, messages, tools, attempt)
		if err == nil || (attempt.wrote && !supportsRollback) || attempt.toolOutput || !StreamResendRetryable(ctx, err) {
			return result, err
		}

		limit := MaxStreamResends + 1
		reason := "network error"
		rateLimited := errors.Is(err, ErrStreamRateLimit)
		switch {
		case rateLimited:
			limit = maxRetryAttempts
			reason = "HTTP 429"
		case errors.Is(err, ErrStreamIdle):
			reason = "provider idle"
		case errors.Is(err, ErrStreamTransient):
			reason = "provider error"
		}
		if attemptNumber >= limit {
			return result, err
		}

		if supportsRollback && !rollback.RollbackStreamAttempt(ctx) {
			return result, err
		}
		delay := withJitter(StreamResendDelay(attemptNumber))
		if rateLimited {
			if !waitRetry(ctx, sink, attemptNumber, limit, delay, reason) {
				return nil, context.Cause(ctx)
			}
		} else if !WaitStreamResend(ctx, sink, attemptNumber, delay, reason) {
			return nil, context.Cause(ctx)
		}
	}
}

// isTransientAnthropicErrorType reports whether an in-band Anthropic stream
// error is load shedding or an internal failure that a resend can outlive.
// Request errors (invalid_request_error, authentication_error, ...) are not.
func isTransientAnthropicErrorType(t string) bool {
	switch t {
	case "overloaded_error", "api_error", "rate_limit_error", "timeout_error":
		return true
	}
	return false
}
