// Package jobs is a small in-process ledger of long-running background tasks
// (agent runs, dump imports, …). It records each job's lifecycle
// (running/success/failed/canceled/timeout), a bounded log, and a cancel hook,
// and emits a "jobs:changed" event so the UI can live-update. Producers call the
// Start/Append/Finish API; the frontend reads ListJobs/GetJob and CancelJob.
package jobs

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// EventChanged is emitted (no payload) whenever any job's state changes; the
// frontend re-fetches ListJobs in response.
const EventChanged = "jobs:changed"

const (
	maxJobs     = 200  // ring cap on retained jobs
	maxLogLines = 1000 // per-job log cap
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
	EndedAt   int64    `json:"endedAt"`   // unix ms, 0 while running
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

// Service is the bound Wails jobs ledger.
type Service struct {
	mu   sync.Mutex
	jobs []*Job // oldest..newest
	byID map[string]*Job
	seq  int
}

// New constructs an empty jobs ledger.
func New() *Service { return &Service{byID: map[string]*Job{}} }

// --- producer API (not bound to the frontend) ---

// Start registers a new running job and returns its id. cancel may be nil for a
// job that can't be interrupted.
func (s *Service) Start(kind, title string, cancel context.CancelFunc) string {
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("job-%d", s.seq)
	j := &Job{ID: id, Kind: kind, Title: title, Status: StatusRunning, StartedAt: nowMs(), cancel: cancel}
	s.jobs = append(s.jobs, j)
	s.byID[id] = j
	for len(s.jobs) > maxJobs {
		delete(s.byID, s.jobs[0].ID)
		s.jobs = s.jobs[1:]
	}
	s.mu.Unlock()
	s.emit()
	return id
}

// Append adds a log line to a job (no-op for an unknown id).
func (s *Service) Append(id, line string) {
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

// Finish marks a running job terminal. It is idempotent — a later Finish (e.g. a
// timeout racing a cancel) is ignored once the job has ended.
func (s *Service) Finish(id string, status Status, errMsg string) {
	s.mu.Lock()
	if j, ok := s.byID[id]; ok && j.Status == StatusRunning {
		j.Status = status
		j.Error = errMsg
		j.EndedAt = nowMs()
		j.cancel = nil
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
	for i := len(s.jobs) - 1; i >= 0; i-- {
		out = append(out, s.jobs[i].clone(false))
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

// CancelJob requests cancellation of a running job via its registered hook.
func (s *Service) CancelJob(id string) error {
	s.mu.Lock()
	j, ok := s.byID[id]
	if !ok {
		s.mu.Unlock()
		return fmt.Errorf("job %q not found", id)
	}
	running := j.Status == StatusRunning
	cancel := j.cancel
	s.mu.Unlock()
	if !running {
		return nil // already finished — nothing to do
	}
	if cancel == nil {
		return fmt.Errorf("this job cannot be canceled")
	}
	cancel()
	return nil
}

// ClearFinished drops every non-running job from the ledger.
func (s *Service) ClearFinished() {
	s.mu.Lock()
	kept := s.jobs[:0]
	for _, j := range s.jobs {
		if j.Status == StatusRunning {
			kept = append(kept, j)
		} else {
			delete(s.byID, j.ID)
		}
	}
	s.jobs = kept
	s.mu.Unlock()
	s.emit()
}

func (s *Service) emit() {
	if app := application.Get(); app != nil {
		app.Event.Emit(EventChanged, nil)
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }
