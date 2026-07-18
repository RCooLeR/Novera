package netsafe

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
)

type scriptedResolver struct {
	mu      sync.Mutex
	answers [][]netip.Addr
	calls   int
}

func (r *scriptedResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if len(r.answers) == 0 {
		return nil, errors.New("no scripted answer")
	}
	answer := r.answers[0]
	if len(r.answers) > 1 {
		r.answers = r.answers[1:]
	}
	return answer, nil
}

func successfulRecordingDial(t *testing.T, addresses *[]string) DialContextFunc {
	t.Helper()
	return func(_ context.Context, _ string, address string) (net.Conn, error) {
		*addresses = append(*addresses, address)
		client, server := net.Pipe()
		t.Cleanup(func() {
			_ = client.Close()
			_ = server.Close()
		})
		return client, nil
	}
}

func TestPolicyDialContextRejectsPrivateMetadataAndMixedAnswers(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		answer []netip.Addr
	}{
		{name: "metadata", host: "attacker.example", answer: []netip.Addr{netip.MustParseAddr("169.254.169.254")}},
		{name: "private", host: "attacker.example", answer: []netip.Addr{netip.MustParseAddr("10.1.2.3")}},
		{name: "loopback through DNS", host: "attacker.example", answer: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{name: "mixed answer fails closed", host: "attacker.example", answer: []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("192.168.1.10")}},
		{name: "localhost cannot resolve public", host: "localhost", answer: []netip.Addr{netip.MustParseAddr("93.184.216.34")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &scriptedResolver{answers: [][]netip.Addr{tt.answer}}
			var dialed []string
			dial := PolicyDialContext(resolver, successfulRecordingDial(t, &dialed))
			if _, err := dial(context.Background(), "tcp", net.JoinHostPort(tt.host, "443")); err == nil {
				t.Fatal("unsafe resolution was accepted")
			}
			if len(dialed) != 0 {
				t.Fatalf("unsafe answer reached the network dialer: %v", dialed)
			}
		})
	}
}

func TestPolicyDialContextPinsVettedNumericAddress(t *testing.T) {
	resolver := &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("93.184.216.34")}}}
	var dialed []string
	dial := PolicyDialContext(resolver, successfulRecordingDial(t, &dialed))
	conn, err := dial(context.Background(), "tcp", "provider.example:443")
	if err != nil {
		t.Fatalf("dial public answer: %v", err)
	}
	_ = conn.Close()
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("dialed %v, want the vetted numeric address", dialed)
	}
}

func TestPolicyDialContextAllowsOnlyExplicitLoopbackNames(t *testing.T) {
	resolver := &scriptedResolver{answers: [][]netip.Addr{{netip.MustParseAddr("127.0.0.1")}}}
	var dialed []string
	dial := PolicyDialContext(resolver, successfulRecordingDial(t, &dialed))
	conn, err := dial(context.Background(), "tcp", "localhost:11434")
	if err != nil {
		t.Fatalf("dial explicit local provider: %v", err)
	}
	_ = conn.Close()
	if len(dialed) != 1 || dialed[0] != "127.0.0.1:11434" {
		t.Fatalf("dialed %v", dialed)
	}
}

func TestPolicyDialContextRevalidatesRebindingOnEveryConnection(t *testing.T) {
	resolver := &scriptedResolver{answers: [][]netip.Addr{
		{netip.MustParseAddr("93.184.216.34")},
		{netip.MustParseAddr("169.254.169.254")},
	}}
	var dialed []string
	dial := PolicyDialContext(resolver, successfulRecordingDial(t, &dialed))
	first, err := dial(context.Background(), "tcp", "rebind.example:443")
	if err != nil {
		t.Fatalf("first public resolution: %v", err)
	}
	_ = first.Close()
	if _, err := dial(context.Background(), "tcp", "rebind.example:443"); err == nil {
		t.Fatal("rebound metadata address was accepted")
	}
	if len(dialed) != 1 {
		t.Fatalf("rebound address reached dialer: %v", dialed)
	}
}

func TestValidateEndpointRejectsLiteralNonPublicDestinations(t *testing.T) {
	for _, rawURL := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://10.0.0.1/api",
		"http://192.168.1.1/api",
		"http://[fe80::1]/api",
		"http://metadata.google.internal./computeMetadata/v1",
	} {
		if err := ValidateEndpoint(rawURL); err == nil {
			t.Errorf("ValidateEndpoint(%q) accepted a non-public destination", rawURL)
		}
	}
	for _, rawURL := range []string{"http://localhost:11434/v1", "http://127.0.0.1:11434/v1", "http://[::1]:11434/v1", "https://provider.example/v1"} {
		if err := ValidateEndpoint(rawURL); err != nil {
			t.Errorf("ValidateEndpoint(%q) = %v", rawURL, err)
		}
	}
}

func TestNewTransportDisablesAmbientProxyResolution(t *testing.T) {
	transport := NewTransport()
	if transport.Proxy != nil {
		t.Fatal("policy transport permits an ambient proxy to resolve the target")
	}
	if transport.DialContext == nil {
		t.Fatal("policy transport has no dial-time destination enforcement")
	}
	if message := unsafeCredentialTransportMessage; !strings.Contains(message, "https://") {
		t.Fatal("credential guidance no longer explains the secure transport requirement")
	}
}
