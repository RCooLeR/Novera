package agent

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// waitRegistered spins until awaitGate has registered callID (bounded), so the
// test's Approve/Cancel races the gate deterministically without flaking.
func waitRegistered(t *testing.T, s *Service, callID string) {
	t.Helper()
	for i := 0; i < 2000; i++ {
		s.mu.Lock()
		_, ok := s.approvals[callID]
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("gate %q never registered", callID)
}

func TestWindowMessages(t *testing.T) {
	asst := func(id string) wireMsg {
		return wireMsg{Role: "assistant", ToolCalls: []wireToolCall{{ID: id, Type: "function", Function: wireFunc{Name: "read_file", Arguments: "{}"}}}}
	}
	tool := func(id string) wireMsg {
		return wireMsg{Role: "tool", ToolCallID: id, Name: "read_file", Content: "ok"}
	}
	msgs := []wireMsg{
		{Role: "system", Content: "SYS"},
		{Role: "user", Content: "TASK"},
		asst("a1"), tool("a1"),
		asst("a2"), tool("a2"),
		asst("a3"), tool("a3"),
		asst("a4"), tool("a4"),
	}

	if got := windowMessages(msgs, 10); len(got) != len(msgs) {
		t.Errorf("keepGroups >= groups should be unchanged: got len %d, want %d", len(got), len(msgs))
	}

	got := windowMessages(msgs, 2)
	if len(got) >= len(msgs) {
		t.Fatalf("expected a trimmed transcript, got len %d", len(got))
	}
	if got[0].Role != "system" || got[0].Content != "SYS" {
		t.Errorf("first message must be the original system prompt, got %+v", got[0])
	}
	if got[1].Role != "user" || got[1].Content != "TASK" {
		t.Errorf("second message must be the original user task, got %+v", got[1])
	}
	// Invariant: no tool message may appear without its assistant tool_calls
	// earlier in the output (OpenAI-compatible APIs reject orphaned tool msgs).
	declared := map[string]bool{}
	for _, m := range got {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				declared[tc.ID] = true
			}
		}
		if m.Role == "tool" && !declared[m.ToolCallID] {
			t.Errorf("orphaned tool message for call %q", m.ToolCallID)
		}
	}
	hasA1, hasA4 := false, false
	for _, m := range got {
		switch m.ToolCallID {
		case "a1":
			hasA1 = true
		case "a4":
			hasA4 = true
		}
	}
	if hasA1 {
		t.Error("oldest round a1 should have been dropped")
	}
	if !hasA4 {
		t.Error("most recent round a4 should be kept")
	}
}

func TestAwaitContinueApprove(t *testing.T) {
	for _, want := range []bool{true, false} {
		s := &Service{approvals: map[string]chan bool{}}
		const runID = "run-test"
		callID := fmt.Sprintf("%s-continue-%d", runID, 50)
		res := make(chan bool, 1)
		go func() { res <- s.awaitContinue(context.Background(), runID, 50) }()
		waitRegistered(t, s, callID)
		s.Approve(callID, want)
		select {
		case got := <-res:
			if got != want {
				t.Errorf("awaitContinue = %v, want %v", got, want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("awaitContinue did not return after Approve")
		}
		// Entry must be cleaned up.
		s.mu.Lock()
		_, leaked := s.approvals[callID]
		s.mu.Unlock()
		if leaked {
			t.Errorf("approval entry leaked after resolve")
		}
	}
}

func TestAwaitContinueCancel(t *testing.T) {
	s := &Service{approvals: map[string]chan bool{}}
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan bool, 1)
	go func() { res <- s.awaitContinue(ctx, "run-c", 50) }()
	waitRegistered(t, s, "run-c-continue-50")
	cancel()
	select {
	case got := <-res:
		if got {
			t.Error("awaitContinue should return false on cancel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("awaitContinue did not return on cancel")
	}
}

func TestParsePlan(t *testing.T) {
	got := parsePlan(map[string]any{"steps": []any{
		map[string]any{"title": "Read code", "status": "done"},
		map[string]any{"title": "Edit files", "status": "in-progress"},
		map[string]any{"step": "Verify", "status": "pending"},
		"Bare string step",
		map[string]any{"title": "   ", "status": "done"}, // empty title → skipped
	}})
	want := []planStep{
		{Title: "Read code", Status: "done"},
		{Title: "Edit files", Status: "in_progress"},
		{Title: "Verify", Status: "todo"},
		{Title: "Bare string step", Status: "todo"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d steps, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("step %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if parsePlan(map[string]any{}) != nil {
		t.Errorf("missing steps should yield nil")
	}
}

func TestIsAgentNoiseFile(t *testing.T) {
	noise := []string{
		"package-lock.json", "frontend/yarn.lock", "pnpm-lock.yaml", "go.sum",
		"Cargo.lock", "poetry.lock", "a/b/app.min.js", "styles/site.min.css",
		"dist/vendor.bundle.js", "x.css.map", "api/foo.pb.go", "gen/schema_pb2.py",
		"src/types.generated.ts", "flake.lock", "Gemfile.lock", "bun.lockb",
	}
	for _, f := range noise {
		if !isAgentNoiseFile(f) {
			t.Errorf("expected %q to be classified as noise", f)
		}
	}
	keep := []string{
		"main.go", "src/store.ts", "README.md", "go.mod", "Cargo.toml",
		"package.json", "app.css", "internal/agent/agent.go", "composer.json",
		"pubspec.yaml", "schema.proto",
	}
	for _, f := range keep {
		if isAgentNoiseFile(f) {
			t.Errorf("expected %q to be kept (not noise)", f)
		}
	}
}
