package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novera/internal/workspace"
)

func openApprovalWorkspace(t *testing.T) (*workspace.Service, string) {
	t.Helper()
	root := t.TempDir()
	ws := workspace.New()
	if _, err := ws.Open(root); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	return ws, root
}

func TestApprovalIntentCanonicalizesAndBindsEveryArgumentByte(t *testing.T) {
	ws, _ := openApprovalWorkspace(t)
	s := &Service{ws: ws}
	expires := time.Unix(1_900_000_000, 123).UTC()

	first, err := s.buildApprovalIntent("run-1", "call-1", "write_file", `{"content":"prefix-dangerous-tail","path":"new.txt"}`, expires)
	if err != nil {
		t.Fatalf("build first intent: %v", err)
	}
	reordered, err := s.buildApprovalIntent("run-1", "call-1", "write_file", `{"path":"new.txt","content":"prefix-dangerous-tail"}`, expires)
	if err != nil {
		t.Fatalf("build reordered intent: %v", err)
	}
	if first.digest != reordered.digest || first.canonical != reordered.canonical {
		t.Fatal("equivalent JSON objects did not produce one canonical intent")
	}
	if !strings.Contains(first.canonical, "dangerous-tail") {
		t.Fatal("canonical intent clipped the operation tail")
	}
	if got := approvalIntentDigest(first.canonical); got != first.digest {
		t.Fatalf("digest %q does not cover the emitted canonical bytes; want %q", first.digest, got)
	}

	mutated, err := s.buildApprovalIntent("run-1", "call-1", "write_file", `{"path":"new.txt","content":"prefix-different-tail"}`, expires)
	if err != nil {
		t.Fatalf("build mutated intent: %v", err)
	}
	if mutated.digest == first.digest {
		t.Fatal("mutating a hidden-tail byte did not change the approval digest")
	}
}

func TestApprovalIntentRejectsOversizedArguments(t *testing.T) {
	s := &Service{}
	raw := `{"command":"` + strings.Repeat("x", maxApprovalArgsBytes) + `"}`
	if _, err := s.buildApprovalIntent("run", "call", "run_command", raw, time.Now().Add(time.Minute)); err == nil {
		t.Fatal("oversized approval arguments were accepted")
	}
}

func TestApprovalIntentRejectsExcessiveResourceFanoutBeforeCapture(t *testing.T) {
	ws, _ := openApprovalWorkspace(t)
	s := &Service{ws: ws}
	sources := make([]string, maxApprovalResources+1)
	for i := range sources {
		sources[i] = filepath.ToSlash(filepath.Join("missing", fmt.Sprintf("source-%04d", i)))
	}
	raw, err := json.Marshal(map[string]any{"path": "artifact.txt", "sources": sources})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.buildApprovalIntent("run", "call", "create_artifact", string(raw), time.Now().Add(time.Minute)); err == nil || !strings.Contains(err.Error(), "resources") {
		t.Fatalf("resource-fanout error = %v, want bounded rejection", err)
	}
}

func TestApprovalIntentIsOneUseAndDigestBound(t *testing.T) {
	s := &Service{
		approvals:           map[string]chan bool{},
		approvalDigests:     map[string]string{},
		approvalExpirations: map[string]time.Time{},
	}
	intent := approvalIntentState{
		digest:    strings.Repeat("a", approvalDigestHexBytes),
		expiresAt: time.Now().Add(time.Minute),
	}
	result := make(chan gateDecision, 1)
	go func() {
		result <- s.awaitIntentGate(context.Background(), "call-1", intent, func() {})
	}()
	waitRegistered(t, s, "call-1")

	if err := s.ApproveIntent("call-1", strings.Repeat("b", approvalDigestHexBytes), true); err == nil {
		t.Fatal("mismatched digest resolved the approval")
	}
	s.mu.Lock()
	_, stillPending := s.approvals["call-1"]
	s.mu.Unlock()
	if !stillPending {
		t.Fatal("a mismatched digest consumed the legitimate pending approval")
	}
	if err := s.ApproveIntent("call-1", intent.digest, true); err != nil {
		t.Fatalf("approve exact intent: %v", err)
	}
	select {
	case got := <-result:
		if got != gateApproved {
			t.Fatalf("gate = %s, want approved", got)
		}
	case <-time.After(time.Second):
		t.Fatal("exact approval did not resolve the gate")
	}
	if err := s.ApproveIntent("call-1", intent.digest, true); err == nil {
		t.Fatal("replayed approval was accepted")
	}
}

func TestApprovalIntentRejectsWorkspaceAndResourceDrift(t *testing.T) {
	ws, root := openApprovalWorkspace(t)
	path := filepath.Join(root, "tracked.txt")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Service{ws: ws}
	intent, err := s.buildApprovalIntent("run", "call", "write_file", `{"path":"tracked.txt","content":"after"}`, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("build intent: %v", err)
	}
	if err := os.WriteFile(path, []byte("changed externally"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.validateApprovalIntent(intent); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("resource drift validation error = %v", err)
	}

	intent, err = s.buildApprovalIntent("run", "call-2", "run_command", `{"command":"go test ./..."}`, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("build workspace intent: %v", err)
	}
	ws.Close()
	if err := s.validateApprovalIntent(intent); err == nil || !strings.Contains(err.Error(), "workspace changed") {
		t.Fatalf("workspace drift validation error = %v", err)
	}
}

func TestCreateArtifactRequiresExactApproval(t *testing.T) {
	s := &Service{}
	tools, _ := s.buildTools()
	if !tools["create_artifact"].mutating {
		t.Fatal("create_artifact can mutate persistent metadata without approval")
	}
}
