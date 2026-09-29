package replace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novera/internal/bigfile/settings"
)

func TestFindRecoveryStatesFiltersBySourceAndSortsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	if err := os.WriteFile(sourcePath, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	fileManifestPath := outputPath + ".quarry.manifest.json"
	if err := writeManifest(fileManifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		TempOutput:     tempPath,
		StartedAt:      time.Unix(1716780000, 0).UTC(),
		SourceSize:     10,
		BytesProcessed: 5,
		Matches:        1,
		Status:         "canceled",
	}, true); err != nil {
		t.Fatal(err)
	}

	inPlaceManifestPath := sourcePath + ".quarry.inplace.20260528T120000.000000000Z.manifest.json"
	if err := writeManifest(inPlaceManifestPath, Manifest{
		Operation:      "plain-replace-in-place",
		Source:         sourcePath,
		Output:         sourcePath,
		StartedAt:      time.Unix(1716790000, 0).UTC(),
		SourceSize:     10,
		BytesProcessed: 3,
		Matches:        1,
		Status:         "failed",
		Error:          "canceled mid-run",
	}, true); err != nil {
		t.Fatal(err)
	}

	otherManifestPath := filepath.Join(dir, "other.sql.quarry.manifest.json")
	if err := writeManifest(otherManifestPath, Manifest{
		Operation:  "plain-replace",
		Source:     filepath.Join(dir, "other.sql"),
		Output:     filepath.Join(dir, "other.out.sql"),
		StartedAt:  time.Unix(1716800000, 0).UTC(),
		SourceSize: 10,
		Status:     "running",
	}, true); err != nil {
		t.Fatal(err)
	}

	states, err := FindRecoveryStates(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("states = %#v, want 2 entries", states)
	}
	if states[0].ManifestPath != inPlaceManifestPath {
		t.Fatalf("first manifest = %q, want %q", states[0].ManifestPath, inPlaceManifestPath)
	}
	if !states[0].InPlace {
		t.Fatal("expected in-place recovery state")
	}
	if !states[0].SourceExists {
		t.Fatal("expected sourceExists for in-place manifest")
	}
	if states[0].TempExists {
		t.Fatal("did not expect tempExists for in-place manifest")
	}
	if states[1].ManifestPath != fileManifestPath {
		t.Fatalf("second manifest = %q, want %q", states[1].ManifestPath, fileManifestPath)
	}
	if states[1].InPlace {
		t.Fatal("did not expect file replace recovery state to be marked in-place")
	}
	if !states[1].TempExists {
		t.Fatal("expected tempExists for canceled file replace")
	}
}

func TestFindRecoveryStatesKeepsLegacyInPlaceManifest(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	if err := os.WriteFile(sourcePath, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	legacyManifestPath := sourcePath + ".quarry.inplace.manifest.json"
	if err := writeManifest(legacyManifestPath, Manifest{
		Operation:  "plain-replace-in-place",
		Source:     sourcePath,
		Output:     sourcePath,
		StartedAt:  time.Unix(1716790000, 0).UTC(),
		SourceSize: 10,
		Status:     "failed",
	}, true); err != nil {
		t.Fatal(err)
	}

	states, err := FindRecoveryStates(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("states = %#v, want 1 entry", states)
	}
	if states[0].ManifestPath != legacyManifestPath {
		t.Fatalf("manifest path = %q, want %q", states[0].ManifestPath, legacyManifestPath)
	}
	if !states[0].InPlace {
		t.Fatal("expected legacy in-place recovery state")
	}
}

func TestFindRecoveryStatesLogsMalformedManifestAndContinues(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(settings.ConfigDirEnv, filepath.Join(dir, "novera-bigfile-home"))
	sourcePath := filepath.Join(dir, "source.sql")
	if err := os.WriteFile(sourcePath, []byte("select 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	outputPath := filepath.Join(dir, "output.sql")
	validManifestPath := outputPath + ".quarry.manifest.json"
	if err := writeManifest(validManifestPath, Manifest{
		Operation:  "plain-replace",
		Source:     sourcePath,
		Output:     outputPath,
		StartedAt:  time.Unix(1716780000, 0).UTC(),
		SourceSize: 10,
		Status:     "failed",
	}, true); err != nil {
		t.Fatal(err)
	}
	badManifestPath := filepath.Join(dir, "bad.sql.quarry.manifest.json")
	if err := os.WriteFile(badManifestPath, []byte("{not-json"), 0o600); err != nil {
		t.Fatal(err)
	}

	states, err := FindRecoveryStates(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ManifestPath != validManifestPath {
		t.Fatalf("states = %#v, want only valid manifest", states)
	}

	logPath := filepath.Join(dir, "novera-bigfile-home", "novera-bigfile.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logText := string(data)
	if !strings.Contains(logText, "skipping recovery manifest") || !strings.Contains(logText, filepath.Base(badManifestPath)) {
		t.Fatalf("log = %q, want malformed manifest warning", logText)
	}
}

func TestRecoveryMutationAPIsFailClosedAndPreserveArtifacts(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeManifest(manifestPath, Manifest{
		Operation:  "plain-replace",
		Source:     sourcePath,
		Output:     outputPath,
		TempOutput: tempPath,
		Phase:      "ready_to_finalize",
		Status:     "failed",
	}, true); err != nil {
		t.Fatal(err)
	}
	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifestBefore, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	sourceBefore, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	tempBefore, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatal(err)
	}

	if canResume, reason := CanResumeRecovery(state); canResume || reason != ErrRecoveryMutationDisabled.Error() {
		t.Fatalf("CanResumeRecovery = %v, %q", canResume, reason)
	}
	returned, err := ResumeRecovery(state)
	if !errors.Is(err, ErrRecoveryMutationDisabled) {
		t.Fatalf("ResumeRecovery error = %v", err)
	}
	if returned.ManifestPath != state.ManifestPath {
		t.Fatalf("ResumeRecovery returned manifest %q, want %q", returned.ManifestPath, state.ManifestPath)
	}
	if err := DeleteRecoveryTemp(state); !errors.Is(err, ErrRecoveryMutationDisabled) {
		t.Fatalf("DeleteRecoveryTemp error = %v", err)
	}

	for path, want := range map[string][]byte{
		manifestPath: manifestBefore,
		sourcePath:   sourceBefore,
		tempPath:     tempBefore,
	} {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read preserved artifact %s: %v", path, err)
		}
		if string(got) != string(want) {
			t.Fatalf("artifact %s changed: got %q, want %q", path, got, want)
		}
	}
	if _, err := os.Stat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("output path was touched: %v", err)
	}
}

func TestInspectRecoveryManifestRejectsUnboundOutputAndTempPaths(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "safe.sql.quarry.manifest.json")
	victim := filepath.Join(dir, "victim.sql")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := fmt.Sprintf(`{"operation":"plain-replace","source":%q,"output":%q,"tempOutput":%q,"status":"failed"}`,
		filepath.Join(dir, "source.sql"), victim, victim)
	if err := os.WriteFile(manifestPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectRecoveryManifest(manifestPath); err == nil || !strings.Contains(err.Error(), "not bound") {
		t.Fatalf("inspect error = %v, want path-binding rejection", err)
	}
	if got, err := os.ReadFile(victim); err != nil || string(got) != "do not touch" {
		t.Fatalf("victim changed: %q, %v", got, err)
	}
}

func TestCleanupCompletedRecoveryManifestsRemovesOnlyOldCompleteFiles(t *testing.T) {
	dir := t.TempDir()
	now := time.Unix(2000, 0).UTC()
	oldCompletedAt := now.Add(-72 * time.Hour)
	recentCompletedAt := now.Add(-time.Hour)
	oldPath := filepath.Join(dir, "old.quarry.manifest.json")
	recentPath := filepath.Join(dir, "recent.quarry.manifest.json")
	runningPath := filepath.Join(dir, "running.quarry.manifest.json")
	forgedPath := filepath.Join(dir, "forged.quarry.manifest.json")
	for _, path := range []string{oldPath, recentPath, runningPath, forgedPath} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := CleanupCompletedRecoveryManifests([]RecoveryState{
		{ManifestPath: oldPath, Manifest: Manifest{Status: "complete", CompletedAt: &oldCompletedAt}, validated: true},
		{ManifestPath: recentPath, Manifest: Manifest{Status: "complete", CompletedAt: &recentCompletedAt}, validated: true},
		{ManifestPath: runningPath, Manifest: Manifest{Status: "running"}, validated: true},
		{ManifestPath: forgedPath, Manifest: Manifest{Status: "complete", CompletedAt: &oldCompletedAt}},
	}, 48*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(oldPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old manifest stat err = %v, want removed", err)
	}
	for _, path := range []string{recentPath, runningPath, forgedPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s should remain: %v", path, err)
		}
	}
}

func TestInspectRecoveryManifestDetectsBackupAndOpenTargets(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("output"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	manifestPath := outputPath + ".quarry.manifest.json"
	if err := writeManifest(manifestPath, Manifest{
		Operation:  "plain-replace",
		Source:     sourcePath,
		Output:     outputPath,
		Backup:     backupPath,
		StartedAt:  time.Unix(1716781000, 0).UTC(),
		SourceSize: 6,
		Status:     "complete",
		Swapped:    true,
	}, true); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !state.OutputExists {
		t.Fatal("expected outputExists")
	}
	if !state.BackupExists {
		t.Fatal("expected backupExists")
	}
	path, label, ok := RecoveryOpenPath(state)
	if !ok {
		t.Fatal("expected recovery open path")
	}
	if path != outputPath {
		t.Fatalf("path = %q, want %q", path, outputPath)
	}
	if label != "output file" {
		t.Fatalf("label = %q, want output file", label)
	}
}

func TestRecoveryOpenPathPrefersTempOutput(t *testing.T) {
	state := RecoveryState{
		Manifest: Manifest{
			TempOutput: "temp.sql",
			Output:     "output.sql",
			Backup:     "backup.sql",
		},
		TempExists:   true,
		OutputExists: true,
		BackupExists: true,
		validated:    true,
	}
	path, label, ok := RecoveryOpenPath(state)
	if !ok {
		t.Fatal("expected open path")
	}
	if path != "temp.sql" || label != "partial temp output" {
		t.Fatalf("got %q / %q", path, label)
	}
}

func TestRecoveryRejectsFabricatedStatePaths(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "source.sql"),
		filepath.Join(dir, "output.sql"),
		filepath.Join(dir, "output.sql.quarry.tmp"),
		filepath.Join(dir, "source.sql.quarry.bak"),
		filepath.Join(dir, "output.sql.quarry.manifest.json"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("preserve "+filepath.Base(path)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := RecoveryState{
		ManifestPath: paths[4],
		Manifest: Manifest{
			Source:     paths[0],
			Output:     paths[1],
			TempOutput: paths[2],
			Backup:     paths[3],
			Phase:      "ready_to_finalize",
			Status:     "failed",
		},
		SourceExists: true,
		OutputExists: true,
		TempExists:   true,
		BackupExists: true,
	}

	if canResume, reason := CanResumeRecovery(state); canResume || reason != ErrRecoveryMutationDisabled.Error() {
		t.Fatalf("CanResumeRecovery = %v, %q", canResume, reason)
	}
	returned, err := ResumeRecovery(state)
	if !errors.Is(err, ErrRecoveryMutationDisabled) {
		t.Fatalf("ResumeRecovery error = %v", err)
	}
	if returned.ManifestPath != state.ManifestPath {
		t.Fatalf("ResumeRecovery returned manifest %q, want %q", returned.ManifestPath, state.ManifestPath)
	}
	if err := DeleteRecoveryTemp(state); !errors.Is(err, ErrRecoveryMutationDisabled) {
		t.Fatalf("DeleteRecoveryTemp error = %v", err)
	}
	if path, label, ok := RecoveryOpenPath(state); ok || path != "" || label != "" {
		t.Fatalf("RecoveryOpenPath accepted fabricated state: %q, %q, %v", path, label, ok)
	}
	for _, path := range paths {
		want := "preserve " + filepath.Base(path)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s changed: got %q, want %q", path, got, want)
		}
	}
}
