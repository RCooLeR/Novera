package llm

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"novera/internal/providerhttp"
	"novera/internal/settings"
)

type checkedTestSecrets struct {
	value string
	found bool
	err   error
}

func (s checkedTestSecrets) Get(string) (string, bool) { return s.value, s.found }
func (s checkedTestSecrets) GetChecked(string) (string, bool, error) {
	return s.value, s.found, s.err
}

func TestResolveKeyFailsClosedOnCheckedStoreErrorsAndMissingCredential(t *testing.T) {
	cfg := settings.LLM{Provider: "custom", BaseURL: "https://provider.example/v1", Model: "model"}
	ref, err := settings.ExpectedLLMAPIKeyRef(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeyRef = ref
	corrupt := errors.New("ciphertext authentication failed")
	for _, tt := range []struct {
		name    string
		secrets checkedTestSecrets
		want    string
	}{
		{name: "corrupt store", secrets: checkedTestSecrets{err: corrupt}, want: "credential storage is unavailable"},
		{name: "missing credential", secrets: checkedTestSecrets{}, want: "configured API credential is missing"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &Service{secrets: tt.secrets}
			if _, err := svc.resolveKey(cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("resolveKey error = %v, want %q", err, tt.want)
			}
		})
	}

	svc := &Service{secrets: checkedTestSecrets{value: "secret", found: true}}
	if got, err := svc.resolveKey(cfg); err != nil || got != "secret" {
		t.Fatalf("resolveKey = %q, %v; want secret, nil", got, err)
	}
}

func TestResolveKeyRejectsQuarantinedLegacyRefAtPointOfUse(t *testing.T) {
	cfg := settings.LLM{
		Provider:  "custom",
		BaseURL:   "https://attacker-controlled.example/v1",
		Model:     "model",
		APIKeyRef: "llm.apikey",
	}
	svc := &Service{secrets: checkedTestSecrets{value: "legacy-secret", found: true}}
	if _, err := svc.resolveKey(cfg); err == nil || !strings.Contains(err.Error(), "pending safe provider-origin migration") {
		t.Fatalf("resolveKey error = %v, want quarantined legacy-ref rejection", err)
	}
}

func TestListModelsRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(providerhttp.MaxModelListResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	s := &Service{http: srv.Client()}
	_, err := s.listModels(context.Background(), srv.URL, "")
	if !errors.Is(err, providerhttp.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "2 MiB") {
		t.Fatalf("error should state the limit, got %q", err)
	}
}

func TestListModelsPreservesJSONParsingAndSorting(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %q, want /models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":" zeta "},{"id":""},{"id":"alpha"},{"id":"alpha"}]}`))
	}))
	s := &Service{http: srv.Client()}
	models, err := s.listModels(context.Background(), srv.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(models, ","); got != "alpha,zeta" {
		t.Fatalf("models = %q, want alpha,zeta", got)
	}
}

func TestListModelsRejectsMalformedShapeAndUnboundedFields(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "missing data", body: `{}`, want: "required data array"},
		{name: "null data", body: `{"data":null}`, want: "data must be an array"},
		{name: "too many models", body: `{"data":[` + strings.Repeat(`{"id":"model"},`, maxModelCount) + `{"id":"model"}]}`, want: "too many models"},
		{name: "too many compact null entries", body: `{"data":[` + strings.Repeat(`null,`, maxModelCount) + `null]}`, want: "too many models"},
		{name: "long model id", body: `{"data":[{"id":"` + strings.Repeat("x", maxModelIDBytes+1) + `"}]}`, want: "model id longer"},
		{name: "duplicate data", body: `{"data":[],"data":[]}`, want: "duplicate data"},
		{name: "provider error", body: `{"data":[],"error":{"message":"access denied"}}`, want: "access denied"},
		{name: "trailing JSON", body: `{"data":[]} {}`, want: "could not parse"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			s := &Service{http: srv.Client()}
			_, err := s.listModels(context.Background(), srv.URL, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestConsumeChatStreamRejectsOversizedResponse(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(providerhttp.MaxStreamResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	err = consumeChatStream(context.Background(), resp, nil, nil)
	if !errors.Is(err, providerhttp.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge", err)
	}
	if !strings.Contains(err.Error(), "32 MiB") {
		t.Fatalf("error should state the limit, got %q", err)
	}
}

func TestConsumeChatStreamPreservesSSEDeltaBehavior(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var deltas []string
	activities := 0
	err = consumeChatStream(context.Background(), resp, func() { activities++ }, func(delta string) {
		deltas = append(deltas, delta)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deltas, ""); got != "hello" {
		t.Fatalf("deltas = %q, want hello", got)
	}
	if activities == 0 {
		t.Fatal("expected stream activity callback")
	}
}

func TestConsumeChatStreamRejectsMalformedOrTruncatedProtocol(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed JSON", body: "data: malformed\n\n", want: "malformed streaming JSON"},
		{name: "missing choices", body: "data: {\"id\":\"chunk\"}\n\n", want: "without choices"},
		{name: "empty choices", body: "data: {\"choices\":[]}\n\n", want: "without choices"},
		{name: "invalid usage shape", body: "data: {\"choices\":[],\"usage\":[]}\n\n", want: "without choices"},
		{name: "invalid prompt annotation shape", body: "data: {\"choices\":[],\"prompt_filter_results\":{}}\n\n", want: "without choices"},
		{name: "null choice", body: "data: {\"choices\":[null]}\n\ndata: [DONE]\n\n", want: "null streaming choice"},
		{name: "empty choice", body: "data: {\"choices\":[{}]}\n\ndata: [DONE]\n\n", want: "without a delta"},
		{name: "null delta", body: "data: {\"choices\":[{\"delta\":null}]}\n\ndata: [DONE]\n\n", want: "without a delta"},
		{name: "empty finish reason", body: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"\"}]}\n\n", want: "empty streaming finish reason"},
		{name: "multiple choices", body: "data: {\"choices\":[{\"delta\":{}},{\"delta\":{}}]}\n\n", want: "unexpected number"},
		{name: "truncated stream", body: "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n", want: "before a completion marker"},
		{name: "unterminated done event", body: "data: [DONE]\n", want: "before a completion marker"},
		{name: "choice after terminal", body: "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"late\"}}]}\n\n", want: "already complete"},
		{name: "provider error", body: "data: {\"error\":{\"message\":\"quota exhausted\"}}\n\n", want: "quota exhausted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte(tt.body))
			}))
			resp, err := srv.Client().Get(srv.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			err = consumeChatStream(context.Background(), resp, nil, nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestConsumeChatStreamRejectsOversizedSSEEvent(t *testing.T) {
	first := strings.Repeat("a", maxSSEEventBytes/2+1)
	second := strings.Repeat("b", maxSSEEventBytes/2+1)
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: " + first + "\n"))
		_, _ = w.Write([]byte("data: " + second + "\n\n"))
	}))
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	err = consumeChatStream(context.Background(), resp, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "SSE event larger") {
		t.Fatalf("error = %v, want per-event size error", err)
	}
}

func TestConsumeChatStreamParsesSSEEventsAndAzureAnnotations(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\ufeff: initial comment\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":null,\"prompt_filter_results\":[{}]}\n\n"))
		_, _ = w.Write([]byte("event: message\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[\n"))
		_, _ = w.Write([]byte("data: {\"delta\":{\"content\":\"multi\"}}\n"))
		_, _ = w.Write([]byte("data: ]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		// Azure may emit an asynchronous content-filter annotation after the
		// terminal token. It is metadata, not another completion or delta.
		_, _ = w.Write([]byte("data: {\"choices\":[{\"content_filter_results\":{\"hate\":{}}}]}\n\n"))
	}))
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var deltas []string
	if err := consumeChatStream(context.Background(), resp, nil, func(delta string) {
		deltas = append(deltas, delta)
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deltas, ""); got != "multi" {
		t.Fatalf("deltas = %q, want multi", got)
	}
}

func TestConsumeChatStreamAcceptsTerminalChoiceWithoutDone(t *testing.T) {
	srv := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"total_tokens\":1}}\n\n"))
	}))
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var deltas []string
	if err := consumeChatStream(context.Background(), resp, nil, func(delta string) {
		deltas = append(deltas, delta)
	}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(deltas, ""); got != "ok" {
		t.Fatalf("deltas = %q, want ok", got)
	}
}
