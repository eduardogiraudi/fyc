package crawl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

var errUnsafeURL = errors.New("crawler network policy rejected URL")

var specialNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"), netip.MustParsePrefix("2001:20::/28"),
}

type networkPolicy struct {
	allowPrivate bool
	lookup       func(context.Context, string) ([]netip.Addr, error)
	dial         func(context.Context, string, string) (net.Conn, error)
}

func publicAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range specialNetworks {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https"))
}

func validatedURL(raw string) (*url.URL, error) {
	if len(raw) > 4096 {
		return nil, fmt.Errorf("%w: URL exceeds 4096-byte limit", errUnsafeURL)
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, fmt.Errorf("%w: only HTTP(S) URLs without credentials are allowed", errUnsafeURL)
	}
	if u.Port() != "" {
		port, err := strconv.Atoi(u.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("%w: invalid port", errUnsafeURL)
		}
	}
	return u, nil
}

func (p networkPolicy) addresses(ctx context.Context, host string) ([]netip.Addr, error) {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !p.allowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost") || host == "metadata.google.internal" || strings.HasSuffix(host, ".local")) {
		return nil, fmt.Errorf("%w: local or metadata hostname", errUnsafeURL)
	}
	var addresses []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addresses = []netip.Addr{ip}
	} else {
		lookup := p.lookup
		if lookup == nil {
			lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			}
		}
		var err error
		addresses, err = lookup(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(addresses) == 0 {
		return nil, errors.New("hostname has no addresses")
	}
	for _, ip := range addresses {
		if ip.Zone() != "" || !ip.IsValid() || (!p.allowPrivate && !publicAddress(ip)) {
			return nil, fmt.Errorf("%w: non-public destination", errUnsafeURL)
		}
	}
	return addresses, ctx.Err()
}

func (p networkPolicy) checkURL(ctx context.Context, raw string) error {
	u, err := validatedURL(raw)
	if err != nil {
		return err
	}
	_, err = p.addresses(ctx, u.Hostname())
	return err
}

// Dial only the validated numeric addresses, never resolve the hostname again:
// this closes DNS-rebinding races for HTTP and the browser's CONNECT proxy.
func (p networkPolicy) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	addresses, err := p.addresses(ctx, host)
	if err != nil {
		return nil, err
	}
	dial := p.dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	var lastErr error
	for _, ip := range addresses {
		conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (p networkPolicy) transport() *http.Transport {
	return &http.Transport{
		Proxy: nil, DialContext: p.dialContext, ForceAttemptHTTP2: true,
		MaxIdleConns: 8, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}
}

func (p networkPolicy) client(transport *http.Transport) *http.Client {
	return &http.Client{
		Transport: transport, Timeout: 25 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return p.checkURL(req.Context(), req.URL.String())
		},
	}
}
