// Package watcher emits "fs:changed" events when files the editor has open are
// modified on disk by something other than Novera. The frontend reloads clean
// tabs and flags dirty ones. It watches the parent directories of the open
// files (fsnotify isn't recursive) rather than the whole tree.
package watcher

import (
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/paths"
)

const EventChanged = "fs:changed"

// selfWriteWindow is how long after Novera writes a file we suppress the
// resulting fs event(s) as our own echo. A single atomic save produces a short
// burst (Create/Write/Rename), so the window spans the whole burst; a genuine
// external edit within this window of a save is the accepted tradeoff for not
// flagging every save as an external change.
const selfWriteWindow = 1500 * time.Millisecond

// RootProvider yields the active workspace root.
type RootProvider interface{ Root() string }

type changeEvent struct {
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
}

// Service is the bound Wails watcher service.
type Service struct {
	roots    RootProvider
	mu       sync.Mutex
	w        *fsnotify.Watcher
	files    map[string]bool      // abs file paths to report on
	dirs     map[string]bool      // abs dirs currently added to fsnotify
	suppress map[string]time.Time // abs file path -> ignore-our-own-write deadline
}

// New constructs the watcher and starts its event loop.
func New(roots RootProvider) *Service {
	s := &Service{roots: roots, files: map[string]bool{}, dirs: map[string]bool{}, suppress: map[string]time.Time{}}
	if w, err := fsnotify.NewWatcher(); err == nil {
		s.w = w
		go s.loop()
	} else {
		// Not fatal — the editor still works, it just won't auto-detect external
		// edits. Surface it so the cause (e.g. inotify limit) isn't a silent void.
		log.Printf("watcher: file-change notifications unavailable: %v", err)
	}
	return s
}

// ServiceShutdown closes the underlying fsnotify watcher when the app exits so
// the OS watch handles aren't leaked.
func (s *Service) ServiceShutdown() error {
	s.mu.Lock()
	w := s.w
	s.w = nil
	s.mu.Unlock()
	if w != nil {
		return w.Close()
	}
	return nil
}

// Suppress marks an absolute path as about-to-be-written by Novera itself, so
// the fs event(s) the write produces are not reported back to the UI as an
// external change. Called by the workspace service just before an atomic save.
func (s *Service) Suppress(abs string) {
	now := time.Now()
	s.mu.Lock()
	// Prune expired entries so the map stays bounded to roughly the open-tab set.
	for k, until := range s.suppress {
		if now.After(until) {
			delete(s.suppress, k)
		}
	}
	s.suppress[fsKey(abs)] = now.Add(selfWriteWindow)
	s.mu.Unlock()
}

// fsKey normalises a path for map lookups: Windows filesystems are
// case-insensitive, so a watch event's casing may differ from the resolved
// path we stored.
func fsKey(p string) string {
	if runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

// Watch sets the workspace-relative files to monitor (the open editor tabs).
// It (re)watches their parent directories and drops directories no longer needed.
func (s *Service) Watch(rels []string) error {
	root := s.roots.Root()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.w == nil || root == "" {
		return nil
	}
	newFiles := map[string]bool{}
	needDirs := map[string]bool{}
	for _, rel := range rels {
		abs, err := paths.Resolve(root, rel)
		if err != nil {
			continue
		}
		newFiles[fsKey(abs)] = true
		needDirs[filepath.Dir(abs)] = true
	}
	for d := range needDirs {
		if !s.dirs[d] {
			if s.w.Add(d) == nil {
				s.dirs[d] = true
			}
		}
	}
	for d := range s.dirs {
		if !needDirs[d] {
			_ = s.w.Remove(d)
			delete(s.dirs, d)
		}
	}
	s.files = newFiles
	return nil
}

func (s *Service) loop() {
	for {
		select {
		case e, ok := <-s.w.Events:
			if !ok {
				return
			}
			s.handle(e)
		case err, ok := <-s.w.Errors:
			if !ok {
				return
			}
			// Don't silently swallow watcher errors (e.g. overflow/queue drops);
			// log so a flood of missed events has a traceable cause.
			log.Printf("watcher: fsnotify error: %v", err)
		}
	}
}

func (s *Service) handle(e fsnotify.Event) {
	if e.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	key := fsKey(e.Name)
	s.mu.Lock()
	watched := s.files[key]
	root := s.roots.Root()
	suppressed := false
	if until, ok := s.suppress[key]; ok && time.Now().Before(until) {
		suppressed = true // our own recent write — not an external change
	}
	s.mu.Unlock()
	if !watched || suppressed {
		return
	}
	rel, err := paths.Rel(root, e.Name)
	if err != nil {
		return
	}
	removed := e.Op&(fsnotify.Remove|fsnotify.Rename) != 0
	if app := application.Get(); app != nil {
		app.Event.Emit(EventChanged, changeEvent{Path: rel, Removed: removed})
	}
}
