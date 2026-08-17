package session

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novera/internal/bigfile/document"
)

func writeRegistrySafetyFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenIdentityDeduplicatesAliases(t *testing.T) {
	path := writeRegistrySafetyFile(t, "source.txt")
	registry := New()
	t.Cleanup(func() { _ = registry.Shutdown(context.Background()) })
	first, err := registry.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	alias, err := registry.Open(filepath.Join(filepath.Dir(path), ".", filepath.Base(path)))
	if err != nil {
		t.Fatal(err)
	}
	defer alias.Release()
	if alias.ID != first.ID || alias.Doc != first.Doc || alias.Generation != first.Generation {
		t.Fatalf("alias opened a second session: first=%+v alias=%+v", first, alias)
	}
	if paths := registry.Paths(); len(paths) != 1 || paths[0] != path {
		t.Fatalf("registered paths = %v, want first spelling %q", paths, path)
	}
}

type transitionOpenResult struct {
	file *File
	err  error
}

type transitionReopenResult struct {
	file *File
	err  error
}

func exerciseOpenIdentityAcrossPathTransition(t *testing.T, concurrentOpens int) {
	t.Helper()
	if concurrentOpens <= 0 || concurrentOpens > DefaultMaxConcurrentOpens {
		t.Fatalf("invalid concurrent open count %d", concurrentOpens)
	}

	dir := t.TempDir()
	primary := filepath.Join(dir, "primary.txt")
	alias := filepath.Join(dir, "old-primary-hardlink.txt")
	const oldContents = "old identity\n"
	const newContents = "new identity\n"
	if err := os.WriteFile(primary, []byte(oldContents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(primary, alias); err != nil {
		t.Fatalf("create hard-link alias: %v", err)
	}

	registry := New()
	t.Cleanup(func() { _ = registry.Shutdown(context.Background()) })
	opened, err := registry.Open(primary)
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Release()
	originalID := opened.ID

	transition, err := registry.BeginTransition(originalID)
	if err != nil {
		t.Fatal(err)
	}
	defer transition.Abort()

	matchedTransition := make(chan struct{}, concurrentOpens)
	registry.candidateTransitionHook = func() {
		matchedTransition <- struct{}{}
	}
	openedAliases := make(chan transitionOpenResult, concurrentOpens)
	for range concurrentOpens {
		go func() {
			file, openErr := registry.Open(alias)
			openedAliases <- transitionOpenResult{file: file, err: openErr}
		}()
	}

	// Do not mutate the primary pathname until every candidate has opened the
	// old inode and matched it against the entry behind the transition barrier.
	// This makes the adversarial interleaving deterministic instead of relying
	// on sleeps or scheduler timing.
	for range concurrentOpens {
		select {
		case <-matchedTransition:
		case <-time.After(5 * time.Second):
			t.Fatal("candidate open did not reach the transition identity barrier")
		}
	}
	if err := os.Remove(primary); err != nil {
		t.Fatalf("unlink primary identity: %v", err)
	}
	if err := os.WriteFile(primary, []byte(newContents), 0o600); err != nil {
		t.Fatalf("install replacement primary identity: %v", err)
	}

	reopenedResult := make(chan transitionReopenResult, 1)
	go func() {
		file, reopenErr := transition.Reopen(false)
		reopenedResult <- transitionReopenResult{file: file, err: reopenErr}
	}()
	opened.Release()

	var reopened *File
	select {
	case result := <-reopenedResult:
		if result.err != nil {
			t.Fatalf("reopen replacement identity: %v", result.err)
		}
		reopened = result.file
	case <-time.After(5 * time.Second):
		t.Fatal("replacement reopen did not drain the original lease")
	}
	if reopened == nil {
		t.Fatal("replacement reopen returned a nil file")
	}
	defer reopened.Release()
	if reopened.ID != originalID || reopened.Generation != 2 {
		t.Fatalf("replacement session = id %q generation %d, want %q generation 2", reopened.ID, reopened.Generation, originalID)
	}
	if got, readErr := reopened.Doc.ReadRange(0, reopened.Doc.Size()); readErr != nil || string(got) != newContents {
		t.Fatalf("replacement session contents = %q, %v; want %q", got, readErr, newContents)
	}

	var aliasID string
	var aliasDoc *document.FileDocument
	for range concurrentOpens {
		select {
		case result := <-openedAliases:
			if result.err != nil {
				t.Fatalf("open retained hard-link identity: %v", result.err)
			}
			if result.file == nil {
				t.Fatal("open retained hard-link identity returned a nil file")
			}
			defer result.file.Release()
			if result.file.ID == originalID {
				t.Fatalf("hard-link open returned transitioned session %q", originalID)
			}
			if result.file.Generation != 1 {
				t.Fatalf("hard-link generation = %d, want new-session generation 1", result.file.Generation)
			}
			if got, readErr := result.file.Doc.ReadRange(0, result.file.Doc.Size()); readErr != nil || string(got) != oldContents {
				t.Fatalf("hard-link session contents = %q, %v; want %q", got, readErr, oldContents)
			}
			if aliasID == "" {
				aliasID = result.file.ID
				aliasDoc = result.file.Doc
			} else if result.file.ID != aliasID || result.file.Doc != aliasDoc {
				t.Fatalf("concurrent hard-link opens diverged: got id %q doc %p, want id %q doc %p", result.file.ID, result.file.Doc, aliasID, aliasDoc)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("hard-link open did not settle after transition")
		}
	}
	if paths := registry.Paths(); len(paths) != 2 {
		t.Fatalf("registered paths = %v, want replacement and retained hard-link sessions", paths)
	}
}

func TestOpenHardLinkIdentityRevalidatedAcrossPathTransition(t *testing.T) {
	exerciseOpenIdentityAcrossPathTransition(t, 1)
}

func TestConcurrentHardLinkOpensRevalidatedAcrossPathTransition(t *testing.T) {
	exerciseOpenIdentityAcrossPathTransition(t, DefaultMaxConcurrentOpens)
}

func TestRegistryOpenLimitsFailWithoutRetainingCandidate(t *testing.T) {
	registry := NewWithLimit(1)
	t.Cleanup(func() { _ = registry.Shutdown(context.Background()) })
	first, err := registry.Open(writeRegistrySafetyFile(t, "first.txt"))
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	if second, err := registry.Open(writeRegistrySafetyFile(t, "second.txt")); !errors.Is(err, ErrOpenFileLimit) {
		if second != nil {
			second.Release()
		}
		t.Fatalf("second open error = %v, want ErrOpenFileLimit", err)
	}
	if paths := registry.Paths(); len(paths) != 1 {
		t.Fatalf("registry retained %d paths after rejected open", len(paths))
	}

	registry.mu.Lock()
	registry.opening = registry.maxOpening
	registry.mu.Unlock()
	if file, err := registry.Open(writeRegistrySafetyFile(t, "queued.txt")); !errors.Is(err, ErrOpenInFlightLimit) {
		if file != nil {
			file.Release()
		}
		t.Fatalf("concurrent open error = %v, want ErrOpenInFlightLimit", err)
	}
	registry.mu.Lock()
	registry.opening = 0
	registry.mu.Unlock()
}

func TestGenerationAndFileIDSequencesFailBeforeWrap(t *testing.T) {
	registry := New()
	t.Cleanup(func() { _ = registry.Shutdown(context.Background()) })
	file, err := registry.Open(writeRegistrySafetyFile(t, "generation.txt"))
	if err != nil {
		t.Fatal(err)
	}
	id := file.ID
	file.Release()

	registry.mu.Lock()
	entry := registry.files[id]
	entry.mu.Lock()
	entry.generation = MaxBridgeFileGeneration
	entry.mu.Unlock()
	registry.mu.Unlock()
	if reopened, err := registry.Reopen(id); !errors.Is(err, ErrFileGenerationExhausted) {
		if reopened != nil {
			reopened.Release()
		}
		t.Fatalf("generation exhaustion error = %v", err)
	}
	stillOpen, ok := registry.Get(id)
	if !ok || stillOpen.Generation != MaxBridgeFileGeneration {
		t.Fatalf("generation exhaustion replaced retained file: %+v, %v", stillOpen, ok)
	}
	stillOpen.Release()

	registry.mu.Lock()
	registry.seq = math.MaxInt64
	registry.mu.Unlock()
	if candidate, err := registry.Open(writeRegistrySafetyFile(t, "id.txt")); !errors.Is(err, ErrFileIDSequenceExhausted) {
		if candidate != nil {
			candidate.Release()
		}
		t.Fatalf("file-id exhaustion error = %v", err)
	}
}

func TestShutdownIsIrreversibleAndDrainsHeldLease(t *testing.T) {
	registry := New()
	opened, err := registry.Open(writeRegistrySafetyFile(t, "shutdown.txt"))
	if err != nil {
		t.Fatal(err)
	}
	id := opened.ID
	doc := opened.Doc
	opened.Release()
	held, ok := registry.Get(id)
	if !ok {
		t.Fatal("missing held lease")
	}

	done := registry.BeginShutdown()
	if file, err := registry.Open(writeRegistrySafetyFile(t, "late.txt")); !errors.Is(err, ErrRegistryStopped) {
		if file != nil {
			file.Release()
		}
		t.Fatalf("late open error = %v, want ErrRegistryStopped", err)
	}
	if file, ok := registry.Get(id); ok || file != nil {
		if file != nil {
			file.Release()
		}
		t.Fatal("shutdown left an id acquirable")
	}
	select {
	case <-done:
		t.Fatal("shutdown returned before held lease drained")
	case <-time.After(20 * time.Millisecond):
	}
	held.Release()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not finish after lease release")
	}
	if err := registry.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.ReadRange(0, 1); err == nil {
		t.Fatal("shutdown left retained descriptor readable")
	}
}
