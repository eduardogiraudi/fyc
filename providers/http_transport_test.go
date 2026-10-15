package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fuckyourcli/types"
)

type blackholeRelay struct {
	dst  net.Conn
	drop *atomic.Bool
}

func (r blackholeRelay) Write(p []byte) (int, error) {
	if r.drop.Load() {
		return len(p), nil
	}
	return r.dst.Write(p)
}

// The first tunnel silently stops forwarding; new connections remain healthy.
func blackholeProxy(t *testing.T, target string) (string, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var drop atomic.Bool
	var accepts atomic.Int32
	var mu sync.Mutex
	var connections []net.Conn
	var wg sync.WaitGroup
	acceptDone := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(acceptDone)
		for {
			downstream, err := listener.Accept()
			if err != nil {
				return
			}
			upstream, err := net.DialTimeout("tcp", target, time.Second)
			if err != nil {
				downstream.Close()
				continue
			}
			mu.Lock()
			connections = append(connections, downstream, upstream)
			mu.Unlock()
			blocked := new(atomic.Bool)
			if accepts.Add(1) == 1 {
				blocked = &drop
			}
			wg.Add(2)
			go func() {
				defer wg.Done()
				defer downstream.Close()
				defer upstream.Close()
				_, _ = io.Copy(blackholeRelay{upstream, blocked}, downstream)
			}()
			go func() {
				defer wg.Done()
				defer downstream.Close()
				defer upstream.Close()
				_, _ = io.Copy(blackholeRelay{downstream, blocked}, upstream)
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-acceptDone
		mu.Lock()
		for _, conn := range connections {
			conn.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return listener.Addr().String(), &drop, &accepts
}

func blackholeProviderClient(t *testing.T, handler http.Handler, readIdle, pingTimeout time.Duration) (*http.Client, string, *atomic.Bool, *atomic.Int32) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	addr, drop, accepts := blackholeProxy(t, srv.Listener.Addr().String())
	base := srv.Client().Transport.(*http.Transport).Clone()
	base.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	transport := &recoveringTransport{newTransport: func() (http.RoundTripper, error) {
		return configureProviderTransport(base, readIdle, pingTimeout)
	}}
	client := &http.Client{Transport: transport}
	t.Cleanup(client.CloseIdleConnections)
	return client, srv.URL, drop, accepts
}

func TestProviderHTTP2RecoversAfterCanceledTurn(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("streaming=%v", streaming), func(t *testing.T) {
			var calls atomic.Int32
			stalled := make(chan struct{})
			client, url, drop, accepts := blackholeProviderClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.ProtoMajor != 2 {
					t.Errorf("protocol = %s, want HTTP/2", r.Proto)
				}
				call := calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, anthropicSSE("content_block_delta", `{"type":"content_block_delta","delta":{"text":"ok"}}`))
				if streaming && call == 2 {
					w.(http.Flusher).Flush()
					close(stalled)
					<-r.Context().Done()
					return
				}
				fmt.Fprint(w, anthropicSSE("message_stop", `{"type":"message_stop"}`))
			}), time.Hour, time.Second)
			previous := providerHTTPClient
			providerHTTPClient = client
			t.Cleanup(func() { providerHTTPClient = previous })

			turn := func(ctx context.Context) (*types.StreamResult, error) {
				// Like streamProviderChat, construct a new provider on each turn.
				p := NewAnthropicProvider(url, "", "model", "test", 0.1)
				return p.StreamChat(ctx, []types.Message{{Role: "user", Content: "hi"}}, nil, NewSilentOutputSink())
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			for i := 0; i < 2; i++ {
				if result, err := turn(ctx); err != nil || result.Content != "ok" {
					t.Fatalf("healthy turn: result=%+v err=%v", result, err)
				}
				if streaming {
					break
				}
			}
			if accepts.Load() != 1 {
				t.Fatal("healthy turns must reuse the connection")
			}

			failedCtx, cancelTurn := context.WithTimeout(context.Background(), time.Second)
			defer cancelTurn()
			if !streaming {
				drop.Store(true)
				result, err := turn(failedCtx)
				if err != nil || !result.Interrupted {
					t.Fatalf("deadline must interrupt: result=%+v err=%v", result, err)
				}
			} else {
				done := make(chan struct{})
				go func() {
					select {
					case <-stalled:
						drop.Store(true)
						cancelTurn()
					case <-done:
					}
				}()
				result, err := turn(failedCtx)
				close(done)
				if err != nil || !result.Interrupted {
					t.Fatalf("Ctrl+C must interrupt streaming: result=%+v err=%v", result, err)
				}
			}
			if result, err := turn(ctx); err != nil || result.Interrupted || result.Content != "ok" {
				t.Fatalf("next turn must recover without restart: result=%+v err=%v", result, err)
			}
			if accepts.Load() != 2 {
				t.Fatalf("connections = %d, want a fresh connection", accepts.Load())
			}
		})
	}
}

func TestProviderHTTP2HealthCheckRemovesDeadConnection(t *testing.T) {
	client, url, drop, accepts := blackholeProviderClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	}), 20*time.Millisecond, 20*time.Millisecond)
	// No rotation here: prove that the HTTP/2 ping itself removes the tunnel.
	transport, err := client.Transport.(*recoveringTransport).generation()
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = transport.transport
	request := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			t.Fatal(err)
		}
	}
	request()
	drop.Store(true)
	// A request already sent through the dead tunnel fails; never replay it here.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
		// Under a slow scheduler the ping may evict the tunnel before this call.
		if accepts.Load() != 2 {
			t.Fatal("request unexpectedly succeeded on the dead tunnel")
		}
	} else if ctx.Err() != nil {
		t.Fatalf("health check failed to detect the dead tunnel before the deadline: %v", err)
	}
	request()
	if accepts.Load() != 2 {
		t.Fatalf("connections = %d, health check did not replace the dead tunnel", accepts.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRecoveringTransportBodyFailuresAndStaleGeneration(t *testing.T) {
	var created int
	transport := &recoveringTransport{newTransport: func() (http.RoundTripper, error) {
		created++
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: nopCloser{&errReader{err: io.ErrUnexpectedEOF}}}, nil
		}), nil
	}}
	req := newRequest(t, "https://example.test", "{}")
	first, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	stale, _ := transport.RoundTrip(req)
	if created != 1 {
		t.Fatal("healthy pool was not reused")
	}
	if _, err := first.Body.Read(make([]byte, 1)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v", err)
	}
	current, _ := transport.RoundTrip(req)
	if created != 2 {
		t.Fatal("read error did not retire the failed pool")
	}
	_, _ = stale.Body.Read(make([]byte, 1))
	_, _ = transport.RoundTrip(req)
	if created != 2 {
		t.Fatal("a stale response retired the healthy replacement")
	}
	first.Body.Close()
	stale.Body.Close()
	current.Body.Close()
}

func TestRecoveringTransportNormalCloseKeepsPool(t *testing.T) {
	var created int
	transport := &recoveringTransport{newTransport: func() (http.RoundTripper, error) {
		created++
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}), nil
	}}
	for i := 0; i < 2; i++ {
		resp, err := transport.RoundTrip(newRequest(t, "https://example.test", "{}"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			_, _ = io.Copy(io.Discard, resp.Body)
		}
		resp.Body.Close()
	}
	if created != 1 {
		t.Fatalf("normal EOF or early close retired a healthy pool: created=%d", created)
	}
}

func TestRecoveringTransportConcurrentRunsSurviveRotation(t *testing.T) {
	var created atomic.Int32
	transport := &recoveringTransport{newTransport: func() (http.RoundTripper, error) {
		created.Add(1)
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	canceled, err := transport.RoundTrip(newRequest(t, "https://example.test", "{}").WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	active, err := transport.RoundTrip(newRequest(t, "https://example.test", "{}"))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	canceled.Body.Close()
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "https://example.test", nil)
			resp, err := transport.RoundTrip(req)
			if err != nil {
				t.Errorf("concurrent run failed: %v", err)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(io.Discard, resp.Body)
		}()
	}
	wg.Wait()
	if body, err := io.ReadAll(active.Body); err != nil || string(body) != "ok" {
		t.Fatalf("rotation canceled another active run: body=%q err=%v", body, err)
	}
	active.Body.Close()
	if created.Load() != 2 {
		t.Fatalf("concurrent runs created %d pools, want 2", created.Load())
	}
}

func TestRecoveringTransportRequestErrorReplacesPool(t *testing.T) {
	var created int
	transport := &recoveringTransport{newTransport: func() (http.RoundTripper, error) {
		created++
		fail := created == 1
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if fail {
				return nil, io.ErrUnexpectedEOF
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok"))}, nil
		}), nil
	}}
	req := newRequest(t, "https://example.test", "{}")
	if _, err := transport.RoundTrip(req); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("request error = %v", err)
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if created != 2 {
		t.Fatalf("request failure did not replace the pool: created=%d", created)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := transport.RoundTrip(req.WithContext(ctx)); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-canceled request error = %v", err)
	}
	if created != 2 {
		t.Fatal("already-canceled request discarded the healthy replacement")
	}
}

func TestProviderTransportSupportsHTTP1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 1 {
			t.Errorf("protocol = %s, want HTTP/1.1", r.Proto)
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	transport, err := configureProviderTransport(http.DefaultTransport.(*http.Transport), time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if body, err := io.ReadAll(resp.Body); err != nil || string(body) != "ok" {
		t.Fatalf("HTTP/1.1 fallback: body=%q err=%v", body, err)
	}
}
