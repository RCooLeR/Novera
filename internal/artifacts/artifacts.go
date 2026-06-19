// Package artifacts is a lightweight registry of generated outputs (reports,
// charts, SQL, datasets, chat answers, …). Content lives as ordinary files in
// the workspace; this package stores sidecar metadata — kind, title, lineage
// (the source files it derived from), and timestamps — in
// <workspace>/.novera/artifacts.json, and computes staleness (a source changed
// since the artifact was produced) and missing-file status on read.
package artifacts

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/paths"
)

// EventChanged is emitted (no payload) when the artifact set changes so the UI
// can refresh.
const EventChanged = "artifacts:changed"

// Workspace is the slice of the workspace service this package needs.
type Workspace interface {
	Root() string
}

// Artifact is one registered output. Stale/Missing are computed on read and not
// authoritative when persisted.
type Artifact struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`  // report | chart | sql | dataset | answer | comparison | notebook | file
	Title     string   `json:"title"`
	Path      string   `json:"path"`  // workspace-relative content file
	Tool      string   `json:"tool"`  // what produced it (agent tool / feature)
	Note      string   `json:"note"`
	Sources   []string `json:"sources"` // workspace-relative inputs it derived from (lineage)
	CreatedAt int64    `json:"createdAt"`
	UpdatedAt int64    `json:"updatedAt"`
	Archived  bool     `json:"archived"`

	Stale   bool `json:"stale"`   // a source is newer than the artifact (recomputed on read)
	Missing bool `json:"missing"` // the content file no longer exists (recomputed on read)
}

// Service is the bound Wails artifacts registry.
type Service struct {
	mu sync.Mutex
	ws Workspace
}

// New constructs the artifacts service over a workspace.
func New(ws Workspace) *Service { return &Service{ws: ws} }

func (s *Service) storePath() (string, error) {
	root := strings.TrimSpace(s.ws.Root())
	if root == "" {
		return "", errors.New("open a folder first")
	}
	return filepath.Join(root, ".novera", "artifacts.json"), nil
}

func (s *Service) load() ([]Artifact, error) {
	p, err := s.storePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return []Artifact{}, nil
		}
		return nil, err
	}
	var out []Artifact
	if json.Unmarshal(b, &out) != nil {
		return []Artifact{}, nil // tolerate a corrupt registry rather than erroring the UI
	}
	return out, nil
}

func (s *Service) saveLocked(list []Artifact) error {
	p, err := s.storePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Don't persist the computed fields.
	clean := make([]Artifact, len(list))
	copy(clean, list)
	for i := range clean {
		clean[i].Stale = false
		clean[i].Missing = false
	}
	b, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
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
	list, err := s.load()
	if err != nil {
		return Artifact{}, err
	}
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
	list, err := s.load()
	if err != nil {
		return Artifact{}, err
	}
	now := nowMs()
	for i := range list {
		if list[i].Path == a.Path && !list[i].Archived {
			list[i].Kind = a.Kind
			list[i].Title = a.Title
			list[i].Tool = a.Tool
			list[i].Note = a.Note
			list[i].Sources = a.Sources
			list[i].UpdatedAt = now
			if err := s.saveLocked(list); err != nil {
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
	if err := s.saveLocked(list); err != nil {
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
	list, err := s.load()
	if err != nil {
		return err
	}
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
	if err := s.saveLocked(kept); err != nil {
		return err
	}
	s.emit()
	return nil
}

func (s *Service) mutate(id string, fn func(*Artifact)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.load()
	if err != nil {
		return err
	}
	for i := range list {
		if list[i].ID == id {
			fn(&list[i])
			if err := s.saveLocked(list); err != nil {
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
