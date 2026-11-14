package providers

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fuckyourcli/logger"
	"fuckyourcli/types"
)

// Retry policy for provider HTTP requests. The request context still bounds
// the total wait and a user interrupt aborts it immediately, so the budgets
// can be generous for load-shedding (429/503/529), which usually clears by
// itself, and stay small for everything that is more likely a real failure.
const (
	// maxRetryAttempts is the total number of attempts (1 initial + retries) for
	// rate-limited (429) and overloaded (503/529) responses.
	maxRetryAttempts = 40
	// maxServerErrorAttempts is the total number of attempts for gateway/server
	// errors (500/502/504/408) and transient network failures: those are often
	// deterministic, so waiting minutes for them would only delay the error.
	maxServerErrorAttempts = 4
	// maxDNSNotFoundAttempts bounds retries of "no such host": usually a typo in
	// the base URL, occasionally a resolver hiccup.
	maxDNSNotFoundAttempts = 2
	// initialRetryBackoff is the delay before the second attempt.
	initialRetryBackoff = 1 * time.Second
	// maxRetryBackoff caps the exponential growth.
	maxRetryBackoff = 30 * time.Second
	// maxRetryAfter is the longest server-advertised wait the CLI sits through.
	// A longer one (an exhausted per-minute token window, a daily quota) is not
	// worth a frozen spinner: the response is returned as-is so the caller shows
	// the provider's own message and the user can /retry later.
	maxRetryAfter = 60 * time.Second
	// spinnerTick is how often the retry countdown shown on the sink refreshes.
	spinnerTick = 1 * time.Second
	// quotaPeekLimit bounds how much of a 429 body is inspected for a hard quota.
	quotaPeekLimit = 8 << 10
)

// randFloat is the jitter source, replaceable in tests.
var randFloat = rand.Float64

// withJitter spreads a computed backoff over +/-25% so concurrent sessions and
// subagents that hit the same 429 do not all come back at the same instant.
func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	return time.Duration(float64(d) * (0.75 + 0.5*randFloat()))
}

// serverJitter returns a small extra wait (up to 10%, at most 2s) added on top
// of a server-advertised delay. The advertised value is a lower bound, so the
// jitter only ever lengthens it.
func serverJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	extra := time.Duration(float64(d) * 0.1 * randFloat())
	if extra > 2*time.Second {
		extra = 2 * time.Second
	}
	return extra
}

// doWithRetry performs req with client, transparently retrying rate-limited
// (429), overloaded (503/529) and transient server (500/502/504/408) responses
// plus transient network errors, with jittered exponential backoff. When the
// server advertises a Retry-After (or retry-after-ms / x-ratelimit-reset-*)
// header its value wins over the computed backoff, unless it exceeds
// maxRetryAfter or the 429 reports an exhausted quota: those are returned
// as-is instead of being waited out.
//
// Every attempt uses a fresh clone of req whose body is replayed through
// req.GetBody, so POST bodies are not lost between tries. The total wait is
// bounded by the request context deadline, and ctx cancellation (user
// interrupt) aborts the wait immediately. If sink is non-nil, the spinner is
// counted down every second while waiting so the CLI never looks frozen.
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

	for attempt := 1; ; attempt++ {
		attemptReq, err := cloneRequest(ctx, req)
		if err != nil {
			return nil, err
		}

		resp, err := client.Do(attemptReq)
		if err != nil {
			limit := networkErrorAttemptLimit(err)
			if ctx.Err() != nil || attempt >= limit || !replayable {
				logger.HTTPAttempt(ctx, 0, 0, true)
				if ctx.Err() != nil {
					return nil, context.Cause(ctx)
				}
				return nil, err
			}
			if sink != nil {
				notified = true
			}
			delay := withJitter(backoff)
			logger.HTTPAttempt(ctx, 0, delay, true)
			if !waitRetry(ctx, sink, attempt, limit, delay, "network error") {
				return nil, context.Cause(ctx)
			}
			backoff = nextBackoff(backoff)
			continue
		}

		limit := retryAttemptLimit(resp.StatusCode)
		if limit == 0 || attempt >= limit || !replayable {
			logger.HTTPAttempt(ctx, resp.StatusCode, 0, false)
			return resp, nil
		}

		// A hard quota ("insufficient_quota") answers 429 too but never clears
		// by waiting: hand it to the caller right away, body intact.
		if resp.StatusCode == http.StatusTooManyRequests && isPermanentQuotaError(peekBody(resp)) {
			logger.HTTPAttempt(ctx, resp.StatusCode, 0, false)
			return resp, nil
		}

		delay := withJitter(backoff)
		if d, ok := retryAfterDelay(resp); ok {
			if d > maxRetryAfter {
				logger.HTTPAttempt(ctx, resp.StatusCode, 0, false)
				return resp, nil
			}
			delay = d + serverJitter(d)
		}
		logger.HTTPAttempt(ctx, resp.StatusCode, delay, false)
		// Drain before closing so the underlying connection can be reused by
		// the next attempt instead of being torn down.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if sink != nil {
			notified = true
		}
		if !waitRetry(ctx, sink, attempt, limit, delay, fmt.Sprintf("HTTP %d", resp.StatusCode)) {
			return nil, context.Cause(ctx)
		}
		backoff = nextBackoff(backoff)
	}
}

// replayBody serves bytes already peeked from a response ahead of the rest of
// its body, and closes the original body.
type replayBody struct {
	io.Reader
	io.Closer
}

// peekBody reads the start of resp's body for inspection and puts it back, so
// the caller (or a later drain) still sees the complete body.
func peekBody(resp *http.Response) []byte {
	head, _ := io.ReadAll(io.LimitReader(resp.Body, quotaPeekLimit))
	resp.Body = &replayBody{Reader: io.MultiReader(bytes.NewReader(head), resp.Body), Closer: resp.Body}
	return head
}

// isPermanentQuotaError recognises a 429 body that reports an exhausted billing
// quota rather than a rate limit.
func isPermanentQuotaError(body []byte) bool {
	b := bytes.ToLower(body)
	return bytes.Contains(b, []byte("insufficient_quota")) || bytes.Contains(b, []byte("exceeded your current quota"))
}

// retryAttemptLimit returns the total attempts allowed for an HTTP status, or 0
// when the status is not worth retrying.
func retryAttemptLimit(code int) int {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusServiceUnavailable, // 503
		529:                           // Anthropic "overloaded"
		return maxRetryAttempts
	case http.StatusRequestTimeout, // 408
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusGatewayTimeout:      // 504
		return maxServerErrorAttempts
	}
	return 0
}

// networkErrorAttemptLimit returns the total attempts allowed after a transport
// error: 0 for failures that no retry can fix (bad certificate, malformed URL),
// a small budget for "no such host", the server-error budget otherwise.
func networkErrorAttemptLimit(err error) int {
	var (
		dnsErr    *net.DNSError
		certErr   *tls.CertificateVerificationError
		unknownCA x509.UnknownAuthorityError
		hostErr   x509.HostnameError
		invalid   x509.CertificateInvalidError
		addrErr   *net.AddrError
		escErr    url.EscapeError
		escHost   url.InvalidHostError
	)
	switch {
	case errors.As(err, &certErr), errors.As(err, &unknownCA), errors.As(err, &hostErr),
		errors.As(err, &invalid), errors.As(err, &addrErr), errors.As(err, &escErr), errors.As(err, &escHost):
		return 0
	case strings.Contains(err.Error(), "unsupported protocol scheme"):
		return 0
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return maxDNSNotFoundAttempts
	}
	return maxServerErrorAttempts
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
	return retryAttemptLimit(code) > 0
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

// formatWait renders a remaining wait for the retry notice: whole seconds
// (rounded up, so a countdown never shows 0s while still waiting) or m:ss.
func formatWait(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	secs := int((d + time.Second - 1) / time.Second)
	if secs >= 60 {
		return fmt.Sprintf("%dm%02ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%ds", secs)
}

// waitRetry sleeps for delay (bounded by the context deadline) before the next
// attempt. It reports false when ctx is cancelled first, and keeps the sink
// spinner counting down so the user sees why the CLI is waiting. limit is the
// total attempts allowed for the failure being retried.
func waitRetry(ctx context.Context, sink types.OutputSink, attempt, limit int, delay time.Duration, reason string) bool {
	return waitWithLabel(ctx, sink, delay, func(d time.Duration) string {
		return fmt.Sprintf("retrying %s (%d/%d) in %s...", reason, attempt+1, limit, formatWait(d))
	})
}

// waitWithLabel sleeps for delay (bounded by the context deadline) before the
// next attempt, refreshing the sink spinner every spinnerTick with the label
// for the time still left. It reports false when ctx is cancelled first.
func waitWithLabel(ctx context.Context, sink types.OutputSink, delay time.Duration, label func(time.Duration) string) bool {
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

	end := time.Now().Add(delay)
	for {
		remaining := time.Until(end)
		if remaining < 0 {
			remaining = 0
		}
		if sink != nil {
			sink.StartSpinner(label(remaining))
		}
		step := remaining
		if step > spinnerTick {
			step = spinnerTick
		}
		timer := time.NewTimer(step)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
		// A delay clamped down to the deadline (or a zero one) fires at the same
		// instant as ctx.Done(), so the select is a coin flip. Re-check the
		// context: returning false MUST imply ctx.Err() != nil, otherwise the
		// caller's "return nil, ctx.Err()" yields a nil error and looks like a
		// successful (429) response.
		if ctx.Err() != nil {
			return false
		}
		if time.Until(end) <= 0 {
			return true
		}
	}
}
