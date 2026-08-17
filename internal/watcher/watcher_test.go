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
	closed    bool
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
	if w.closed {
		return fsnotify.ErrClosed
	}
	if err := w.addErr[path]; err != nil {
		return err
	}
	w.added[path] = true
	return nil
}

func (w *fakeWatchBackend) Remove(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fsnotify.ErrClosed
	}
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
	if !w.closed {
		w.closed = true
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

func (w *fakeWatchBackend) failChannels() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.closed = true
	close(w.eventsCh)
	close(w.errorsCh)
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

func TestRuntimeFailureReconstructsAndResynchronizesOnNextWatch(t *testing.T) {
	root := t.TempDir()
	backend := newFakeWatchBackend()
	replacement := newFakeWatchBackend()
	sink := newErrorSink()
	var factoryMu sync.Mutex
	factoryCalls := 0
	service := newService(staticRoot(root), func() (watchBackend, error) {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		factoryCalls++
		if factoryCalls == 1 {
			return backend, nil
		}
		return replacement, nil
	}, sink.report)
	t.Cleanup(func() { _ = service.ServiceShutdown() })
	if err := service.Watch([]string{"file.txt"}); err != nil {
		t.Fatal(err)
	}

	runtimeFailure := errors.New("event queue overflow")
	backend.errorsCh <- runtimeFailure
	if reported := receiveError(t, sink); !errors.Is(reported, runtimeFailure) {
		t.Fatalf("reported error = %v, want runtime failure", reported)
	}
	if err := service.Watch([]string{"file.txt"}); err != nil {
		t.Fatalf("Watch recovery error = %v, want successful reconstruction", err)
	}

	added, _, _ := replacement.snapshot()
	if !added[root] {
		t.Fatalf("replacement backend watches = %v, want resynchronized root %q", added, root)
	}
	_, _, oldCloses := backend.snapshot()
	if oldCloses != 1 {
		t.Fatalf("poisoned backend Close calls = %d, want 1", oldCloses)
	}
	service.mu.Lock()
	healthy := service.w == replacement && service.generation != nil && service.initErr == nil && service.runtimeErr == nil
	fileWatched := service.files[fsKey(filepath.Join(root, "file.txt"))]
	service.mu.Unlock()
	if !healthy || !fileWatched {
		t.Fatalf("recovered state: healthy=%v fileWatched=%v", healthy, fileWatched)
	}
	factoryMu.Lock()
	gotFactoryCalls := factoryCalls
	factoryMu.Unlock()
	if gotFactoryCalls != 2 {
		t.Fatalf("factory calls = %d, want initial + one reconstruction", gotFactoryCalls)
	}
}

func TestClosedRuntimeChannelsAreRecoverable(t *testing.T) {
	backend := newFakeWatchBackend()
	replacement := newFakeWatchBackend()
	sink := newErrorSink()
	created := 0
	service := newService(staticRoot(t.TempDir()), func() (watchBackend, error) {
		created++
		if created == 1 {
			return backend, nil
		}
		return replacement, nil
	}, sink.report)
	t.Cleanup(func() { _ = service.ServiceShutdown() })

	backend.failChannels()
	if reported := receiveError(t, sink); !strings.Contains(reported.Error(), "channel closed unexpectedly") {
		t.Fatalf("reported channel failure = %v", reported)
	}
	if err := service.Watch(nil); err != nil {
		t.Fatalf("Watch after channel close = %v, want reconstructed empty watcher", err)
	}
	service.mu.Lock()
	healthy := service.w == replacement && service.runtimeErr == nil && service.initErr == nil
	service.mu.Unlock()
	if !healthy {
		t.Fatal("channel-close recovery did not install a healthy replacement")
	}
}

func TestRecoveryFailureStaysLatchedUntilLaterSuccessfulReconstruction(t *testing.T) {
	root := t.TempDir()
	backend := newFakeWatchBackend()
	replacement := newFakeWatchBackend()
	runtimeFailure := errors.New("queue overflow")
	createFailure := errors.New("replacement creation failed")
	sink := newErrorSink()
	created := 0
	service := newService(staticRoot(root), func() (watchBackend, error) {
		created++
		switch created {
		case 1:
			return backend, nil
		case 2:
			return nil, createFailure
		default:
			return replacement, nil
		}
	}, sink.report)
	t.Cleanup(func() { _ = service.ServiceShutdown() })

	backend.errorsCh <- runtimeFailure
	_ = receiveError(t, sink)
	if err := service.Watch([]string{"file.txt"}); !errors.Is(err, runtimeFailure) || !errors.Is(err, createFailure) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed reconstruction error = %v", err)
	}
	service.mu.Lock()
	stillLatched := service.runtimeErr != nil && service.initErr != nil && service.generation == nil && service.w == nil
	service.mu.Unlock()
	if !stillLatched {
		t.Fatal("failed reconstruction cleared the runtime failure or installed a backend")
	}

	if err := service.Watch([]string{"file.txt"}); err != nil {
		t.Fatalf("later reconstruction error = %v", err)
	}
	service.mu.Lock()
	cleared := service.runtimeErr == nil && service.initErr == nil && service.w == replacement
	service.mu.Unlock()
	if !cleared {
		t.Fatal("successful reconstruction did not clear latched failures")
	}
}

func TestRecoveryDoesNotClearFailureUntilDirectoryResynchronizationSucceeds(t *testing.T) {
	root := t.TempDir()
	backend := newFakeWatchBackend()
	badReplacement := newFakeWatchBackend()
	goodReplacement := newFakeWatchBackend()
	addFailure := errors.New("replacement add failed")
	badReplacement.addErr[root] = addFailure
	sink := newErrorSink()
	created := 0
	service := newService(staticRoot(root), func() (watchBackend, error) {
		created++
		switch created {
		case 1:
			return backend, nil
		case 2:
			return badReplacement, nil
		default:
			return goodReplacement, nil
		}
	}, sink.report)
	t.Cleanup(func() { _ = service.ServiceShutdown() })
	if err := service.Watch([]string{"file.txt"}); err != nil {
		t.Fatal(err)
	}

	backend.errorsCh <- errors.New("runtime failure")
	_ = receiveError(t, sink)
	if err := service.Watch([]string{"file.txt"}); !errors.Is(err, addFailure) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed resynchronization error = %v", err)
	}
	_, _, badCloses := badReplacement.snapshot()
	if badCloses != 1 {
		t.Fatalf("partially synchronized backend Close calls = %d, want 1", badCloses)
	}
	service.mu.Lock()
	failedState := service.runtimeErr != nil && service.initErr != nil && service.w == nil && service.generation == nil
	service.mu.Unlock()
	if !failedState {
		t.Fatal("partial directory resynchronization was incorrectly committed")
	}

	if err := service.Watch([]string{"file.txt"}); err != nil {
		t.Fatalf("retry after resynchronization failure = %v", err)
	}
	added, _, _ := goodReplacement.snapshot()
	service.mu.Lock()
	healthy := service.runtimeErr == nil && service.initErr == nil && service.w == goodReplacement
	service.mu.Unlock()
	if !healthy || !added[root] {
		t.Fatalf("successful retry state: healthy=%v watches=%v", healthy, added)
	}
}

func TestShutdownWinsRaceWithRuntimeRecoveryCandidate(t *testing.T) {
	backend := newFakeWatchBackend()
	candidate := newFakeWatchBackend()
	sink := newErrorSink()
	factoryEntered := make(chan struct{}, 1)
	releaseFactory := make(chan struct{})
	created := 0
	service := newService(staticRoot(t.TempDir()), func() (watchBackend, error) {
		created++
		if created == 1 {
			return backend, nil
		}
		factoryEntered <- struct{}{}
		<-releaseFactory
		return candidate, nil
	}, sink.report)

	backend.errorsCh <- errors.New("runtime failure")
	_ = receiveError(t, sink)
	watchDone := make(chan error, 1)
	go func() { watchDone <- service.Watch(nil) }()
	select {
	case <-factoryEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not reach replacement factory")
	}

	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- service.ServiceShutdown() }()
	waitForWatcherStopping(t, service)
	close(releaseFactory)
	select {
	case err := <-watchDone:
		if !errors.Is(err, ErrStopped) {
			t.Fatalf("recovery racing shutdown error = %v, want ErrStopped", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not abort after shutdown")
	}
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not finish after recovery candidate was released")
	}

	_, _, candidateCloses := candidate.snapshot()
	if candidateCloses != 1 {
		t.Fatalf("abandoned replacement Close calls = %d, want 1", candidateCloses)
	}
	service.mu.Lock()
	stoppedClean := service.stopping && service.stopped && service.w == nil && service.generation == nil
	service.mu.Unlock()
	if !stoppedClean {
		t.Fatal("recovery resurrected watcher after permanent shutdown admission")
	}
}

func waitForWatcherStopping(t *testing.T, service *Service) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		service.mu.Lock()
		stopping := service.stopping
		service.mu.Unlock()
		if stopping {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("ServiceShutdown did not publish stopping state")
		}
		time.Sleep(time.Millisecond)
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
