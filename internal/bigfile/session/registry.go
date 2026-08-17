// Package session tracks files opened in the editor. Each open file keeps a
// streaming document handle plus its background-indexing lifecycle. Registry
// leases prevent close, refresh, and shutdown from closing a document while an
// RPC is still using that exact generation.
package session

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/manualedit"
)

var (
	// ErrStagedEdits prevents refresh from silently replacing a generation
	// while its edit session still owns unsaved changes.
	ErrStagedEdits             = errors.New("session: file has staged edits")
	ErrUnknownFile             = errors.New("session: unknown file id")
	ErrFileClosing             = errors.New("session: file is closing")
	ErrTransitioning           = errors.New("session: file lifecycle transition is in progress")
	ErrInvalidTransition       = errors.New("session: invalid lifecycle transition")
	ErrOpenFileLimit           = errors.New("session: open file limit reached")
	ErrOpenInFlightLimit       = errors.New("session: concurrent open limit reached")
	ErrFileIDSequenceExhausted = errors.New("session: file id sequence is exhausted")
	ErrFileGenerationExhausted = errors.New("session: file generation sequence is exhausted")
	ErrRegistryStopped         = errors.New("session: registry is shutting down")
)

const (
	// These bounds cover retained descriptors, per-document caches, indexers,
	// and native opens that have not yet reached registry admission.
	DefaultMaxOpenFiles       = 128
	DefaultMaxConcurrentOpens = 4

	// Generations cross the JSON bridge as numbers. Values above this limit
	// cannot be compared exactly by JavaScript.
	MaxBridgeFileGeneration uint64 = 1<<53 - 1
)

// File is a lease on one immutable document generation. Callers must Release
// every File returned by Open, Reopen, Get, or GetEdit.
//
// Edit is populated by GetEdit. That lease additionally owns the file's
// single-writer edit lock, making manualedit.Session safe across concurrent
// bridge calls without serializing unrelated document reads.
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

// EditSession returns the staging session, creating a legacy size-bound
// session on first use. Production FileService staging installs an exact
// source-bound session with InstallEditSession before accepting edited bytes.
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

// InstallEditSession installs the first exact source-bound staging session.
// It is valid only on a GetEdit lease. A concurrently installed session cannot
// occur because edit leases are single-writer, but the existing value is
// returned defensively so callers never replace staged state.
func (f *File) InstallEditSession(edit *manualedit.Session) (*manualedit.Session, error) {
	if f == nil || !f.editLocked {
		return nil, errors.New("session: InstallEditSession requires an edit lease")
	}
	if edit == nil {
		return nil, errors.New("session: edit session is required")
	}
	if f.entry.edit == nil {
		f.entry.edit = edit
	}
	f.Edit = f.entry.edit
	return f.Edit, nil
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
	mu          sync.Mutex
	files       map[string]*entry
	seq         int64
	maxOpen     int
	maxOpening  int
	openCount   int
	opening     int
	openingDone chan struct{}

	stopping     bool
	shutdownDone chan struct{}
	shutdownErr  error

	// Detached close handles still consume retained-open slots until Finish
	// closes their exact descriptors.
	closing map[*CloseHandle]struct{}

	// candidateTransitionHook is an internal deterministic-test seam. Tests set
	// it before starting concurrent opens and never mutate it afterwards.
	candidateTransitionHook func()
}

// New returns an empty registry with production resource limits.
func New() *Registry { return NewWithLimit(DefaultMaxOpenFiles) }

// NewWithLimit constructs a registry with a bounded retained-file limit.
func NewWithLimit(maxOpen int) *Registry {
	if maxOpen <= 0 {
		maxOpen = DefaultMaxOpenFiles
	}
	maxOpening := DefaultMaxConcurrentOpens
	if maxOpening > maxOpen {
		maxOpening = maxOpen
	}
	return &Registry{
		files:      make(map[string]*entry),
		closing:    make(map[*CloseHandle]struct{}),
		maxOpen:    maxOpen,
		maxOpening: maxOpening,
	}
}

// Open opens path as a streaming document. Aliases of an already retained
// regular file are identity-deduplicated and return a lease on the existing
// session instead of consuming another descriptor/cache/indexer slot.
func (r *Registry) Open(path string) (*File, error) {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil, ErrRegistryStopped
	}
	if r.opening >= r.maxOpening {
		limit := r.maxOpening
		r.mu.Unlock()
		return nil, fmt.Errorf("%w (%d)", ErrOpenInFlightLimit, limit)
	}
	r.opening++
	r.mu.Unlock()
	defer r.finishOpening()

	doc, err := document.OpenFile(path)
	if err != nil {
		return nil, err
	}
	candidateInfo, err := doc.OpenedFileInfo()
	if err != nil {
		_ = doc.Close()
		return nil, fmt.Errorf("session: stat opened file %q: %w", path, err)
	}

	for {
		r.mu.Lock()
		if r.stopping {
			r.mu.Unlock()
			return nil, errors.Join(ErrRegistryStopped, doc.Close())
		}

		var transitioning *entry
		for _, e := range r.files {
			e.mu.Lock()
			available := !e.closing && e.doc != nil
			if available {
				existingInfo, statErr := e.doc.OpenedFileInfo()
				if statErr != nil {
					e.mu.Unlock()
					r.mu.Unlock()
					return nil, errors.Join(
						fmt.Errorf("session: stat existing opened file %q: %w", e.path, statErr),
						doc.Close(),
					)
				}
				if os.SameFile(existingInfo, candidateInfo) {
					if e.transition {
						if r.candidateTransitionHook != nil {
							r.candidateTransitionHook()
						}
						transitioning = e
						e.mu.Unlock()
						break
					}
					e.leases++
					f := e.snapshotLocked(false)
					e.mu.Unlock()
					r.mu.Unlock()
					if closeErr := doc.Close(); closeErr != nil {
						f.Release()
						return nil, fmt.Errorf("session: close redundant handle for %q: %w", path, closeErr)
					}
					return f, nil
				}
			}
			e.mu.Unlock()
		}
		if transitioning != nil {
			r.mu.Unlock()

			// Keep the candidate descriptor and its captured identity alive while
			// the matching entry changes generation. Once the barrier clears,
			// restart the registry scan and compare that same candidate identity
			// with the newly retained descriptors. The path may now name a
			// different file, so returning Get(entry.id) here would be unsafe.
			transitioning.mu.Lock()
			for transitioning.transition && !transitioning.closing {
				transitioning.cond.Wait()
			}
			transitioning.mu.Unlock()
			continue
		}
		if r.openCount >= r.maxOpen {
			limit := r.maxOpen
			r.mu.Unlock()
			return nil, errors.Join(fmt.Errorf("%w (%d)", ErrOpenFileLimit, limit), doc.Close())
		}
		if r.seq == math.MaxInt64 {
			r.mu.Unlock()
			return nil, errors.Join(ErrFileIDSequenceExhausted, doc.Close())
		}

		r.seq++
		e := newEntry(fmt.Sprintf("f%d", r.seq), path, doc)
		e.mu.Lock()
		e.startIndexingLocked()
		e.leases = 1
		f := e.snapshotLocked(false)
		e.mu.Unlock()
		r.files[e.id] = e
		r.openCount++
		r.mu.Unlock()
		return f, nil
	}
}

func (r *Registry) finishOpening() {
	r.mu.Lock()
	if r.opening > 0 {
		r.opening--
	}
	if r.stopping && r.opening == 0 && r.openingDone != nil {
		close(r.openingDone)
		r.openingDone = nil
	}
	r.mu.Unlock()
}

// Reopen reloads the document from disk under the same id.
func (r *Registry) Reopen(id string) (*File, error) {
	transition, err := r.BeginTransition(id)
	if err != nil {
		return nil, err
	}
	return transition.Reopen(false)
}

// ReopenIfClean reloads only when no edits are staged.
func (r *Registry) ReopenIfClean(id string) (*File, error) {
	transition, err := r.BeginTransition(id)
	if err != nil {
		return nil, err
	}
	return transition.Reopen(true)
}

// Transition is a two-phase refresh barrier. BeginTransition immediately
// blocks new leases; callers may then close registration in service-owned job
// systems, cancel existing work, and only afterwards wait for leases to drain.
type Transition struct {
	entry *entry
	id    string
	once  sync.Once
}

func (r *Registry) BeginTransition(id string) (*Transition, error) {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil, ErrRegistryStopped
	}
	e, ok := r.files[id]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w %q", ErrUnknownFile, id)
	}
	e.mu.Lock()
	r.mu.Unlock()
	defer e.mu.Unlock()
	if e.closing {
		return nil, fmt.Errorf("%w %q", ErrFileClosing, id)
	}
	if e.transition {
		return nil, fmt.Errorf("%w for %q", ErrTransitioning, id)
	}
	e.transition = true
	e.cond.Broadcast()
	return &Transition{entry: e, id: id}, nil
}

// Abort releases an unused transition. It is idempotent.
func (t *Transition) Abort() {
	if t == nil || t.entry == nil {
		return
	}
	t.once.Do(func() {
		t.entry.mu.Lock()
		if t.entry.transition {
			t.entry.transition = false
			t.entry.cond.Broadcast()
		}
		t.entry.mu.Unlock()
	})
}

// Reopen drains old leases and installs one new generation. The returned File
// is the caller's lease on that generation.
func (t *Transition) Reopen(requireClean bool) (result *File, retErr error) {
	if t == nil || t.entry == nil {
		return nil, ErrInvalidTransition
	}
	ran := false
	t.once.Do(func() {
		ran = true
		result, retErr = t.reopen(requireClean)
	})
	if !ran {
		return nil, ErrInvalidTransition
	}
	return result, retErr
}

func (t *Transition) reopen(requireClean bool) (*File, error) {
	e := t.entry
	clearTransition := func() {
		e.mu.Lock()
		if e.transition {
			e.transition = false
			e.cond.Broadcast()
		}
		e.mu.Unlock()
	}

	e.mu.Lock()
	for e.leases > 0 && !e.closing {
		e.cond.Wait()
	}
	if e.closing {
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		return nil, fmt.Errorf("%w %q", ErrFileClosing, t.id)
	}
	if requireClean {
		e.editMu.Lock()
		dirty := e.edit != nil && e.edit.HasEdits()
		e.editMu.Unlock()
		if dirty {
			e.transition = false
			e.cond.Broadcast()
			e.mu.Unlock()
			return nil, ErrStagedEdits
		}
	}
	if e.generation >= MaxBridgeFileGeneration {
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		return nil, fmt.Errorf("%w for %q", ErrFileGenerationExhausted, t.id)
	}
	path := e.path
	oldGeneration := e.generation
	e.mu.Unlock()

	doc, err := document.OpenFile(path)
	if err != nil {
		clearTransition()
		return nil, err
	}

	e.mu.Lock()
	if e.closing {
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		_ = doc.Close()
		return nil, fmt.Errorf("%w %q", ErrFileClosing, t.id)
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
		e.doc = nil
		e.transition = false
		e.cond.Broadcast()
		e.mu.Unlock()
		_ = doc.Close()
		return nil, fmt.Errorf("%w %q", ErrFileClosing, t.id)
	}
	e.doc = doc
	e.editMu.Lock()
	e.edit = nil
	e.editMu.Unlock()
	e.generation = oldGeneration + 1
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
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil, false
	}
	e, ok := r.files[id]
	if !ok {
		r.mu.Unlock()
		return nil, false
	}
	e.mu.Lock()
	r.mu.Unlock()
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

// IsCurrentGeneration performs a non-blocking lifecycle-barrier check without
// acquiring a lease. Callers that publish service-owned work while holding
// their own registry mutex use this as the second half of a handshake:
// barriers established before this check are rejected, while barriers
// established afterwards must wait for that same service mutex before their
// cancellation pass can complete.
func (r *Registry) IsCurrentGeneration(id string, generation uint64) bool {
	if r == nil || generation == 0 {
		return false
	}
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return false
	}
	e, ok := r.files[id]
	if !ok {
		r.mu.Unlock()
		return false
	}
	e.mu.Lock()
	r.mu.Unlock()
	current := !e.transition && !e.closing && e.doc != nil && e.generation == generation
	e.mu.Unlock()
	return current
}

// GetEdit acquires an operation lease and the file's staging lock.
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

// CloseHandle owns the detached second half of Close. Finish waits for every
// existing lease/transition, joins the indexer, and closes the exact retained
// descriptor. It is safe to call concurrently or repeatedly.
type CloseHandle struct {
	registry *Registry
	entry    *entry
	id       string
	once     sync.Once
	err      error
}

func (r *Registry) newCloseHandleLocked(e *entry, id string) *CloseHandle {
	h := &CloseHandle{registry: r, entry: e, id: id}
	r.closing[h] = struct{}{}
	return h
}

// BeginClose removes id and rejects new leases without waiting. Service code
// uses this barrier before canceling registered work.
func (r *Registry) BeginClose(id string) (*CloseHandle, error) {
	r.mu.Lock()
	if r.stopping {
		r.mu.Unlock()
		return nil, ErrRegistryStopped
	}
	e, ok := r.files[id]
	if !ok {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w %q", ErrUnknownFile, id)
	}
	e.mu.Lock()
	e.closing = true
	delete(r.files, id)
	e.cond.Broadcast()
	e.mu.Unlock()
	h := r.newCloseHandleLocked(e, id)
	r.mu.Unlock()
	e.cancelIndexing()
	return h, nil
}

func (h *CloseHandle) Finish() error {
	if h == nil || h.entry == nil {
		return ErrInvalidTransition
	}
	h.once.Do(func() {
		defer h.releaseOpenSlot()
		e := h.entry
		e.mu.Lock()
		for e.leases > 0 || e.transition {
			e.cond.Wait()
		}
		doc := e.doc
		e.doc = nil
		e.editMu.Lock()
		e.edit = nil
		e.editMu.Unlock()
		e.mu.Unlock()

		e.indexWG.Wait()
		if doc != nil {
			h.err = doc.Close()
		}
	})
	return h.err
}

func (h *CloseHandle) releaseOpenSlot() {
	if h.registry == nil {
		return
	}
	h.registry.mu.Lock()
	if h.registry.openCount > 0 {
		h.registry.openCount--
	}
	delete(h.registry.closing, h)
	h.registry.mu.Unlock()
}

func (r *Registry) Close(id string) error {
	h, err := r.BeginClose(id)
	if err != nil {
		return err
	}
	return h.Finish()
}

// BeginShutdown raises an irreversible admission barrier, detaches every ID,
// synchronously cancels all indexers, and starts bounded lease-drain workers.
// The returned channel closes only after retained descriptors and opens that
// were already in flight have settled.
func (r *Registry) BeginShutdown() <-chan struct{} {
	r.mu.Lock()
	if r.stopping {
		done := r.shutdownDone
		r.mu.Unlock()
		return done
	}
	r.stopping = true
	r.shutdownDone = make(chan struct{})
	done := r.shutdownDone

	openDone := make(chan struct{})
	if r.opening == 0 {
		close(openDone)
	} else {
		r.openingDone = openDone
	}
	for id, e := range r.files {
		e.mu.Lock()
		e.closing = true
		e.cond.Broadcast()
		e.mu.Unlock()
		r.newCloseHandleLocked(e, id)
		delete(r.files, id)
	}
	handles := make([]*CloseHandle, 0, len(r.closing))
	for h := range r.closing {
		handles = append(handles, h)
	}
	r.mu.Unlock()

	for _, h := range handles {
		h.entry.cancelIndexing()
	}

	go func() {
		errs := make(chan error, len(handles))
		var wg sync.WaitGroup
		for _, h := range handles {
			h := h
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := h.Finish(); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		<-openDone
		close(errs)
		var shutdownErr error
		for err := range errs {
			shutdownErr = errors.Join(shutdownErr, err)
		}
		r.mu.Lock()
		r.shutdownErr = shutdownErr
		close(done)
		r.mu.Unlock()
	}()
	return done
}

// Shutdown begins irreversible shutdown and waits only as long as ctx permits.
// Drain workers continue after a deadline so released leases are still closed.
func (r *Registry) Shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := r.BeginShutdown()
	select {
	case <-done:
		r.mu.Lock()
		err := r.shutdownErr
		r.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) CloseAll(ctx context.Context) error { return r.Shutdown(ctx) }

// Paths returns stable path spellings for the currently registered sessions.
func (r *Registry) Paths() []string {
	r.mu.Lock()
	entries := make([]*entry, 0, len(r.files))
	for _, e := range r.files {
		entries = append(entries, e)
	}
	paths := make([]string, 0, len(entries))
	for _, e := range entries {
		e.mu.Lock()
		if !e.closing && e.doc != nil {
			paths = append(paths, e.path)
		}
		e.mu.Unlock()
	}
	r.mu.Unlock()
	return paths
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

func (e *entry) cancelIndexing() {
	if e == nil {
		return
	}
	e.mu.Lock()
	cancel := e.cancelIndex
	e.cancelIndex = nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
