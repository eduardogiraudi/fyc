package providers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"fuckyourcli/types"
)

// reply is one scripted transport answer: a response, or a transport error.
type reply struct {
	status int
	header http.Header
	body   string
	err    error
}

// scriptedClient answers attempt i with replies[i] (the last one repeats) and
// counts the attempts made. It never touches the network, so it can run inside
// a synctest bubble where waits cost no wall-clock time.
func scriptedClient(attempts *int, replies ...reply) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		i := *attempts
		*attempts++
		if i >= len(replies) {
			i = len(replies) - 1
		}
		r := replies[i]
		if r.err != nil {
			return nil, r.err
		}
		h := r.header
		if h == nil {
			h = http.Header{}
		}
		return &http.Response{StatusCode: r.status, Header: h, Body: io.NopCloser(strings.NewReader(r.body)), Request: req}, nil
	})}
}

// withRand pins the jitter source: 0.5 means "no jitter" for withJitter (x1.0).
func withRand(t *testing.T, v float64) {
	t.Helper()
	old := randFloat
	randFloat = func() float64 { return v }
	t.Cleanup(func() { randFloat = old })
}

func retryAfter(v string) http.Header { return http.Header{"Retry-After": []string{v}} }

// totalBackoff is the wait accumulated between `attempts` attempts with no
// jitter and no server-advertised delay.
func totalBackoff(attempts int) time.Duration {
	var total time.Duration
	b := initialRetryBackoff
	for i := 1; i < attempts; i++ {
		total += b
		b = nextBackoff(b)
	}
	return total
}

// countdownSink records every spinner label and the number of stops.
type countdownSink struct {
	SilentOutputSink
	labels []string
	stops  int
}

func (s *countdownSink) StartSpinner(label string) { s.labels = append(s.labels, label) }
func (s *countdownSink) StopSpinner()              { s.stops++ }

func TestDoWithRetryRetriesTransientServerErrors(t *testing.T) {
	for _, status := range []int{500, 502, 504, 408} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var attempts int
				client := scriptedClient(&attempts, reply{status: status}, reply{status: 200})
				resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				resp.Body.Close()
				if resp.StatusCode != 200 || attempts != 2 {
					t.Fatalf("status=%d attempts=%d, want 200 after 2", resp.StatusCode, attempts)
				}
			})
		})
	}
}

func TestDoWithRetryBudgets(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		attempts int
		elapsed  time.Duration
	}{
		{"server error", 500, maxServerErrorAttempts, totalBackoff(maxServerErrorAttempts)},
		{"rate limit", 429, maxRetryAttempts, totalBackoff(maxRetryAttempts)},
		{"overloaded", 529, maxRetryAttempts, totalBackoff(maxRetryAttempts)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withRand(t, 0.5)
				var attempts int
				client := scriptedClient(&attempts, reply{status: tc.status, body: "still failing"})
				start := time.Now()
				resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != tc.status || string(body) != "still failing" {
					t.Fatalf("last response not preserved: %d %q", resp.StatusCode, body)
				}
				if attempts != tc.attempts {
					t.Fatalf("attempts=%d, want %d", attempts, tc.attempts)
				}
				if got := time.Since(start); got != tc.elapsed {
					t.Fatalf("waited %s, want %s", got, tc.elapsed)
				}
			})
		})
	}
}

func TestDoWithRetryJitterBounds(t *testing.T) {
	cases := []struct {
		name    string
		rnd     float64
		elapsed time.Duration
	}{
		{"low", 0, 2250 * time.Millisecond},  // (1s+2s) x 0.75
		{"none", 0.5, 3 * time.Second},       // (1s+2s) x 1.00
		{"high", 1, 3750 * time.Millisecond}, // (1s+2s) x 1.25
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withRand(t, tc.rnd)
				var attempts int
				client := scriptedClient(&attempts, reply{status: 429}, reply{status: 429}, reply{status: 200})
				start := time.Now()
				resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				resp.Body.Close()
				if got := time.Since(start); got != tc.elapsed {
					t.Fatalf("waited %s, want %s", got, tc.elapsed)
				}
			})
		})
	}
}

// The server's wait is a lower bound: jitter may lengthen it, never shorten it,
// and the addition is small.
func TestDoWithRetryServerDelayIsOnlyLengthened(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		rnd     float64
		elapsed time.Duration
	}{
		{"exact", "5", 0, 5 * time.Second},
		{"ten percent", "5", 1, 5500 * time.Millisecond},
		{"capped extra", "60", 1, 62 * time.Second}, // 10% would be 6s, capped at 2s
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				withRand(t, tc.rnd)
				var attempts int
				client := scriptedClient(&attempts, reply{status: 429, header: retryAfter(tc.header)}, reply{status: 200})
				start := time.Now()
				resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				resp.Body.Close()
				if got := time.Since(start); got != tc.elapsed {
					t.Fatalf("waited %s, want %s", got, tc.elapsed)
				}
			})
		})
	}
}

func TestDoWithRetryDoesNotWaitOutLongRetryAfter(t *testing.T) {
	headers := map[string]http.Header{
		"retry-after":    retryAfter("61"),
		"ratelimit":      {"X-Ratelimit-Reset-Tokens": []string{"6m0s"}},
		"retry-after-ms": {"Retry-After-Ms": []string{"90000"}},
	}
	for name, h := range headers {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var attempts int
				client := scriptedClient(&attempts, reply{status: 429, header: h, body: `{"error":"rate limited"}`}, reply{status: 200})
				sink := &countdownSink{}
				start := time.Now()
				resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), sink)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				defer resp.Body.Close()
				body, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != 429 || string(body) != `{"error":"rate limited"}` {
					t.Fatalf("429 must surface intact: %d %q", resp.StatusCode, body)
				}
				if attempts != 1 || time.Since(start) != 0 || len(sink.labels) != 0 {
					t.Fatalf("waited for an over-cap Retry-After: attempts=%d elapsed=%s labels=%v", attempts, time.Since(start), sink.labels)
				}
			})
		})
	}

	// Exactly the cap is still worth waiting for.
	synctest.Test(t, func(t *testing.T) {
		withRand(t, 0)
		var attempts int
		client := scriptedClient(&attempts, reply{status: 429, header: retryAfter("60")}, reply{status: 200})
		resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || attempts != 2 {
			t.Fatalf("status=%d attempts=%d, want 200 after 2", resp.StatusCode, attempts)
		}
	})
}

func TestDoWithRetryDoesNotRetryExhaustedQuota(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		quota := `{"error":{"message":"You exceeded your current quota","code":"insufficient_quota"}}`
		var attempts int
		client := scriptedClient(&attempts, reply{status: 429, body: quota}, reply{status: 200})
		resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 429 || string(body) != quota || attempts != 1 {
			t.Fatalf("quota 429 must surface at once with its body: status=%d attempts=%d body=%q", resp.StatusCode, attempts, body)
		}
	})

	// A plain rate limit still retries.
	synctest.Test(t, func(t *testing.T) {
		var attempts int
		client := scriptedClient(&attempts, reply{status: 429, body: `{"error":"rate limited"}`}, reply{status: 200})
		resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || attempts != 2 {
			t.Fatalf("status=%d attempts=%d, want 200 after 2", resp.StatusCode, attempts)
		}
	})
}

func TestNetworkErrorAttemptLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"unknown authority", &url.Error{Op: "Post", URL: "https://x.test", Err: &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}}, 0},
		{"bare unknown authority", x509.UnknownAuthorityError{}, 0},
		{"bad scheme", errors.New(`Post "ftp://x": unsupported protocol scheme "ftp"`), 0},
		{"bad escape", &url.Error{Op: "Post", URL: "%zz", Err: url.EscapeError("%zz")}, 0},
		{"unknown address", &net.OpError{Op: "dial", Err: &net.AddrError{Err: "missing port in address", Addr: "x"}}, 0},
		{"dns not found", &url.Error{Op: "Post", URL: "https://x.test", Err: &net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}}, maxDNSNotFoundAttempts},
		{"dns timeout", &net.DNSError{Err: "i/o timeout", Name: "x.test", IsTimeout: true}, maxServerErrorAttempts},
		{"connection reset", &url.Error{Op: "Post", URL: "https://x.test", Err: syscall.ECONNRESET}, maxServerErrorAttempts},
		{"unexpected eof", io.ErrUnexpectedEOF, maxServerErrorAttempts},
		{"unknown", errors.New("something odd"), maxServerErrorAttempts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := networkErrorAttemptLimit(tc.err); got != tc.want {
				t.Fatalf("networkErrorAttemptLimit(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

func TestDoWithRetryNetworkErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		attempts int
	}{
		{"certificate", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, 1},
		{"no such host", &net.DNSError{Err: "no such host", Name: "x.test", IsNotFound: true}, maxDNSNotFoundAttempts},
		{"connection reset", syscall.ECONNRESET, maxServerErrorAttempts},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var attempts int
				client := scriptedClient(&attempts, reply{err: tc.err})
				resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
				if err == nil {
					resp.Body.Close()
					t.Fatal("expected the transport error")
				}
				if !errors.Is(err, tc.err) && !errors.As(err, new(*net.DNSError)) {
					t.Fatalf("error identity lost: %v", err)
				}
				if attempts != tc.attempts {
					t.Fatalf("attempts=%d, want %d", attempts, tc.attempts)
				}
			})
		})
	}

	// A transient failure that clears is retried to success.
	synctest.Test(t, func(t *testing.T) {
		var attempts int
		client := scriptedClient(&attempts, reply{err: syscall.ECONNRESET}, reply{err: syscall.ECONNRESET}, reply{status: 200})
		resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		if attempts != 3 {
			t.Fatalf("attempts=%d, want 3", attempts)
		}
	})
}

func TestFormatWait(t *testing.T) {
	cases := map[time.Duration]string{
		0:                        "0s",
		40 * time.Millisecond:    "1s",
		400 * time.Millisecond:   "1s",
		999 * time.Millisecond:   "1s",
		time.Second:              "1s",
		1001 * time.Millisecond:  "2s",
		30 * time.Second:         "30s",
		59200 * time.Millisecond: "1m00s",
		90 * time.Second:         "1m30s",
		6 * time.Minute:          "6m00s",
	}
	for d, want := range cases {
		if got := formatWait(d); got != want {
			t.Errorf("formatWait(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestWaitRetryCountsDown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		sink := &countdownSink{}
		start := time.Now()
		if !waitRetry(context.Background(), sink, 1, 4, 3500*time.Millisecond, "HTTP 429") {
			t.Fatal("wait aborted without a cancellation")
		}
		if got := time.Since(start); got != 3500*time.Millisecond {
			t.Fatalf("waited %s, want 3.5s", got)
		}
		want := []string{
			"retrying HTTP 429 (2/4) in 4s...",
			"retrying HTTP 429 (2/4) in 3s...",
			"retrying HTTP 429 (2/4) in 2s...",
			"retrying HTTP 429 (2/4) in 1s...",
		}
		if strings.Join(sink.labels, "|") != strings.Join(want, "|") {
			t.Fatalf("countdown labels:\n got %q\nwant %q", sink.labels, want)
		}
	})
}

func TestWaitCountdownStopsOnCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { time.Sleep(2500 * time.Millisecond); cancel() }()
		sink := &countdownSink{}
		if waitRetry(ctx, sink, 1, 8, 10*time.Second, "HTTP 429") {
			t.Fatal("cancelled wait reported success")
		}
		if len(sink.labels) != 3 {
			t.Fatalf("want 3 refreshes before the cancel, got %q", sink.labels)
		}
	})
}

func TestDoWithRetryShowsCountdownAndStopsSpinner(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withRand(t, 0)
		var attempts int
		client := scriptedClient(&attempts, reply{status: 429, header: retryAfter("3")}, reply{status: 200})
		sink := &countdownSink{}
		resp, err := doWithRetry(context.Background(), client, newRequest(t, "http://example.test/", "{}"), sink)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		resp.Body.Close()
		want := []string{
			"retrying HTTP 429 (2/40) in 3s...",
			"retrying HTTP 429 (2/40) in 2s...",
			"retrying HTTP 429 (2/40) in 1s...",
		}
		if strings.Join(sink.labels, "|") != strings.Join(want, "|") {
			t.Fatalf("countdown labels:\n got %q\nwant %q", sink.labels, want)
		}
		if sink.stops != 1 {
			t.Fatalf("want 1 spinner stop, got %d", sink.stops)
		}
	})
}

func TestStreamResendRetryableTransient(t *testing.T) {
	base := errors.New("stream ended before a terminal event")
	tagged := transientStream(base)
	if tagged.Error() != base.Error() {
		t.Fatalf("message changed: %q", tagged.Error())
	}
	if !errors.Is(tagged, base) || !errors.Is(tagged, ErrStreamTransient) {
		t.Fatalf("tagged error lost an identity: %v", tagged)
	}
	if transientStream(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	if !StreamResendRetryable(context.Background(), fmt.Errorf("turn failed: %w", tagged)) {
		t.Fatal("a wrapped transient failure must be resendable")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if StreamResendRetryable(cancelled, tagged) {
		t.Fatal("a cancelled turn must never be resent")
	}
}

func TestIsTransientAnthropicErrorType(t *testing.T) {
	for _, typ := range []string{"overloaded_error", "api_error", "rate_limit_error", "timeout_error"} {
		if !isTransientAnthropicErrorType(typ) {
			t.Errorf("%s should be transient", typ)
		}
	}
	for _, typ := range []string{"invalid_request_error", "authentication_error", "permission_error", "not_found_error", ""} {
		if isTransientAnthropicErrorType(typ) {
			t.Errorf("%s must not be resent", typ)
		}
	}
}

func TestParseAnthropicStreamMarksTransientFailures(t *testing.T) {
	parse := func(stream string) error {
		_, err := parseAnthropicStreamResponse(context.Background(), nopCloser{strings.NewReader(stream)}, NewSilentOutputSink())
		return err
	}
	overloaded := anthropicSSE("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	if err := parse(overloaded); !errors.Is(err, ErrStreamTransient) || !strings.Contains(err.Error(), "Overloaded") {
		t.Fatalf("overloaded_error must be resendable with its message kept: %v", err)
	}
	invalid := anthropicSSE("error", `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`)
	if err := parse(invalid); err == nil || errors.Is(err, ErrStreamTransient) {
		t.Fatalf("invalid_request_error must not be resent: %v", err)
	}
	if err := parse(anthropicStreamPrefix()); !errors.Is(err, ErrStreamTransient) {
		t.Fatalf("a stream cut before message_stop must be resendable: %v", err)
	}
}

func TestWithStreamResendResendsTransientFailures(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		withRand(t, 0.5)
		calls := 0
		p := WithStreamResend(streamFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			calls++
			if calls <= 2 {
				return nil, transientStream(errors.New("overloaded"))
			}
			return &types.StreamResult{Content: "ok"}, nil
		}))
		sink := &countdownSink{}
		res, err := p.StreamChat(context.Background(), nil, nil, sink)
		if err != nil || res == nil || res.Content != "ok" || calls != 3 {
			t.Fatalf("res=%+v err=%v calls=%d, want success on the 3rd request", res, err, calls)
		}
		var shown bool
		for _, l := range sink.labels {
			shown = shown || strings.Contains(l, "provider error")
		}
		if !shown {
			t.Fatalf("resend reason not shown: %q", sink.labels)
		}
	})

	synctest.Test(t, func(t *testing.T) {
		withRand(t, 0.5)
		calls := 0
		p := WithStreamResend(streamFunc(func(context.Context, []types.Message, []types.Tool, types.OutputSink) (*types.StreamResult, error) {
			calls++
			return nil, transientStream(errors.New("overloaded"))
		}))
		start := time.Now()
		_, err := p.StreamChat(context.Background(), nil, nil, &SilentOutputSink{})
		if !errors.Is(err, ErrStreamTransient) {
			t.Fatalf("exhausted resends must surface the failure: %v", err)
		}
		if calls != MaxStreamResends+1 {
			t.Fatalf("calls=%d, want %d", calls, MaxStreamResends+1)
		}
		if got := time.Since(start); got != 7*time.Second { // 1s + 2s + 4s
			t.Fatalf("waited %s, want 7s", got)
		}
	})
}
