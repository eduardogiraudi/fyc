package crawl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNetworkPolicyAddresses(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.1.1", "169.254.169.254", "100.100.100.200", "0.0.0.0", "224.0.0.1", "fd00::1", "fe80::1", "2001:db8::1", "64:ff9b::a00:1"} {
		if publicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("private/special address accepted: %s", raw)
		}
	}
	for _, raw := range []string{"93.184.216.34", "1.1.1.1", "2606:4700:4700::1111", "::ffff:1.1.1.1"} {
		if !publicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("public address rejected: %s", raw)
		}
	}
	policy := networkPolicy{}
	for _, raw := range []string{"http://localhost", "http://foo.localhost", "http://metadata.google.internal", "http://169.254.169.254", "http://[::1]/", "http://user:secret@example.com", "file:///etc/passwd", "http://example.com:0"} {
		if !errors.Is(policy.checkURL(context.Background(), raw), errUnsafeURL) {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	if err := (networkPolicy{allowPrivate: true}).checkURL(context.Background(), "http://127.0.0.1:8080"); err != nil {
		t.Fatal("local-development opt-in failed:", err)
	}
}

func TestDNSPinningAndMixedAnswers(t *testing.T) {
	var lookups atomic.Int32
	policy := networkPolicy{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			lookups.Add(1)
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			if address != "93.184.216.34:443" {
				t.Errorf("dial re-resolved DNS: %s", address)
			}
			a, b := net.Pipe()
			_ = b.Close()
			return a, nil
		},
	}
	conn, err := policy.dialContext(context.Background(), "tcp", "public.test:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if lookups.Load() != 1 {
		t.Fatal("unexpected lookup count", lookups.Load())
	}
	policy.lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
	}
	if _, err := policy.dialContext(context.Background(), "tcp", "mixed.test:443"); !errors.Is(err, errUnsafeURL) {
		t.Fatalf("mixed DNS answer accepted: %v", err)
	}
}

func mappedPublicPolicy(t *testing.T, address string) networkPolicy {
	t.Helper()
	return networkPolicy{
		lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		},
		dial: func(ctx context.Context, network, pinned string) (net.Conn, error) {
			if !strings.HasPrefix(pinned, "93.184.216.34:") {
				return nil, fmt.Errorf("unexpected unpinned address %s", pinned)
			}
			return (&net.Dialer{}).DialContext(ctx, network, address)
		},
	}
}

func TestHTTPRedirectToPrivateDestinationIsBlocked(t *testing.T) {
	var privateHits atomic.Int32
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { privateHits.Add(1) }))
	defer private.Close()
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, private.URL, http.StatusFound)
	}))
	defer public.Close()
	policy := mappedPublicPolicy(t, public.Listener.Addr().String())
	_, _, err := fetchHTTP(context.Background(), "http://public.test/redirect", PagedOptions{}, policy)
	if !errors.Is(err, errUnsafeURL) || privateHits.Load() != 0 {
		t.Fatalf("redirect reached private destination: hits=%d error=%v", privateHits.Load(), err)
	}
}

func TestBrowserProxyBlocksHTTPAndCONNECTToPrivateHosts(t *testing.T) {
	proxy, err := newBrowserProxy(context.Background(), networkPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	for _, request := range []string{
		"GET http://127.0.0.1:12345/ HTTP/1.1\r\nHost: 127.0.0.1:12345\r\n\r\n",
		"CONNECT 169.254.169.254:80 HTTP/1.1\r\nHost: 169.254.169.254:80\r\n\r\n",
		"CONNECT [::1]:443 HTTP/1.1\r\nHost: [::1]:443\r\n\r\n",
	} {
		conn, err := net.Dial("tcp", proxy.listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, request)
		response, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusForbidden {
			t.Errorf("unsafe proxy request accepted: %s", response.Status)
		}
		_ = response.Body.Close()
		_ = conn.Close()
	}
}

func TestBrowserProxyPinsCONNECTAndClosesTunnels(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	proxy, err := newBrowserProxy(context.Background(), mappedPublicPolicy(t, listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	conn, err := net.Dial("tcp", proxy.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "CONNECT public.test:443 HTTP/1.1\r\nHost: public.test:443\r\n\r\n")
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, nil)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT failed: %v %v", response, err)
	}
	_, _ = io.WriteString(conn, "hello")
	buffer := make([]byte, 5)
	if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "hello" {
		t.Fatalf("tunnel corrupted: %q %v", buffer, err)
	}
	proxy.close()
	<-finished
}
