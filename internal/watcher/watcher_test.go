package watcher

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"novera/internal/paths"
)

type staticRoot string

func (r staticRoot) Root() string { return string(r) }

type fakeWatchBackend struct {
	mu sync.Mutex

	eventsCh  chan fsnotify.Event
	errorsCh  chan error
	addErr    map[string]error
	removeErr map[string]error
	added     map[string]bool
	addCalls  []string
	closeErr  error
	closes    int
}

func newFakeWatchBackend() *fakeWatchBackend {
	return &fakeWatchBackend{
		eventsCh:  make(chan fsnotify.Event, 8),
		errorsCh:  make(chan error, 8),
		addErr:    map[string]error{},
		removeErr: map[string]error{},
		added:     map[string]bool{},
	}
}

func (w *fakeWatchBackend) Add(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.addCalls = append(w.addCalls, path)
	if err := w.addErr[path]; err != nil {
		return err
	}
	w.added[path] = true
	return nil
}

func (w *fakeWatchBackend) Remove(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.removeErr[path]; err != nil {
		return err
	}
	if !w.added[path] {
		return fsnotify.ErrNonExistentWatch
	}
	delete(w.added, path)
	return nil
}

func (w *fakeWatchBackend) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closes++
	if w.closes == 1 {
		close(w.eventsCh)
		close(w.errorsCh)
	}
	return w.closeErr
}

func (w *fakeWatchBackend) Events() <-chan fsnotify.Event { return w.eventsCh }
func (w *fakeWatchBackend) Errors() <-chan error          { return w.errorsCh }

func (w *fakeWatchBackend) snapshot() (map[string]bool, []string, int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	added := make(map[string]bool, len(w.added))
	for path, present := range w.added {
		added[path] = present
	}
	return added, append([]string(nil), w.addCalls...), w.closes
}

type errorSink struct{ ch chan error }

func newErrorSink() *errorSink        { return &errorSink{ch: make(chan error, 16)} }
func (s *errorSink) report(err error) { s.ch <- err }

func serviceWithFake(root string, backend *fakeWatchBackend, sink *errorSink) *Service {
	return newService(staticRoot(root), func() (watchBackend, error) { return backend, nil }, sink.report)
}

func receiveError(t *testing.T, sink *errorSink) error {
	t.Helper()
	select {
	case err := <-sink.ch:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for watcher error report")
		return nil
	}
}

func TestWatchReturnsPerPathAddFailureAndKeepsSuccessfulWatches(t *testing.T) {
	root := t.TempDir()
	goodDir := filepath.Join(root, "good")
	badDir := filepath.Join(root, "bad")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(badDir, 0o755); err != nil {
		t.Fatal(err)
	}

	backend := newFakeWatchBackend()
	addFailure := errors.New("configured add failure")
	backend.addErr[badDir] = addFailure
	sink := newErrorSink()
	service := serviceWithFake(root, backend, sink)
	t.Cleanup(func() { _ = service.ServiceShutdown() })

	err := service.Watch([]string{"good/a.txt", "bad/b.txt"})
	if !errors.Is(err, addFailure) {
		t.Fatalf("Watch error = %v, want configured Add failure", err)
	}
	if !strings.Contains(err.Error(), "bad/b.txt") {
		t.Fatalf("Watch error should identify the unwatched path: %v", err)
	}
	if reported := receiveError(t, sink); !errors.Is(reported, addFailure) {
		t.Fatalf("reported error = %v, want configured Add failure", reported)
	}

	added, _, _ := backend.snapshot()
	if !added[goodDir] || added[badDir] {
		t.Fatalf("backend watches = %v, want only %q", added, goodDir)
	}
	service.mu.Lock()
	goodWatched := service.files[fsKey(filepath.Join(goodDir, "a.txt"))]
	badWatched := service.files[fsKey(filepath.Join(badDir, "b.txt"))]
	service.mu.Unlock()
	if !goodWatched || badWatched {
		t.Fatalf("reported files: good=%v bad=%v, want true/false", goodWatched, badWatched)
	}
}

func TestWatchReturnsResolveFailuresInsteadOfIgnoringThem(t *testing.T) {
	backend := newFakeWatchBackend()
	sink := newErrorSink()
	service := serviceWithFake("relative-root", backend, sink)
	t.Cleanup(func() { _ = service.ServiceShutdown() })

	err := service.Watch([]string{"file.txt"})
	if !errors.Is(err, paths.ErrRootNotAbsolute) {
		t.Fatalf("Watch error = %v, want ErrRootNotAbsolute", err)
	}
	if !strings.Contains(err.Error(), "file.txt") {
		t.Fatalf("Watch error should identify the rejected path: %v", err)
	}
	_, calls, _ := backend.snapshot()
	if len(calls) != 0 {
		t.Fatalf("Add called for an unresolved path: %v", calls)
	}
	_ = receiveError(t, sink)
}

func TestWatchSurfacesWatcherCreationFailure(t *testing.T) {
	createFailure := errors.New("watcher creation failed")
	sink := newErrorSink()
	service := newService(staticRoot(t.TempDir()), func() (watchBackend, error) {
		return nil, createFailure
	}, sink.report)
	t.Cleanup(func() { _ = service.ServiceShutdown() })

	if reported := receiveError(t, sink); !errors.Is(reported, createFailure) {
		t.Fatalf("reported error = %v, want creation failure", reported)
	}
	err := service.Watch([]string{"file.txt"})
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, createFailure) {
		t.Fatalf("Watch error = %v, want ErrUnavailable wrapping creation failure", err)
	}
}

func TestRuntimeFailureIsReportedAndReturnedByWatch(t *testing.T) {
	backend := newFakeWatchBackend()
	sink := newErrorSink()
	service := serviceWithFake(t.TempDir(), backend, sink)
	t.Cleanup(func() { _ = service.ServiceShutdown() })

	runtimeFailure := errors.New("event queue overflow")
	backend.errorsCh <- runtimeFailure
	if reported := receiveError(t, sink); !errors.Is(reported, runtimeFailure) {
		t.Fatalf("reported error = %v, want runtime failure", reported)
	}
	if err := service.Watch(nil); !errors.Is(err, runtimeFailure) {
		t.Fatalf("Watch error = %v, want recorded runtime failure", err)
	}
}

func TestServiceShutdownIsConcurrentIdempotentAndWaitsForLoop(t *testing.T) {
	backend := newFakeWatchBackend()
	closeFailure := errors.New("close failure")
	backend.closeErr = closeFailure
	sink := newErrorSink()
	service := serviceWithFake(t.TempDir(), backend, sink)

	const callers = 32
	start := make(chan struct{})
	results := make(chan error, callers)
	var callersWG sync.WaitGroup
	for i := 0; i < callers; i++ {
		callersWG.Add(1)
		go func() {
			defer callersWG.Done()
			<-start
			results <- service.ServiceShutdown()
		}()
	}
	close(start)
	callersWG.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, closeFailure) {
			t.Fatalf("ServiceShutdown error = %v, want close failure", err)
		}
	}

	_, _, closes := backend.snapshot()
	if closes != 1 {
		t.Fatalf("backend Close calls = %d, want 1", closes)
	}
	service.mu.Lock()
	watcherCleared := service.w == nil
	stopped := service.stopped
	service.mu.Unlock()
	if !watcherCleared || !stopped {
		t.Fatalf("shutdown state: watcherCleared=%v stopped=%v", watcherCleared, stopped)
	}
	select {
	case err := <-sink.ch:
		t.Fatalf("normal shutdown reported a runtime watcher failure: %v", err)
	default:
	}
	if err := service.Watch(nil); !errors.Is(err, ErrStopped) {
		t.Fatalf("Watch after shutdown error = %v, want ErrStopped", err)
	}
}

func TestProducerSuppressIsNotBridgeBound(t *testing.T) {
	typ := reflect.TypeOf(&Service{})
	if _, ok := typ.MethodByName("Suppress"); ok {
		t.Fatal("producer-only Suppress must not be an exported Service method")
	}
	bindingPath := filepath.Join("..", "..", "frontend", "bindings", "novera", "internal", "watcher", "service.ts")
	binding, err := os.ReadFile(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(binding), "function Suppress") {
		t.Fatal("generated watcher binding exposes producer-only Suppress")
	}
}
