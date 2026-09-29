package agent

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	"novera/internal/jobs"
	"novera/internal/settings"
)

func TestServiceShutdownWaitsForPendingStartAndPermanentlyClosesAdmission(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	blocking := &blockingAgentSettings{
		value:   settings.Settings{LLM: settings.LLM{Provider: "custom", BaseURL: "https://provider.example/v1", Model: "test-model"}},
		entered: entered,
		release: release,
	}
	service := newLifecycleService(agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider must not be called")
	}))
	service.settings = blocking
	startResult := make(chan error, 1)
	go func() {
		_, err := service.Start("pending start")
		startResult <- err
	}()
	waitAgentSignal(t, entered, "blocked Agent Start")

	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- service.ServiceShutdown() }()
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before pending Start drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-startResult; !errors.Is(err, ErrAgentShuttingDown) {
		t.Fatalf("pending Start error = %v, want ErrAgentShuttingDown", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if _, err := service.Start("after shutdown"); !errors.Is(err, ErrAgentShuttingDown) {
		t.Fatalf("Start after shutdown error = %v, want ErrAgentShuttingDown", err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("idempotent ServiceShutdown: %v", err)
	}
}

func TestServiceShutdownCancelsAndAwaitsApprovalWait(t *testing.T) {
	const toolName = "shutdown_approval_tool"
	transport := agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return agentJSONResponse(req, `{"choices":[{"message":{"tool_calls":[{"id":"provider-call","type":"function","function":{"name":"shutdown_approval_tool","arguments":"{}"}}]}}]}`), nil
	})
	service := newLifecycleService(transport)
	toolRan := false
	service.toolset[toolName] = tool{
		description: "shutdown approval test",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		mutating:    true,
		run: func(map[string]any) (string, error) {
			toolRan = true
			return "unexpected", nil
		},
	}
	service.toolCanonical[normalizeToolName(toolName)] = toolName

	runID, err := service.Start("use the shutdown approval tool")
	if err != nil {
		t.Fatal(err)
	}
	callID := runID + "-tool-1"
	waitRegistered(t, service, callID)
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if toolRan {
		t.Fatal("approval-gated tool ran during shutdown")
	}
	service.mu.Lock()
	remainingRuns := len(service.runs)
	remainingApprovals := len(service.approvals)
	service.mu.Unlock()
	if remainingRuns != 0 || remainingApprovals != 0 {
		t.Fatalf("shutdown state = runs %d approvals %d, want 0/0", remainingRuns, remainingApprovals)
	}
	listed := service.jobs.ListJobs()
	if len(listed) != 1 || listed[0].Status != jobs.StatusCanceled {
		t.Fatalf("shutdown job state = %+v, want canceled", listed)
	}
}

func TestServiceShutdownCancelsOwnedCommandTree(t *testing.T) {
	service := commandTestService(t)
	service.runs = map[string]*runLease{}
	service.approvals = map[string]chan bool{}
	service.approvalDigests = map[string]string{}
	service.approvalExpirations = map[string]time.Time{}
	service.shutdownTimeout = 5 * time.Second
	commandStarted := make(chan struct{})
	service.commandStarted = func() { close(commandStarted) }

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	service.runs["run-command"] = &runLease{cancel: cancel, done: done}
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "ping -n 30 127.0.0.1 >nul"
	}
	commandResult := make(chan error, 1)
	go func() {
		_, err := service.runCommand(ctx, command, 30*time.Second)
		service.mu.Lock()
		close(done)
		delete(service.runs, "run-command")
		service.mu.Unlock()
		commandResult <- err
	}()
	waitAgentSignal(t, commandStarted, "owned command process tree")

	started := time.Now()
	if err := service.ServiceShutdown(); err != nil {
		t.Fatalf("ServiceShutdown: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("shutdown did not terminate command tree promptly: %s", elapsed)
	}
	if err := <-commandResult; err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("command error = %v, want cancellation", err)
	}
}
