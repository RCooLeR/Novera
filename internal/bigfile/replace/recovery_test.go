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

func TestDeleteRecoveryTempRemovesPartialOutput(t *testing.T) {
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
		Phase:      "processing",
		Status:     "failed",
	}, true); err != nil {
		t.Fatal(err)
	}
	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := DeleteRecoveryTemp(state); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp path should be removed, stat err = %v", err)
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
	for _, path := range []string{oldPath, recentPath, runningPath} {
		if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := CleanupCompletedRecoveryManifests([]RecoveryState{
		{ManifestPath: oldPath, Manifest: Manifest{Status: "complete", CompletedAt: &oldCompletedAt}},
		{ManifestPath: recentPath, Manifest: Manifest{Status: "complete", CompletedAt: &recentCompletedAt}},
		{ManifestPath: runningPath, Manifest: Manifest{Status: "running"}},
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
	for _, path := range []string{recentPath, runningPath} {
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
	}
	path, label, ok := RecoveryOpenPath(state)
	if !ok {
		t.Fatal("expected open path")
	}
	if path != "temp.sql" || label != "partial temp output" {
		t.Fatalf("got %q / %q", path, label)
	}
}

func TestCanResumeRecoveryRequiresReadyPhase(t *testing.T) {
	state := RecoveryState{
		Manifest: Manifest{
			Operation:  "plain-replace",
			Source:     "source.sql",
			Output:     "output.sql",
			TempOutput: "output.sql.quarry.tmp",
			Phase:      "processing",
			Status:     "failed",
		},
		SourceExists: true,
		TempExists:   true,
	}

	canResume, reason := CanResumeRecovery(state)
	if canResume {
		t.Fatal("expected recovery state to be non-resumable")
	}
	if reason != "manifest phase is processing" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestResumeRecoveryFinalizesOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("source body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("new output"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		TempOutput:     tempPath,
		Phase:          "ready_to_finalize",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     int64(len("source body")),
		BytesProcessed: int64(len("source body")),
		Status:         "failed",
		Error:          "interrupted before finalize",
	}, true); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeRecovery(state)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", resumed.Manifest.Status)
	}
	if resumed.Manifest.Phase != "complete" {
		t.Fatalf("manifest phase = %q", resumed.Manifest.Phase)
	}
	if !resumed.OutputExists {
		t.Fatal("expected output to exist after resume")
	}
	if resumed.TempExists {
		t.Fatal("did not expect temp output after resume")
	}
	gotOutput, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutput) != "new output" {
		t.Fatalf("output = %q", string(gotOutput))
	}
	gotSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSource) != "source body" {
		t.Fatalf("source = %q", string(gotSource))
	}
}

func TestResumeRecoveryWithSwapOriginal(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		TempOutput:     tempPath,
		Phase:          "ready_to_finalize",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     srcInfo.Size(),
		SourceModTime:  srcInfo.ModTime().UnixNano(),
		BytesProcessed: srcInfo.Size(),
		BackupPlanned:  backupPath,
		SwapRequested:  true,
		Status:         "failed",
		Error:          "interrupted before swap",
	}, true); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeRecovery(state)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", resumed.Manifest.Status)
	}
	if resumed.Manifest.Phase != "complete" {
		t.Fatalf("manifest phase = %q", resumed.Manifest.Phase)
	}
	if !resumed.Manifest.Swapped {
		t.Fatal("expected swapped=true")
	}
	if resumed.Manifest.Backup != backupPath {
		t.Fatalf("backup = %q, want %q", resumed.Manifest.Backup, backupPath)
	}

	gotSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSource) != "new source" {
		t.Fatalf("source = %q", string(gotSource))
	}
	gotBackup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBackup) != "old source" {
		t.Fatalf("backup = %q", string(gotBackup))
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("expected intermediate output to be moved into source, stat err = %v", err)
	}
}

func TestCanResumeRecoveryRejectsWhenOutputAlreadyExists(t *testing.T) {
	state := RecoveryState{
		Manifest: Manifest{
			Operation:  "plain-replace",
			Source:     "source.sql",
			Output:     "output.sql",
			TempOutput: "output.sql.quarry.tmp",
			Phase:      "ready_to_finalize",
			Status:     "failed",
		},
		TempExists:   true,
		OutputExists: true,
	}

	canResume, reason := CanResumeRecovery(state)
	if canResume {
		t.Fatal("expected non-resumable state")
	}
	if reason != "output file already exists" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestCanResumeRecoveryAllowsOutputWrittenWithoutSwap(t *testing.T) {
	state := RecoveryState{
		Manifest: Manifest{
			Operation: "plain-replace",
			Source:    "source.sql",
			Output:    "output.sql",
			Phase:     "output_written",
			Status:    "failed",
		},
		OutputExists: true,
	}

	canResume, reason := CanResumeRecovery(state)
	if !canResume {
		t.Fatalf("expected resumable state, reason = %q", reason)
	}
}

func TestCanResumeRecoveryAllowsOutputWrittenSwapWithMissingSourceAndBackup(t *testing.T) {
	state := RecoveryState{
		Manifest: Manifest{
			Operation:     "plain-replace",
			Source:        "source.sql",
			Output:        "output.sql",
			Phase:         "output_written",
			Status:        "failed",
			SwapRequested: true,
		},
		OutputExists: true,
		BackupExists: true,
	}

	canResume, reason := CanResumeRecovery(state)
	if !canResume {
		t.Fatalf("expected resumable state, reason = %q", reason)
	}
}

func TestResumeRecoverySwapPrecheckSourceModifiedKeepsOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		TempOutput:     tempPath,
		Phase:          "ready_to_finalize",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     srcInfo.Size(),
		SourceModTime:  srcInfo.ModTime().UnixNano(),
		BytesProcessed: srcInfo.Size(),
		BackupPlanned:  backupPath,
		SwapRequested:  true,
		Status:         "failed",
		Error:          "interrupted before swap",
	}, true); err != nil {
		t.Fatal(err)
	}

	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(sourcePath, []byte("source changed externally"), 0o600); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResumeRecovery(state)
	if !errors.Is(err, ErrSourceModifiedDuringOperation) {
		t.Fatalf("err = %v, want %v", err, ErrSourceModifiedDuringOperation)
	}
	if _, err := os.Stat(tempPath); !os.IsNotExist(err) {
		t.Fatalf("temp should be promoted before swap checks, stat err = %v", err)
	}
	if _, err := os.Stat(outputPath); err != nil {
		t.Fatalf("output should remain for retry/audit, stat err = %v", err)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup should not exist, stat err = %v", err)
	}

	manifest := readManifest(t, manifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Error != ErrSourceModifiedDuringOperation.Error() {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestResumeRecoveryOutputWrittenNoSwapCompletes(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("source body"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("new output"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		Phase:          "output_written",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     int64(len("source body")),
		BytesProcessed: int64(len("source body")),
		Status:         "failed",
		Error:          "interrupted before completion",
	}, true); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeRecovery(state)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", resumed.Manifest.Status)
	}
	if resumed.Manifest.Phase != "complete" {
		t.Fatalf("manifest phase = %q", resumed.Manifest.Phase)
	}

	gotOutput, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutput) != "new output" {
		t.Fatalf("output = %q", gotOutput)
	}
}

func TestResumeRecoveryOutputWrittenSwapSourceMissingUsesBackup(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(sourcePath, backupPath); err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		Phase:          "output_written",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     int64(len("old source")),
		BytesProcessed: int64(len("old source")),
		SwapRequested:  true,
		BackupPlanned:  backupPath,
		Status:         "failed",
		Error:          "interrupted mid-swap",
	}, true); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeRecovery(state)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", resumed.Manifest.Status)
	}
	if !resumed.Manifest.Swapped {
		t.Fatal("expected swapped=true")
	}
	if resumed.Manifest.Backup != backupPath {
		t.Fatalf("backup = %q, want %q", resumed.Manifest.Backup, backupPath)
	}

	gotSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSource) != "new source" {
		t.Fatalf("source = %q", gotSource)
	}
	gotBackup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBackup) != "old source" {
		t.Fatalf("backup = %q", gotBackup)
	}
	if _, err := os.Stat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("expected output to be moved into source, stat err = %v", err)
	}
}

func TestResumeRecoverySwappedPhaseCompletesManifest(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(backupPath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		Phase:          "swapped",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     int64(len("old source")),
		BytesProcessed: int64(len("old source")),
		SwapRequested:  true,
		Swapped:        true,
		Backup:         backupPath,
		Status:         "failed",
		Error:          "interrupted before manifest close-out",
	}, true); err != nil {
		t.Fatal(err)
	}

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := ResumeRecovery(state)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Manifest.Status != "complete" {
		t.Fatalf("manifest status = %q", resumed.Manifest.Status)
	}
	if resumed.Manifest.Phase != "complete" {
		t.Fatalf("manifest phase = %q", resumed.Manifest.Phase)
	}
}

func TestResumeRecoverySwapFailureRollsBackSource(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		TempOutput:     tempPath,
		Phase:          "ready_to_finalize",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     srcInfo.Size(),
		SourceModTime:  srcInfo.ModTime().UnixNano(),
		BytesProcessed: srcInfo.Size(),
		BackupPlanned:  backupPath,
		SwapRequested:  true,
		Status:         "failed",
		Error:          "interrupted before swap",
	}, true); err != nil {
		t.Fatal(err)
	}

	lockedErr := errors.New("destination is locked")
	restoreRename := renamePath
	renamePath = func(oldPath string, newPath string) error {
		if filepath.Clean(oldPath) == filepath.Clean(outputPath) && filepath.Clean(newPath) == filepath.Clean(sourcePath) {
			return lockedErr
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		renamePath = restoreRename
	})

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResumeRecovery(state)
	if !errors.Is(err, lockedErr) {
		t.Fatalf("err = %v, want %v", err, lockedErr)
	}

	gotSource, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotSource) != "old source" {
		t.Fatalf("source = %q", gotSource)
	}
	if _, err := os.Stat(backupPath); !os.IsNotExist(err) {
		t.Fatalf("backup should be rolled back, stat err = %v", err)
	}
	gotOutput, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotOutput) != "new source" {
		t.Fatalf("output = %q", gotOutput)
	}

	manifest := readManifest(t, manifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Error != lockedErr.Error() {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}

func TestResumeRecoverySwapRollbackFailureIncludesRollbackError(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	outputPath := filepath.Join(dir, "output.sql")
	tempPath := outputPath + ".quarry.tmp"
	backupPath := filepath.Join(dir, "source.sql.quarry.bak")
	manifestPath := outputPath + ".quarry.manifest.json"
	if err := os.WriteFile(sourcePath, []byte("old source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tempPath, []byte("new source"), 0o600); err != nil {
		t.Fatal(err)
	}
	srcInfo, err := os.Stat(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	if err := writeManifest(manifestPath, Manifest{
		Operation:      "plain-replace",
		Source:         sourcePath,
		Output:         outputPath,
		TempOutput:     tempPath,
		Phase:          "ready_to_finalize",
		StartedAt:      time.Now().Add(-2 * time.Minute).UTC(),
		SourceSize:     srcInfo.Size(),
		SourceModTime:  srcInfo.ModTime().UnixNano(),
		BytesProcessed: srcInfo.Size(),
		BackupPlanned:  backupPath,
		SwapRequested:  true,
		Status:         "failed",
		Error:          "interrupted before swap",
	}, true); err != nil {
		t.Fatal(err)
	}

	swapErr := errors.New("destination is locked")
	rollbackErr := errors.New("rollback failed")
	restoreRename := renamePath
	renamePath = func(oldPath string, newPath string) error {
		cleanOld := filepath.Clean(oldPath)
		cleanNew := filepath.Clean(newPath)
		if cleanOld == filepath.Clean(outputPath) && cleanNew == filepath.Clean(sourcePath) {
			return swapErr
		}
		if cleanOld == filepath.Clean(backupPath) && cleanNew == filepath.Clean(sourcePath) {
			return rollbackErr
		}
		return os.Rename(oldPath, newPath)
	}
	t.Cleanup(func() {
		renamePath = restoreRename
	})

	state, err := InspectRecoveryManifest(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ResumeRecovery(state)
	if err == nil {
		t.Fatal("expected resume failure")
	}
	if !strings.Contains(err.Error(), swapErr.Error()) || !strings.Contains(err.Error(), rollbackErr.Error()) {
		t.Fatalf("unexpected error: %v", err)
	}

	manifest := readManifest(t, manifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if !strings.Contains(manifest.Error, swapErr.Error()) || !strings.Contains(manifest.Error, rollbackErr.Error()) {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}
