package agent

import (
	"path/filepath"
	"testing"
)

func TestAuditLogRoundTrip(t *testing.T) {
	a := &auditLog{path: filepath.Join(t.TempDir(), "audit.jsonl")}

	// Empty log returns an empty (non-nil) slice, never an error.
	if got := a.list(0); len(got) != 0 {
		t.Fatalf("empty log should return 0 entries, got %d", len(got))
	}

	a.record(AuditEntry{RunID: "r1", Tool: "read_file", Summary: "a.go", Decision: "auto", Status: "ok"})
	a.record(AuditEntry{RunID: "r1", Tool: "write_file", Summary: "b.go", Decision: "approved", Status: "ok"})
	a.record(AuditEntry{RunID: "r1", Tool: "run_command", Summary: "rm -rf x", Decision: "denied", Status: "denied"})

	all := a.list(0)
	if len(all) != 3 {
		t.Fatalf("want 3 entries, got %d", len(all))
	}
	// Newest first.
	if all[0].Tool != "run_command" || all[2].Tool != "read_file" {
		t.Errorf("entries not newest-first: %s ... %s", all[0].Tool, all[2].Tool)
	}
	// Timestamps auto-filled.
	if all[0].Time == "" {
		t.Error("record should auto-fill Time")
	}
	// Limit honored.
	if got := a.list(1); len(got) != 1 || got[0].Tool != "run_command" {
		t.Errorf("limit=1 should return newest only, got %+v", got)
	}
}

func TestAuditSummaryExcludesContent(t *testing.T) {
	// write_file content must never be summarized into the (plaintext) audit file.
	got := auditSummary(map[string]any{"path": "secrets.env", "content": "API_KEY=supersecret"})
	if got != "secrets.env" {
		t.Errorf("summary should be the path, not content; got %q", got)
	}
}
