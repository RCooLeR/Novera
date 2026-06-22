package cachepath

import (
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/settings"
)

func TestSourcePathUsesQuarryHomeCacheDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, home)
	source := filepath.Join(t.TempDir(), "data", "dump.sql")

	got, err := SourcePath("indexes", source, ".json")
	if err != nil {
		t.Fatal(err)
	}
	wantPrefix := filepath.Join(home, "cache", "indexes") + string(filepath.Separator)
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("cache path = %q, want prefix %q", got, wantPrefix)
	}
	if filepath.Ext(got) != ".json" {
		t.Fatalf("cache path = %q, want .json extension", got)
	}

	again, err := SourcePath("indexes", source, "json")
	if err != nil {
		t.Fatal(err)
	}
	if again != got {
		t.Fatalf("cache path is not stable: %q then %q", got, again)
	}
}

func TestSourcePathDistinguishesSameBasenameSources(t *testing.T) {
	t.Setenv(settings.ConfigDirEnv, t.TempDir())
	first := filepath.Join(t.TempDir(), "a", "dump.sql")
	second := filepath.Join(t.TempDir(), "b", "dump.sql")

	firstPath, err := SourcePath("indexes", first, ".json")
	if err != nil {
		t.Fatal(err)
	}
	secondPath, err := SourcePath("indexes", second, ".json")
	if err != nil {
		t.Fatal(err)
	}
	if firstPath == secondPath {
		t.Fatalf("cache paths collided for same basename sources: %q", firstPath)
	}
}

func TestSourcePathRejectsUnsafeKind(t *testing.T) {
	t.Setenv(settings.ConfigDirEnv, t.TempDir())
	if _, err := SourcePath(filepath.Join("bad", "kind"), "source.sql", ".json"); err == nil {
		t.Fatal("expected unsafe kind to fail")
	}
}
