// Package netsafe holds shared guards for outbound HTTP: base-URL validation
// (scheme / host / SSRF) and a redirect policy that refuses cross-origin hops so
// a request body or credential can't follow to a different host or scheme. The llm service,
// agent completions, and the approval-gated agent HTTP tool all dial URLs that
// can be influenced outside this package, so they share one validator rather
// than each rolling their own.
package netsafe

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
)

// ValidateEndpoint rejects base URLs that aren't safe to dial regardless of
// whether a key is attached: non-http(s) schemes (file://, gopher://, …), a
// missing host, and literal non-public / cloud-metadata destinations. Hostname
// answers are additionally filtered and pinned by NewTransport at dial time.
func ValidateEndpoint(base string) error {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return errors.New("The base URL is not a valid URL.")
	}
	if !strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https") {
		return errors.New("The base URL must use http or https.")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("The base URL must include a host.")
	}
	if isMetadataHostname(host) {
		return errors.New("Refusing to connect to a cloud-metadata address.")
	}
	if ip := net.ParseIP(host); ip != nil && !isExplicitLoopbackHost(host) {
		addr, ok := netip.AddrFromSlice(ip)
		if !ok || isBlockedAddress(addr.Unmap()) {
			return errors.New("Refusing to connect to a non-public address.")
		}
	}
	return nil
}

// ValidateCredentialTransport rejects a credential-bearing provider request
// when its destination is plaintext and remote. Plain HTTP remains supported
// for deliberate, local-only providers on the exact loopback hostnames used by
// Novera's local-model workflow. Callers must also call ValidateEndpoint: this
// guard is intentionally about credential confidentiality, not general URL
// validity or SSRF policy.
func ValidateCredentialTransport(base, credential string) error {
	if strings.TrimSpace(credential) == "" {
		return nil
	}
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return errors.New(unsafeCredentialTransportMessage)
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1":
		return nil
	default:
		return errors.New(unsafeCredentialTransportMessage)
	}
}

const unsafeCredentialTransportMessage = "Refusing to send a credential over plaintext HTTP to a remote host. Use an https:// base URL (or a localhost provider)."

// RedirectPolicy returns a CheckRedirect that caps the redirect count and
// refuses any cross-origin hop. In addition to protecting request bodies from
// a different host, comparing schemes prevents a same-host HTTPS-to-HTTP
// downgrade from carrying an Authorization header onto plaintext transport.
func RedirectPolicy(max int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return errors.New("refusing to follow a cross-host redirect")
		}
		if len(via) > 0 && !strings.EqualFold(req.URL.Scheme, via[0].URL.Scheme) {
			return errors.New("refusing to follow a scheme-changing redirect")
		}
		return nil
	}
}
