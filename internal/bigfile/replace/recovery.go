package replace

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"novera/internal/bigfile/logger"
	"novera/internal/persistfile"
)

// RecoveryState describes a manifest-backed replace artifact that Novera can inspect.
type RecoveryState struct {
	ManifestPath string
	Manifest     Manifest
	InPlace      bool
	SourceExists bool
	OutputExists bool
	TempExists   bool
	BackupExists bool
}

const minRecoveryManifestRetention = 24 * time.Hour
const maxRecoveryManifestBytes = 1 << 20

// LoadManifest reads a replace manifest from disk.
func LoadManifest(path string) (Manifest, error) {
	data, err := persistfile.Read(path, maxRecoveryManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&manifest); err != nil {
		return Manifest{}, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("recovery manifest contains multiple JSON values")
		}
		return Manifest{}, err
	}
	if err := validateRecoveryManifestPaths(path, manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateRecoveryManifestPaths(manifestPath string, manifest Manifest) error {
	if strings.TrimSpace(manifestPath) == "" {
		return errors.New("recovery manifest path is required")
	}
	if strings.TrimSpace(manifest.Source) == "" {
		return errors.New("recovery manifest source path is required")
	}
	if manifest.Operation == "plain-replace-in-place" {
		legacy := manifest.Source + ".quarry.inplace.manifest.json"
		prefix := filepath.Clean(manifest.Source) + ".quarry.inplace."
		cleanManifest := filepath.Clean(manifestPath)
		if !sameRecoveryPath(cleanManifest, legacy) &&
			!(strings.HasPrefix(cleanManifest, prefix) && strings.HasSuffix(cleanManifest, ".manifest.json")) {
			return errors.New("in-place recovery manifest path is not bound to its source")
		}
		if manifest.Output != "" && !sameRecoveryPath(manifest.Output, manifest.Source) {
			return errors.New("in-place recovery output is not its source")
		}
		return nil
	}

	const suffix = ".quarry.manifest.json"
	if !strings.HasSuffix(manifestPath, suffix) {
		return errors.New("replace recovery manifest has an invalid filename")
	}
	expectedOutput := strings.TrimSuffix(manifestPath, suffix)
	if strings.TrimSpace(manifest.Output) == "" || !sameRecoveryPath(manifest.Output, expectedOutput) {
		return errors.New("replace recovery output path is not bound to the manifest filename")
	}
	if manifest.TempOutput != "" && !sameRecoveryPath(manifest.TempOutput, manifest.Output+".quarry.tmp") {
		return errors.New("replace recovery temp path is not bound to its output")
	}
	return nil
}

func sameRecoveryPath(a, b string) bool {
	a = filepath.Clean(a)
	b = filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// InspectRecoveryManifest loads a manifest and reports which related files still exist.
func InspectRecoveryManifest(path string) (RecoveryState, error) {
	manifest, err := LoadManifest(path)
	if err != nil {
		return RecoveryState{}, err
	}
	state := RecoveryState{
		ManifestPath: path,
		Manifest:     manifest,
		InPlace:      manifest.Operation == "plain-replace-in-place",
	}
	if _, err := statPath(manifest.Source); err == nil {
		state.SourceExists = true
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return RecoveryState{}, err
	}
	if manifest.Output != "" {
		if _, err := statPath(manifest.Output); err == nil {
			state.OutputExists = true
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RecoveryState{}, err
		}
	}
	if manifest.TempOutput != "" {
		if _, err := statPath(manifest.TempOutput); err == nil {
			state.TempExists = true
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RecoveryState{}, err
		}
	}
	if manifest.SwapRequested || manifest.Backup != "" || manifest.BackupPlanned != "" {
		backupPath := resolveRecoveryBackupPath(manifest)
		if backupPath != "" {
			if manifest.Backup == "" {
				state.Manifest.Backup = backupPath
			}
		}
		if _, err := statPath(backupPath); err == nil {
			state.BackupExists = true
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return RecoveryState{}, err
		}
	}
	return state, nil
}

// FindRecoveryStates returns replace manifests associated with sourcePath, newest first.
func FindRecoveryStates(sourcePath string) ([]RecoveryState, error) {
	dir := filepath.Dir(sourcePath)
	paths := map[string]struct{}{}

	legacyInPlacePath := sourcePath + ".quarry.inplace.manifest.json"
	paths[legacyInPlacePath] = struct{}{}

	sourceBase := filepath.Base(sourcePath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	inPlacePrefix := sourceBase + ".quarry.inplace."
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, inPlacePrefix) && strings.HasSuffix(name, ".manifest.json") {
			paths[filepath.Join(dir, name)] = struct{}{}
		}
	}

	pattern := filepath.Join(dir, "*.quarry.manifest.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		paths[path] = struct{}{}
	}

	states := make([]RecoveryState, 0, len(paths))
	for path := range paths {
		state, err := InspectRecoveryManifest(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			// Ignore malformed or unrelated recovery files instead of breaking the UI.
			logger.Warn("replace.recovery", "skipping recovery manifest", map[string]string{
				"path":  path,
				"error": err.Error(),
			})
			continue
		}
		if state.Manifest.Source != sourcePath {
			continue
		}
		states = append(states, state)
	}

	sort.Slice(states, func(i, j int) bool {
		if states[i].Manifest.StartedAt.Equal(states[j].Manifest.StartedAt) {
			return states[i].ManifestPath < states[j].ManifestPath
		}
		return states[i].Manifest.StartedAt.After(states[j].Manifest.StartedAt)
	})
	return states, nil
}

// DeleteRecoveryTemp removes a partial temp output while keeping the manifest for audit/history.
func DeleteRecoveryTemp(state RecoveryState) error {
	refreshed, err := InspectRecoveryManifest(state.ManifestPath)
	if err != nil {
		return err
	}
	if refreshed.Manifest.TempOutput == "" {
		return errors.New("manifest has no temp output")
	}
	if err := removePath(refreshed.Manifest.TempOutput); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// CleanupCompletedRecoveryManifests removes completed manifest files older than
// retention while keeping recent audit records and all incomplete recovery
// states. Retention is clamped to at least 24 hours to avoid surprise cleanup.
func CleanupCompletedRecoveryManifests(states []RecoveryState, retention time.Duration, now time.Time) (int, error) {
	if retention < minRecoveryManifestRetention {
		retention = minRecoveryManifestRetention
	}
	removed := 0
	for _, state := range states {
		if state.ManifestPath == "" || state.Manifest.Status != "complete" || state.Manifest.CompletedAt == nil {
			continue
		}
		if now.Sub(*state.Manifest.CompletedAt) < retention {
			continue
		}
		if err := removePath(state.ManifestPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// RecoveryOpenPath returns the most useful artifact path to inspect from a recovery manifest.
func RecoveryOpenPath(state RecoveryState) (string, string, bool) {
	switch {
	case state.TempExists:
		return state.Manifest.TempOutput, "partial temp output", true
	case state.OutputExists && state.Manifest.Output != "" && state.Manifest.Output != state.Manifest.Source:
		return state.Manifest.Output, "output file", true
	case state.BackupExists:
		return state.Manifest.Backup, "backup file", true
	default:
		return "", "", false
	}
}

// CanResumeRecovery reports whether a recovery manifest can be safely resumed.
func CanResumeRecovery(state RecoveryState) (bool, string) {
	manifest := state.Manifest
	if state.InPlace {
		return false, "in-place patch recovery cannot be resumed"
	}
	if manifest.Status == "complete" {
		return false, "manifest is already complete"
	}
	if manifest.Output == "" {
		return false, "manifest has no output path"
	}
	if manifest.Phase == "" {
		return false, "manifest phase is unknown"
	}

	switch manifest.Phase {
	case "ready_to_finalize":
		if manifest.TempOutput == "" {
			return false, "manifest has no temp output"
		}
		if !state.TempExists {
			return false, "temp output is missing"
		}
		if state.OutputExists {
			return false, "output file already exists"
		}
		if manifest.SwapRequested {
			if !state.SourceExists {
				return false, "source file is missing"
			}
			backupPath := resolveRecoveryBackupPath(manifest)
			if _, err := statPath(backupPath); err == nil {
				return false, "backup file already exists"
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, "backup path check failed: " + err.Error()
			}
		}
		return true, ""
	case "output_written":
		if !state.OutputExists {
			return false, "output file is missing"
		}
		if !manifest.SwapRequested {
			return true, ""
		}
		backupPath := resolveRecoveryBackupPath(manifest)
		if state.SourceExists {
			if _, err := statPath(backupPath); err == nil {
				return false, "backup file already exists"
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return false, "backup path check failed: " + err.Error()
			}
			return true, ""
		}
		if !state.BackupExists {
			return false, "source file is missing"
		}
		return true, ""
	case "swapped":
		if !manifest.SwapRequested {
			return false, "manifest phase is swapped without swap request"
		}
		if !state.SourceExists {
			return false, "source file is missing"
		}
		return true, ""
	default:
		if manifest.Phase == "" {
			return false, "manifest phase is unknown"
		}
		return false, "manifest phase is " + manifest.Phase
	}
}

// ResumeRecovery finalizes a safe recovery state by promoting temp output and optional source swap.
func ResumeRecovery(state RecoveryState) (RecoveryState, error) {
	refreshed, err := InspectRecoveryManifest(state.ManifestPath)
	if err != nil {
		return RecoveryState{}, err
	}
	canResume, reason := CanResumeRecovery(refreshed)
	if !canResume {
		return refreshed, errors.New("recovery is not resumable: " + reason)
	}

	manifest := refreshed.Manifest
	switch manifest.Phase {
	case "ready_to_finalize":
		if err := renamePath(manifest.TempOutput, manifest.Output); err != nil {
			return recoveryFailure(refreshed.ManifestPath, manifest, err)
		}
		if err := writeManifestPhaseOrFail(refreshed.ManifestPath, &manifest, "output_written"); err != nil {
			state, inspectErr := InspectRecoveryManifest(refreshed.ManifestPath)
			if inspectErr != nil {
				return RecoveryState{}, err
			}
			return state, err
		}
	case "output_written":
		// Continue from already-promoted output.
	case "swapped":
		// Continue directly to final manifest close-out.
	default:
		return refreshed, errors.New("recovery is not resumable: manifest phase is " + manifest.Phase)
	}

	if manifest.SwapRequested && manifest.Phase == "output_written" {
		backupPath := resolveRecoveryBackupPath(manifest)
		sameBackup, err := samePath(manifest.Source, backupPath)
		if err != nil {
			return recoveryFailure(refreshed.ManifestPath, manifest, err)
		}
		if sameBackup {
			return recoveryFailure(refreshed.ManifestPath, manifest, errors.New("backup path must be different from source path"))
		}

		if _, err := statPath(manifest.Source); err == nil {
			if _, err := statPath(backupPath); err == nil {
				return recoveryFailure(refreshed.ManifestPath, manifest, errors.New("backup file already exists"))
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return recoveryFailure(refreshed.ManifestPath, manifest, fmt.Errorf("backup path check failed: %w", err))
			}
			if manifest.SourceModTime != 0 {
				before := sourceSnapshot{
					size: manifest.SourceSize,
				}
				before.modTime = time.Unix(0, manifest.SourceModTime)
				if err := verifySourceUnchanged(manifest.Source, before); err != nil {
					return recoveryFailure(refreshed.ManifestPath, manifest, err)
				}
			}
			if err := swapOutputIntoSource(manifest.Source, manifest.Output, backupPath); err != nil {
				return recoveryFailure(refreshed.ManifestPath, manifest, err)
			}
		} else if errors.Is(err, os.ErrNotExist) {
			if _, backupErr := statPath(backupPath); backupErr != nil {
				if errors.Is(backupErr, os.ErrNotExist) {
					return recoveryFailure(refreshed.ManifestPath, manifest, errors.New("source file is missing"))
				}
				return recoveryFailure(refreshed.ManifestPath, manifest, fmt.Errorf("backup path check failed: %w", backupErr))
			}
			if err := applyBackupModeToOutput(manifest.Output, backupPath); err != nil {
				return recoveryFailure(refreshed.ManifestPath, manifest, err)
			}
			if err := renamePath(manifest.Output, manifest.Source); err != nil {
				return recoveryFailure(refreshed.ManifestPath, manifest, err)
			}
		} else {
			return recoveryFailure(refreshed.ManifestPath, manifest, err)
		}

		manifest.Backup = backupPath
		manifest.Swapped = true
		if err := writeManifestPhaseOrFail(refreshed.ManifestPath, &manifest, "swapped"); err != nil {
			state, inspectErr := InspectRecoveryManifest(refreshed.ManifestPath)
			if inspectErr != nil {
				return RecoveryState{}, err
			}
			return state, err
		}
	}

	now := time.Now().UTC()
	manifest.Status = "complete"
	manifest.Error = ""
	manifest.Phase = "complete"
	manifest.BytesProcessed = manifest.SourceSize
	manifest.CompletedAt = &now
	if err := writeManifest(refreshed.ManifestPath, manifest, false); err != nil {
		return RecoveryState{}, fmt.Errorf("finalize recovery manifest: %w", err)
	}

	return InspectRecoveryManifest(refreshed.ManifestPath)
}

func recoveryFailure(path string, manifest Manifest, failure error) (RecoveryState, error) {
	manifest.Status = "failed"
	manifest.Error = failure.Error()
	_ = writeManifest(path, manifest, false)
	state, err := InspectRecoveryManifest(path)
	if err != nil {
		return RecoveryState{}, failure
	}
	return state, failure
}

func resolveRecoveryBackupPath(manifest Manifest) string {
	backupPath := manifest.BackupPlanned
	if backupPath == "" {
		backupPath = manifest.Backup
	}
	if backupPath == "" {
		backupPath = manifest.Source + ".quarry.bak"
	}
	return backupPath
}
