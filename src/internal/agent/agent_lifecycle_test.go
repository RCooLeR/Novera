package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"novera/internal/jobs"
	"novera/internal/netsafe"
	"novera/internal/settings"
)

type staticAgentSettings struct {
	value settings.Settings
}

func (s staticAgentSettings) Load() settings.Settings { return s.value }

type staticAgentSecrets map[string]string

func (s staticAgentSecrets) Get(ref string) (string, bool) {
	v, ok := s[ref]
	return v, ok
}

type blockingAgentSettings struct {
	value   settings.Settings
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingAgentSettings) Load() settings.Settings {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.value
}

type blockingAgentSecrets struct {
	value   string
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

type checkedAgentSecrets struct {
	value string
	found bool
	err   error
}

func (s checkedAgentSecrets) Get(string) (string, bool) {
	return "legacy fallback must not be used", true
}
func (s checkedAgentSecrets) GetChecked(string) (string, bool, error) {
	return s.value, s.found, s.err
}

func (s *blockingAgentSecrets) Get(string) (string, bool) {
	s.once.Do(func() { close(s.entered) })
	<-s.release
	return s.value, true
}

type agentRoundTripFunc func(*http.Request) (*http.Response, error)

func (f agentRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func agentJSONResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func newLifecycleService(transport http.RoundTripper) *Service {
	cfg := settings.Settings{
		LLM: settings.LLM{
			Provider: "custom",
			BaseURL:  "https://provider.example/v1",
			Model:    "test-model",
		},
	}
	s := &Service{
		settings:        staticAgentSettings{value: cfg},
		http:            &http.Client{Transport: transport, CheckRedirect: netsafe.RedirectPolicy(5)},
		rollback:        newRollbackJournal(),
		jobs:            jobs.New(),
		runs:            map[string]*runLease{},
		approvals:       map[string]chan bool{},
		shutdownTimeout: time.Second,
	}
	s.toolset, s.toolOrder = s.buildTools()
	s.toolCanonical = make(map[string]string, len(s.toolOrder))
	for _, name := range s.toolOrder {
		s.toolCanonical[normalizeToolName(name)] = name
	}
	return s
}

func waitAgentSignal(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

func waitRunLeaseReleased(t *testing.T, s *Service, runID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, active := s.runs[runID]
		s.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("active-run lease %q was not released", runID)
}

func assertRunLeaseHeld(t *testing.T, s *Service, runID string) {
	t.Helper()
	s.mu.Lock()
	_, active := s.runs[runID]
	s.mu.Unlock()
	if !active {
		t.Fatalf("active-run lease %q was released before deferred work returned", runID)
	}
}

func assertImmediateRestartRejected(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Start("must not overlap"); err == nil || !strings.Contains(err.Error(), "already in progress") {
		t.Fatalf("immediate restart error = %v, want active-run rejection", err)
	}
}

func TestAgentRejectsCredentialBearingRemotePlaintextAtAdmissionAndRequest(t *testing.T) {
	cfg := settings.LLM{Provider: "custom", BaseURL: "http://provider.example/v1", Model: "test-model"}
	ref, err := settings.ExpectedLLMAPIKeyRef(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeyRef = ref
	s := &Service{
		settings:  staticAgentSettings{value: settings.Settings{LLM: cfg}},
		secrets:   staticAgentSecrets{ref: "top-secret"},
		runs:      map[string]*runLease{},
		approvals: map[string]chan bool{},
	}
	if _, err := s.Start("do work"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "plaintext") {
		t.Fatalf("Start error = %v, want plaintext credential refusal", err)
	}
	if len(s.runs) != 0 {
		t.Fatal("rejected transport unexpectedly admitted an active run")
	}

	requests := 0
	s.http = &http.Client{Transport: agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("network must not be reached")
	})}
	if _, err := s.complete(context.Background(), "run-direct", "http://provider.example/v1", "test-model", "top-secret", nil, nil); err == nil || !strings.Contains(strings.ToLower(err.Error()), "plaintext") {
		t.Fatalf("complete error = %v, want request-boundary plaintext refusal", err)
	}
	if requests != 0 {
		t.Fatalf("plaintext credential request reached transport %d time(s)", requests)
	}
}

func TestAgentFailsClosedOnCheckedCredentialReadFailureOrMissingRef(t *testing.T) {
	cfg := settings.LLM{Provider: "custom", BaseURL: "https://provider.example/v1", Model: "test-model"}
	ref, err := settings.ExpectedLLMAPIKeyRef(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeyRef = ref
	for _, tc := range []struct {
		name    string
		secrets checkedAgentSecrets
		want    string
	}{
		{name: "missing", secrets: checkedAgentSecrets{}, want: "credential is missing"},
		{name: "read failure", secrets: checkedAgentSecrets{err: errors.New("ciphertext corrupt")}, want: "ciphertext corrupt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{
				settings:  staticAgentSettings{value: settings.Settings{LLM: cfg}},
				secrets:   tc.secrets,
				runs:      map[string]*runLease{},
				approvals: map[string]chan bool{},
			}
			if _, err := s.Start("must not reach provider"); err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("Start error = %v, want %q", err, tc.want)
			}
			if len(s.runs) != 0 {
				t.Fatal("credential failure admitted a run")
			}
		})
	}
	s := &Service{
		settings:  staticAgentSettings{value: settings.Settings{LLM: cfg}},
		runs:      map[string]*runLease{},
		approvals: map[string]chan bool{},
	}
	if _, err := s.Start("missing secret service"); err == nil || !strings.Contains(strings.ToLower(err.Error()), "credential is unavailable") {
		t.Fatalf("nil secret service error = %v, want unavailable credential", err)
	}
}

func TestAgentRejectsQuarantinedLegacyCredentialAtPointOfUse(t *testing.T) {
	cfg := settings.LLM{
		Provider:  "custom",
		BaseURL:   "https://attacker-controlled.example/v1",
		Model:     "model",
		APIKeyRef: "llm.apikey",
	}
	s := &Service{
		settings:  staticAgentSettings{value: settings.Settings{LLM: cfg}},
		secrets:   staticAgentSecrets{"llm.apikey": "legacy-secret"},
		runs:      map[string]*runLease{},
		approvals: map[string]chan bool{},
	}
	if _, err := s.Start("must not receive the legacy key"); err == nil || !strings.Contains(err.Error(), "pending safe provider-origin migration") {
		t.Fatalf("Start error = %v, want quarantined legacy-ref rejection", err)
	}
	if len(s.runs) != 0 {
		t.Fatal("legacy credential rejection unexpectedly admitted a run")
	}
}

func TestResetInvalidatesStartAttemptBlockedInSettingsLoad(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := &blockingAgentSettings{
		value:   settings.Settings{LLM: settings.LLM{Provider: "custom", BaseURL: "https://provider.example/v1", Model: "model"}},
		entered: entered,
		release: release,
	}
	s := &Service{
		settings:        reader,
		runs:            map[string]*runLease{},
		approvals:       map[string]chan bool{},
		shutdownTimeout: time.Second,
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.Start("stale workspace prompt")
		result <- err
	}()
	waitAgentSignal(t, entered, "blocked settings load")
	if err := s.ResetConversation(); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "workspace reset") {
			t.Fatalf("stale Start error = %v, want reset invalidation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale Start did not return after settings load released")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runs) != 0 || s.startAttempts != 0 {
		t.Fatalf("post-reset admission state = runs %d attempts %d", len(s.runs), s.startAttempts)
	}
}

func TestResetInvalidatesStartAttemptBlockedInSecretResolution(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	secrets := &blockingAgentSecrets{value: "secret", entered: entered, release: release}
	cfg := settings.LLM{Provider: "custom", BaseURL: "https://provider.example/v1", Model: "model"}
	ref, err := settings.ExpectedLLMAPIKeyRef(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.APIKeyRef = ref
	s := &Service{
		settings:        staticAgentSettings{value: settings.Settings{LLM: cfg}},
		secrets:         secrets,
		runs:            map[string]*runLease{},
		approvals:       map[string]chan bool{},
		shutdownTimeout: time.Second,
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.Start("stale secret-bound prompt")
		result <- err
	}()
	waitAgentSignal(t, entered, "blocked secret resolution")
	if err := s.ResetConversation(); err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "workspace reset") {
			t.Fatalf("stale Start error = %v, want reset invalidation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale Start did not return after secret resolution released")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runs) != 0 || s.startAttempts != 0 {
		t.Fatalf("post-reset admission state = runs %d attempts %d", len(s.runs), s.startAttempts)
	}
}

func TestResetDeletesOnlyApprovalsOwnedByStoppedRuns(t *testing.T) {
	cancelEntered := make(chan struct{})
	releaseCancel := make(chan struct{})
	oldDone := make(chan struct{})
	oldApproval := make(chan bool, 1)
	newApproval := make(chan bool, 1)
	s := &Service{
		runs: map[string]*runLease{
			"old-run": {
				cancel: func() {
					close(cancelEntered)
					<-releaseCancel
					close(oldDone)
				},
				done: oldDone,
			},
		},
		approvals: map[string]chan bool{"old-run-tool-1": oldApproval},
	}
	resetResult := make(chan error, 1)
	go func() { resetResult <- s.ResetConversation() }()
	waitAgentSignal(t, cancelEntered, "Reset cancellation window")

	// Reset has advanced the session and unlocked. This models a new Start that
	// has already registered its approval before the old reset cleanup resumes.
	s.mu.Lock()
	s.approvals["new-run-tool-1"] = newApproval
	s.mu.Unlock()
	close(releaseCancel)
	if err := <-resetResult; err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	_, oldPresent := s.approvals["old-run-tool-1"]
	gotNew := s.approvals["new-run-tool-1"]
	s.mu.Unlock()
	if oldPresent {
		t.Fatal("Reset retained an approval owned by the stopped run")
	}
	if gotNew != newApproval {
		t.Fatal("Reset erased an approval registered by a post-reset run")
	}
}

func TestWorkspaceTransitionBarrierRejectsStartUntilLatestOwnerEnds(t *testing.T) {
	s := newLifecycleService(agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"done"}}]}`), nil
	}))

	first, err := s.BeginWorkspaceTransition()
	if err != nil {
		t.Fatal(err)
	}
	latest, err := s.BeginWorkspaceTransition()
	if err != nil {
		t.Fatal(err)
	}
	if first == latest || first == "" || latest == "" {
		t.Fatalf("transition tokens are not unique and opaque: first=%q latest=%q", first, latest)
	}

	// An obsolete renderer continuation must not reopen admission owned by the
	// newer transition.
	s.EndWorkspaceTransition(first)
	if _, err := s.Start("must remain blocked"); err == nil || !strings.Contains(err.Error(), "workspace transition") {
		t.Fatalf("Start after stale End error = %v, want transition barrier", err)
	}

	s.EndWorkspaceTransition(latest)
	runID, err := s.Start("fresh workspace prompt")
	if err != nil {
		t.Fatalf("Start after latest End: %v", err)
	}
	waitRunLeaseReleased(t, s, runID)
}

func TestConcurrentWorkspaceBeginRejectsOwnerSupersededWhileWaiting(t *testing.T) {
	done := make(chan struct{})
	cancelCalls := make(chan struct{}, 2)
	s := &Service{
		runs: map[string]*runLease{
			"run-blocked": {
				cancel: func() { cancelCalls <- struct{}{} },
				done:   done,
			},
		},
		approvals:       map[string]chan bool{},
		shutdownTimeout: time.Second,
	}
	type beginResult struct {
		token string
		err   error
	}
	firstResult := make(chan beginResult, 1)
	secondResult := make(chan beginResult, 1)
	go func() {
		token, err := s.BeginWorkspaceTransition()
		firstResult <- beginResult{token: token, err: err}
	}()
	select {
	case <-cancelCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("first Begin did not reach the blocked run")
	}

	go func() {
		token, err := s.BeginWorkspaceTransition()
		secondResult <- beginResult{token: token, err: err}
	}()
	select {
	case <-cancelCalls:
	case <-time.After(2 * time.Second):
		t.Fatal("second Begin did not supersede the first blocked owner")
	}
	close(done)

	first := <-firstResult
	second := <-secondResult
	if first.token != "" || first.err == nil || !strings.Contains(first.err.Error(), "superseded") {
		t.Fatalf("first Begin result = token %q error %v, want superseded rejection", first.token, first.err)
	}
	if second.err != nil || second.token == "" {
		t.Fatalf("latest Begin result = token %q error %v, want current token", second.token, second.err)
	}
	s.EndWorkspaceTransition(second.token)
}

func TestWorkspaceTransitionAbortRestoresTranscriptWhileEndCommitsReset(t *testing.T) {
	s := newLifecycleService(agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"unused"}}]}`), nil
	}))
	s.session = []wireMsg{{Role: "user", Content: "old turn"}, {Role: "assistant", Content: "old answer"}}

	token, err := s.BeginWorkspaceTransition()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.session) != 0 {
		t.Fatalf("Begin did not isolate transcript: %+v", s.session)
	}
	s.AbortWorkspaceTransition(token)
	if len(s.session) != 2 || s.session[1].Content != "old answer" {
		t.Fatalf("Abort did not restore transcript: %+v", s.session)
	}

	token, err = s.BeginWorkspaceTransition()
	if err != nil {
		t.Fatal(err)
	}
	s.EndWorkspaceTransition(token)
	if len(s.session) != 0 {
		t.Fatalf("End did not commit transcript reset: %+v", s.session)
	}
}

func TestBeginWorkspaceTransitionInvalidatesBlockedStartAndKeepsAdmissionClosed(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	reader := &blockingAgentSettings{
		value:   settings.Settings{LLM: settings.LLM{Provider: "custom", BaseURL: "https://provider.example/v1", Model: "model"}},
		entered: entered,
		release: release,
	}
	s := &Service{
		settings:        reader,
		runs:            map[string]*runLease{},
		approvals:       map[string]chan bool{},
		shutdownTimeout: time.Second,
	}
	result := make(chan error, 1)
	go func() {
		_, err := s.Start("old workspace prompt")
		result <- err
	}()
	waitAgentSignal(t, entered, "blocked Start settings load")
	token, err := s.BeginWorkspaceTransition()
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "workspace reset") {
			t.Fatalf("blocked Start error = %v, want transition invalidation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked Start did not return")
	}
	if _, err := s.Start("during transition"); err == nil || !strings.Contains(err.Error(), "workspace transition") {
		t.Fatalf("Start while transition active error = %v", err)
	}
	s.EndWorkspaceTransition(token)
}

func TestAgentEventsHaveMonotonicSequenceWithoutHoldingLifecycleLock(t *testing.T) {
	s := &Service{
		runs: map[string]*runLease{
			"run-events": {done: make(chan struct{})},
		},
	}
	var got []agentEvent
	s.eventSink = func(ev agentEvent) {
		// Re-entering the lifecycle lock proves emitActive/emitTerminal do not
		// invoke an external dispatcher while holding it.
		s.mu.Lock()
		got = append(got, ev)
		s.mu.Unlock()
	}
	if !s.emitActive("run-events", agentEvent{RunID: "run-events", Type: "assistant_text", Text: "one"}) {
		t.Fatal("first event unexpectedly suppressed")
	}
	if !s.emitActive("run-events", agentEvent{RunID: "run-events", Type: "tool_call", Tool: "read_file"}) {
		t.Fatal("second event unexpectedly suppressed")
	}
	s.mu.Lock()
	s.runs["run-events"].cancelRequested = true
	s.mu.Unlock()
	if s.emitActive("run-events", agentEvent{RunID: "run-events", Type: "assistant_text", Text: "late"}) {
		t.Fatal("ordinary post-cancel event was emitted")
	}
	if !s.emitTerminal("run-events", agentEvent{RunID: "run-events", Type: "error", Text: "Run cancelled."}) {
		t.Fatal("terminal cancellation event was suppressed")
	}
	if len(got) != 3 {
		t.Fatalf("emitted %d events, want 3: %+v", len(got), got)
	}
	for i, ev := range got {
		if ev.Seq != uint64(i+1) {
			t.Fatalf("event %d sequence = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestAgentPanicRollsBackUnpairedTranscriptAndMarksTerminal(t *testing.T) {
	var transportMu sync.Mutex
	providerCalls := 0
	transport := agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		transportMu.Lock()
		providerCalls++
		call := providerCalls
		transportMu.Unlock()
		if call == 1 {
			return agentJSONResponse(req, `{"choices":[{"message":{"tool_calls":[{"id":"provider-id","type":"function","function":{"name":"panic_test_tool","arguments":"{}"}}]}}]}`), nil
		}
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"restart complete"}}]}`), nil
	})
	s := newLifecycleService(transport)
	s.toolset["panic_test_tool"] = tool{
		description: "panic transcript rollback test",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		run: func(map[string]any) (string, error) {
			panic("deterministic tool panic")
		},
	}
	s.toolCanonical[normalizeToolName("panic_test_tool")] = "panic_test_tool"
	var eventsMu sync.Mutex
	var events []agentEvent
	s.eventSink = func(ev agentEvent) {
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	}

	runID, err := s.Start("panic run")
	if err != nil {
		t.Fatal(err)
	}
	waitRunLeaseReleased(t, s, runID)
	s.mu.Lock()
	if len(s.session) != 0 {
		got := cloneWireMessages(s.session)
		s.mu.Unlock()
		t.Fatalf("panic left a partial transcript: %+v", got)
	}
	s.mu.Unlock()
	eventsMu.Lock()
	if len(events) == 0 || events[len(events)-1].Type != "error" || !events[len(events)-1].RolledBack {
		got := append([]agentEvent(nil), events...)
		eventsMu.Unlock()
		t.Fatalf("panic terminal event did not declare rollback: %+v", got)
	}
	eventsMu.Unlock()

	restartID, err := s.Start("restart after panic")
	if err != nil {
		t.Fatalf("restart after panic: %v", err)
	}
	waitRunLeaseReleased(t, s, restartID)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, message := range s.session {
		if len(message.ToolCalls) > 0 {
			t.Fatalf("restart inherited an unpaired tool call: %+v", s.session)
		}
	}
}

func TestCancelBetweenAssistantOutputAndDoneRollsBackAndEmitsOrderedTerminal(t *testing.T) {
	s := newLifecycleService(agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"tentative answer"}}]}`), nil
	}))
	assistantEmitting := make(chan struct{})
	releaseAssistant := make(chan struct{})
	var eventsMu sync.Mutex
	var events []agentEvent
	s.eventSink = func(ev agentEvent) {
		if ev.Type == "assistant_text" {
			close(assistantEmitting)
			<-releaseAssistant
		}
		eventsMu.Lock()
		events = append(events, ev)
		eventsMu.Unlock()
	}

	runID, err := s.Start("cancel at terminal boundary")
	if err != nil {
		t.Fatal(err)
	}
	waitAgentSignal(t, assistantEmitting, "assistant event dispatch")
	s.Cancel(runID)
	close(releaseAssistant)
	waitRunLeaseReleased(t, s, runID)

	s.mu.Lock()
	if len(s.session) != 0 {
		got := cloneWireMessages(s.session)
		s.mu.Unlock()
		t.Fatalf("terminal-boundary cancel did not restore transcript: %+v", got)
	}
	s.mu.Unlock()
	eventsMu.Lock()
	defer eventsMu.Unlock()
	if len(events) != 2 || events[0].Type != "assistant_text" || events[0].Seq != 1 {
		t.Fatalf("events before terminal = %+v", events)
	}
	terminal := events[1]
	if terminal.Type != "error" || terminal.Seq != 2 || !terminal.Canceled || !terminal.RolledBack {
		t.Fatalf("terminal event = %+v, want ordered canceled rollback", terminal)
	}
}

func TestCancelRequestedWithLiveContextFinalizesJobAsCanceled(t *testing.T) {
	providerCalls := 0
	s := newLifecycleService(agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		providerCalls++
		return nil, errors.New("provider must not be called after cancellation wins")
	}))
	const runID = "run-live-context-cancel"
	s.runs[runID] = &runLease{
		done:            make(chan struct{}),
		sessionID:       s.sessionID,
		cancelRequested: true,
	}
	var terminal agentEvent
	s.eventSink = func(ev agentEvent) { terminal = ev }
	jobID := jobs.Start(s.jobs, "agent", "live context cancellation", nil)
	appSettings := s.settings.Load()

	s.run(context.Background(), runID, jobID, "canceled", s.sessionID, nil, appSettings, "")

	if providerCalls != 0 {
		t.Fatalf("provider called %d time(s) after cancellation flag won", providerCalls)
	}
	job, err := s.jobs.GetJob(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobs.StatusCanceled {
		t.Fatalf("job status = %s, want canceled", job.Status)
	}
	if !terminal.Canceled || !terminal.RolledBack {
		t.Fatalf("terminal = %+v, want canceled rollback", terminal)
	}
}

func TestCanceledGatesAreAuditedAsCanceled(t *testing.T) {
	t.Run("continuation", func(t *testing.T) {
		s := &Service{
			runs: map[string]*runLease{
				"run-continue": {done: make(chan struct{})},
			},
			approvals: map[string]chan bool{},
			audit:     &auditLog{path: filepath.Join(t.TempDir(), "agent-audit.jsonl")},
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan gateDecision, 1)
		go func() { result <- s.awaitContinue(ctx, "run-continue", 10) }()
		waitRegistered(t, s, "run-continue-continue-10")
		cancel()
		if got := <-result; got != gateCanceled {
			t.Fatalf("continuation decision = %s, want canceled", got)
		}
		entries := s.audit.list(0)
		if len(entries) != 1 || entries[0].Status != "canceled" || entries[0].Decision != string(gateCanceled) {
			t.Fatalf("continuation audit = %+v, want canceled", entries)
		}
	})

	t.Run("tool approval", func(t *testing.T) {
		s := newLifecycleService(agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("provider is not used")
		}))
		s.audit = &auditLog{path: filepath.Join(t.TempDir(), "agent-audit.jsonl")}
		s.runs["run-tool"] = &runLease{done: make(chan struct{})}
		const toolName = "cancel_audit_tool"
		s.toolset[toolName] = tool{
			description: "canceled approval audit test",
			parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			gated:       true,
			run: func(map[string]any) (string, error) {
				t.Fatal("canceled gated tool ran")
				return "", nil
			},
		}
		s.toolCanonical[normalizeToolName(toolName)] = toolName
		approvalEmitted := make(chan struct{})
		s.eventSink = func(ev agentEvent) {
			if ev.Type == "approval_request" {
				close(approvalEmitted)
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan dispatchResult, 1)
		go func() {
			result <- s.dispatch(ctx, "run-tool", wireToolCall{
				ID: "run-tool-tool-1", Type: "function", Function: wireFunc{Name: toolName, Arguments: `{}`},
			}, map[string]bool{toolName: true}, []string{toolName}, settings.Agent{}.Normalized())
		}()
		waitAgentSignal(t, approvalEmitted, "approval request")
		cancel()
		if got := <-result; !got.canceled {
			t.Fatalf("dispatch result = %+v, want canceled", got)
		}
		entries := s.audit.list(0)
		if len(entries) != 1 || entries[0].Status != "canceled" || entries[0].Decision != string(gateCanceled) {
			t.Fatalf("tool audit = %+v, want canceled", entries)
		}
	})
}

func TestCancelImmediateStartWaitsForDeferredProviderExit(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	calls := 0
	inFlight := 0
	maxInFlight := 0
	transport := agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		mu.Lock()
		calls++
		call := calls
		inFlight++
		if inFlight > maxInFlight {
			maxInFlight = inFlight
		}
		mu.Unlock()
		defer func() {
			mu.Lock()
			inFlight--
			mu.Unlock()
		}()

		if call == 1 {
			close(entered)
			<-release // deliberately emulate provider work slow to acknowledge cancellation
			// Deliberately ignore req.Context and return a valid late response. The
			// run must discard it because cancellation is authoritative.
			return agentJSONResponse(req, `{"choices":[{"message":{"content":"late provider response"}}]}`), nil
		}
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"second run complete"}}]}`), nil
	})
	s := newLifecycleService(transport)

	runID, err := s.Start("first run")
	if err != nil {
		t.Fatal(err)
	}
	waitAgentSignal(t, entered, "provider request")
	s.Cancel(runID)
	assertRunLeaseHeld(t, s, runID)
	assertImmediateRestartRejected(t, s)

	close(release)
	waitRunLeaseReleased(t, s, runID)
	s.mu.Lock()
	for _, msg := range s.session {
		if strings.Contains(msg.Content, "late provider response") {
			s.mu.Unlock()
			t.Fatal("late canceled provider response crossed into session state")
		}
	}
	s.mu.Unlock()
	secondID, err := s.Start("second run")
	if err != nil {
		t.Fatalf("restart after acknowledged exit failed: %v", err)
	}
	if secondID == runID {
		t.Fatalf("run ID was reused: %q", secondID)
	}
	waitRunLeaseReleased(t, s, secondID)

	mu.Lock()
	defer mu.Unlock()
	if calls != 2 || maxInFlight != 1 {
		t.Fatalf("provider calls = %d, max concurrent = %d; want 2 sequential calls", calls, maxInFlight)
	}
}

func TestCancelImmediateStartWaitsForDeferredToolExitAndScopesCallID(t *testing.T) {
	toolEntered := make(chan struct{})
	releaseTool := make(chan struct{})
	var transportMu sync.Mutex
	providerCalls := 0
	transport := agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		transportMu.Lock()
		providerCalls++
		call := providerCalls
		transportMu.Unlock()
		if call == 1 {
			return agentJSONResponse(req, `{"choices":[{"message":{"tool_calls":[{"id":"provider-reused-id","type":"function","function":{"name":"deferred_test_tool","arguments":"{}"}}]}}]}`), nil
		}
		if call == 2 {
			return agentJSONResponse(req, `{"choices":[{"message":{"tool_calls":[{"id":"provider-reused-id","type":"function","function":{"name":"approval_test_tool","arguments":"{}"}}]}}]}`), nil
		}
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"new run complete"}}]}`), nil
	})
	s := newLifecycleService(transport)
	s.audit = &auditLog{path: filepath.Join(t.TempDir(), "agent-audit.jsonl")}
	s.toolset["deferred_test_tool"] = tool{
		description: "deterministic cancellation barrier test tool",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		run: func(map[string]any) (string, error) {
			close(toolEntered)
			<-releaseTool // deliberately ignores ctx to model a context-free legacy tool
			return "deferred tool returned", nil
		},
	}
	s.toolCanonical[normalizeToolName("deferred_test_tool")] = "deferred_test_tool"
	approvedToolCalls := 0
	s.toolset["approval_test_tool"] = tool{
		description: "deterministic run-scoped approval test tool",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		mutating:    true,
		run: func(map[string]any) (string, error) {
			approvedToolCalls++
			return "approved tool returned", nil
		},
	}
	s.toolCanonical[normalizeToolName("approval_test_tool")] = "approval_test_tool"

	runID, err := s.Start("use the deferred test tool")
	if err != nil {
		t.Fatal(err)
	}
	waitAgentSignal(t, toolEntered, "deferred tool")
	cancelReturned := make(chan struct{})
	go func() {
		s.Cancel(runID)
		close(cancelReturned)
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		lease := s.runs[runID]
		cancelRequested := lease != nil && lease.cancelRequested
		s.mu.Unlock()
		if cancelRequested {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Cancel never marked deferred tool run canceled")
		}
		time.Sleep(time.Millisecond)
	}
	assertRunLeaseHeld(t, s, runID)
	assertImmediateRestartRejected(t, s)
	select {
	case <-cancelReturned:
		t.Fatal("Cancel returned before the context-free tool finished")
	default:
	}

	close(releaseTool)
	waitAgentSignal(t, cancelReturned, "Cancel to await context-free tool exit")
	waitRunLeaseReleased(t, s, runID)

	wantCallID := runID + "-tool-1"
	s.mu.Lock()
	if len(s.session) != 0 {
		stale := cloneWireMessages(s.session)
		s.mu.Unlock()
		t.Fatalf("canceled run crossed into backend transcript: %+v", stale)
	}
	s.mu.Unlock()
	audit := s.audit.list(0)
	if len(audit) == 0 || audit[0].Tool != "deferred_test_tool" || audit[0].Status != "completed_after_cancel" {
		t.Fatalf("late tool audit = %+v, want truthful completed_after_cancel record", audit)
	}
	calls := []wireToolCall{{ID: "provider-reused-id"}, {ID: "provider-reused-id"}}
	next := 0
	scopeToolCallIDs(runID, calls, &next)
	if calls[0].ID != wantCallID || calls[1].ID != runID+"-tool-2" {
		t.Fatalf("scoped call IDs = %q, %q", calls[0].ID, calls[1].ID)
	}

	secondID, err := s.Start("new run")
	if err != nil {
		t.Fatalf("restart after deferred tool exit failed: %v", err)
	}
	secondCallID := secondID + "-tool-1"
	waitRegistered(t, s, secondCallID)
	// A delayed UI response for the provider-reused call from the canceled run
	// must not resolve the new run's approval gate.
	s.Approve(wantCallID, true)
	s.mu.Lock()
	_, stillPending := s.approvals[secondCallID]
	secondDigest := s.approvalDigests[secondCallID]
	s.mu.Unlock()
	if !stillPending {
		t.Fatal("stale approval from the canceled run resolved the new run's gate")
	}
	if err := s.ApproveIntent(secondCallID, secondDigest, true); err != nil {
		t.Fatalf("approve exact second intent: %v", err)
	}
	waitRunLeaseReleased(t, s, secondID)
	if approvedToolCalls != 1 {
		t.Fatalf("approved tool calls = %d, want 1", approvedToolCalls)
	}
	transportMu.Lock()
	defer transportMu.Unlock()
	if providerCalls != 3 {
		t.Fatalf("provider calls = %d, want exactly 3 sequential calls", providerCalls)
	}
}

func TestCancelRestoresTranscriptAcrossHistoryTrimming(t *testing.T) {
	before := make([]wireMsg, maxConversationMessages)
	for i := range before {
		before[i] = wireMsg{Role: "user", Content: fmt.Sprintf("history-%d", i)}
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Service{
		runs: map[string]*runLease{
			"run-trim": {cancel: cancel, done: make(chan struct{}), sessionID: 7, sessionBefore: cloneWireMessages(before)},
		},
		approvals: map[string]chan bool{},
		session:   cloneWireMessages(before),
		sessionID: 7,
	}
	// Model the two trims a real full-history run can trigger: Start appends its
	// user turn, then a provider/tool message arrives.
	s.session = trimConversationMessages(append(s.session, wireMsg{Role: "user", Content: "canceled prompt"}))
	if !s.appendSession("run-trim", 7, wireMsg{Role: "assistant", Content: "partial canceled output"}) {
		t.Fatal("test run could not append partial output")
	}
	s.requestCancel("run-trim")
	if !reflect.DeepEqual(s.session, before) {
		t.Fatalf("canceled transcript was not restored across trimming: got %d messages", len(s.session))
	}
	if ctx.Err() == nil {
		t.Fatal("requestCancel did not cancel the run context")
	}
}

func TestResetConversationWaitsForDeferredToolAndClearsCanceledState(t *testing.T) {
	toolEntered := make(chan struct{})
	releaseTool := make(chan struct{})
	transport := agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return agentJSONResponse(req, `{"choices":[{"message":{"tool_calls":[{"id":"provider-id","type":"function","function":{"name":"reset_deferred_tool","arguments":"{}"}}]}}]}`), nil
	})
	s := newLifecycleService(transport)
	s.toolset["reset_deferred_tool"] = tool{
		description: "deterministic reset barrier test tool",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		run: func(map[string]any) (string, error) {
			close(toolEntered)
			<-releaseTool
			return "late reset result", nil
		},
	}
	s.toolCanonical[normalizeToolName("reset_deferred_tool")] = "reset_deferred_tool"

	runID, err := s.Start("run deferred reset tool")
	if err != nil {
		t.Fatal(err)
	}
	waitAgentSignal(t, toolEntered, "reset-deferred tool")
	resetResult := make(chan error, 1)
	go func() { resetResult <- s.ResetConversation() }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.Lock()
		lease := s.runs[runID]
		cancelRequested := lease != nil && lease.cancelRequested
		s.mu.Unlock()
		if cancelRequested {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset never marked the run canceled")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case err := <-resetResult:
		t.Fatalf("ResetConversation returned before deferred tool exit: %v", err)
	default:
	}

	close(releaseTool)
	select {
	case err := <-resetResult:
		if err != nil {
			t.Fatalf("ResetConversation failed after run exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResetConversation did not return after run exit")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.runs) != 0 || len(s.session) != 0 || len(s.approvals) != 0 {
		t.Fatalf("reset state = runs %d session %d approvals %d, want all empty", len(s.runs), len(s.session), len(s.approvals))
	}
}

func TestResetConversationTimeoutRetainsRunBarrier(t *testing.T) {
	done := make(chan struct{})
	canceled := false
	s := &Service{
		runs: map[string]*runLease{
			"run-stuck": {cancel: func() { canceled = true }, done: done, sessionID: 0},
		},
		approvals:       map[string]chan bool{},
		shutdownTimeout: 5 * time.Millisecond,
	}
	err := s.ResetConversation()
	if err == nil || !strings.Contains(err.Error(), "workspace transition blocked") {
		t.Fatalf("ResetConversation error = %v, want bounded transition-blocking timeout", err)
	}
	if !canceled {
		t.Fatal("timed-out reset did not signal cancellation")
	}
	assertRunLeaseHeld(t, s, "run-stuck")
}

func TestFailedTransitionRestoresPreRunSnapshotNotBlockedToolCall(t *testing.T) {
	toolEntered := make(chan struct{})
	releaseTool := make(chan struct{})
	var callsMu sync.Mutex
	providerCalls := 0
	transport := agentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		callsMu.Lock()
		providerCalls++
		call := providerCalls
		callsMu.Unlock()
		if call == 1 {
			return agentJSONResponse(req, `{"choices":[{"message":{"tool_calls":[{"id":"provider-id","type":"function","function":{"name":"transition_timeout_tool","arguments":"{}"}}]}}]}`), nil
		}
		return agentJSONResponse(req, `{"choices":[{"message":{"content":"restart complete"}}]}`), nil
	})
	s := newLifecycleService(transport)
	s.shutdownTimeout = 20 * time.Millisecond
	s.toolset["transition_timeout_tool"] = tool{
		description: "transition timeout snapshot test",
		parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		run: func(map[string]any) (string, error) {
			close(toolEntered)
			<-releaseTool
			return "late", nil
		},
	}
	s.toolCanonical[normalizeToolName("transition_timeout_tool")] = "transition_timeout_tool"

	runID, err := s.Start("blocked old-workspace turn")
	if err != nil {
		t.Fatal(err)
	}
	waitAgentSignal(t, toolEntered, "context-free tool")
	if _, err := s.BeginWorkspaceTransition(); err == nil || !strings.Contains(err.Error(), "workspace transition blocked") {
		t.Fatalf("BeginWorkspaceTransition error = %v, want timeout", err)
	}
	s.mu.Lock()
	if len(s.session) != 0 {
		got := cloneWireMessages(s.session)
		s.mu.Unlock()
		t.Fatalf("failed transition restored partial active turn: %+v", got)
	}
	if s.transitionToken != "" || s.transitionSnapshotSet {
		s.mu.Unlock()
		t.Fatal("failed Begin left Agent admission barrier active")
	}
	s.mu.Unlock()

	close(releaseTool)
	waitRunLeaseReleased(t, s, runID)
	restartID, err := s.Start("fresh turn after failed transition")
	if err != nil {
		t.Fatal(err)
	}
	waitRunLeaseReleased(t, s, restartID)
}
