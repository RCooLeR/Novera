package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/settings"
)

func TestAuditLogRoundTrip(t *testing.T) {
	a := &auditLog{path: filepath.Join(t.TempDir(), "audit.jsonl")}

	// Empty log returns an empty (non-nil) slice, never an error.
	if got := a.list(0); len(got) != 0 {
		t.Fatalf("empty log should return 0 entries, got %d", len(got))
	}

	a.record(auditAction{RunID: "r1", Tool: "read_file", Args: map[string]any{"path": "a.go"}, Decision: "auto", Status: "ok", ResultBytes: 12})
	a.record(auditAction{RunID: "r1", Tool: "write_file", Args: map[string]any{"path": "b.go"}, Decision: "approved", Status: "ok", ResultBytes: 24})
	a.record(auditAction{RunID: "r1", Tool: "run_command", Args: map[string]any{"command": "rm -rf x"}, Decision: "denied", Status: "denied"})

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
	if strings.Contains(all[0].Summary, "rm -rf x") || !strings.Contains(all[0].Summary, "command omitted (8 bytes)") {
		t.Errorf("command summary must contain only bounded metadata, got %q", all[0].Summary)
	}
	if strings.Contains(all[1].Detail, "b.go") || !strings.Contains(all[1].Detail, "24 bytes") {
		t.Errorf("result detail must contain only outcome metadata, got %q", all[1].Detail)
	}
}

func TestAuditLogListDoesNotSurfaceLegacyFreeFormPayloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	legacy := `{"time":"2026-08-17T00:00:00Z","tool":"read_file","summary":"legacy-secret","detail":"legacy-result-secret"}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	entries := (&auditLog{path: path}).list(0)
	if len(entries) != 1 {
		t.Fatalf("legacy entries = %d, want 1", len(entries))
	}
	if strings.Contains(entries[0].Summary, "legacy-secret") || strings.Contains(entries[0].Detail, "legacy-result-secret") {
		t.Fatalf("legacy payload surfaced through audit API: %+v", entries[0])
	}
}

func TestAuditPrivacyMigrationRemovesLegacyPayloadFromDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	legacy := `{"time":"2026-08-17T00:00:00Z","tool":"read_file","summary":"legacy-secret","detail":"legacy-result-secret","unknown":"unknown-secret"}` + "\n"
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	a := &auditLog{path: path}
	a.mu.Lock()
	err := a.sanitizeAndCompactLocked(false, retainAuditBytes)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"legacy-secret", "legacy-result-secret", "unknown-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("physical audit migration retained %q: %s", secret, raw)
		}
	}
	var entry AuditEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatal(err)
	}
	if entry.FormatVersion != auditFormatVersion || entry.Summary != "legacy audit payload omitted" || entry.Detail != "legacy audit payload omitted" {
		t.Fatalf("migrated entry = %+v", entry)
	}
}

func TestAuditCompactionRetainsNewestBoundedWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	var raw []byte
	for i := 0; i < 20; i++ {
		entry := AuditEntry{
			FormatVersion: auditFormatVersion,
			Time:          "2026-08-17T00:00:00Z",
			CallID:        fmt.Sprintf("call-%02d", i),
			Tool:          "read_file",
			Summary:       strings.Repeat("x", 96),
			Status:        "ok",
		}
		line, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, append(line, '\n')...)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	const budget = 700
	a := &auditLog{path: path}
	a.mu.Lock()
	err := a.sanitizeAndCompactLocked(true, budget)
	a.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	compacted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(compacted) > budget {
		t.Fatalf("compacted audit is %d bytes, budget %d", len(compacted), budget)
	}
	entries := a.list(0)
	if len(entries) == 0 || entries[0].CallID != "call-19" {
		t.Fatalf("newest audit entry was not retained: %+v", entries)
	}
	for _, entry := range entries {
		if entry.CallID == "call-00" {
			t.Fatalf("oldest audit entry survived bounded compaction: %+v", entries)
		}
	}
}

func TestAuditSummaryExcludesSensitivePayloads(t *testing.T) {
	// write_file content must never be summarized into the (plaintext) audit file.
	got := auditSummary(map[string]any{"path": "secrets.env", "content": "API_KEY=supersecret"})
	if got != "secrets.env" {
		t.Errorf("summary should be the path, not content; got %q", got)
	}

	got = auditSummary(map[string]any{
		"url":     "https://user:url-secret@api.example.test/status/url-secret?token=url-secret#url-secret",
		"headers": map[string]any{"Authorization": "Bearer secret"},
		"body":    "token=secret",
	})
	if got != "https://api.example.test" {
		t.Errorf("summary should be the URL origin, not credentials/path/headers/body; got %q", got)
	}
	if strings.Contains(got, "url-secret") {
		t.Errorf("URL summary leaked credential or path data: %q", got)
	}

	for _, tc := range []struct {
		name  string
		key   string
		label string
	}{
		{name: "command", key: "command", label: "command omitted"},
		{name: "SQL", key: "sql", label: "SQL omitted"},
		{name: "query", key: "query", label: "query omitted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const secret = "audit-payload-secret"
			got := auditSummary(map[string]any{tc.key: secret})
			if strings.Contains(got, secret) || !strings.Contains(got, tc.label) || !strings.Contains(got, "20 bytes") {
				t.Fatalf("sensitive summary = %q, want redacted byte metadata", got)
			}
		})
	}
}

func TestDispatchAuditJSONLExcludesArgumentsResultsAndErrors(t *testing.T) {
	const (
		argumentSecret = "argument-secret-do-not-persist"
		sqlSecret      = "sql-secret-do-not-persist"
		querySecret    = "query-secret-do-not-persist"
		resultSecret   = "result-secret-do-not-persist"
		errorSecret    = "error-secret-do-not-persist"
	)
	path := filepath.Join(t.TempDir(), "agent-audit.jsonl")
	const toolName = "audit_secret_test_tool"
	s := &Service{
		runs: map[string]*runLease{
			"run-audit": {done: make(chan struct{})},
		},
		approvals: map[string]chan bool{},
		audit:     &auditLog{path: path},
		toolset: map[string]tool{
			toolName: {
				description: "audit redaction regression tool",
				parameters:  map[string]any{"type": "object"},
				run: func(map[string]any) (string, error) {
					return resultSecret, errors.New(errorSecret)
				},
			},
		},
		toolCanonical: map[string]string{normalizeToolName(toolName): toolName},
	}
	result := s.dispatch(context.Background(), "run-audit", wireToolCall{
		ID:   "run-audit-tool-1",
		Type: "function",
		Function: wireFunc{
			Name:      toolName,
			Arguments: `{"command":"` + argumentSecret + `"}`,
		},
	}, map[string]bool{toolName: true}, []string{toolName}, settings.Agent{}.Normalized())
	if !strings.Contains(result.output, resultSecret) || !strings.Contains(result.output, errorSecret) {
		t.Fatalf("dispatch result did not exercise sensitive output/error path: %q", result.output)
	}
	s.audit.record(auditAction{
		RunID: "run-audit", Tool: "db_query", Args: map[string]any{"connectionId": "db-1", "sql": sqlSecret},
		Decision: "approved", Status: "ok", ResultBytes: 50,
	})
	s.audit.record(auditAction{
		RunID: "run-audit", Tool: "search_workspace", Args: map[string]any{"query": querySecret},
		Decision: "auto", Status: "ok", ResultBytes: 75,
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{argumentSecret, sqlSecret, querySecret, resultSecret, errorSecret} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("audit JSONL leaked %q: %s", secret, raw)
		}
	}
	entries := s.audit.list(0)
	if len(entries) != 3 || entries[2].Status != "error" {
		t.Fatalf("audit entries = %+v, want two metadata entries and one error outcome", entries)
	}
	if !strings.Contains(entries[2].Summary, "command omitted") || !strings.Contains(entries[2].Detail, "diagnostic text omitted") {
		t.Fatalf("dispatch audit entry lost useful redacted metadata: %+v", entries[2])
	}
	if !strings.Contains(entries[1].Summary, "db-1; SQL omitted") || !strings.Contains(entries[0].Summary, "query omitted") {
		t.Fatalf("payload audit entries lost useful redacted metadata: %+v", entries)
	}
}
