package agent

import (
	"fmt"
	"os"
	"sync"
	"time"

	"novera/internal/workspace"
)

// rollbackMaxFileBytes caps the size of a file we snapshot for undo. A mutation
// touching a larger file is still performed, but isn't journaled (rollback would
// otherwise pin large buffers in memory); the tool result notes this.
const rollbackMaxFileBytes = 8 << 20 // 8 MiB

// rollbackMaxEntries bounds the in-memory undo ring.
const rollbackMaxEntries = 50

// fileSnap is the prior state of one path before a mutation: its bytes, or a
// marker that it did not exist (so undo deletes a created file).
type fileSnap struct {
	path    string
	existed bool
	data    []byte
}

// RollbackEntry is one undoable mutation, surfaced to the model via list_rollbacks.
type RollbackEntry struct {
	ID    string   `json:"id"`
	Time  string   `json:"time"`
	Tool  string   `json:"tool"`
	Label string   `json:"label"`
	Files []string `json:"files"`

	snaps      []fileSnap // prior states, restored on rollback (not serialized)
	incomplete bool       // a touched file was too large to snapshot
}

// rollbackJournal is an in-memory ring of recent mutation snapshots so the agent
// can undo file changes it made. It is intentionally session-scoped (lost on app
// restart) — an undo facility, not durable version history.
type rollbackJournal struct {
	mu      sync.Mutex
	entries []RollbackEntry // oldest..newest
	seq     int
}

func newRollbackJournal() *rollbackJournal { return &rollbackJournal{} }

// snapshot records the current state of paths as a new undo point and returns
// it. Call it BEFORE performing the mutation. Best-effort: an unreadable or
// oversized file is skipped (and the entry flagged incomplete) rather than
// aborting the mutation.
func (j *rollbackJournal) snapshot(ws *workspace.Service, tool, label string, paths ...string) RollbackEntry {
	snaps := make([]fileSnap, 0, len(paths))
	display := make([]string, 0, len(paths))
	incomplete := false
	for _, p := range paths {
		data, existed, err := ws.ReadRaw(p)
		if err != nil {
			incomplete = true
			continue
		}
		if existed && len(data) > rollbackMaxFileBytes {
			incomplete = true
			display = append(display, p)
			continue // too big to hold for undo
		}
		snaps = append(snaps, fileSnap{path: p, existed: existed, data: data})
		display = append(display, p)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e := RollbackEntry{
		ID:         fmt.Sprintf("rb-%d", j.seq),
		Time:       time.Now().Format(time.RFC3339),
		Tool:       tool,
		Label:      label,
		Files:      display,
		snaps:      snaps,
		incomplete: incomplete,
	}
	j.entries = append(j.entries, e)
	if len(j.entries) > rollbackMaxEntries {
		j.entries = j.entries[len(j.entries)-rollbackMaxEntries:]
	}
	return e
}

// list returns undo points, newest first.
func (j *rollbackJournal) list() []RollbackEntry {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]RollbackEntry, len(j.entries))
	copy(out, j.entries)
	for i, k := 0, len(out)-1; i < k; i, k = i+1, k-1 {
		out[i], out[k] = out[k], out[i]
	}
	return out
}

// rollback restores the snapshot with id (recreating, overwriting, or deleting
// each recorded path to its prior state) and removes the entry on success.
func (j *rollbackJournal) rollback(ws *workspace.Service, id string) (RollbackEntry, error) {
	j.mu.Lock()
	idx := -1
	for i := range j.entries {
		if j.entries[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		j.mu.Unlock()
		return RollbackEntry{}, fmt.Errorf("no rollback point %q (it may have aged out of the undo history)", id)
	}
	e := j.entries[idx]
	j.mu.Unlock()

	for _, s := range e.snaps {
		if s.existed {
			if err := ws.WriteRaw(s.path, s.data); err != nil {
				return RollbackEntry{}, err
			}
		} else if err := ws.Delete(s.path); err != nil && !os.IsNotExist(err) {
			return RollbackEntry{}, err
		}
	}

	j.mu.Lock()
	for i := range j.entries {
		if j.entries[i].ID == id {
			j.entries = append(j.entries[:i], j.entries[i+1:]...)
			break
		}
	}
	j.mu.Unlock()
	return e, nil
}
