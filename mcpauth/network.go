package mcpauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.Unmap().IsLoopback()
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("64:ff9b::/96"), netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("2001::/32"),
}

func publicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPrefixes {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}
func allowedURL(raw, resource string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid OAuth endpoint URL")
	}
	base, err := url.Parse(resource)
	if err != nil {
		return fmt.Errorf("invalid MCP resource URL")
	}
	if (u.Scheme == "http" || u.Scheme == "https") && isLoopbackHost(u.Hostname()) && sameOrigin(u, base) {
		return nil
	}
	if u.Scheme != "https" || isLoopbackHost(u.Hostname()) {
		return fmt.Errorf("OAuth requires HTTPS (HTTP is allowed only on the configured loopback origin)")
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !publicIP(ip) {
		return fmt.Errorf("OAuth endpoint has a non-public address")
	}
	return nil
}

// Metadata is untrusted. Validate and pin DNS, and never redirect token POSTs.
func discoveryClient(resource string) (*http.Client, error) {
	if err := allowedURL(resource, resource); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := endpointIPs(ctx, host, resource)
		if err != nil {
			return nil, err
		}
		var dialErr error
		for _, ip := range ips {
			conn, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErr = err
		}
		return nil, dialErr
	}
	return &http.Client{
		Transport: &checkedTransport{next: transport, resource: resource},
		Timeout:   20 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("OAuth HTTP redirects are refused")
		},
	}, nil
}

func endpointIPs(ctx context.Context, host, resource string) ([]netip.Addr, error) {
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf("OAuth endpoint DNS resolution failed")
	}
	base, _ := url.Parse(resource)
	local := isLoopbackHost(base.Hostname()) && strings.EqualFold(host, base.Hostname())
	for _, ip := range ips {
		ip = ip.Unmap()
		if local && ip.IsLoopback() {
			continue
		}
		if !publicIP(ip) {
			return nil, fmt.Errorf("OAuth endpoint resolves to a non-public address")
		}
	}
	return ips, nil
}

type checkedTransport struct {
	next     http.RoundTripper
	resource string
}

func (t *checkedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := allowedURL(req.URL.String(), t.resource); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}
