package cachepath

import (
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/settings"
)

func TestSourcePathUsesNoveraBigFileCacheDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, home)
	t.Setenv(settings.LegacyConfigDirEnv, "")
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
	t.Setenv(settings.LegacyConfigDirEnv, "")
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
	t.Setenv(settings.LegacyConfigDirEnv, "")
	if _, err := SourcePath(filepath.Join("bad", "kind"), "source.sql", ".json"); err == nil {
		t.Fatal("expected unsafe kind to fail")
	}
}

func TestSourcePathRejectsUnsafeExtension(t *testing.T) {
	t.Setenv(settings.ConfigDirEnv, t.TempDir())
	t.Setenv(settings.LegacyConfigDirEnv, "")
	for _, extension := range []string{"../escape", `..\escape`, "."} {
		if _, err := SourcePath("indexes", "source.sql", extension); err == nil {
			t.Fatalf("extension %q should be rejected", extension)
		}
	}
}

func TestLegacySourcePathIsReadOnlyMigrationInput(t *testing.T) {
	current := filepath.Join(t.TempDir(), "current")
	legacy := filepath.Join(t.TempDir(), "legacy")
	t.Setenv(settings.ConfigDirEnv, current)
	t.Setenv(settings.LegacyConfigDirEnv, legacy)
	source := filepath.Join(t.TempDir(), "dump.sql")

	canonical, err := SourcePath("indexes", source, ".novera-index.json")
	if err != nil {
		t.Fatal(err)
	}
	old, available, err := LegacySourcePath("indexes", source, ".quarry-index.json")
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("legacy source path should be available for explicit migration input")
	}
	if !strings.HasPrefix(canonical, filepath.Join(current, "cache", "indexes")+string(filepath.Separator)) {
		t.Fatalf("canonical cache path = %q, want current root", canonical)
	}
	if !strings.HasPrefix(old, filepath.Join(legacy, "cache", "indexes")+string(filepath.Separator)) {
		t.Fatalf("legacy cache path = %q, want legacy root", old)
	}
}
