package agent

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"novera/internal/providerhttp"
)

func TestCompleteRejectsOversizedProviderResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.FormatInt(providerhttp.MaxCompletionResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := &Service{http: srv.Client()}
	_, err := s.complete(context.Background(), "run-oversize", srv.URL, "model", "", []wireMsg{{Role: "user", Content: "hello"}}, nil)
	if !errors.Is(err, providerhttp.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "8 MiB") {
		t.Fatalf("error should state the limit, got %q", err)
	}
}

func TestCompleteDoesNotWriteProviderDebugLogByDefault(t *testing.T) {
	t.Setenv(agentDebugResponsesEnv, "")
	path := filepath.Join(t.TempDir(), "agent-debug.jsonl")
	if err := os.WriteFile(path, []byte(`{"rawResponse":"legacy private response"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	debug := newAgentDebugLogAt(path)
	if debug != nil {
		t.Fatal("provider debug logging must be disabled unless explicitly enabled")
	}

	srv := completionTestServer(t, `{"choices":[{"message":{"content":"private answer"}}]}`)
	defer srv.Close()
	s := &Service{http: srv.Client(), debug: debug}
	got, err := s.complete(context.Background(), "run-default", srv.URL, "model", "", []wireMsg{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "private answer" {
		t.Fatalf("content = %q, want normal completion content", got.Content)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("debug log (including a legacy raw log) should not exist by default; stat error = %v", err)
	}
}

func TestCompleteOptInDebugLogRedactsProviderContent(t *testing.T) {
	t.Setenv(agentDebugResponsesEnv, "true")
	path := filepath.Join(t.TempDir(), "agent-debug.jsonl")
	debug := newAgentDebugLogAt(path)
	if debug == nil {
		t.Fatal("provider debug logging should be enabled by explicit opt-in")
	}

	response := `{"id":"chatcmpl-safe-id","model":"test-model","choices":[{"message":{"role":"assistant","content":"private answer","tool_calls":[{"id":"call-safe-id","type":"function","function":{"name":"write_file","arguments":"{\"content\":\"private file body\"}"}}]}}]}`
	srv := completionTestServer(t, response)
	defer srv.Close()
	s := &Service{http: srv.Client(), debug: debug}
	got, err := s.complete(context.Background(), "run-debug", srv.URL+"?access_token=do-not-log", "model", "", []wireMsg{{Role: "user", Content: "hello"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Content != "private answer" {
		t.Fatalf("content = %q, want normal completion content", got.Content)
	}

	logged, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(logged)
	for _, secret := range []string{"private answer", "private file body", "do-not-log", "chatcmpl-safe-id", "write_file", "test-model"} {
		if strings.Contains(text, secret) {
			t.Fatalf("debug log leaked %q: %s", secret, text)
		}
	}
	for _, want := range []string{`"responseRedacted":true`, `"responsePreview":`, "[REDACTED", `\"content\"`, `\"arguments\"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("debug log missing %q: %s", want, text)
		}
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Size() > maxAgentDebugLogBytes {
		t.Fatalf("debug log size = %d, exceeds %d", info.Size(), maxAgentDebugLogBytes)
	}
}

func TestAgentDebugLogRotatesWithinTotalBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent-debug.jsonl")
	d := &agentDebugLog{path: path, maxBytes: 1024}
	entry := AgentDebugEntry{RunID: strings.Repeat("r", 250), Event: "completion_response", ResponseRedacted: true}
	for i := 0; i < 20; i++ {
		d.record(entry)
	}
	for _, candidate := range []string{path, path + ".1"} {
		info, err := os.Stat(candidate)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if info.Size() > d.maxBytes {
			t.Fatalf("%s size = %d, exceeds %d", candidate, info.Size(), d.maxBytes)
		}
	}
}

func TestDebugResponsePreviewDropsUnknownFieldsAndRedactsEveryScalar(t *testing.T) {
	preview, truncated, redacted := debugResponsePreview([]byte(`{"id":123456789,"content":true,"unknown-private-key":"private value","usage":{"total_tokens":42}}`))
	if truncated || !redacted {
		t.Fatalf("truncated = %v, redacted = %v", truncated, redacted)
	}
	for _, secret := range []string{"123456789", "private value", "unknown-private-key", ":true", ":42"} {
		if strings.Contains(preview, secret) {
			t.Fatalf("preview leaked %q: %s", secret, preview)
		}
	}
	for _, want := range []string{"[REDACTED number]", "[REDACTED boolean]", `"usage"`} {
		if !strings.Contains(preview, want) {
			t.Fatalf("preview missing %q: %s", want, preview)
		}
	}
}

func completionTestServer(t *testing.T, response string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
	}))
}
