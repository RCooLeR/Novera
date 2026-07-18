// Package watcher emits "fs:changed" events when files the editor has open are
// modified on disk by something other than Novera. The frontend reloads clean
// tabs and flags dirty ones. It watches the parent directories of the open
// files (fsnotify isn't recursive) rather than the whole tree.
package watcher

import (
	"errors"
	"fmt"
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

const (
	EventChanged = "fs:changed"
	EventError   = "fs:watch-error"
)

var (
	ErrUnavailable = errors.New("file watcher unavailable")
	ErrStopped     = errors.New("file watcher stopped")
)

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

type errorEvent struct {
	Message string `json:"message"`
}

type watchBackend interface {
	Add(string) error
	Remove(string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type fsnotifyBackend struct{ *fsnotify.Watcher }

func (w *fsnotifyBackend) Events() <-chan fsnotify.Event { return w.Watcher.Events }
func (w *fsnotifyBackend) Errors() <-chan error          { return w.Watcher.Errors }

type watcherFactory func() (watchBackend, error)
type errorReporter func(error)

// Service is the bound Wails watcher service.
type Service struct {
	roots RootProvider

	mu         sync.Mutex
	w          watchBackend
	files      map[string]bool      // abs file paths to report on
	dirs       map[string]bool      // abs dirs currently added to fsnotify
	suppress   map[string]time.Time // abs file path -> ignore-our-own-write deadline
	initErr    error
	runtimeErr error
	stopping   bool
	stopped    bool

	stop         chan struct{}
	loopWG       sync.WaitGroup
	shutdownOnce sync.Once
	shutdownErr  error
	report       errorReporter
}

// New constructs the watcher and starts its event loop.
func New(roots RootProvider) *Service {
	return newService(roots, func() (watchBackend, error) {
		w, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		return &fsnotifyBackend{Watcher: w}, nil
	}, reportError)
}

func newService(roots RootProvider, factory watcherFactory, report errorReporter) *Service {
	if report == nil {
		report = func(error) {}
	}
	s := &Service{
		roots:    roots,
		files:    map[string]bool{},
		dirs:     map[string]bool{},
		suppress: map[string]time.Time{},
		stop:     make(chan struct{}),
		report:   report,
	}
	w, err := factory()
	if err != nil {
		s.initErr = fmt.Errorf("%w: %w", ErrUnavailable, err)
		s.report(s.initErr)
		return s
	}
	if w == nil {
		s.initErr = fmt.Errorf("%w: watcher factory returned nil", ErrUnavailable)
		s.report(s.initErr)
		return s
	}
	s.w = w
	s.loopWG.Add(1)
	go s.loop(w, s.stop)
	return s
}

func reportError(err error) {
	log.Printf("watcher: %v", err)
	if app := application.Get(); app != nil {
		app.Event.Emit(EventError, errorEvent{Message: err.Error()})
	}
}

// ServiceShutdown closes the underlying fsnotify watcher when the app exits so
// the OS watch handles aren't leaked.
func (s *Service) ServiceShutdown() error {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.stopping = true
		w := s.w
		close(s.stop)
		s.mu.Unlock()

		if w != nil {
			s.shutdownErr = w.Close()
		}
		s.loopWG.Wait()

		s.mu.Lock()
		s.w = nil
		s.files = map[string]bool{}
		s.dirs = map[string]bool{}
		s.suppress = map[string]time.Time{}
		s.stopped = true
		s.mu.Unlock()
	})
	return s.shutdownErr
}

// Suppress marks an absolute path as about-to-be-written by Novera itself, so
// the fs event(s) the write produces are not reported back to the UI as an
// external change. It is a package function, rather than an exported Service
// method, so this producer-only primitive cannot be Wails-bound.
func Suppress(s *Service, abs string) {
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
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return ErrStopped
	}
	if s.w == nil {
		err := s.initErr
		if err == nil {
			err = ErrUnavailable
		}
		s.mu.Unlock()
		s.report(err)
		return err
	}

	type candidate struct {
		rel string
		key string
	}
	byDir := map[string][]candidate{}
	var operationErrs []error
	for _, rel := range rels {
		abs, err := paths.Resolve(root, rel)
		if err != nil {
			operationErrs = append(operationErrs, fmt.Errorf("watch %q: %w", rel, err))
			continue
		}
		dir := filepath.Dir(abs)
		byDir[dir] = append(byDir[dir], candidate{rel: rel, key: fsKey(abs)})
	}

	newFiles := map[string]bool{}
	needDirs := map[string]bool{}
	for d, candidates := range byDir {
		needDirs[d] = true
		if !s.dirs[d] {
			if err := s.w.Add(d); err != nil {
				for _, file := range candidates {
					operationErrs = append(operationErrs, fmt.Errorf("watch %q (parent %q): %w", file.rel, d, err))
				}
				continue
			}
			s.dirs[d] = true
		}
		for _, file := range candidates {
			newFiles[file.key] = true
		}
	}

	for d := range s.dirs {
		if !needDirs[d] {
			if err := s.w.Remove(d); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
				operationErrs = append(operationErrs, fmt.Errorf("stop watching directory %q: %w", d, err))
				continue
			}
			delete(s.dirs, d)
		}
	}
	s.files = newFiles
	operationErr := errors.Join(operationErrs...)
	resultErr := errors.Join(operationErr, s.runtimeErr)
	s.mu.Unlock()

	if operationErr != nil {
		s.report(operationErr)
	}
	return resultErr
}

func (s *Service) loop(w watchBackend, stop <-chan struct{}) {
	defer s.loopWG.Done()
	for {
		select {
		case <-stop:
			return
		case e, ok := <-w.Events():
			if !ok {
				s.recordRuntimeError(errors.New("event channel closed unexpectedly"))
				return
			}
			s.handle(e)
		case err, ok := <-w.Errors():
			if !ok {
				s.recordRuntimeError(errors.New("error channel closed unexpectedly"))
				return
			}
			s.recordRuntimeError(err)
		}
	}
}

func (s *Service) recordRuntimeError(err error) {
	if err == nil {
		return
	}
	wrapped := fmt.Errorf("file watcher runtime failure: %w", err)
	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return
	}
	s.runtimeErr = wrapped
	s.mu.Unlock()
	s.report(wrapped)
}

func (s *Service) handle(e fsnotify.Event) {
	if e.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename) == 0 {
		return
	}
	key := fsKey(e.Name)
	s.mu.Lock()
	watched := s.files[key]
	suppressed := false
	if until, ok := s.suppress[key]; ok && time.Now().Before(until) {
		suppressed = true // our own recent write — not an external change
	}
	s.mu.Unlock()
	if !watched || suppressed {
		return
	}
	root := s.roots.Root()
	rel, err := paths.Rel(root, e.Name)
	if err != nil {
		return
	}
	removed := e.Op&(fsnotify.Remove|fsnotify.Rename) != 0
	if app := application.Get(); app != nil {
		app.Event.Emit(EventChanged, changeEvent{Path: rel, Removed: removed})
	}
}
