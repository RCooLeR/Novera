package buildinfo

import (
	"strings"
	"testing"
)

func TestCurrentUsesDefaults(t *testing.T) {
	info := Current()
	if info.ApplicationName != ApplicationName {
		t.Fatalf("ApplicationName = %q, want %q", info.ApplicationName, ApplicationName)
	}
	if info.Version == "" || info.Commit == "" || info.BuildDate == "" {
		t.Fatalf("Current() returned empty metadata: %+v", info)
	}
	if !strings.Contains(info.OneLine(), ApplicationName) {
		t.Fatalf("OneLine() = %q, want application name", info.OneLine())
	}
}

func TestCurrentSanitizesLdflagValues(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate := Version, Commit, BuildDate
	t.Cleanup(func() {
		Version, Commit, BuildDate = oldVersion, oldCommit, oldBuildDate
	})

	Version = "  1.2.3  "
	Commit = "\nabc123\t"
	BuildDate = "  2026-05-28T10:11:12Z  "

	info := Current()
	if info.Version != "1.2.3" {
		t.Fatalf("Version = %q, want sanitized value", info.Version)
	}
	if info.Commit != "abc123" {
		t.Fatalf("Commit = %q, want sanitized value", info.Commit)
	}
	if info.BuildDate != "2026-05-28T10:11:12Z" {
		t.Fatalf("BuildDate = %q, want sanitized value", info.BuildDate)
	}
}
