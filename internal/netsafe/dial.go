package netsafe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// IPResolver is the narrow resolver contract used by the dial policy and its
// deterministic rebinding tests.
type IPResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialContextFunc matches net.Dialer's context-aware dial method.
type DialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

var blockedAddressPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:2::/48"),
	netip.MustParsePrefix("2001:10::/28"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

// NewTransport creates a direct-only HTTP transport whose resolver answer is
// filtered and whose TCP connection is made to the vetted numeric address.
// Environment proxies are deliberately disabled: a proxy would resolve the
// target outside this process and bypass the destination policy. A future
// explicit proxy feature must provide equivalent target enforcement.
func NewTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           PolicyDialContext(net.DefaultResolver, dialer.DialContext),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

// PolicyDialContext resolves once per new connection, rejects the entire
// answer set if any address is non-public, and dials only numeric addresses
// from that vetted set. Existing pooled connections remain pinned to the
// address that was checked when they were created.
func PolicyDialContext(resolver IPResolver, dial DialContextFunc) DialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if resolver == nil || dial == nil {
			return nil, errors.New("network destination policy is unavailable")
		}
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("invalid dial address: %w", err)
		}
		host = strings.TrimSuffix(strings.TrimSpace(host), ".")
		if host == "" || port == "" {
			return nil, errors.New("dial address must include a host and port")
		}
		if isMetadataHostname(host) {
			return nil, errors.New("refusing to resolve a cloud-metadata hostname")
		}

		var ips []netip.Addr
		if literal, parseErr := netip.ParseAddr(host); parseErr == nil {
			ips = []netip.Addr{literal}
		} else {
			ips, err = resolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, fmt.Errorf("resolve %q: %w", host, err)
			}
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve %q: no addresses", host)
		}

		loopbackOnly := isExplicitLoopbackHost(host)
		vetted := make([]netip.Addr, 0, len(ips))
		for _, ip := range ips {
			addr := ip.Unmap()
			if !addr.IsValid() {
				return nil, fmt.Errorf("resolve %q: invalid address", host)
			}
			if err := validateDialAddress(host, addr, loopbackOnly); err != nil {
				return nil, err
			}
			if (network == "tcp4" && !addr.Is4()) || (network == "tcp6" && !addr.Is6()) {
				continue
			}
			vetted = append(vetted, addr)
		}
		if len(vetted) == 0 {
			return nil, fmt.Errorf("resolve %q: no addresses compatible with %s", host, network)
		}

		var dialErrors []error
		for _, addr := range vetted {
			conn, err := dial(ctx, network, net.JoinHostPort(addr.String(), port))
			if err == nil {
				return conn, nil
			}
			dialErrors = append(dialErrors, err)
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
		}
		return nil, fmt.Errorf("dial vetted addresses for %q: %w", host, errors.Join(dialErrors...))
	}
}

func validateDialAddress(host string, addr netip.Addr, loopbackOnly bool) error {
	if loopbackOnly {
		if addr.IsLoopback() {
			return nil
		}
		return fmt.Errorf("loopback host %q resolved to non-loopback address %s", host, addr)
	}
	if isBlockedAddress(addr) {
		return fmt.Errorf("refusing to connect to non-public address %s resolved for %q", addr, host)
	}
	return nil
}

func isBlockedAddress(addr netip.Addr) bool {
	if !addr.IsValid() || !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return true
	}
	for _, prefix := range blockedAddressPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func isExplicitLoopbackHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}

func isMetadataHostname(host string) bool {
	return strings.EqualFold(strings.TrimSuffix(host, "."), "metadata.google.internal")
}
