package jobs

import "testing"

func TestJobLifecycle(t *testing.T) {
	s := New()
	canceled := false
	id := s.Start("agent", "do a thing", func() { canceled = true })

	list := s.ListJobs()
	if len(list) != 1 || list[0].Status != StatusRunning || list[0].ID != id {
		t.Fatalf("unexpected list after Start: %+v", list)
	}
	if list[0].Log != nil {
		t.Error("ListJobs should omit the log")
	}

	s.Append(id, "step 1")
	s.Append(id, "step 2")
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

	s.Finish(id, StatusCanceled, "stopped by user")
	j, _ = s.GetJob(id)
	if j.Status != StatusCanceled || j.EndedAt == 0 {
		t.Errorf("finish did not apply: %+v", j)
	}

	// Finishing again is a no-op (idempotent).
	s.Finish(id, StatusSuccess, "")
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
