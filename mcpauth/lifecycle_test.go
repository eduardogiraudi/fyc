package mcpauth

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRefreshLockWaitCanBeCancelled(t *testing.T) {
	f := newFixture(t)
	f.login(t)
	creds, err := load(f.cfg, f.store)
	if err != nil {
		t.Fatal(err)
	}
	creds.Token.Expiry = time.Now().Add(-time.Hour)
	if err := save(f.cfg, f.store, creds); err != nil {
		t.Fatal(err)
	}
	lock, err := credentialLock(context.Background(), f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err = tokenForRequest(ctx, f.cfg, f.store, "")
	if !errors.Is(err, context.DeadlineExceeded) || f.refreshes.Load() != 0 {
		t.Fatalf("lock wait ignored cancellation: %v", err)
	}
}

func TestCallbackCompletionReachesBrowser(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	callback, _ := url.Parse("http://" + listener.Addr().String() + "/callback")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	browser := make(chan error, 1)
	code, err := waitForCode(ctx, listener, callback, "state", "https://issuer.example", false, "https://issuer.example/authorize", func(string) error {
		go func() {
			u := *callback
			u.RawQuery = url.Values{"state": {"state"}, "code": {"code"}}.Encode()
			resp, err := http.Get(u.String())
			if err != nil {
				browser <- err
				return
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil && !strings.Contains(string(body), "Authentication complete") {
				err = errors.New("completion page was not delivered")
			}
			browser <- err
		}()
		return nil
	})
	if err != nil || code != "code" {
		t.Fatalf("callback: %s, %v", code, err)
	}
	select {
	case err := <-browser:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("browser callback did not finish")
	}
}
