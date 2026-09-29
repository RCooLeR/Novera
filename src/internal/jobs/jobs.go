// Package jobs is a small in-process ledger of long-running background tasks
// (agent runs, dump imports, …). It records each job's lifecycle
// (running/success/failed/canceled/timeout), a bounded log, and a cancel hook,
// and emits a "jobs:changed" event so the UI can live-update. Producers call the
// package-level Start/Append/Finish API; the frontend reads ListJobs/GetJob and
// can invoke the consumer methods exposed on Service.
package jobs

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// EventChanged is emitted (no payload) whenever any job's state changes; the
// frontend re-fetches ListJobs in response.
const EventChanged = "jobs:changed"

const (
	maxFinishedJobs = 200  // cap on retained terminal history; active jobs are never evicted
	maxLogLines     = 1000 // per-job log cap
)

// Status is a job's lifecycle state.
type Status string

const (
	StatusRunning  Status = "running"
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
	StatusTimeout  Status = "timeout"
)

// Job is one tracked background task. Log is omitted from list views and filled
// only by GetJob.
type Job struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`  // agent | dump | csv | …
	Title     string   `json:"title"` // short human label
	Status    Status   `json:"status"`
	StartedAt int64    `json:"startedAt"` // unix ms
	EndedAt   int64    `json:"endedAt"`   // unix ms, 0 while active
	Error     string   `json:"error"`
	Log       []string `json:"log"`

	cancel context.CancelFunc // nil if the job can't be canceled / has ended
}

func (j *Job) clone(includeLog bool) Job {
	c := *j
	c.cancel = nil
	if includeLog {
		c.Log = append([]string(nil), j.Log...)
	} else {
		c.Log = nil
	}
	return c
}

// active is intentionally based on lifecycle completion rather than a list of
// status strings. That keeps queued, running, and cancellation-requested work
// protected if producers add intermediate non-terminal statuses in the future.
func (j *Job) active() bool { return j.EndedAt == 0 }

// Service is the bound Wails jobs ledger. Its exported methods are the complete
// renderer contract. Producer operations deliberately remain package-level
// functions so Wails cannot publish them as methods on this bound type.
type Service struct {
	mu       sync.Mutex
	jobs     []*Job // retained jobs in start order, oldest..newest
	byID     map[string]*Job
	finished []string // retained terminal IDs in completion order, oldest..newest
	seq      int
}

// New constructs an empty jobs ledger.
func New() *Service { return &Service{byID: map[string]*Job{}} }

// --- producer API (package-level functions are not bound by Wails) ---

// Start registers a new running job and returns its id. cancel may be nil for a
// job that can't be interrupted.
func Start(s *Service, kind, title string, cancel context.CancelFunc) string {
	return s.start(kind, title, cancel)
}

func (s *Service) start(kind, title string, cancel context.CancelFunc) string {
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("job-%d", s.seq)
	j := &Job{ID: id, Kind: kind, Title: title, Status: StatusRunning, StartedAt: nowMs(), cancel: cancel}
	s.jobs = append(s.jobs, j)
	s.byID[id] = j
	s.mu.Unlock()
	s.emit()
	return id
}

// Append adds a log line to a job (no-op for an unknown id).
func Append(s *Service, id, line string) {
	s.append(id, line)
}

func (s *Service) append(id, line string) {
	s.mu.Lock()
	if j, ok := s.byID[id]; ok {
		j.Log = append(j.Log, line)
		if len(j.Log) > maxLogLines {
			j.Log = j.Log[len(j.Log)-maxLogLines:]
		}
	}
	s.mu.Unlock()
	s.emit()
}

// Finish marks an active job terminal. It is idempotent — a later Finish (e.g. a
// timeout racing a cancel) is ignored once the job has ended.
func Finish(s *Service, id string, status Status, errMsg string) {
	s.finish(id, status, errMsg)
}

func (s *Service) finish(id string, status Status, errMsg string) {
	s.mu.Lock()
	if j, ok := s.byID[id]; ok && j.active() {
		j.Status = status
		j.Error = errMsg
		j.EndedAt = nowMs()
		j.cancel = nil
		s.finished = append(s.finished, id)
		s.pruneFinishedLocked()
	}
	s.mu.Unlock()
	s.emit()
}

// --- bound API ---

// ListJobs returns all retained jobs (newest first) without their logs.
func (s *Service) ListJobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, 0, len(s.jobs))
	for _, job := range slices.Backward(s.jobs) {
		out = append(out, job.clone(false))
	}
	return out
}

// GetJob returns one job including its log.
func (s *Service) GetJob(id string) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.byID[id]
	if !ok {
		return Job{}, fmt.Errorf("job %q not found", id)
	}
	return j.clone(true), nil
}

// CancelJob requests cancellation of an active job via its registered hook.
func (s *Service) CancelJob(id string) error {
	s.mu.Lock()
	j, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("job %q not found", id)
	}
	active := j.active()
	cancel := j.cancel
	s.mu.Unlock()
	if !active {
		return nil // already finished — nothing to do
	}
	if cancel == nil {
		return fmt.Errorf("this job cannot be canceled")
	}
	cancel()
	return nil
}

// ClearFinished drops every terminal job from the ledger.
func (s *Service) ClearFinished() {
	s.mu.Lock()
	kept := s.jobs[:0]
	for _, j := range s.jobs {
		if j.active() {
			kept = append(kept, j)
		} else {
			delete(s.byID, j.ID)
		}
	}
	for i := len(kept); i < len(s.jobs); i++ {
		s.jobs[i] = nil
	}
	s.jobs = kept
	s.finished = nil
	s.mu.Unlock()
	s.emit()
}

// pruneFinishedLocked retains the most recently completed terminal jobs. It
// must be called with s.mu held. Active entries are never considered for
// eviction, regardless of their status string or their position in s.jobs.
func (s *Service) pruneFinishedLocked() {
	for len(s.finished) > maxFinishedJobs {
		id := s.finished[0]
		s.finished[0] = ""
		s.finished = s.finished[1:]

		j, ok := s.byID[id]
		if !ok || j.active() {
			continue
		}
		delete(s.byID, id)
		s.removeJobLocked(id)
	}
}

// removeJobLocked removes one retained entry without leaving its pointer in
// the slice backing array. It must be called with s.mu held.
func (s *Service) removeJobLocked(id string) {
	for i, j := range s.jobs {
		if j.ID != id {
			continue
		}
		copy(s.jobs[i:], s.jobs[i+1:])
		s.jobs[len(s.jobs)-1] = nil
		s.jobs = s.jobs[:len(s.jobs)-1]
		return
	}
}

func (s *Service) emit() {
	if app := application.Get(); app != nil {
		app.Event.Emit(EventChanged, nil)
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }
