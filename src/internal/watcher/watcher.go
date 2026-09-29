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

type watchGeneration struct {
	backend watchBackend
	stop    chan struct{}
	done    chan struct{}

	closeOnce sync.Once
	closeErr  error
}

func newWatchGeneration(backend watchBackend) *watchGeneration {
	return &watchGeneration{
		backend: backend,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (generation *watchGeneration) closeBackend() error {
	if generation == nil {
		return nil
	}
	generation.closeOnce.Do(func() {
		close(generation.stop)
		generation.closeErr = generation.backend.Close()
	})
	return generation.closeErr
}

func (generation *watchGeneration) shutdown() error {
	if generation == nil {
		return nil
	}
	err := generation.closeBackend()
	<-generation.done
	return err
}

// Service is the bound Wails watcher service.
type Service struct {
	roots RootProvider

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	w           watchBackend
	generation  *watchGeneration
	files       map[string]bool      // abs file paths to report on
	dirs        map[string]bool      // abs dirs currently added to fsnotify
	suppress    map[string]time.Time // abs file path -> ignore-our-own-write deadline
	initErr     error
	runtimeErr  error
	stopping    bool
	stopped     bool

	factory      watcherFactory
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
		factory:  factory,
		report:   report,
	}
	w, err := factory()
	if err != nil {
		var closeErr error
		if w != nil {
			closeErr = w.Close()
		}
		s.initErr = errors.Join(fmt.Errorf("%w: %w", ErrUnavailable, err), closeErr)
		s.report(s.initErr)
		return s
	}
	if w == nil {
		s.initErr = fmt.Errorf("%w: watcher factory returned nil", ErrUnavailable)
		s.report(s.initErr)
		return s
	}
	generation := newWatchGeneration(w)
	s.w = w
	s.generation = generation
	go s.loop(generation)
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
		// Publish the permanent admission gate before waiting for a Watch that is
		// currently rebuilding a backend. That Watch will recheck before install
		// and dispose of its candidate instead of resurrecting the service.
		s.mu.Lock()
		s.stopping = true
		s.mu.Unlock()

		s.lifecycleMu.Lock()
		s.mu.Lock()
		generation := s.generation
		s.w = nil
		s.generation = nil
		s.files = map[string]bool{}
		s.dirs = map[string]bool{}
		s.suppress = map[string]time.Time{}
		s.mu.Unlock()

		s.shutdownErr = generation.shutdown()

		s.mu.Lock()
		s.stopped = true
		s.mu.Unlock()
		s.lifecycleMu.Unlock()
	})
	return s.shutdownErr
}

// Suppress marks an absolute path as about-to-be-written by Novera itself, so
// the fs event(s) the write produces are not reported back to the UI as an
// external change. It is a package function, rather than an exported Service
// method, so this producer-only primitive cannot be Wails-bound.
func Suppress(s *Service, abs string) {
	if s == nil {
		return
	}
	now := time.Now()
	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return
	}
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
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()

	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return ErrStopped
	}
	s.mu.Unlock()

	root := s.roots.Root()
	byDir := map[string][]watchCandidate{}
	var operationErrs []error
	for _, rel := range rels {
		abs, err := paths.Resolve(root, rel)
		if err != nil {
			operationErrs = append(operationErrs, fmt.Errorf("watch %q: %w", rel, err))
			continue
		}
		dir := filepath.Dir(abs)
		byDir[dir] = append(byDir[dir], watchCandidate{rel: rel, key: fsKey(abs)})
	}

	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return ErrStopped
	}
	unhealthy := s.generation == nil || s.w == nil || s.initErr != nil || s.runtimeErr != nil
	s.mu.Unlock()
	if unhealthy {
		return s.rebuildWatcher(byDir, operationErrs)
	}

	s.mu.Lock()
	// The current generation cannot be replaced while lifecycleMu is held.
	// recordRuntimeError also takes mu before it closes that generation, so the
	// backend stays usable for the duration of this update.
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
	s.mu.Unlock()

	if operationErr != nil {
		s.report(operationErr)
	}
	return operationErr
}

type watchCandidate struct {
	rel string
	key string
}

func (s *Service) rebuildWatcher(byDir map[string][]watchCandidate, pathErrs []error) error {
	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		return ErrStopped
	}
	oldGeneration := s.generation
	previousFailure := errors.Join(s.initErr, s.runtimeErr)
	s.generation = nil
	s.w = nil
	s.files = map[string]bool{}
	s.dirs = map[string]bool{}
	s.mu.Unlock()

	// Detach before Close so a channel-close notification from the poisoned
	// generation cannot race in and overwrite the replacement's health state.
	teardownErr := oldGeneration.shutdown()
	if s.shutdownRequested() {
		return errors.Join(ErrStopped, teardownErr)
	}

	backend, createErr := s.factory()
	if createErr == nil && backend == nil {
		createErr = errors.New("watcher factory returned nil")
	}
	if s.shutdownRequested() {
		var closeErr error
		if backend != nil {
			closeErr = backend.Close()
		}
		return errors.Join(ErrStopped, teardownErr, closeErr)
	}
	if createErr != nil {
		var closeErr error
		if backend != nil {
			closeErr = backend.Close()
		}
		recoveryErr := fmt.Errorf("%w: watcher reconstruction: %w", ErrUnavailable, createErr)
		s.latchRecoveryFailure(recoveryErr)
		result := errors.Join(errors.Join(pathErrs...), previousFailure, teardownErr, recoveryErr, closeErr)
		s.report(result)
		return result
	}

	newFiles := map[string]bool{}
	newDirs := map[string]bool{}
	var resyncErrs []error
	for dir, candidates := range byDir {
		if err := backend.Add(dir); err != nil {
			for _, file := range candidates {
				resyncErrs = append(resyncErrs, fmt.Errorf("watch %q (parent %q): %w", file.rel, dir, err))
			}
			continue
		}
		newDirs[dir] = true
		for _, file := range candidates {
			newFiles[file.key] = true
		}
	}
	if s.shutdownRequested() {
		return errors.Join(ErrStopped, teardownErr, backend.Close())
	}
	if resyncErr := errors.Join(resyncErrs...); resyncErr != nil {
		closeErr := backend.Close()
		recoveryErr := fmt.Errorf("%w: watcher resynchronization: %w", ErrUnavailable, resyncErr)
		s.latchRecoveryFailure(recoveryErr)
		result := errors.Join(errors.Join(pathErrs...), previousFailure, teardownErr, recoveryErr, closeErr)
		s.report(result)
		return result
	}

	generation := newWatchGeneration(backend)
	s.mu.Lock()
	if s.stopping || s.stopped {
		s.mu.Unlock()
		_ = backend.Close()
		return ErrStopped
	}
	s.w = backend
	s.generation = generation
	s.files = newFiles
	s.dirs = newDirs
	// Clear both startup and runtime failures only after factory creation and
	// the complete desired-directory resynchronization have succeeded.
	s.initErr = nil
	s.runtimeErr = nil
	s.mu.Unlock()
	go s.loop(generation)

	result := errors.Join(errors.Join(pathErrs...), teardownErr)
	if result != nil {
		s.report(result)
	}
	return result
}

func (s *Service) latchRecoveryFailure(err error) {
	s.mu.Lock()
	if !s.stopping && !s.stopped {
		s.initErr = err
	}
	s.mu.Unlock()
}

func (s *Service) shutdownRequested() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping || s.stopped
}

func (s *Service) loop(generation *watchGeneration) {
	defer close(generation.done)
	for {
		select {
		case <-generation.stop:
			return
		case e, ok := <-generation.backend.Events():
			if !ok {
				s.recordRuntimeError(generation, errors.New("event channel closed unexpectedly"))
				_ = generation.closeBackend()
				return
			}
			s.handle(e)
		case err, ok := <-generation.backend.Errors():
			if !ok {
				s.recordRuntimeError(generation, errors.New("error channel closed unexpectedly"))
				_ = generation.closeBackend()
				return
			}
			if err == nil {
				err = errors.New("watcher reported a nil runtime error")
			}
			s.recordRuntimeError(generation, err)
			_ = generation.closeBackend()
			return
		}
	}
}

func (s *Service) recordRuntimeError(generation *watchGeneration, err error) {
	if err == nil {
		return
	}
	wrapped := fmt.Errorf("file watcher runtime failure: %w", err)
	s.mu.Lock()
	if s.stopping || s.stopped || s.generation != generation {
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
