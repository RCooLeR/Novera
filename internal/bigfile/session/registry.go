// Package session tracks files opened in the editor. Each open file keeps a
// streaming document handle plus its background-indexing lifecycle. The
// registry hands out opaque ids so the frontend never holds a Go pointer.
package session

import (
	"context"
	"fmt"
	"sync"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/manualedit"
)

// File is one open document and its lifecycle state.
type File struct {
	ID   string
	Doc  *document.FileDocument
	Path string
	Edit *manualedit.Session // staged edits; lazily created on first edit

	cancelIndex context.CancelFunc
}

// EditSession returns the staging session, creating it on first use.
func (f *File) EditSession() *manualedit.Session {
	if f.Edit == nil {
		f.Edit = manualedit.NewSession(f.Doc.Size(), manualedit.DefaultMaxInsertedBytes)
	}
	return f.Edit
}

// ResetEdits discards all staged edits.
func (f *File) ResetEdits() {
	f.Edit = nil
}

// Registry tracks open files by id. Safe for concurrent use.
type Registry struct {
	mu    sync.Mutex
	files map[string]*File
	seq   int64
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{files: make(map[string]*File)}
}

// Open opens path as a streaming document and registers it under a fresh id.
func (r *Registry) Open(path string) (*File, error) {
	doc, err := document.OpenFile(path)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	f := &File{ID: fmt.Sprintf("f%d", r.seq), Doc: doc, Path: path}
	r.files[f.ID] = f
	return f, nil
}

// Reopen reloads the document from disk under the same id, discarding edits and
// the chunk cache. Use after an in-place write so reads see fresh bytes.
func (r *Registry) Reopen(id string) (*File, error) {
	r.mu.Lock()
	f, ok := r.files[id]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("session: unknown file id %q", id)
	}
	doc, err := document.OpenFile(f.Path)
	if err != nil {
		return nil, err
	}
	old := f.Doc
	f.Doc = doc
	f.ResetEdits()
	if old != nil {
		_ = old.Close()
	}
	f.StartIndexing()
	return f, nil
}

// Get returns the file for id.
func (r *Registry) Get(id string) (*File, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.files[id]
	return f, ok
}

// Close cancels indexing, closes the document, and forgets the id.
func (r *Registry) Close(id string) error {
	r.mu.Lock()
	f, ok := r.files[id]
	if ok {
		delete(r.files, id)
	}
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("session: unknown file id %q", id)
	}
	if f.cancelIndex != nil {
		f.cancelIndex()
	}
	return f.Doc.Close()
}

// StartIndexing builds the sparse line index in the background. Navigation by
// byte offset works immediately; exact line numbers become available once this
// completes. Errors are non-fatal (approximate navigation still works).
func (f *File) StartIndexing() {
	ctx, cancel := context.WithCancel(context.Background())
	f.cancelIndex = cancel
	go func() { _ = f.Doc.StartIndexing(ctx) }()
}
