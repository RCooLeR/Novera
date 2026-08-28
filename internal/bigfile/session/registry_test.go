package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"novera/internal/bigfile/manualedit"
)

func openTestRegistryFile(t *testing.T) (*Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := New()
	f, err := r.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := f.ID
	f.Release()
	return r, id
}

func TestCloseWaitsForActiveLease(t *testing.T) {
	r, id := openTestRegistryFile(t)
	held, ok := r.Get(id)
	if !ok {
		t.Fatal("expected lease")
	}

	done := make(chan error, 1)
	go func() { done <- r.Close(id) }()

	// Close removes the id before waiting. Observing that removal proves the
	// close goroutine reached its lease-drain barrier without timing guesses.
	deadline := time.Now().Add(5 * time.Second)
	for {
		probe, exists := r.Get(id)
		if probe != nil {
			probe.Release()
		}
		if !exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("close never removed registry entry")
		}
	}
	select {
	case err := <-done:
		t.Fatalf("close returned before lease release: %v", err)
	default:
	}

	held.Release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close did not finish after lease release")
	}
}

func TestEditLeasesSerializeMutableSession(t *testing.T) {
	r, id := openTestRegistryFile(t)
	first, ok := r.GetEdit(id)
	if !ok {
		t.Fatal("expected edit lease")
	}
	sess := first.EditSession()
	if err := sess.ApplyEdit(manualedit.Edit{Start: 0, End: 1, Text: []byte("A")}); err != nil {
		t.Fatal(err)
	}

	acquired := make(chan *File, 1)
	go func() {
		second, ok := r.GetEdit(id)
		if !ok {
			acquired <- nil
			return
		}
		acquired <- second
	}()
	select {
	case second := <-acquired:
		if second != nil {
			second.Release()
		}
		t.Fatal("second edit lease was not serialized")
	default:
	}

	first.Release()
	select {
	case second := <-acquired:
		if second == nil {
			t.Fatal("second edit acquisition failed")
		}
		if second.Edit == nil || second.Edit.EditCount() != 1 {
			t.Fatalf("second lease saw edit state %#v", second.Edit)
		}
		second.Release()
	case <-time.After(5 * time.Second):
		t.Fatal("second edit lease did not acquire")
	}
	if err := r.Close(id); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentReadEditAndReopen(t *testing.T) {
	r, id := openTestRegistryFile(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			for j := 0; j < 40; j++ {
				f, ok := r.Get(id)
				if !ok {
					return
				}
				_, _ = f.Doc.ReadRange(0, 5)
				f.Release()
			}
		})
	}
	for i := 0; i < 4; i++ {
		wg.Go(func() {
			for j := 0; j < 20; j++ {
				f, ok := r.GetEdit(id)
				if !ok {
					return
				}
				_ = f.EditSession().ApplyEdit(manualedit.Edit{Start: 0, End: 1, Text: []byte("A")})
				f.ResetEdits()
				f.Release()
			}
		})
	}
	for i := 0; i < 5; i++ {
		f, err := r.Reopen(id)
		if err != nil {
			t.Fatal(err)
		}
		if f.Generation != uint64(i+2) {
			t.Fatalf("generation = %d, want %d", f.Generation, i+2)
		}
		f.Release()
	}
	wg.Wait()
	if err := r.Close(id); err != nil {
		t.Fatal(err)
	}
}

func TestReopenIfCleanPreservesStagedEdits(t *testing.T) {
	r, id := openTestRegistryFile(t)
	edit, ok := r.GetEdit(id)
	if !ok {
		t.Fatal("expected edit lease")
	}
	if err := edit.EditSession().ApplyEdit(manualedit.Edit{Start: 0, End: 1, Text: []byte("A")}); err != nil {
		t.Fatal(err)
	}
	generation := edit.Generation
	edit.Release()

	if reopened, err := r.ReopenIfClean(id); !errors.Is(err, ErrStagedEdits) {
		if reopened != nil {
			reopened.Release()
		}
		t.Fatalf("ReopenIfClean error = %v, want ErrStagedEdits", err)
	}

	stillOpen, ok := r.GetEdit(id)
	if !ok {
		t.Fatal("failed refresh removed the open session")
	}
	defer stillOpen.Release()
	if stillOpen.Generation != generation {
		t.Fatalf("generation = %d, want unchanged %d", stillOpen.Generation, generation)
	}
	if stillOpen.Edit == nil || stillOpen.Edit.EditCount() != 1 {
		t.Fatalf("staged edits were lost: %#v", stillOpen.Edit)
	}
}

func TestConcurrentCloseAndReopenDrainExactGeneration(t *testing.T) {
	r, id := openTestRegistryFile(t)
	held, ok := r.Get(id)
	if !ok {
		t.Fatal("expected lease")
	}
	start := make(chan struct{})
	reopened := make(chan error, 1)
	closed := make(chan error, 1)
	go func() {
		<-start
		f, err := r.Reopen(id)
		if f != nil {
			f.Release()
		}
		reopened <- err
	}()
	go func() {
		<-start
		closed <- r.Close(id)
	}()
	close(start)
	held.Release()

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("close/reopen race did not drain")
	}
	select {
	case err := <-reopened:
		// Either linearization is valid: Reopen may complete immediately before
		// Close, or it may observe Close and return an unknown-id error. The
		// invariant is that both settle and no generation remains acquirable.
		_ = err
	case <-time.After(5 * time.Second):
		t.Fatal("reopen did not settle after close")
	}
	if f, ok := r.Get(id); ok || f != nil {
		if f != nil {
			f.Release()
		}
		t.Fatal("closed id remained acquirable")
	}
}
