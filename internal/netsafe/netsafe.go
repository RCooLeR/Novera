// Package netsafe holds shared guards for outbound HTTP to user-configured LLM
// endpoints: base-URL validation (scheme / host / SSRF) and a redirect policy
// that refuses cross-host hops so a request body can't follow a redirect to a
// different host. Both the llm and agent services dial endpoints whose base URL
// is free-form, editable from the UI, and persisted in settings.json, so they
// share one validator rather than each rolling their own.
package netsafe

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ValidateEndpoint rejects base URLs that aren't safe to dial regardless of
// whether a key is attached: non-http(s) schemes (file://, gopher://, …), a
// missing host, and link-local / cloud-metadata addresses (SSRF targets such as
// 169.254.169.254). This bounds the exfiltration/SSRF surface from a base URL
// that another local process or a synced settings file could have rewritten.
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
	if isLinkLocalOrMetadata(host) {
		return errors.New("Refusing to connect to a link-local / cloud-metadata address.")
	}
	return nil
}

func isLinkLocalOrMetadata(host string) bool {
	if strings.EqualFold(host, "metadata.google.internal") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
	}
	return false
}

// RedirectPolicy returns a CheckRedirect that caps the redirect count and
// refuses any cross-host hop (so the request body can't follow to another host).
func RedirectPolicy(max int) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return errors.New("too many redirects")
		}
		if len(via) > 0 && req.URL.Host != via[0].URL.Host {
			return errors.New("refusing to follow a cross-host redirect")
		}
		return nil
	}
}
