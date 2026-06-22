package logger

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/settings"
)

func TestWriteCreatesJSONLUnderQuarryHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, home)

	if err := Write("warn", "replace.recovery", "skipped manifest", map[string]string{
		"path":  "output.sql.quarry.manifest.json",
		"error": "invalid character",
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(home, logFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	var entry Entry
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Level != "warn" || entry.Component != "replace.recovery" || entry.Message != "skipped manifest" {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Fields["path"] == "" || entry.Fields["error"] == "" {
		t.Fatalf("fields = %#v", entry.Fields)
	}
}

func TestWriteRotatesExistingLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, home)
	oldMax := maxBytes
	maxBytes = 8
	defer func() {
		maxBytes = oldMax
	}()

	path := filepath.Join(home, logFileName)
	if err := os.WriteFile(path, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write("info", "test", "after rotation", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, rotatedLogFileName)); err != nil {
		t.Fatalf("rotated log missing: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "after rotation") {
		t.Fatalf("active log = %q, want new entry", data)
	}
}

func TestReadRecentReturnsNewestEntriesFirstAndSkipsMalformedLines(t *testing.T) {
	home := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, home)

	if err := Write("info", "first", "oldest", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, logFileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{not-json}\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := Write("warn", "second", "newer", nil); err != nil {
		t.Fatal(err)
	}
	if err := Write("info", "third", "newest", nil); err != nil {
		t.Fatal(err)
	}

	entries, err := ReadRecent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2: %#v", len(entries), entries)
	}
	if entries[0].Component != "third" || entries[1].Component != "second" {
		t.Fatalf("entries = %#v, want newest valid entries first", entries)
	}
}

func TestReadRecentMissingLogReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, home)

	entries, err := ReadRecent(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %#v, want none for missing log", entries)
	}
}
