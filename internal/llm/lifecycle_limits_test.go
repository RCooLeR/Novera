package llm

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"novera/internal/settings"
)

type llmSettingsReaderFunc func() settings.Settings

func (f llmSettingsReaderFunc) Load() settings.Settings { return f() }

type llmRoundTripFunc func(*http.Request) (*http.Response, error)

func (f llmRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSendRequestBudgets(t *testing.T) {
	t.Run("message count", func(t *testing.T) {
		if err := validateSendRequest(SendRequest{Messages: make([]Message, maxSendMessages)}); err != nil {
			t.Fatalf("exact message-count limit rejected: %v", err)
		}
		err := validateSendRequest(SendRequest{Messages: make([]Message, maxSendMessages+1)})
		if !errors.Is(err, ErrRequestTooLarge) {
			t.Fatalf("message-count error = %v, want ErrRequestTooLarge", err)
		}
	})

	for _, tt := range []struct {
		name string
		req  SendRequest
	}{
		{name: "role", req: SendRequest{Messages: []Message{{Role: strings.Repeat("r", maxMessageRoleBytes+1)}}}},
		{name: "content", req: SendRequest{Messages: []Message{{Content: strings.Repeat("x", maxMessageContentBytes+1)}}}},
		{name: "context", req: SendRequest{Context: strings.Repeat("x", maxContextBytes+1)}},
		{name: "system", req: SendRequest{System: strings.Repeat("x", maxSystemBytes+1)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateSendRequest(tt.req); !errors.Is(err, ErrRequestTooLarge) {
				t.Fatalf("field-limit error = %v, want ErrRequestTooLarge", err)
			}
		})
	}

	t.Run("exact field limits", func(t *testing.T) {
		req := SendRequest{
			System:  strings.Repeat("s", maxSystemBytes),
			Context: strings.Repeat("c", maxContextBytes),
			Messages: []Message{{
				Role:    strings.Repeat("r", maxMessageRoleBytes),
				Content: strings.Repeat("m", maxMessageContentBytes),
			}},
		}
		if err := validateSendRequest(req); err != nil {
			t.Fatalf("exact per-field limits rejected: %v", err)
		}
		body := chatRequest{Model: "model", Stream: true, Messages: buildMessages(req)}
		if _, err := marshalChatRequest(body); err != nil {
			t.Fatalf("bounded normalized request rejected: %v", err)
		}
	})

	t.Run("aggregate", func(t *testing.T) {
		messages := make([]Message, 5)
		for i := range messages {
			messages[i] = Message{Role: "user", Content: strings.Repeat("x", 900<<10)}
		}
		if err := validateSendRequest(SendRequest{Messages: messages}); !errors.Is(err, ErrRequestTooLarge) {
			t.Fatalf("aggregate error = %v, want ErrRequestTooLarge", err)
		}
		wireMessages := make([]apiMessage, len(messages))
		for i, message := range messages {
			wireMessages[i] = apiMessage{Role: message.Role, Content: message.Content}
		}
		if _, err := marshalChatRequest(chatRequest{Model: "model", Messages: wireMessages}); !errors.Is(err, ErrRequestTooLarge) {
			t.Fatalf("normalized aggregate error = %v, want ErrRequestTooLarge", err)
		}
	})

	t.Run("model before marshal", func(t *testing.T) {
		_, err := marshalChatRequest(chatRequest{Model: strings.Repeat("m", maxChatModelBytes+1)})
		if !errors.Is(err, ErrRequestTooLarge) {
			t.Fatalf("model error = %v, want ErrRequestTooLarge", err)
		}
	})
}

func TestSendRejectsOversizedPayloadBeforeSettingsAccess(t *testing.T) {
	service := &Service{settings: llmSettingsReaderFunc(func() settings.Settings {
		t.Fatal("oversized Send reached settings.Load")
		return settings.Settings{}
	})}
	_, err := service.Send(SendRequest{Messages: make([]Message, maxSendMessages+1)})
	if !errors.Is(err, ErrRequestTooLarge) {
		t.Fatalf("Send error = %v, want ErrRequestTooLarge", err)
	}
}

func TestConcurrentStreamAdmissionIsBoundedUntilCanceledWorkDrains(t *testing.T) {
	started := make(chan struct{}, 4)
	transport := llmRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	service := newLifecycleLLMService(staticLifecycleReader(), transport)
	service.streamLimit = 2

	first, err := service.Send(SendRequest{Messages: []Message{{Role: "user", Content: "one"}}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Send(SendRequest{Messages: []Message{{Role: "user", Content: "two"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitLLMSignal(t, started, "first admitted HTTP stream")
	waitLLMSignal(t, started, "second admitted HTTP stream")

	if id, err := service.Send(SendRequest{Messages: []Message{{Role: "user", Content: "over capacity"}}}); id != "" || !errors.Is(err, ErrTooManyStreams) {
		t.Fatalf("over-capacity Send = (%q, %v), want ErrTooManyStreams", id, err)
	}

	firstDone := requestDone(t, service, first)
	service.Cancel(first)
	waitLLMSignal(t, firstDone, "canceled stream cleanup")

	third, err := service.Send(SendRequest{Messages: []Message{{Role: "user", Content: "replacement"}}})
	if err != nil {
		t.Fatalf("Send after drain: %v", err)
	}
	waitLLMSignal(t, started, "replacement HTTP stream")
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	assertLLMDrained(t, service)
	if first == second || second == third || first == third {
		t.Fatalf("request ids were reused: %q, %q, %q", first, second, third)
	}
}

func TestConcurrentModelListAdmissionIsBounded(t *testing.T) {
	started := make(chan struct{}, 1)
	transport := llmRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	service := newLifecycleLLMService(staticLifecycleReader(), transport)
	service.modelListLimit = 1
	firstResult := make(chan error, 1)
	go func() {
		_, err := service.ListModels()
		firstResult <- err
	}()
	waitLLMSignal(t, started, "first admitted model-list request")

	if _, err := service.ListModels(); !errors.Is(err, ErrTooManyModelLists) {
		t.Fatalf("over-capacity ListModels error = %v, want ErrTooManyModelLists", err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if err := <-firstResult; !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("admitted ListModels error = %v, want ErrServiceShuttingDown", err)
	}
	assertLLMDrained(t, service)
}

func TestServiceShutdownWaitsForPendingSendAndPermanentlyClosesAdmission(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := llmSettingsReaderFunc(func() settings.Settings {
		close(entered)
		<-release
		return staticLifecycleSettings()
	})
	service := newLifecycleLLMService(reader, llmRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("shutdown-raced pending Send reached HTTP")
		return nil, errors.New("unexpected HTTP")
	}))

	sendResult := make(chan error, 1)
	go func() {
		_, err := service.Send(SendRequest{Messages: []Message{{Role: "user", Content: "pending"}}})
		sendResult <- err
	}()
	waitLLMSignal(t, entered, "settings-blocked Send")
	lease := onlyRequestLease(t, service)
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- service.ServiceShutdown() }()
	waitLLMSignal(t, lease.ctx.Done(), "shutdown cancellation of pending Send")
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before pending Send drained: %v", err)
	default:
	}

	close(release)
	if err := <-sendResult; !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("pending Send error = %v, want ErrServiceShuttingDown", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if _, err := service.Send(SendRequest{}); !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("Send after shutdown error = %v, want ErrServiceShuttingDown", err)
	}
	if _, err := service.ListModels(); !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("ListModels after shutdown error = %v, want ErrServiceShuttingDown", err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("idempotent ServiceShutdown: %v", err)
	}
	assertLLMDrained(t, service)
}

func TestServiceShutdownCancelsAndAwaitsInFlightStreamCleanup(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	transport := llmRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(canceled)
		<-release // deterministic non-cooperative cleanup after observing cancel
		return nil, req.Context().Err()
	})
	service := newLifecycleLLMService(staticLifecycleReader(), transport)
	if _, err := service.Send(SendRequest{Messages: []Message{{Role: "user", Content: "stream"}}}); err != nil {
		t.Fatal(err)
	}
	waitLLMSignal(t, started, "HTTP stream")

	shutdownResult := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { shutdownResult <- service.ServiceShutdown() }()
	}
	waitLLMSignal(t, canceled, "HTTP request cancellation")
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before RoundTripper cleanup: %v", err)
	default:
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-shutdownResult; err != nil {
			t.Fatalf("concurrent ServiceShutdown %d: %v", i+1, err)
		}
	}
	assertLLMDrained(t, service)
}

func TestServiceShutdownCancelsAndAwaitsInFlightModelList(t *testing.T) {
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	transport := llmRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		close(started)
		<-req.Context().Done()
		close(canceled)
		<-release
		return nil, req.Context().Err()
	})
	service := newLifecycleLLMService(staticLifecycleReader(), transport)
	listResult := make(chan error, 1)
	go func() {
		_, err := service.ListModels()
		listResult <- err
	}()
	waitLLMSignal(t, started, "model-list HTTP request")

	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- service.ServiceShutdown() }()
	waitLLMSignal(t, canceled, "model-list request cancellation")
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before model-list cleanup: %v", err)
	default:
	}
	close(release)
	if err := <-listResult; !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("ListModels error = %v, want ErrServiceShuttingDown", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	assertLLMDrained(t, service)
}

func TestServiceShutdownTimeoutLeavesPermanentGateAndLateCleanup(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := llmSettingsReaderFunc(func() settings.Settings {
		close(entered)
		<-release
		return staticLifecycleSettings()
	})
	service := newLifecycleLLMService(reader, nil)
	service.shutdownTimeout = 10 * time.Millisecond
	sendResult := make(chan error, 1)
	go func() {
		_, err := service.Send(SendRequest{})
		sendResult <- err
	}()
	waitLLMSignal(t, entered, "settings-blocked Send")
	if err := service.ServiceShutdown(); !errors.Is(err, ErrShutdownTimeout) {
		t.Fatalf("ServiceShutdown error = %v, want ErrShutdownTimeout", err)
	}
	if _, err := service.Send(SendRequest{}); !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("Send after timed-out shutdown error = %v, want ErrServiceShuttingDown", err)
	}
	close(release)
	if err := <-sendResult; !errors.Is(err, ErrServiceShuttingDown) {
		t.Fatalf("late pending Send error = %v, want ErrServiceShuttingDown", err)
	}
	assertLLMDrained(t, service)
}

func newLifecycleLLMService(reader settingsReader, transport http.RoundTripper) *Service {
	service := New(nil, nil)
	service.settings = reader
	if transport != nil {
		service.http = &http.Client{Transport: transport}
	}
	service.shutdownTimeout = 2 * time.Second
	service.eventSink = func(string, any) {}
	return service
}

func staticLifecycleSettings() settings.Settings {
	return settings.Settings{LLM: settings.LLM{
		Provider:          "custom",
		BaseURL:           "https://provider.example/v1",
		Model:             "test-model",
		RequestTimeoutSec: settings.MinRequestTimeoutSec,
	}}
}

func staticLifecycleReader() settingsReader {
	return llmSettingsReaderFunc(staticLifecycleSettings)
}

func waitLLMSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func requestDone(t *testing.T, service *Service, id string) <-chan struct{} {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	lease := service.requests[id]
	if lease == nil {
		t.Fatalf("request %q is not admitted", id)
	}
	return lease.done
}

func onlyRequestLease(t *testing.T, service *Service) *requestLease {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.requests) != 1 {
		t.Fatalf("admitted requests = %d, want 1", len(service.requests))
	}
	for _, lease := range service.requests {
		return lease
	}
	panic("unreachable")
}

func assertLLMDrained(t *testing.T, service *Service) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	if len(service.requests) != 0 || service.streams != 0 || service.modelLists != 0 {
		t.Fatalf("LLM lifecycle not drained: requests=%d streams=%d modelLists=%d", len(service.requests), service.streams, service.modelLists)
	}
}
