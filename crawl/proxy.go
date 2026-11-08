package crawl

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Playwright routing only sees the first hop of a redirect. A validating proxy
// enforces policy at every actual connection, including HTTPS and subresources.
type browserProxy struct {
	server    *http.Server
	listener  net.Listener
	transport *http.Transport
	policy    networkPolicy
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	tunnels   map[net.Conn]struct{}
}

func newBrowserProxy(parent context.Context, policy networkPolicy) (*browserProxy, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	p := &browserProxy{listener: listener, transport: policy.transport(), policy: policy,
		ctx: ctx, cancel: cancel, tunnels: map[net.Conn]struct{}{}}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *browserProxy) address() string { return "http://" + p.listener.Addr().String() }

func (p *browserProxy) close() {
	p.cancel()
	_ = p.server.Close()
	p.transport.CloseIdleConnections()
	p.mu.Lock()
	defer p.mu.Unlock()
	for conn := range p.tunnels {
		_ = conn.Close()
	}
}

func (p *browserProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		p.connect(w, req)
		return
	}
	if err := p.policy.checkURL(req.Context(), req.URL.String()); err != nil {
		http.Error(w, "crawler network policy blocked destination", http.StatusForbidden)
		return
	}
	forward := req.Clone(req.Context())
	forward.RequestURI = ""
	stripHopHeaders(forward.Header)
	resp, err := p.transport.RoundTrip(forward)
	if err != nil {
		http.Error(w, "crawler proxy could not fetch destination", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func stripHopHeaders(headers http.Header) {
	for _, name := range strings.Split(headers.Get("Connection"), ",") {
		headers.Del(strings.TrimSpace(name))
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		headers.Del(name)
	}
}

func (p *browserProxy) connect(w http.ResponseWriter, req *http.Request) {
	// CONNECT accepts authorities, never paths or arbitrary schemes.
	authority, err := validatedURL("https://" + req.Host)
	if err != nil || authority.Path != "" || authority.RawQuery != "" || authority.Fragment != "" || authority.Port() == "" {
		http.Error(w, "invalid proxy authority", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Second)
	upstream, err := p.policy.dialContext(ctx, "tcp", req.Host)
	cancel()
	if err != nil {
		http.Error(w, "crawler network policy blocked destination", http.StatusForbidden)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "proxy tunnel unavailable", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	p.mu.Lock()
	if p.ctx.Err() != nil {
		p.mu.Unlock()
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	p.tunnels[client], p.tunnels[upstream] = struct{}{}, struct{}{}
	p.mu.Unlock()
	defer func() {
		_ = client.Close()
		_ = upstream.Close()
		p.mu.Lock()
		delete(p.tunnels, client)
		delete(p.tunnels, upstream)
		p.mu.Unlock()
	}()
	deadline := time.Now().Add(crawlTimeout + 10*time.Second)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if buffered.Flush() != nil {
		return
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, buffered)
		_ = upstream.Close()
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	<-done
}
