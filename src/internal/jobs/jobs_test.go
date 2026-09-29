package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
)

func TestJobLifecycle(t *testing.T) {
	s := New()
	canceled := false
	id := Start(s, "agent", "do a thing", func() { canceled = true })

	list := s.ListJobs()
	if len(list) != 1 || list[0].Status != StatusRunning || list[0].ID != id {
		t.Fatalf("unexpected list after Start: %+v", list)
	}
	if list[0].Log != nil {
		t.Error("ListJobs should omit the log")
	}

	Append(s, id, "step 1")
	Append(s, id, "step 2")
	j, err := s.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(j.Log) != 2 || j.Log[0] != "step 1" {
		t.Errorf("log not recorded: %+v", j.Log)
	}

	// Cancel invokes the hook but does not itself finish the job (the producer does).
	if err := s.CancelJob(id); err != nil {
		t.Fatal(err)
	}
	if !canceled {
		t.Error("cancel hook was not invoked")
	}

	Finish(s, id, StatusCanceled, "stopped by user")
	j, _ = s.GetJob(id)
	if j.Status != StatusCanceled || j.EndedAt == 0 {
		t.Errorf("finish did not apply: %+v", j)
	}

	// Finishing again is a no-op (idempotent).
	Finish(s, id, StatusSuccess, "")
	j, _ = s.GetJob(id)
	if j.Status != StatusCanceled {
		t.Error("second Finish should be ignored")
	}

	// Cancelling a finished job is a no-op, not an error.
	if err := s.CancelJob(id); err != nil {
		t.Errorf("cancel of finished job should be nil, got %v", err)
	}

	s.ClearFinished()
	if len(s.ListJobs()) != 0 {
		t.Error("ClearFinished should have emptied the ledger")
	}
}

func TestTerminalRetentionNeverEvictsActiveJobs(t *testing.T) {
	const (
		activeCount   = maxFinishedJobs + 17
		finishedCount = maxFinishedJobs + 23
	)

	s := New()
	cancelCalls := make([]atomic.Int32, activeCount)
	activeIDs := make([]string, activeCount)
	for i := range activeIDs {
		idx := i
		activeIDs[i] = Start(s, "active", fmt.Sprintf("active-%d", i), func() {
			cancelCalls[idx].Add(1)
		})
	}

	// Retention and clearing use lifecycle completion, not a status allowlist.
	// This keeps future queued/cancellation-requested producer states live.
	s.mu.Lock()
	s.byID[activeIDs[0]].Status = Status("queued")
	s.byID[activeIDs[1]].Status = Status("cancel_requested")
	s.mu.Unlock()
	s.ClearFinished()

	finishedIDs := make([]string, finishedCount)
	for i := range finishedIDs {
		finishedIDs[i] = Start(s, "finished", fmt.Sprintf("finished-%d", i), nil)
	}

	// Finish in reverse start order so the assertion proves pruning follows
	// completion order, not slice position or millisecond timestamp ties.
	completionOrder := slices.Clone(finishedIDs)
	slices.Reverse(completionOrder)
	for _, id := range completionOrder {
		Finish(s, id, StatusSuccess, "")
	}

	if got, want := len(s.ListJobs()), activeCount+maxFinishedJobs; got != want {
		t.Fatalf("retained job count = %d, want %d", got, want)
	}

	for i, id := range activeIDs {
		Append(s, id, "still tracked")
		job, err := s.GetJob(id)
		if err != nil {
			t.Fatalf("active job %s was evicted: %v", id, err)
		}
		if !job.active() {
			t.Fatalf("active job %s unexpectedly terminal: %+v", id, job)
		}
		if len(job.Log) != 1 || job.Log[0] != "still tracked" {
			t.Fatalf("active job %s stopped accepting producer updates: %+v", id, job.Log)
		}
		if err := s.CancelJob(id); err != nil {
			t.Fatalf("active job %s lost its cancellation handle: %v", id, err)
		}
		if got := cancelCalls[i].Load(); got != 1 {
			t.Fatalf("active job %s cancellation calls = %d, want 1", id, got)
		}
	}

	overflow := finishedCount - maxFinishedJobs
	for i, id := range completionOrder {
		_, err := s.GetJob(id)
		if i < overflow && err == nil {
			t.Errorf("oldest completed job %s should have been pruned", id)
		}
		if i >= overflow && err != nil {
			t.Errorf("recent completed job %s should be retained: %v", id, err)
		}
	}

	terminal := 0
	for _, job := range s.ListJobs() {
		if !job.active() {
			terminal++
		}
	}
	if terminal != maxFinishedJobs {
		t.Fatalf("terminal history size = %d, want %d", terminal, maxFinishedJobs)
	}
}

func TestConcurrentJobLifecycleIsRaceSafe(t *testing.T) {
	const jobCount = maxFinishedJobs + 64

	s := New()
	cancelCalls := make([]atomic.Int32, jobCount)
	ids := make([]string, jobCount)
	var starts sync.WaitGroup
	starts.Add(jobCount)
	for i := range ids {
		i := i
		go func() {
			defer starts.Done()
			ids[i] = Start(s, "concurrent", fmt.Sprintf("job-%d", i), func() {
				cancelCalls[i].Add(1)
			})
		}()
	}
	starts.Wait()

	// Exercise readers, logs, cancellation, and clearing concurrently while all
	// jobs are live. ClearFinished must not lose any active entry.
	var activeOps sync.WaitGroup
	activeOps.Add(jobCount + 4)
	for i, id := range ids {
		i, id := i, id
		go func() {
			defer activeOps.Done()
			Append(s, id, "active")
			if err := s.CancelJob(id); err != nil {
				t.Errorf("cancel live job %s: %v", id, err)
			}
			if _, err := s.GetJob(id); err != nil {
				t.Errorf("get live job %s: %v", id, err)
			}
			if cancelCalls[i].Load() != 1 {
				t.Errorf("cancel hook for %s was not retained", id)
			}
		}()
	}
	for range 4 {
		go func() {
			defer activeOps.Done()
			for range 50 {
				_ = s.ListJobs()
				s.ClearFinished()
			}
		}()
	}
	activeOps.Wait()

	for _, id := range ids {
		if _, err := s.GetJob(id); err != nil {
			t.Fatalf("active job %s disappeared during concurrent operations: %v", id, err)
		}
	}

	// Race terminal transitions against readers, cancellation requests, log
	// appends, retention, and explicit history clearing. Every producer gets to
	// Finish because clearing cannot remove entries until after that transition.
	start := make(chan struct{})
	var lifecycle sync.WaitGroup
	lifecycle.Add(jobCount + 4)
	for i, id := range ids {
		i, id := i, id
		go func() {
			defer lifecycle.Done()
			<-start
			Append(s, id, "finishing")
			status := StatusSuccess
			if i%3 == 0 {
				status = StatusCanceled
			}
			Finish(s, id, status, "")
			_ = s.CancelJob(id)
		}()
	}
	for range 4 {
		go func() {
			defer lifecycle.Done()
			<-start
			for range 100 {
				jobs := s.ListJobs()
				if len(jobs) > 0 {
					_, _ = s.GetJob(jobs[0].ID)
				}
				s.ClearFinished()
			}
		}()
	}
	close(start)
	lifecycle.Wait()

	s.ClearFinished()
	if got := len(s.ListJobs()); got != 0 {
		t.Fatalf("finished ledger length = %d after clear, want 0", got)
	}
}

func TestBoundServiceMethodAllowlist(t *testing.T) {
	want := []string{"CancelJob", "ClearFinished", "GetJob", "ListJobs"}
	typ := reflect.TypeOf((*Service)(nil))
	got := make([]string, 0, typ.NumMethod())
	for i := 0; i < typ.NumMethod(); i++ {
		got = append(got, typ.Method(i).Name)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("exported Service methods changed; review the Wails renderer contract: got %v, want %v", got, want)
	}
}

func TestGeneratedBindingFunctionAllowlist(t *testing.T) {
	path := filepath.Join("..", "..", "frontend", "bindings", "novera", "internal", "jobs", "service.ts")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated jobs binding: %v", err)
	}

	re := regexp.MustCompile(`(?m)^export function ([A-Za-z0-9_]+)\(`)
	matches := re.FindAllSubmatch(source, -1)
	got := make([]string, 0, len(matches))
	for _, match := range matches {
		got = append(got, string(match[1]))
	}
	slices.Sort(got)
	want := []string{"CancelJob", "ClearFinished", "GetJob", "ListJobs"}
	if !slices.Equal(got, want) {
		t.Fatalf("generated jobs binding changed; internal producer methods must not be renderer-callable: got %v, want %v", got, want)
	}
}
