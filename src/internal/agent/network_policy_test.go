package agent

import (
	"net/http"
	"testing"

	"novera/internal/netsafe"
)

func assertPolicyTransport(t *testing.T, client *http.Client) {
	t.Helper()
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("HTTP client transport = %T, want policy *http.Transport", client.Transport)
	}
	if transport.Proxy != nil || transport.DialContext == nil {
		t.Fatal("HTTP client can bypass direct dial-time destination policy")
	}
}

func TestAgentHTTPClientsUseDialTimeDestinationPolicy(t *testing.T) {
	s := New(nil, nil, nil, nil, nil, nil, nil)
	assertPolicyTransport(t, s.http)
	assertPolicyTransport(t, agentHTTPClient())

	// Keep the dependency visible to this contract test so replacing the
	// transport with http.DefaultTransport cannot appear equivalent.
	if netsafe.NewTransport().DialContext == nil {
		t.Fatal("netsafe transport has no policy dialer")
	}
}
