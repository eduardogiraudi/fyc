package providers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"fuckyourcli/logger"
	"fuckyourcli/types"
)

// Retry policy for provider HTTP requests. These bounds are deliberately
// small: the request context bounds the wait and the CLI must stay responsive
// when a backend is rate limiting.
const (
	// maxRetryAttempts is the total number of attempts (1 initial + retries).
	maxRetryAttempts = 4
	// initialRetryBackoff is the delay before the second attempt.
	initialRetryBackoff = 1 * time.Second
	// maxRetryBackoff caps the exponential growth.
	maxRetryBackoff = 15 * time.Second
)

// doWithRetry performs req with client, transparently retrying rate-limited
// (429), overloaded (503/529) responses with exponential backoff. When the
// server advertises a Retry-After (or retry-after-ms / x-ratelimit-reset-*)
// header its value wins over the computed backoff.
//
// Every attempt uses a fresh clone of req whose body is replayed through
// req.GetBody, so POST bodies are not lost between tries. The total wait is
// bounded by the request context deadline, and ctx cancellation (user
// interrupt) aborts the wait immediately. If sink is non-nil, the spinner is
// updated while waiting so the CLI never looks frozen.
//
// The response of the last attempt is returned as-is, so the caller keeps its
// usual "API error (429): ..." handling once the retries are exhausted.
func doWithRetry(ctx context.Context, client *http.Client, req *http.Request, sink types.OutputSink) (*http.Response, error) {
	if client == nil {
		client = providerHTTPClient
	}
	// Without a replayable body we cannot safely retry: the first attempt would
	// consume it. All current callers use *bytes.Buffer, which sets GetBody.
	replayable := req.Body == nil || req.GetBody != nil

	// notified records whether a retry notice was shown on the sink, so the
	// spinner is cleared deterministically once this call returns. The common
	// path also clears it on the first text token, but a 200 response that
	// starts with reasoning (or is empty) would otherwise leave the stale
	// "retrying ..." label on screen mid-answer.
	notified := false
	defer func() {
		if notified && sink != nil {
			sink.StopSpinner()
		}
	}()

	backoff := initialRetryBackoff

	for attempt := 1; attempt <= maxRetryAttempts; attempt++ {
		attemptReq, err := cloneRequest(ctx, req)
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(attemptReq)
		if err != nil {
			if ctx.Err() != nil || attempt == maxRetryAttempts || !replayable {
				logger.HTTPAttempt(ctx, 0, 0, true)
				if ctx.Err() != nil {
					return nil, context.Cause(ctx)
				}
				return nil, err
			}
			if sink != nil {
				notified = true
			}
			logger.HTTPAttempt(ctx, 0, backoff, true)
			if !waitRetry(ctx, sink, attempt, backoff, "network error") {
				return nil, context.Cause(ctx)
			}
			backoff = nextBackoff(backoff)
			continue
		}

		if !isRetryableStatus(resp.StatusCode) || attempt == maxRetryAttempts || !replayable {
			logger.HTTPAttempt(ctx, resp.StatusCode, 0, false)
			return resp, nil
		}

		delay := backoff
		if d, ok := retryAfterDelay(resp); ok {
			delay = d
		}
		logger.HTTPAttempt(ctx, resp.StatusCode, delay, false)
		// Drain before closing so the underlying connection can be reused by
		// the next attempt instead of being torn down.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if sink != nil {
			notified = true
		}
		if !waitRetry(ctx, sink, attempt, delay, fmt.Sprintf("HTTP %d", resp.StatusCode)) {
			return nil, context.Cause(ctx)
		}
		backoff = nextBackoff(backoff)
	}

	// Unreachable: every iteration returns.
	return nil, fmt.Errorf("request retries exhausted")
}

// cloneRequest returns a deep copy of req carrying ctx, with its body reset to
// a fresh reader via GetBody so a retry can replay the payload.
func cloneRequest(ctx context.Context, req *http.Request) (*http.Request, error) {
	clone := req.Clone(ctx)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		clone.Body = body
	}
	return clone, nil
}

// isRetryableStatus reports whether an HTTP status code is worth retrying.
func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusServiceUnavailable, // 503
		529:                           // Anthropic "overloaded"
		return true
	}
	return false
}

// nextBackoff doubles the current backoff, capped at maxRetryBackoff.
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > maxRetryBackoff {
		next = maxRetryBackoff
	}
	return next
}

// retryAfterDelay extracts the server-advertised wait, if any. It understands
// Retry-After (delta-seconds or HTTP-date), retry-after-ms (milliseconds) and
// the x-ratelimit-reset-* headers (Go-style durations such as "6m0s").
func retryAfterDelay(resp *http.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	if v := strings.TrimSpace(resp.Header.Get("retry-after-ms")); v != "" {
		if ms, err := strconv.Atoi(v); err == nil && ms >= 0 {
			return time.Duration(ms) * time.Millisecond, true
		}
	}
	for _, h := range []string{"x-ratelimit-reset-requests", "x-ratelimit-reset-tokens"} {
		if v := strings.TrimSpace(resp.Header.Get(h)); v != "" {
			if d, err := time.ParseDuration(v); err == nil && d >= 0 {
				return d, true
			}
		}
	}
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second, true
		}
		if t, err := http.ParseTime(v); err == nil {
			if d := time.Until(t); d > 0 {
				return d, true
			}
			return 0, true
		}
	}
	return 0, false
}

// waitRetry sleeps for delay (bounded by the context deadline) before the next
// attempt. It reports false when ctx is cancelled first, and updates the sink
// spinner so the user sees why the CLI is waiting.
func waitRetry(ctx context.Context, sink types.OutputSink, attempt int, delay time.Duration, reason string) bool {
	if delay < 0 {
		delay = 0
	}
	// Never oversleep past the request deadline: the context would be cancelled
	// anyway and the caller should surface the cancellation promptly.
	if ctx.Err() != nil {
		return false
	}
	if dl, ok := ctx.Deadline(); ok {
		remaining := time.Until(dl)
		if remaining <= 0 {
			// The wall-clock deadline already elapsed but the context timer may
			// not have fired yet. Wait for it so ctx.Err() is guaranteed
			// non-nil when we report cancellation.
			<-ctx.Done()
			return false
		}
		if remaining < delay {
			delay = remaining
		}
	}
	if delay < 0 {
		delay = 0
	}

	if sink != nil {
		sink.StartSpinner(fmt.Sprintf("retrying %s (%d/%d) in %s...",
			reason, attempt+1, maxRetryAttempts, delay.Round(100*time.Millisecond)))
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	// A delay clamped down to the deadline (or a zero one) fires at the same
	// instant as ctx.Done(), so the select is a coin flip. Re-check the context
	// afterwards: returning false MUST imply ctx.Err() != nil, otherwise the
	// caller's "return nil, ctx.Err()" yields a nil error and looks like a
	// successful (429) response.
	if ctx.Err() != nil {
		return false
	}
	return true
}
