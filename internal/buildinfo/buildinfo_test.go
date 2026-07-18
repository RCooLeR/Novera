package buildinfo

import "testing"

func TestCurrentUsesNoveraIdentityAndNonEmptyMetadata(t *testing.T) {
	info := Current()
	if info.ProductName != ProductName {
		t.Fatalf("ProductName = %q, want %q", info.ProductName, ProductName)
	}
	if info.Version == "" || info.Commit == "" || info.BuildDate == "" || info.Channel == "" || info.GoVersion == "" || info.WailsVersion == "" {
		t.Fatalf("Current returned empty metadata: %+v", info)
	}
}

func TestCurrentSanitizesInjectedValues(t *testing.T) {
	oldVersion, oldCommit, oldBuildDate, oldChannel := Version, Commit, BuildDate, Channel
	t.Cleanup(func() {
		Version, Commit, BuildDate, Channel = oldVersion, oldCommit, oldBuildDate, oldChannel
	})

	Version = "  1.2.3  "
	Commit = "\nabc123\t"
	BuildDate = "  2026-05-28T10:11:12Z  "
	Channel = "  stable  "

	info := Current()
	if info.Version != "1.2.3" || info.Commit != "abc123" || info.BuildDate != "2026-05-28T10:11:12Z" || info.Channel != "stable" {
		t.Fatalf("Current did not sanitize injected values: %+v", info)
	}
}

func TestCleanBoundsAndNormalizesValues(t *testing.T) {
	if got := clean("  alpha\n beta  "); got != "alpha beta" {
		t.Fatalf("clean whitespace = %q", got)
	}
	if got := clean(""); got != unknownBuildValue {
		t.Fatalf("clean empty = %q", got)
	}
	long := string(make([]byte, maxBuildValueLen+50))
	if got := clean(long); len(got) != maxBuildValueLen {
		t.Fatalf("bounded length = %d, want %d", len(got), maxBuildValueLen)
	}
}
