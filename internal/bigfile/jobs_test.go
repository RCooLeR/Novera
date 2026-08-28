package bigfile

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"novera/internal/bigfile/fileio"
)

type recordedBigFileJobEvent struct {
	name string
	data map[string]any
}

func installBigFileJobRecorder(service *FileService) (*sync.Mutex, *[]recordedBigFileJobEvent) {
	var eventMu sync.Mutex
	events := make([]recordedBigFileJobEvent, 0, 8)
	manager := service.jobs()
	manager.mu.Lock()
	manager.emit = func(name string, value any) {
		data, _ := value.(map[string]any)
		eventMu.Lock()
		events = append(events, recordedBigFileJobEvent{name: name, data: data})
		eventMu.Unlock()
	}
	manager.mu.Unlock()
	return &eventMu, &events
}

func activeBigFileJobID(t *testing.T, service *FileService) string {
	t.Helper()
	manager := service.jobs()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active == nil {
		t.Fatal("expected active job")
	}
	return manager.active.id
}

func TestWithJobResultCancelUsesExactActiveIDAndClearsOwnership(t *testing.T) {
	service := NewFileService()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := service.withJobResult("Analyze SQL", func(ctx context.Context, _ func(int64, string)) (int, error) {
			close(started)
			<-ctx.Done()
			return 1, ctx.Err()
		})
		done <- err
	}()
	<-started
	jobID := activeBigFileJobID(t, service)
	if err := service.CancelJob(jobID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}

	manager := service.jobs()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != nil {
		t.Fatalf("job remained active: %+v", manager.active)
	}
}

func TestWithJobResultRejectsStaleCancelAndConcurrentOperation(t *testing.T) {
	service := NewFileService()
	if _, err := service.withJobResult("First complete", func(context.Context, func(int64, string)) (int, error) {
		return 1, nil
	}); err != nil {
		t.Fatal(err)
	}
	firstID := "job1"

	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := service.withJobResult("Second", func(context.Context, func(int64, string)) (int, error) {
			close(started)
			<-release
			return 2, nil
		})
		done <- err
	}()
	<-started
	secondID := activeBigFileJobID(t, service)
	if secondID == firstID {
		t.Fatalf("job ID reused: %s", secondID)
	}
	if err := service.CancelJob(firstID); !errors.Is(err, ErrJobIDMismatch) {
		t.Fatalf("stale cancel error = %v, want ErrJobIDMismatch", err)
	}
	if _, err := service.withJobResult("Overlap", func(context.Context, func(int64, string)) (string, error) {
		return "unexpected", nil
	}); !errors.Is(err, ErrJobAlreadyRunning) {
		t.Fatalf("overlap error = %v, want ErrJobAlreadyRunning", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServiceJobProgressEventsAreOwnedBoundedAndMonotonic(t *testing.T) {
	service := NewFileService()
	eventMu, events := installBigFileJobRecorder(service)
	var lateProgress func(int64, int64, string)
	_, err := service.runServiceJob(jobSpec{Title: "Progress", Kind: jobKindSQLAnalysis, FileID: "f1", Total: 10}, func(_ context.Context, progress func(int64, int64, string)) (int, error) {
		lateProgress = progress
		progress(-5, -2, strings.Repeat("é", 200))
		progress(math.MaxInt64, 10, "done")
		progress(5, 10, "regressed")
		return 1, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	eventMu.Lock()
	eventCountAtEnd := len(*events)
	progressEvents := make([]recordedBigFileJobEvent, 0, 3)
	for _, event := range *events {
		if event.name == "bigfile:job-progress" {
			progressEvents = append(progressEvents, event)
		}
	}
	eventMu.Unlock()
	if len(progressEvents) != 3 {
		t.Fatalf("progress events = %d, want 3", len(progressEvents))
	}
	previous := int64(0)
	for _, event := range progressEvents {
		completed, _ := event.data["completed"].(int64)
		total, _ := event.data["total"].(int64)
		note, _ := event.data["note"].(string)
		if completed < previous || completed < 0 || completed > 10 || total != 10 {
			t.Fatalf("invalid progress event: %#v", event.data)
		}
		if len(note) > maxJobNoteBytes {
			t.Fatalf("note length = %d", len(note))
		}
		if event.data["sequence"] != int64(1) || event.data["fileId"] != "f1" || event.data["kind"] != jobKindSQLAnalysis {
			t.Fatalf("ownership fields = %#v", event.data)
		}
		previous = completed
	}

	lateProgress(10, 10, "late")
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(*events) != eventCountAtEnd {
		t.Fatalf("late progress emitted after terminal event")
	}
}

func TestServiceJobPanicAndEmitterFailureAlwaysReleaseOwnership(t *testing.T) {
	service := NewFileService()
	manager := service.jobs()
	manager.mu.Lock()
	manager.emit = func(string, any) { panic("transport failure") }
	manager.mu.Unlock()

	_, err := service.runServiceJob(jobSpec{Title: "panic"}, func(context.Context, func(int64, int64, string)) (int, error) {
		panic("deliberate")
	})
	if err == nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("panic error = %v", err)
	}
	value, err := service.runServiceJob(jobSpec{Title: "next"}, func(_ context.Context, progress func(int64, int64, string)) (int, error) {
		progress(1, 1, "done")
		return 7, nil
	})
	if err != nil || value != 7 {
		t.Fatalf("next result = %d, %v", value, err)
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != nil {
		t.Fatal("active ownership leaked")
	}
}

func TestServiceJobFileCancellationTargetsOnlyMatchingOwner(t *testing.T) {
	service := NewFileService()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := service.runServiceJob(jobSpec{Title: "owned", FileID: "f1", FileIDs: []string{"f2", "f1"}}, func(ctx context.Context, _ func(int64, int64, string)) (int, error) {
			close(started)
			<-ctx.Done()
			return 0, ctx.Err()
		})
		done <- err
	}()
	<-started
	if service.jobs().cancelFile("f3") {
		t.Fatal("other file canceled the active job")
	}
	select {
	case err := <-done:
		t.Fatalf("other-file cancel ended job: %v", err)
	default:
	}
	if !service.jobs().cancelFile("f2") {
		t.Fatal("secondary owned file did not cancel the active job")
	}
	if err := <-done; !errors.Is(err, ErrJobCancelled) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestJobSpecFileOwnershipIsCanonicalAndBlockedAsASet(t *testing.T) {
	input := []string{" f3 ", "f1", "f2", "f1", ""}
	spec := normalizeJobSpec(jobSpec{Title: "multi", FileID: " f2 ", FileIDs: input})
	if spec.FileID != "f2" {
		t.Fatalf("primary file ID = %q, want f2", spec.FileID)
	}
	if got := strings.Join(spec.FileIDs, ","); got != "f1,f2,f3" {
		t.Fatalf("canonical file IDs = %q", got)
	}
	if input[0] != " f3 " {
		t.Fatal("normalization mutated the caller-owned slice")
	}
	implicitPrimary := normalizeJobSpec(jobSpec{FileIDs: []string{"f9", "f4"}})
	if implicitPrimary.FileID != "f4" || strings.Join(implicitPrimary.FileIDs, ",") != "f4,f9" {
		t.Fatalf("implicit primary = %q, owners = %v", implicitPrimary.FileID, implicitPrimary.FileIDs)
	}

	manager := &jobManager{blocked: map[string]int{"f2": 1}}
	if _, err := manager.begin(jobSpec{Title: "blocked secondary", FileID: "f1", FileIDs: []string{"f3", "f2"}}); !errors.Is(err, ErrFileJobsBlocked) || !strings.Contains(err.Error(), `"f2"`) {
		t.Fatalf("secondary blocked admission error = %v", err)
	}
	manager.mu.Lock()
	active := manager.active
	manager.mu.Unlock()
	if active != nil {
		t.Fatal("blocked multi-file job retained active ownership")
	}
}

func TestServiceJobSequenceExhaustionAndJobIDValidation(t *testing.T) {
	service := NewFileService()
	manager := service.jobs()
	manager.mu.Lock()
	manager.seq = maxJobProgressValue
	manager.mu.Unlock()
	if _, err := service.withJobResult("too late", func(context.Context, func(int64, string)) (int, error) {
		return 1, nil
	}); !errors.Is(err, ErrJobSequenceExhausted) {
		t.Fatalf("error = %v, want ErrJobSequenceExhausted", err)
	}
	for _, invalid := range []string{"", "job0", "job01", "Job1", "job-1", "job9007199254740992"} {
		if err := service.CancelJob(invalid); !errors.Is(err, ErrInvalidJobID) {
			t.Fatalf("%q: error = %v, want ErrInvalidJobID", invalid, err)
		}
	}
	if err := service.CancelJob("job1"); !errors.Is(err, ErrNoActiveJob) {
		t.Fatalf("valid inactive cancel error = %v", err)
	}
}

func TestBigFileJobEndCannotBeOvertakenByLateProgress(t *testing.T) {
	service := NewFileService()
	eventMu, events := installBigFileJobRecorder(service)
	release := make(chan struct{})
	done := make(chan struct{})
	var progress func(int64, int64, string)
	go func() {
		_, _ = service.runServiceJob(jobSpec{Title: "ordered"}, func(_ context.Context, report func(int64, int64, string)) (int, error) {
			progress = report
			close(release)
			return 1, nil
		})
		close(done)
	}()
	select {
	case <-release:
	case <-time.After(2 * time.Second):
		t.Fatal("job did not start")
	}
	<-done
	progress(1, 1, "late")

	eventMu.Lock()
	defer eventMu.Unlock()
	if len(*events) < 2 || (*events)[len(*events)-1].name != "bigfile:job-end" {
		t.Fatalf("events = %#v, want terminal event last", *events)
	}
}

func TestServiceJobCancellationBeforeOutputPublicationPreservesDestination(t *testing.T) {
	service := NewFileService()
	source, doc := openOutputTestDocument(t, "source-data")
	destination := filepath.Join(filepath.Dir(source), "output.txt")
	if err := os.WriteFile(destination, []byte("old-output"), 0o600); err != nil {
		t.Fatal(err)
	}

	producerReady := make(chan struct{})
	releaseProducer := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := service.runServiceJob(jobSpec{Title: "cancel before publish", FileID: "f1"}, func(ctx context.Context, _ func(int64, int64, string)) (int, error) {
			_, writeErr := writeSafeOutputContext(ctx, doc, source, destination, func(output io.Writer) error {
				if _, err := output.Write([]byte("new-output")); err != nil {
					return err
				}
				close(producerReady)
				<-releaseProducer
				return nil
			})
			return 0, writeErr
		})
		done <- err
	}()

	<-producerReady
	jobID := activeBigFileJobID(t, service)
	if err := service.CancelJob(jobID); err != nil {
		t.Fatal(err)
	}
	close(releaseProducer)
	if err := <-done; !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("job error = %v, want cancellation", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "old-output" {
		t.Fatalf("destination = %q, %v; want preserved old output", got, err)
	}
}

func TestServiceJobAtomicOutputCancellationWinsBeforePublication(t *testing.T) {
	service := NewFileService()
	path := filepath.Join(t.TempDir(), "cancel-first.txt")
	ready := make(chan struct{})
	continueCommit := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := service.runServiceJob(jobSpec{Title: "cancel-first output"}, func(ctx context.Context, _ func(int64, int64, string)) (string, error) {
			out, err := fileio.OpenAtomicOutput(path, nil, 0o600)
			if err != nil {
				return "", err
			}
			defer out.Cleanup()
			if _, err := out.Write([]byte("complete but unpublished")); err != nil {
				return "", err
			}
			close(ready)
			<-continueCommit
			if err := out.CommitContext(ctx); err != nil {
				return "", err
			}
			return path, nil
		})
		result <- err
	}()
	<-ready
	jobID := activeBigFileJobID(t, service)
	if err := service.CancelJob(jobID); err != nil {
		t.Fatal(err)
	}
	close(continueCommit)
	if err := <-result; !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("job error = %v, want cancellation", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancel-first race published output: %v", err)
	}
}

func TestServiceJobPublicationErrorMakesCancellationStale(t *testing.T) {
	service := NewFileService()
	published := make(chan struct{})
	release := make(chan struct{})
	wantPublication := &fileio.PublicationError{
		FinalPath: "uncertain-output.txt",
		Err:       errors.New("simulated directory sync failure"),
	}
	result := make(chan error, 1)
	go func() {
		_, err := service.runServiceJob(jobSpec{Title: "publication evidence"}, func(ctx context.Context, _ func(int64, int64, string)) (int, error) {
			err := service.jobs().publishAtomicOutput("job1", func() error { return wantPublication })
			close(published)
			<-release
			return 0, err
		})
		result <- err
	}()
	<-published
	if err := service.CancelJob("job1"); !errors.Is(err, ErrJobAlreadyCommitted) {
		t.Fatalf("CancelJob after PublicationError = %v, want ErrJobAlreadyCommitted", err)
	}
	close(release)
	err := <-result
	var gotPublication *fileio.PublicationError
	if !errors.As(err, &gotPublication) || gotPublication != wantPublication {
		t.Fatalf("job error = %v, want original PublicationError", err)
	}
}

func TestServiceJobAtomicOutputPublicationMakesLateCancellationStale(t *testing.T) {
	service := NewFileService()
	path := filepath.Join(t.TempDir(), "committed.txt")
	committed := make(chan struct{})
	release := make(chan struct{})
	ctxCanceled := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		_, err := service.runServiceJob(jobSpec{Title: "atomic commit"}, func(ctx context.Context, _ func(int64, int64, string)) (string, error) {
			out, err := fileio.OpenAtomicOutput(path, nil, 0o600)
			if err != nil {
				return "", err
			}
			defer out.Cleanup()
			if _, err := out.Write([]byte("complete output")); err != nil {
				return "", err
			}
			if err := out.CommitContext(ctx); err != nil {
				return "", err
			}
			close(committed)
			<-release
			select {
			case <-ctx.Done():
				ctxCanceled <- struct{}{}
			default:
			}
			return path, nil
		})
		result <- err
	}()
	<-committed
	if err := service.CancelJob("job1"); !errors.Is(err, ErrJobAlreadyCommitted) {
		t.Fatalf("late CancelJob error = %v, want ErrJobAlreadyCommitted", err)
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctxCanceled:
		t.Fatal("late cancellation canceled committed job context")
	default:
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "complete output" {
		t.Fatalf("committed output = %q, %v", got, err)
	}
}

func TestServiceJobOutputPublicationMakesLateCancellationStale(t *testing.T) {
	service := NewFileService()
	source, doc := openOutputTestDocument(t, "source-data")
	destination := filepath.Join(filepath.Dir(source), "output.txt")
	published := make(chan struct{})
	allowCommitRecord := make(chan struct{})
	writeReturned := make(chan struct{})
	finishJob := make(chan struct{})

	manager := service.jobs()
	manager.mu.Lock()
	manager.afterOutputPublication = func(string, error) {
		close(published)
		<-allowCommitRecord
	}
	manager.mu.Unlock()
	t.Cleanup(func() {
		manager.mu.Lock()
		manager.afterOutputPublication = nil
		manager.mu.Unlock()
	})

	jobDone := make(chan error, 1)
	go func() {
		_, err := service.runServiceJob(jobSpec{Title: "publish", FileID: "f1"}, func(ctx context.Context, _ func(int64, int64, string)) (int, error) {
			_, writeErr := writeSafeOutputContext(ctx, doc, source, destination, func(output io.Writer) error {
				_, err := output.Write([]byte("complete-output"))
				return err
			})
			close(writeReturned)
			<-finishJob
			return 1, writeErr
		})
		jobDone <- err
	}()

	<-published
	jobID := "job1"
	cancelAttempted := make(chan struct{})
	cancelDone := make(chan error, 1)
	go func() {
		close(cancelAttempted)
		cancelDone <- service.CancelJob(jobID)
	}()
	<-cancelAttempted
	close(allowCommitRecord)
	<-writeReturned
	if err := <-cancelDone; !errors.Is(err, ErrJobAlreadyCommitted) {
		t.Fatalf("late cancel error = %v, want ErrJobAlreadyCommitted", err)
	}
	close(finishJob)
	if err := <-jobDone; err != nil {
		t.Fatalf("published job error = %v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "complete-output" {
		t.Fatalf("destination = %q, %v", got, err)
	}
}
