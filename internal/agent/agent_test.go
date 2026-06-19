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

func TestSelectToolNamesForPrompt(t *testing.T) {
	tests := []struct {
		name    string
		prompt  string
		want    []string
		wantNot []string
	}{
		{
			name:    "question stays read only",
			prompt:  "What is the architecture of this project?",
			want:    []string{"update_plan", "list_files", "read_file", "search_workspace"},
			wantNot: []string{"write_file", "run_command", "db_query", "csv_to_sql", "git_diff"},
		},
		{
			name:   "file output enables write tools",
			prompt: "Review the project and write the result to gemma-review.md file.",
			want:   []string{"write_file", "apply_edit", "run_command"},
		},
		{
			name: "do it resolves from recent context",
			prompt: `Recent conversation context for resolving references like "it", "that", or "do it".
assistant: I can fix the sidebar by editing AssistantPanel.tsx and global.css.

Current user request:
do it`,
			want: []string{"write_file", "apply_patch", "run_command"},
		},
		{
			name:    "orphan do it does not assume writes",
			prompt:  "do it",
			want:    []string{"list_files", "read_file"},
			wantNot: []string{"write_file", "run_command"},
		},
		{
			name:   "database task enables db tools",
			prompt: "Query the configured database and inspect tables.",
			want:   []string{"db_list_connections", "db_list_tables", "db_query"},
		},
		{
			name:   "csv conversion enables data tools",
			prompt: "Convert users.csv to SQL and clean the dump.",
			want:   []string{"infer_csv_schema", "csv_to_sql", "clean_sql_dump"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := selectToolNamesForPrompt(tt.prompt)
			for _, want := range tt.want {
				if !hasTool(got, want) {
					t.Errorf("expected %q in selected tools, got %v", want, got)
				}
			}
			for _, wantNot := range tt.wantNot {
				if hasTool(got, wantNot) {
					t.Errorf("did not expect %q in selected tools, got %v", wantNot, got)
				}
			}
		})
	}
}

func TestVisibleAssistantContentStripsInternalTail(t *testing.T) {
	report := "# Project Review: Novera\n\nUseful report body.\n\n*Report generated by Gemma Review Agent.*"
	raw := report + "\nThe user denied the write_file request for gemma-review.md. I will attempt to use append_file instead.\n\nWait, I see what happened.<channel|>\n" + report
	if got := visibleAssistantContent(raw); got != report {
		t.Fatalf("visibleAssistantContent() = %q, want %q", got, report)
	}
	if got := visibleAssistantContent("thought\n<channel|>\nsecret"); got != "" {
		t.Fatalf("pure internal content should be hidden, got %q", got)
	}
	if got := visibleAssistantContent(report + "\n<channel|>\n" + report); got != report {
		t.Fatalf("channel marker should trim duplicate tail, got %q", got)
	}
}

func hasTool(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
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
