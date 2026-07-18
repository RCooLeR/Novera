package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type blockingSelfWriteNotifier struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (n *blockingSelfWriteNotifier) Suppress(string) {
	n.mu.Lock()
	n.calls++
	if n.calls == 1 {
		close(n.entered)
	}
	n.mu.Unlock()
	<-n.release
}

func TestWriteFileSerializesRevisionCheckAndCommit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}
	notifier := &blockingSelfWriteNotifier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	WireSelfWriteNotifier(s, notifier)
	expectedRevision := revisionOfFile(path)

	type result struct {
		write WriteResult
		err   error
	}
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		write, err := s.WriteFile("notes.txt", "first", expectedRevision, "utf-8")
		firstDone <- result{write: write, err: err}
	}()
	select {
	case <-notifier.entered: // first call is between its revision check and commit
	case <-time.After(time.Second):
		t.Fatal("first write did not reach the pre-commit notifier")
	}
	go func() {
		write, err := s.WriteFile("notes.txt", "second", expectedRevision, "utf-8")
		secondDone <- result{write: write, err: err}
	}()

	// Give the second goroutine a chance to contend while the first is paused.
	// The service lock must keep it before the revision check/notifier.
	time.Sleep(20 * time.Millisecond)
	close(notifier.release)

	first := <-firstDone
	second := <-secondDone
	if first.err != nil {
		t.Fatalf("first write failed: %v", first.err)
	}
	if !errors.Is(second.err, ErrStale) {
		t.Fatalf("second write error = %v, want ErrStale", second.err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first" {
		t.Fatalf("final file = %q, want first successful snapshot", got)
	}
	if first.write.Revision != revisionOfFile(path) {
		t.Fatalf("successful revision = %q, disk revision = %q", first.write.Revision, revisionOfFile(path))
	}
}

func TestWriteFileEmptyRevisionCannotOverwriteFileCreatedByAnotherWriter(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "draft.txt")
	if err := os.WriteFile(path, []byte("external content"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}

	if _, err := s.WriteFile("draft.txt", "local draft", "", "utf-8"); !errors.Is(err, ErrStale) {
		t.Fatalf("write error = %v, want ErrStale", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "external content" {
		t.Fatalf("existing file was overwritten: %q", got)
	}
}

func TestWriteFileEmptyRevisionCreatesStillAbsentFile(t *testing.T) {
	root := t.TempDir()
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatal(err)
	}

	result, err := s.WriteFile("draft.txt", "local draft", "", "utf-8")
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision == "" {
		t.Fatal("created file has no revision")
	}
	got, err := os.ReadFile(filepath.Join(root, "draft.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "local draft" {
		t.Fatalf("created file = %q", got)
	}
}

func TestQueuedWriteNeverResolvesAgainstNewWorkspace(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	pathA := filepath.Join(rootA, "notes.txt")
	pathB := filepath.Join(rootB, "notes.txt")
	for _, path := range []string{pathA, pathB} {
		if err := os.WriteFile(path, []byte("same original"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := New()
	if _, err := s.Open(rootA); err != nil {
		t.Fatal(err)
	}
	notifier := &blockingSelfWriteNotifier{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	WireSelfWriteNotifier(s, notifier)
	capturedRoot, capturedGeneration := s.workspaceSnapshot()
	expectedRevision := revisionOfFile(pathA)

	type result struct {
		write WriteResult
		err   error
	}
	firstDone := make(chan result, 1)
	secondDone := make(chan result, 1)
	go func() {
		write, err := s.writeFileInWorkspace(capturedRoot, capturedGeneration, "notes.txt", "first A save", expectedRevision, "utf-8")
		firstDone <- result{write: write, err: err}
	}()
	select {
	case <-notifier.entered:
	case <-time.After(time.Second):
		t.Fatal("first write did not reach the pre-commit notifier")
	}
	go func() {
		write, err := s.writeFileInWorkspace(capturedRoot, capturedGeneration, "notes.txt", "queued A save", expectedRevision, "utf-8")
		secondDone <- result{write: write, err: err}
	}()

	if _, err := s.Open(rootB); err != nil {
		t.Fatal(err)
	}
	close(notifier.release)
	if first := <-firstDone; first.err != nil {
		t.Fatalf("already-started A write failed: %v", first.err)
	}
	if second := <-secondDone; !errors.Is(second.err, ErrWorkspaceChanged) {
		t.Fatalf("queued A write error = %v, want ErrWorkspaceChanged", second.err)
	}

	gotB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotB) != "same original" {
		t.Fatalf("queued A write mutated workspace B: %q", gotB)
	}
	gotA, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "first A save" {
		t.Fatalf("already-started A write did not stay bound to A: %q", gotA)
	}
}
