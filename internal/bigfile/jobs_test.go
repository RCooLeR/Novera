package bigfile

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWithJobResultCancelClearsActiveJob(t *testing.T) {
	s := NewFileService()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := withJobResult(s, "Analyze SQL", func(ctx context.Context, _ func(int64, string)) (int, error) {
			close(started)
			<-ctx.Done()
			return 1, ctx.Err()
		})
		done <- err
	}()
	<-started
	s.CancelJob()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v, want context.Canceled", err)
	}

	jm := s.jobs()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if jm.id != "" || jm.cancel != nil {
		t.Fatalf("job remained active after cancellation: id=%q cancel=%v", jm.id, jm.cancel != nil)
	}
}

func TestWithJobResultRejectsConcurrentOperation(t *testing.T) {
	s := NewFileService()
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := withJobResult(s, "First", func(context.Context, func(int64, string)) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
		done <- err
	}()
	<-started

	_, err := withJobResult(s, "Second", func(context.Context, func(int64, string)) (string, error) {
		return "unexpected", nil
	})
	if err == nil || !strings.Contains(err.Error(), "another transform is already running (First)") {
		t.Fatalf("concurrent job error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
