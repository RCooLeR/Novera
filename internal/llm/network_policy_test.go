package llm

import (
	"net/http"
	"testing"
)

func TestLLMClientUsesDialTimeDestinationPolicy(t *testing.T) {
	s := New(nil, nil)
	transport, ok := s.http.Transport.(*http.Transport)
	if !ok || transport == nil {
		t.Fatalf("LLM transport = %T, want policy *http.Transport", s.http.Transport)
	}
	if transport.Proxy != nil || transport.DialContext == nil {
		t.Fatal("LLM client can bypass direct dial-time destination policy")
	}
}
