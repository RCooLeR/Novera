package bigfile

import (
	"errors"
	"fmt"
	"sync"
)

const csvGridCursorLimit = 8_192

var ErrCSVGridCursorInvalid = errors.New("CSV grid cursor was not issued for this file generation and delimiter")

type csvGridCursorKey struct {
	fileID     string
	generation uint64
	delimiter  rune
	offset     int64
}

// csvGridCursorRegistry authenticates raw-offset continuations without placing
// an unbounded cursor history in the service. Offset zero is the only
// caller-constructible cursor; every nonzero offset must have been issued from
// a parser-confirmed logical-record boundary for the same file generation and
// delimiter.
type csvGridCursorRegistry struct {
	mu    sync.Mutex
	keys  map[csvGridCursorKey]struct{}
	order []csvGridCursorKey
	next  int
}

func (r *csvGridCursorRegistry) validate(fileID string, generation uint64, delimiter rune, offset int64) error {
	if offset == 0 {
		return nil
	}
	key := csvGridCursorKey{fileID: fileID, generation: generation, delimiter: delimiter, offset: offset}
	r.mu.Lock()
	_, ok := r.keys[key]
	r.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: raw byte %d", ErrCSVGridCursorInvalid, offset)
	}
	return nil
}

func (r *csvGridCursorRegistry) issue(fileID string, generation uint64, delimiter rune, offset int64) {
	if offset <= 0 {
		return
	}
	key := csvGridCursorKey{fileID: fileID, generation: generation, delimiter: delimiter, offset: offset}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.keys == nil {
		r.keys = make(map[csvGridCursorKey]struct{})
	}
	if _, exists := r.keys[key]; exists {
		return
	}
	r.keys[key] = struct{}{}
	if len(r.order) < csvGridCursorLimit {
		r.order = append(r.order, key)
		return
	}
	if r.next < 0 || r.next >= len(r.order) {
		r.next = 0
	}
	evicted := r.order[r.next]
	delete(r.keys, evicted)
	r.order[r.next] = key
	r.next = (r.next + 1) % csvGridCursorLimit
}

func (r *csvGridCursorRegistry) invalidateFile(fileID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.order) == 0 {
		return
	}
	kept := r.order[:0]
	for _, key := range r.order {
		if key.fileID == fileID {
			delete(r.keys, key)
			continue
		}
		kept = append(kept, key)
	}
	r.order = kept
	r.next = 0
}
