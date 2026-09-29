package netsafe

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestValidateCredentialTransport(t *testing.T) {
	tests := []struct {
		name       string
		base       string
		credential string
		wantErr    bool
	}{
		{name: "remote plaintext with credential", base: "http://provider.example/v1", credential: "secret", wantErr: true},
		{name: "misleading localhost suffix", base: "http://localhost.attacker.example/v1", credential: "secret", wantErr: true},
		{name: "https with credential", base: "https://provider.example/v1", credential: "secret"},
		{name: "localhost plaintext", base: "http://localhost:11434/v1", credential: "secret"},
		{name: "ipv4 loopback plaintext", base: "http://127.0.0.1:11434/v1", credential: "secret"},
		{name: "ipv6 loopback plaintext", base: "http://[::1]:11434/v1", credential: "secret"},
		{name: "remote plaintext without credential", base: "http://provider.example/v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateCredentialTransport(tt.base, tt.credential)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateCredentialTransport(%q) error = %v, wantErr %v", tt.base, err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(strings.ToLower(err.Error()), "plaintext") {
				t.Fatalf("error should explain the plaintext refusal, got %q", err)
			}
		})
	}
}

func TestRedirectPolicyRejectsSameHostSchemeDowngrade(t *testing.T) {
	httpsURL, err := url.Parse("https://provider.example/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	httpURL, err := url.Parse("http://provider.example/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	original := &http.Request{URL: httpsURL}
	redirected := &http.Request{URL: httpURL}

	if err := RedirectPolicy(5)(redirected, []*http.Request{original}); err == nil {
		t.Fatal("same-host HTTPS-to-HTTP redirect was accepted")
	}
}

func TestRedirectPolicyAllowsSameOriginRedirect(t *testing.T) {
	from, err := url.Parse("https://provider.example/v1")
	if err != nil {
		t.Fatal(err)
	}
	to, err := url.Parse("https://PROVIDER.example/v2")
	if err != nil {
		t.Fatal(err)
	}
	if err := RedirectPolicy(5)(&http.Request{URL: to}, []*http.Request{{URL: from}}); err != nil {
		t.Fatalf("same-origin redirect rejected: %v", err)
	}
}
