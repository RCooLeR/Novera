// Package session tracks files opened in the editor. Each open file keeps a
// streaming document handle plus its background-indexing lifecycle. The
// registry hands out opaque ids so the frontend never holds a Go pointer.
package session

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/manualedit"
)

// ErrStagedEdits prevents a refresh from silently replacing a document
// generation while its edit session still owns unsaved changes.
var ErrStagedEdits = errors.New("session: file has staged edits")

// File is a lease on one immutable document generation. Callers must Release
// every File returned by Open, Reopen, Get, or GetEdit. Close and Reopen wait
// for all leases on the old generation before closing its descriptor.
//
// Edit is only populated by GetEdit. That lease additionally owns the file's
// single-writer edit lock, which makes manualedit.Session safe across
// concurrent bridge calls without unnecessarily serialising read-only work.
type File struct {
	ID         string
	Doc        *document.FileDocument
	Path       string
	Generation uint64
	Edit       *manualedit.Session

	entry       *entry
	editLocked  bool
	releaseOnce sync.Once
}

// Release relinquishes this operation's ownership of the document generation.
// It is safe to call more than once.
func (f *File) Release() {
	if f == nil || f.entry == nil {
		return
	}
	f.releaseOnce.Do(func() {
		if f.editLocked {
			f.entry.editMu.Unlock()
		}
		f.entry.mu.Lock()
		f.entry.leases--
		if f.entry.leases < 0 {
			panic("session: negative lease count")
		}
		f.entry.cond.Broadcast()
		f.entry.mu.Unlock()
	})
}

// EditSession returns the staging session, creating it on first use. It is
// valid only on a lease obtained through Registry.GetEdit.
func (f *File) EditSession() *manualedit.Session {
	if f == nil || !f.editLocked {
		panic("session: EditSession requires an edit lease")
	}
	if f.entry.edit == nil {
		f.entry.edit = manualedit.NewSession(f.Doc.Size(), manualedit.DefaultMaxInsertedBytes)
	}
	f.Edit = f.entry.edit
	return f.Edit
}

// ResetEdits discards all staged edits. It is valid only on an edit lease.
func (f *File) ResetEdits() {
	if f == nil || !f.editLocked {
		panic("session: ResetEdits requires an edit lease")
	}
	f.entry.edit = nil
	f.Edit = nil
}

type entry struct {
	mu   sync.Mutex
	cond *sync.Cond

	id         string
	path       string
	doc        *document.FileDocument
	generation uint64
	leases     int
	transition bool
	closing    bool

	editMu sync.Mutex
	edit   *manualedit.Session

	cancelIndex context.CancelFunc
	indexWG     sync.WaitGroup
}

func newEntry(id, path string, doc *document.FileDocument) *entry {
	e := &entry{id: id, path: path, doc: doc, generation: 1}
	e.cond = sync.NewCond(&e.mu)
	return e
}

// Registry tracks open files by id. Safe for concurrent use.
type Registry struct {
	mu    sync.RWMutex
	files map[string]*entry
	seq   int64
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{files: make(map[string]*entry)}
}

// Open opens path as a streaming document and registers it under a fresh id.
// The returned lease owns the initial generation until Release is called.
func (r *Registry) Open(path string) (*File, error) {
	doc, err := document.OpenFile(path)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	r.seq++
	e := newEntry(fmt.Sprintf("f%d", r.seq), path, doc)
	e.mu.Lock()
	e.startIndexingLocked()
	e.leases = 1
	f := e.snapshotLocked(false)
	e.mu.Unlock()
	r.files[e.id] = e
	r.mu.Unlock()
	return f, nil
}

// Reopen reloads the document from disk under the same id, discarding edits
// and the chunk cache. New acquisitions wait while the generation changes;
// the old descriptor is closed only after its active operations drain.
func (r *Registry) Reopen(id string) (*File, error) {
	return r.reopen(id, false)
}

// ReopenIfClean reloads the document only when no edits are staged. The clean
// check and generation transition are one registry operation, so a concurrent
// StageEdit cannot slip between a caller-side check and the destructive reopen.
func (r *Registry) ReopenIfClean(id string) (*File, error) {
	return r.reopen(id, true)
}

func (r *Registry) reopen(id string, requireClean bool) (*File, error) {
	e, ok := r.lookup(id)
	if !ok {
		return nil, fmt.Errorf("session: unknown file id %q", id)
	}

	e.mu.Lock()
	for e.transition && !e.closing {
		e.cond.Wait()
	}
	if e.closing {
		e.mu.Unlock()
		return nil, fmt.Errorf("session: unknown file id %q", id)
	}
	e.transition = true
	path := e.path
	e.mu.Unlock()

	doc, openErr := document.OpenFile(path)
	if openErr != nil {
		e.mu.Lock()
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		return nil, openErr
	}

	e.mu.Lock()
	for e.leases > 0 && !e.closing {
		e.cond.Wait()
	}
	if e.closing {
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		_ = doc.Close()
		return nil, fmt.Errorf("session: unknown file id %q", id)
	}
	if requireClean {
		e.editMu.Lock()
		dirty := e.edit != nil && e.edit.HasEdits()
		e.editMu.Unlock()
		if dirty {
			e.transition = false
			e.cond.Broadcast()
			e.mu.Unlock()
			_ = doc.Close()
			return nil, ErrStagedEdits
		}
	}
	cancel := e.cancelIndex
	e.cancelIndex = nil
	old := e.doc
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	e.indexWG.Wait()
	if old != nil {
		_ = old.Close()
	}

	e.mu.Lock()
	if e.closing {
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		_ = doc.Close()
		return nil, fmt.Errorf("session: unknown file id %q", id)
	}
	e.doc = doc
	e.editMu.Lock()
	e.edit = nil
	e.editMu.Unlock()
	e.generation++
	e.startIndexingLocked()
	e.leases = 1
	f := e.snapshotLocked(false)
	e.transition = false
	e.cond.Broadcast()
	e.mu.Unlock()
	return f, nil
}

// Get acquires a read-only operation lease for id.
func (r *Registry) Get(id string) (*File, bool) {
	e, ok := r.lookup(id)
	if !ok {
		return nil, false
	}
	e.mu.Lock()
	for e.transition && !e.closing {
		e.cond.Wait()
	}
	if e.closing || e.doc == nil {
		e.mu.Unlock()
		return nil, false
	}
	e.leases++
	f := e.snapshotLocked(false)
	e.mu.Unlock()
	return f, true
}

// GetEdit acquires an operation lease and the file's single-writer staging
// lock. Read-only calls can continue concurrently on the same document.
func (r *Registry) GetEdit(id string) (*File, bool) {
	f, ok := r.Get(id)
	if !ok {
		return nil, false
	}
	f.entry.editMu.Lock()
	f.editLocked = true
	f.Edit = f.entry.edit
	return f, true
}

// Close prevents new acquisitions, waits for every active operation and the
// indexer to finish, then closes the exact generation removed from the map.
func (r *Registry) Close(id string) error {
	r.mu.Lock()
	e, ok := r.files[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("session: unknown file id %q", id)
	}
	e.mu.Lock()
	e.closing = true
	delete(r.files, id)
	r.mu.Unlock()
	for e.leases > 0 || e.transition {
		e.cond.Wait()
	}
	cancel := e.cancelIndex
	e.cancelIndex = nil
	doc := e.doc
	e.doc = nil
	e.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	e.indexWG.Wait()
	if doc != nil {
		return doc.Close()
	}
	return nil
}

func (r *Registry) lookup(id string) (*entry, bool) {
	r.mu.RLock()
	e, ok := r.files[id]
	r.mu.RUnlock()
	return e, ok
}

func (e *entry) snapshotLocked(edit bool) *File {
	f := &File{
		ID:         e.id,
		Doc:        e.doc,
		Path:       e.path,
		Generation: e.generation,
		entry:      e,
		editLocked: edit,
	}
	if edit {
		f.Edit = e.edit
	}
	return f
}

// startIndexingLocked builds the sparse line index in the background.
// Navigation by byte offset works immediately; indexing errors are non-fatal.
func (e *entry) startIndexingLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	doc := e.doc
	e.cancelIndex = cancel
	e.indexWG.Add(1)
	go func() {
		defer e.indexWG.Done()
		_ = doc.StartIndexing(ctx)
	}()
}
