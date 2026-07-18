// Package artifacts is a lightweight registry of generated outputs (reports,
// charts, SQL, datasets, chat answers, …). Content lives as ordinary files in
// the workspace; this package stores sidecar metadata — kind, title, lineage
// (the source files it derived from), and timestamps — in
// <workspace>/.novera/artifacts.json, and computes staleness (a source changed
// since the artifact was produced) and missing-file status on read.
package artifacts

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/paths"
)

// EventChanged is emitted (no payload) when the artifact set changes so the UI
// can refresh.
const EventChanged = "artifacts:changed"

const (
	registryVersion        = 1
	registryLastGoodSuffix = ".last-good"
	maxRegistryBytes       = 16 << 20
	maxRegistryArtifacts   = 100_000
	maxArtifactFieldBytes  = 32 << 10
	maxArtifactNoteBytes   = 1 << 20
	maxArtifactSources     = 4096
	maxArtifactTextBytes   = 2 << 20
)

var (
	ErrRegistryCorrupt          = errors.New("artifact registry is corrupt")
	ErrRegistryConcurrentChange = errors.New("artifact registry changed concurrently")
	ErrRegistryUnsafePath       = errors.New("artifact registry path uses a symbolic link or reparse point")
)

// Workspace is the slice of the workspace service this package needs.
type Workspace interface {
	Root() string
}

// Artifact is one registered output. Stale/Missing are computed on read and not
// authoritative when persisted.
type Artifact struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"` // report | chart | sql | dataset | answer | comparison | notebook | file
	Title     string   `json:"title"`
	Path      string   `json:"path"` // workspace-relative content file
	Tool      string   `json:"tool"` // what produced it (agent tool / feature)
	Note      string   `json:"note"`
	Sources   []string `json:"sources"` // workspace-relative inputs it derived from (lineage)
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
	Archived  bool     `json:"archived"`

	Stale   bool `json:"stale"`   // a source is newer than the artifact (recomputed on read)
	Missing bool `json:"missing"` // the content file no longer exists (recomputed on read)
}

type registryEnvelope struct {
	Version   int        `json:"version"`
	Artifacts []Artifact `json:"artifacts"`
}

type registryState struct {
	path      string
	artifacts []Artifact
	raw       []byte
	exists    bool
}

// RegistryCorruptionError identifies the exact evidence file and, when one is
// available and verified, a last-known-good copy. Neither file is moved,
// deleted, or restored automatically; every mutation remains blocked until a
// deliberate recovery action occurs outside this service.
type RegistryCorruptionError struct {
	Path              string
	LastKnownGoodPath string
	Cause             error
}

func (e *RegistryCorruptionError) Error() string {
	hint := "no verified last-known-good copy is available"
	if e.LastKnownGoodPath != "" {
		hint = fmt.Sprintf("verified last-known-good copy: %q", e.LastKnownGoodPath)
	}
	return fmt.Sprintf("artifact registry %q cannot be safely loaded (%v); mutations are blocked and the evidence was left unchanged; %s; inspect and restore deliberately before retrying", e.Path, e.Cause, hint)
}

func (e *RegistryCorruptionError) Unwrap() error { return e.Cause }
func (e *RegistryCorruptionError) Is(target error) bool {
	return target == ErrRegistryCorrupt
}

// Service is the bound Wails artifacts registry.
type Service struct {
	mu            sync.Mutex
	ws            Workspace
	writeRegistry func(string, []byte) error
}

// New constructs the artifacts service over a workspace.
func New(ws Workspace) *Service {
	return &Service{ws: ws, writeRegistry: atomicWriteRegistry}
}

func (s *Service) storePath() (string, error) {
	root := strings.TrimSpace(s.ws.Root())
	if root == "" {
		return "", errors.New("open a folder first")
	}
	lexical := filepath.Join(root, ".novera", "artifacts.json")
	resolved, err := paths.Resolve(root, filepath.Join(".novera", "artifacts.json"))
	if err != nil {
		return "", fmt.Errorf("resolve artifact registry path: %w", err)
	}
	if !sameRegistryPath(lexical, resolved) {
		return "", fmt.Errorf("%w: %q does not resolve to its workspace-local location", ErrRegistryUnsafePath, filepath.Dir(lexical))
	}
	if err := rejectLinkedRegistryDir(filepath.Dir(lexical)); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect artifact registry directory: %w", err)
	}
	return lexical, nil
}

func sameRegistryPath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func rejectLinkedRegistryDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: %q", ErrRegistryUnsafePath, dir)
	}
	resolved, linked, err := registryPathLinkStatus(dir)
	if err != nil {
		return err
	}
	if linked {
		return fmt.Errorf("%w: %q resolves to %q", ErrRegistryUnsafePath, dir, resolved)
	}
	return nil
}

func (s *Service) load() ([]Artifact, error) {
	state, err := s.loadState()
	if err != nil {
		return nil, err
	}
	return state.artifacts, nil
}

func (s *Service) loadState() (registryState, error) {
	p, err := s.storePath()
	if err != nil {
		return registryState{}, err
	}
	state := registryState{path: p, artifacts: []Artifact{}}
	b, err := readRegistryFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			lastGoodPath := p + registryLastGoodSuffix
			if _, backupErr := os.Stat(lastGoodPath); backupErr == nil {
				return registryState{}, &RegistryCorruptionError{
					Path:              p,
					LastKnownGoodPath: verifiedRegistryPath(lastGoodPath),
					Cause:             errors.New("primary registry is missing while a recovery copy still exists"),
				}
			} else if !os.IsNotExist(backupErr) {
				return registryState{}, &RegistryCorruptionError{
					Path:  p,
					Cause: fmt.Errorf("inspect recovery copy %q: %w", lastGoodPath, backupErr),
				}
			}
			return state, nil
		}
		return registryState{}, &RegistryCorruptionError{
			Path:              p,
			LastKnownGoodPath: verifiedRegistryPath(p + registryLastGoodSuffix),
			Cause:             err,
		}
	}
	out, err := parseRegistry(b)
	if err != nil {
		return registryState{}, &RegistryCorruptionError{
			Path:              p,
			LastKnownGoodPath: verifiedRegistryPath(p + registryLastGoodSuffix),
			Cause:             err,
		}
	}
	state.artifacts = out
	state.raw = b
	state.exists = true
	return state, nil
}

func readRegistryFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%w: registry file %q is a symbolic link or reparse point", ErrRegistryUnsafePath, path)
	}
	if resolved, linked, linkErr := registryPathLinkStatus(path); linkErr != nil {
		return nil, linkErr
	} else if linked {
		return nil, fmt.Errorf("%w: registry file %q resolves to %q", ErrRegistryUnsafePath, path, resolved)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: registry file %q is not a regular file", ErrRegistryUnsafePath, path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: opened registry file %q is not regular", ErrRegistryUnsafePath, path)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxRegistryBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRegistryBytes {
		return nil, fmt.Errorf("registry exceeds the %d-byte safety limit", maxRegistryBytes)
	}
	return b, nil
}

func decodeStrict(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple top-level JSON values")
		}
		return err
	}
	return nil
}

func parseRegistry(data []byte) ([]Artifact, error) {
	if len(data) > maxRegistryBytes {
		return nil, fmt.Errorf("registry exceeds the %d-byte safety limit", maxRegistryBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("registry JSON is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("registry is empty")
	}
	if err := rejectDuplicateJSONKeys(trimmed); err != nil {
		return nil, fmt.Errorf("validate registry JSON object keys: %w", err)
	}
	var list []Artifact
	switch trimmed[0] {
	case '[':
		// Compatibility with the unversioned array written by Novera <= 0.1.
		if err := decodeStrict(trimmed, &list); err != nil {
			return nil, fmt.Errorf("decode legacy registry: %w", err)
		}
	case '{':
		var raw struct {
			Version   *int            `json:"version"`
			Artifacts json.RawMessage `json:"artifacts"`
		}
		if err := decodeStrict(trimmed, &raw); err != nil {
			return nil, fmt.Errorf("decode registry envelope: %w", err)
		}
		if raw.Version == nil {
			return nil, errors.New("registry version is required")
		}
		if *raw.Version != registryVersion {
			return nil, fmt.Errorf("unsupported registry version %d (supported version is %d)", *raw.Version, registryVersion)
		}
		if len(raw.Artifacts) == 0 || bytes.Equal(bytes.TrimSpace(raw.Artifacts), []byte("null")) {
			return nil, errors.New("registry artifacts array is required")
		}
		if err := decodeStrict(raw.Artifacts, &list); err != nil {
			return nil, fmt.Errorf("decode registry artifacts: %w", err)
		}
	default:
		return nil, errors.New("registry must be a versioned object or legacy array")
	}
	if list == nil {
		return nil, errors.New("registry artifacts must be an array, not null")
	}
	if len(list) > maxRegistryArtifacts {
		return nil, fmt.Errorf("registry contains %d artifacts, exceeding the %d-entry safety limit", len(list), maxRegistryArtifacts)
	}
	ids := make(map[string]struct{}, len(list))
	for i := range list {
		if err := validateArtifactStrings(list[i]); err != nil {
			return nil, fmt.Errorf("artifact %d: %w", i, err)
		}
		id := strings.TrimSpace(list[i].ID)
		path := strings.TrimSpace(list[i].Path)
		if id == "" {
			return nil, fmt.Errorf("artifact %d has no id", i)
		}
		if path == "" {
			return nil, fmt.Errorf("artifact %q has no path", id)
		}
		if list[i].ID != id || list[i].Path != path {
			return nil, fmt.Errorf("artifact %q has non-canonical id or path whitespace", id)
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("artifact id %q is duplicated", id)
		}
		ids[id] = struct{}{}
	}
	return list, nil
}

func validateArtifactStrings(a Artifact) error {
	values := []struct {
		label string
		value string
		limit int
	}{
		{"id", a.ID, maxArtifactFieldBytes},
		{"kind", a.Kind, maxArtifactFieldBytes},
		{"title", a.Title, maxArtifactFieldBytes},
		{"path", a.Path, maxArtifactFieldBytes},
		{"tool", a.Tool, maxArtifactFieldBytes},
		{"note", a.Note, maxArtifactNoteBytes},
	}
	if len(a.Sources) > maxArtifactSources {
		return fmt.Errorf("source count exceeds the safety limit of %d", maxArtifactSources)
	}
	for i, source := range a.Sources {
		values = append(values, struct {
			label string
			value string
			limit int
		}{fmt.Sprintf("source %d", i), source, maxArtifactFieldBytes})
	}
	total := 0
	for _, item := range values {
		if !utf8.ValidString(item.value) {
			return fmt.Errorf("%s is not valid UTF-8", item.label)
		}
		if len(item.value) > item.limit {
			return fmt.Errorf("%s exceeds the %d-byte safety limit", item.label, item.limit)
		}
		total += len(item.value)
		if total > maxArtifactTextBytes {
			return fmt.Errorf("artifact text exceeds the aggregate %d-byte safety limit", maxArtifactTextBytes)
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := validateUniqueJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple top-level JSON values")
		}
		return err
	}
	return nil
}

func validateUniqueJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]string{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			canonical := strings.ToLower(key)
			if prior, exists := seen[canonical]; exists {
				return fmt.Errorf("duplicate JSON object key %q (conflicts with %q)", key, prior)
			}
			seen[canonical] = key
			if err := validateUniqueJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim('}') {
			if err != nil {
				return err
			}
			return errors.New("malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := validateUniqueJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil || end != json.Delim(']') {
			if err != nil {
				return err
			}
			return errors.New("malformed JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func verifiedRegistryPath(path string) string {
	b, err := readRegistryFile(path)
	if err != nil {
		return ""
	}
	if _, err := parseRegistry(b); err != nil {
		return ""
	}
	return path
}

func (s *Service) saveLocked(list []Artifact, expected registryState) error {
	if expected.path == "" {
		return errors.New("artifact registry state has no path")
	}
	// Do not persist the computed fields.
	clean := make([]Artifact, len(list))
	copy(clean, list)
	for i := range clean {
		if err := validateArtifactStrings(clean[i]); err != nil {
			return fmt.Errorf("refuse unsafe artifact %d: %w", i, err)
		}
		clean[i].Stale = false
		clean[i].Missing = false
	}
	b, err := json.MarshalIndent(registryEnvelope{Version: registryVersion, Artifacts: clean}, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxRegistryBytes {
		return fmt.Errorf("artifact registry would exceed the %d-byte safety limit", maxRegistryBytes)
	}
	if _, err := parseRegistry(b); err != nil {
		return fmt.Errorf("verify encoded artifact registry: %w", err)
	}
	if err := registryUnchanged(expected, false); err != nil {
		return err
	}
	currentPath, err := s.storePath()
	if err != nil {
		return err
	}
	if !sameRegistryPath(currentPath, expected.path) {
		return fmt.Errorf("%w: workspace registry location changed during the operation", ErrRegistryConcurrentChange)
	}
	if err := os.MkdirAll(filepath.Dir(expected.path), 0o700); err != nil {
		return err
	}
	if err := rejectLinkedRegistryDir(filepath.Dir(expected.path)); err != nil {
		return err
	}
	writer := s.writeRegistry
	if writer == nil {
		writer = atomicWriteRegistry
	}
	lastGoodBytes := expected.raw
	if !expected.exists {
		lastGoodBytes = b
	}
	lastGoodPath := expected.path + registryLastGoodSuffix
	if err := writer(lastGoodPath, lastGoodBytes); err != nil {
		return fmt.Errorf("preserve last-known-good artifact registry %q: %w", lastGoodPath, err)
	}
	// A fault or external writer may have changed the primary while the backup
	// was being published. Recheck immediately before the primary replacement.
	if err := registryUnchanged(expected, true); err != nil {
		return err
	}
	if err := writer(expected.path, b); err != nil {
		return fmt.Errorf("publish artifact registry %q: %w", expected.path, err)
	}
	return nil
}

func registryUnchanged(expected registryState, allowExpectedBackup bool) error {
	current, err := readRegistryFile(expected.path)
	if !expected.exists {
		if os.IsNotExist(err) {
			if _, backupErr := os.Stat(expected.path + registryLastGoodSuffix); backupErr == nil && !allowExpectedBackup {
				return fmt.Errorf("%w: last-known-good file appeared before initial publish", ErrRegistryConcurrentChange)
			} else if backupErr != nil && !os.IsNotExist(backupErr) {
				return fmt.Errorf("check artifact registry recovery file: %w", backupErr)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("check artifact registry before initial publish: %w", err)
		}
		return fmt.Errorf("%w: primary appeared before initial publish", ErrRegistryConcurrentChange)
	}
	if err != nil {
		return fmt.Errorf("%w: read current primary: %v", ErrRegistryConcurrentChange, err)
	}
	if !bytes.Equal(current, expected.raw) {
		return fmt.Errorf("%w: primary bytes no longer match the loaded registry", ErrRegistryConcurrentChange)
	}
	return nil
}

func atomicWriteRegistry(path string, data []byte) error {
	if _, err := parseRegistry(data); err != nil {
		return fmt.Errorf("refuse to write invalid registry data: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rejectLinkedRegistryDir(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".artifacts-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		_ = os.Remove(tmp)
	}()
	if err := f.Chmod(0o600); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	verify, err := readRegistryFile(tmp)
	if err != nil {
		return fmt.Errorf("verify staged registry: %w", err)
	}
	if !bytes.Equal(verify, data) {
		return errors.New("verify staged registry: bytes differ from encoded registry")
	}
	if _, err := parseRegistry(verify); err != nil {
		return fmt.Errorf("verify staged registry schema: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	return syncRegistryDirectory(path)
}

func syncRegistryDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		// Go's Windows directory handle support is not reliable under all host
		// security policies (notably race-instrumented test binaries). The rename
		// has already succeeded and directory Sync is best-effort on Windows.
		if runtime.GOOS == "windows" {
			return nil
		}
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

// statusOf computes (stale, missing) for an artifact against the current files.
func (s *Service) statusOf(root string, a Artifact) (stale, missing bool) {
	abs, err := paths.Resolve(root, a.Path)
	if err != nil {
		return false, true
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return false, true
	}
	artMod := fi.ModTime().UnixMilli()
	for _, src := range a.Sources {
		sabs, err := paths.Resolve(root, strings.TrimSpace(src))
		if err != nil {
			continue
		}
		sfi, err := os.Stat(sabs)
		if err != nil {
			continue // a missing source isn't "stale"; just unverifiable
		}
		if sfi.ModTime().UnixMilli() > artMod {
			return true, false
		}
	}
	return false, false
}

// --- bound API ---

// ListArtifacts returns all registered artifacts (newest first) with freshness
// computed.
func (s *Service) ListArtifacts() ([]Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return nil, err
	}
	root := s.ws.Root()
	for i := range list {
		list[i].Stale, list[i].Missing = s.statusOf(root, list[i])
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
	return list, nil
}

// GetArtifact returns one artifact by id.
func (s *Service) GetArtifact(id string) (Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadState()
	if err != nil {
		return Artifact{}, err
	}
	list := state.artifacts
	root := s.ws.Root()
	for _, a := range list {
		if a.ID == id {
			a.Stale, a.Missing = s.statusOf(root, a)
			return a, nil
		}
	}
	return Artifact{}, fmt.Errorf("artifact %q not found", id)
}

// CreateArtifact registers an existing workspace file as an artifact. If an
// active artifact already points at the same path it is updated in place rather
// than duplicated (so re-running a producer refreshes lineage/timestamp).
func (s *Service) CreateArtifact(a Artifact) (Artifact, error) {
	if err := validateArtifactStrings(a); err != nil {
		return Artifact{}, err
	}
	a.Kind = strings.TrimSpace(a.Kind)
	a.Title = strings.TrimSpace(a.Title)
	a.Path = strings.TrimSpace(a.Path)
	if a.Kind == "" {
		a.Kind = "file"
	}
	if a.Path == "" {
		return Artifact{}, errors.New("an artifact path is required")
	}
	root := s.ws.Root()
	abs, err := paths.Resolve(root, a.Path)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := os.Stat(abs); err != nil {
		return Artifact{}, fmt.Errorf("artifact file %q does not exist", a.Path)
	}
	if a.Title == "" {
		a.Title = filepath.Base(a.Path)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadState()
	if err != nil {
		return Artifact{}, err
	}
	list := state.artifacts
	now := nowMs()
	for i := range list {
		if list[i].Path == a.Path && !list[i].Archived {
			list[i].Kind = a.Kind
			list[i].Title = a.Title
			list[i].Tool = a.Tool
			list[i].Note = a.Note
			list[i].Sources = a.Sources
			list[i].UpdatedAt = now
			if err := s.saveLocked(list, state); err != nil {
				return Artifact{}, err
			}
			s.emit()
			return list[i], nil
		}
	}
	id, err := newID()
	if err != nil {
		return Artifact{}, err
	}
	a.ID = id
	a.CreatedAt = now
	a.UpdatedAt = now
	a.Archived = false
	list = append(list, a)
	if err := s.saveLocked(list, state); err != nil {
		return Artifact{}, err
	}
	s.emit()
	return a, nil
}

// SetArchived archives or restores an artifact (its content file is untouched).
func (s *Service) SetArchived(id string, archived bool) error {
	return s.mutate(id, func(a *Artifact) { a.Archived = archived; a.UpdatedAt = nowMs() })
}

// DeleteArtifact removes the registry entry. The content file is intentionally
// left on disk — deleting user files is the file tree's job, not this registry's.
func (s *Service) DeleteArtifact(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadState()
	if err != nil {
		return err
	}
	list := state.artifacts
	kept := list[:0]
	found := false
	for _, a := range list {
		if a.ID == id {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("artifact %q not found", id)
	}
	if err := s.saveLocked(kept, state); err != nil {
		return err
	}
	s.emit()
	return nil
}

func (s *Service) mutate(id string, fn func(*Artifact)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadState()
	if err != nil {
		return err
	}
	list := state.artifacts
	for i := range list {
		if list[i].ID == id {
			fn(&list[i])
			if err := s.saveLocked(list, state); err != nil {
				return err
			}
			s.emit()
			return nil
		}
	}
	return fmt.Errorf("artifact %q not found", id)
}

func (s *Service) emit() {
	if app := application.Get(); app != nil {
		app.Event.Emit(EventChanged, nil)
	}
}

func newID() (string, error) {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func nowMs() int64 { return time.Now().UnixMilli() }
