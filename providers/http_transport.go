package providers

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/net/http2"
)

var providerHTTPClient = &http.Client{Transport: &recoveringTransport{newTransport: newProviderTransport}}

func newProviderTransport() (http.RoundTripper, error) {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Preserve custom round trippers used for tracing or wire capture.
		return http.DefaultTransport, nil
	}
	return configureProviderTransport(base, 30*time.Second, 10*time.Second)
}

func configureProviderTransport(base *http.Transport, readIdle, pingTimeout time.Duration) (*http.Transport, error) {
	transport := base.Clone()
	// Clone must not retain a negotiated protocol handler's old connection pool.
	transport.TLSNextProto = nil
	h2, err := http2.ConfigureTransports(transport)
	if err != nil {
		return nil, err
	}
	// A dead TCP tunnel can otherwise remain usable in the HTTP/2 pool forever,
	// even after a request deadline or CloseIdleConnections.
	h2.ReadIdleTimeout = readIdle
	h2.PingTimeout = pingTimeout
	h2.WriteByteTimeout = 15 * time.Second
	return transport, nil
}

type transportGeneration struct {
	transport http.RoundTripper
}

// recoveringTransport retires failed pools without canceling other active runs.
// A late failure from a retired generation must not discard the replacement.
type recoveringTransport struct {
	mu           sync.Mutex
	current      *transportGeneration
	newTransport func() (http.RoundTripper, error)
}

func (t *recoveringTransport) generation() (*transportGeneration, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		transport, err := t.newTransport()
		if err != nil {
			return nil, err
		}
		t.current = &transportGeneration{transport: transport}
	}
	return t.current, nil
}

func (t *recoveringTransport) retire(g *transportGeneration) {
	t.mu.Lock()
	if t.current != g {
		t.mu.Unlock()
		return
	}
	t.current = nil
	t.mu.Unlock()
	closeIdleTransport(g.transport)
}

func closeIdleTransport(transport http.RoundTripper) {
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *recoveringTransport) CloseIdleConnections() {
	t.mu.Lock()
	g := t.current
	t.mu.Unlock()
	if g != nil {
		closeIdleTransport(g.transport)
	}
}

func (t *recoveringTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	g, err := t.generation()
	if err != nil {
		return nil, err
	}
	resp, err := g.transport.RoundTrip(req)
	if err != nil {
		t.retire(g)
		return resp, err
	}
	resp.Body = &recoveringBody{ReadCloser: resp.Body, ctx: req.Context(), retire: func() { t.retire(g) }}
	return resp, nil
}

type recoveringBody struct {
	io.ReadCloser
	ctx    context.Context
	retire func()
}

func (b *recoveringBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && (err != io.EOF || b.ctx.Err() != nil) {
		b.retire()
	}
	return n, err
}

func (b *recoveringBody) Close() error {
	if b.ctx.Err() != nil {
		b.retire()
	}
	err := b.ReadCloser.Close()
	if err != nil {
		b.retire()
	}
	return err
}
