package replace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"novera/internal/bigfile/logger"
	"novera/internal/jsonsafe"
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
	validated    bool
}

const minRecoveryManifestRetention = 24 * time.Hour
const maxRecoveryManifestBytes = 1 << 20

// ErrRecoveryMutationDisabled keeps recovery inspection-only until every
// mutation can be authorized against securely opened, manifest-bound files.
var ErrRecoveryMutationDisabled = errors.New(
	"automatic replace recovery mutation is disabled; inspect the preserved artifacts and choose an explicit safe output operation",
)

// LoadManifest reads a replace manifest from disk.
func LoadManifest(path string) (Manifest, error) {
	data, err := persistfile.Read(path, maxRecoveryManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := jsonsafe.Unmarshal(data, &manifest); err != nil {
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
		validated:    true,
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

// DeleteRecoveryTemp is disabled so recovery remains inspection-only.
func DeleteRecoveryTemp(RecoveryState) error {
	return ErrRecoveryMutationDisabled
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
		if !state.validated || state.ManifestPath == "" || state.Manifest.Status != "complete" || state.Manifest.CompletedAt == nil {
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
	if !state.validated {
		return "", "", false
	}
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

// CanResumeRecovery reports that automatic recovery mutation is unavailable.
func CanResumeRecovery(RecoveryState) (bool, string) {
	return false, ErrRecoveryMutationDisabled.Error()
}

// ResumeRecovery is disabled so recovery remains inspection-only.
func ResumeRecovery(state RecoveryState) (RecoveryState, error) {
	return state, ErrRecoveryMutationDisabled
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
