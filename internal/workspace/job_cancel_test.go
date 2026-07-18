package workspace

import (
	"context"
	"errors"
	"testing"
	"time"

	"novera/internal/jobs"
)

func TestRunTrackedWiresJobCancellationContext(t *testing.T) {
	s := New()
	ledger := jobs.New()
	WireJobsAndArtifacts(s, ledger, nil)

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.runTracked("dump-test", "cancellable dump", func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("tracked operation did not start")
	}
	list := ledger.ListJobs()
	if len(list) != 1 {
		t.Fatalf("jobs = %+v, want one running job", list)
	}
	if err := ledger.CancelJob(list[0].ID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("runTracked error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tracked operation did not stop after cancellation")
	}
	job, err := ledger.GetJob(list[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobs.StatusCanceled {
		t.Fatalf("job status = %q, want canceled", job.Status)
	}
}
