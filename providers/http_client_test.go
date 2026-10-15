package providers

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newRequest builds a POST request with a replayable body, mirroring how the
// providers create their requests (bytes.NewBuffer sets GetBody).
func newRequest(t *testing.T, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func TestDoWithRetryRetriesOn429ThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, `{"a":1}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want 2 attempts, got %d", got)
	}
}

func TestDoWithRetryExhaustsAndReturnsLast429(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, "{}"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", resp.StatusCode)
	}
	// The caller must still be able to read the error body.
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "slow down" {
		t.Fatalf("want body preserved, got %q", string(body))
	}
	if got := atomic.LoadInt32(&calls); got != maxRetryAttempts {
		t.Fatalf("want %d attempts, got %d", maxRetryAttempts, got)
	}
}

func TestDoWithRetryNoRetryOnNonRetryableStatus(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, "{}"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("want 1 attempt, got %d", got)
	}
}

// TestDoWithRetryReplaysBody ensures the POST payload survives across attempts.
func TestDoWithRetryReplaysBody(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"payload":42}` {
			t.Errorf("attempt %d: unexpected body %q", atomic.LoadInt32(&calls)+1, string(body))
		}
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, `{"payload":42}`), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
}

func TestDoWithRetryHonorsRetryAfterMs(t *testing.T) {
	// Answer 429 only once so the retry-after-ms delay is exercised.
	var calls int32
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("retry-after-ms", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv2.Close()

	start := time.Now()
	resp, err := doWithRetry(context.Background(), srv2.Client(), newRequest(t, srv2.URL, "{}"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if elapsed := time.Since(start); elapsed < 50*time.Millisecond {
		t.Fatalf("retry-after-ms not honored, elapsed=%v", elapsed)
	}
}

func TestDoWithRetryAbortsOnContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := doWithRetry(ctx, srv.Client(), newRequest(t, srv.URL, "{}"), nil)
	if err == nil {
		t.Fatal("expected context error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("waited past context deadline, elapsed=%v", elapsed)
	}
}

func TestRetryAfterDelayParsing(t *testing.T) {
	tests := []struct {
		name  string
		set   func(http.Header)
		want  time.Duration
		found bool
	}{
		{"seconds", func(h http.Header) { h.Set("Retry-After", "5") }, 5 * time.Second, true},
		{"millis", func(h http.Header) { h.Set("retry-after-ms", "250") }, 250 * time.Millisecond, true},
		{"ratelimit-reset", func(h http.Header) { h.Set("x-ratelimit-reset-requests", "1m30s") }, 90 * time.Second, true},
		{"none", func(h http.Header) {}, 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			tc.set(resp.Header)
			got, ok := retryAfterDelay(resp)
			if ok != tc.found {
				t.Fatalf("found=%v want %v", ok, tc.found)
			}
			if tc.found && got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestIsRetryableStatus(t *testing.T) {
	for _, code := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, 529} {
		if !isRetryableStatus(code) {
			t.Errorf("status %d should be retryable", code)
		}
	}
	for _, code := range []int{http.StatusOK, http.StatusBadRequest, http.StatusUnauthorized, http.StatusInternalServerError} {
		if isRetryableStatus(code) {
			t.Errorf("status %d should not be retryable", code)
		}
	}
}

// spinnerRecorder counts spinner start/stop calls to verify the retry notice is
// cleaned up from the UI. It reuses SilentOutputSink for the rest of the
// OutputSink contract.
type spinnerRecorder struct {
	SilentOutputSink
	starts int
	stops  int
}

func (s *spinnerRecorder) StartSpinner(string) { s.starts++ }
func (s *spinnerRecorder) StopSpinner()        { s.stops++ }

func TestDoWithRetryStopsSpinnerOnSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &spinnerRecorder{}
	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, "{}"), rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if rec.starts != 1 {
		t.Fatalf("want 1 spinner start, got %d", rec.starts)
	}
	if rec.stops != 1 {
		t.Fatalf("want 1 spinner stop after success, got %d", rec.stops)
	}
}

func TestDoWithRetryStopsSpinnerOnExhaustedRetries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	rec := &spinnerRecorder{}
	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, "{}"), rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if rec.starts == 0 {
		t.Fatal("want at least one spinner start")
	}
	if rec.stops != 1 {
		t.Fatalf("want 1 spinner stop, got %d", rec.stops)
	}
}

func TestDoWithRetryNoSpinnerWithoutRetry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := &spinnerRecorder{}
	resp, err := doWithRetry(context.Background(), srv.Client(), newRequest(t, srv.URL, "{}"), rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if rec.starts != 0 || rec.stops != 0 {
		t.Fatalf("want no spinner activity, got starts=%d stops=%d", rec.starts, rec.stops)
	}
}
