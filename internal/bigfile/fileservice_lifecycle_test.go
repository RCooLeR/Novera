package bigfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/manualedit"
)

func prepareEditSessionForTest(t *testing.T, service *FileService, fileID string) {
	t.Helper()
	if _, err := service.PrepareEditSession(fileID); err != nil {
		t.Fatalf("PrepareEditSession: %v", err)
	}
}

func waitForBigFileServiceStopping(t *testing.T, service *FileService) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.serviceMu.Lock()
		stopping := service.serviceStopping
		service.serviceMu.Unlock()
		if stopping {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("service did not raise its shutdown admission gate")
}

func waitForFileJobBlock(t *testing.T, service *FileService, fileID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager := service.jobs()
		manager.mu.Lock()
		_, blocked := manager.blocked[fileID]
		manager.mu.Unlock()
		if blocked {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("jobs for %q were not blocked", fileID)
}

func startTwoFileLeaseJob(service *FileService, firstID, secondID string) (<-chan error, <-chan error) {
	ready := make(chan error, 1)
	done := make(chan error, 1)
	var readyOnce sync.Once
	signalReady := func(err error) {
		readyOnce.Do(func() { ready <- err })
	}
	go func() {
		_, err := service.runServiceJob(jobSpec{
			Title:   "two-file lifecycle test",
			Kind:    jobKindTransform,
			FileID:  firstID,
			FileIDs: []string{secondID, firstID},
		}, func(ctx context.Context, _ func(int64, int64, string)) (struct{}, error) {
			leaseIDs := []string{firstID, secondID}
			if leaseIDs[1] < leaseIDs[0] {
				leaseIDs[0], leaseIDs[1] = leaseIDs[1], leaseIDs[0]
			}
			first, ok := service.reg.Get(leaseIDs[0])
			if !ok {
				err := errors.New("first test lease is unavailable")
				signalReady(err)
				return struct{}{}, err
			}
			defer first.Release()
			second, ok := service.reg.Get(leaseIDs[1])
			if !ok {
				err := errors.New("second test lease is unavailable")
				signalReady(err)
				return struct{}{}, err
			}
			defer second.Release()
			signalReady(nil)
			<-ctx.Done()
			return struct{}{}, ctx.Err()
		})
		signalReady(err)
		done <- err
	}()
	return ready, done
}

func waitForTwoFileLeaseJob(t *testing.T, ready <-chan error) {
	t.Helper()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("two-file job did not acquire leases: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("two-file job did not acquire leases")
	}
}

func waitForCancelledTwoFileJob(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("two-file job error = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("two-file job did not finish after lifecycle cancellation")
	}
}

func TestServiceShutdownIsIdempotentBlocksAdmissionAndDrainsLease(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "service-shutdown.txt", []byte("alpha\nbeta\n"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	held, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("missing held lease")
	}
	doc := held.Doc

	results := make(chan error, 2)
	go func() { results <- service.ServiceShutdown() }()
	waitForBigFileServiceStopping(t, service)
	if _, err := service.OpenFile(path); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("late OpenFile error = %v, want ErrServiceStopped", err)
	}
	select {
	case err := <-results:
		t.Fatalf("shutdown returned before lease drain: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	held.Release()
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := service.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if _, err := doc.ReadRange(0, 1); err == nil {
		t.Fatal("shutdown left the document descriptor readable")
	}
	if paths := service.reg.Paths(); len(paths) != 0 {
		t.Fatalf("shutdown retained paths: %v", paths)
	}
}

func TestCloseSecondaryOwnerCancelsAndDrainsTwoFileJob(t *testing.T) {
	service := NewFileService()
	first, err := service.OpenFile(writeTempFile(t, "two-file-close-a.txt", []byte("alpha\n")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.OpenFile(writeTempFile(t, "two-file-close-b.txt", []byte("beta\n")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.CloseFile(first.FileID)
		_ = service.CloseFile(second.FileID)
	})

	ready, jobDone := startTwoFileLeaseJob(service, first.FileID, second.FileID)
	waitForTwoFileLeaseJob(t, ready)
	closeDone := make(chan error, 1)
	go func() { closeDone <- service.CloseFile(second.FileID) }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the secondary job owner deadlocked")
	}
	waitForCancelledTwoFileJob(t, jobDone)
	if _, ok := service.reg.Get(second.FileID); ok {
		t.Fatal("closed secondary file remained registered")
	}
	remaining, ok := service.reg.Get(first.FileID)
	if !ok {
		t.Fatal("closing the secondary owner removed the primary file")
	}
	remaining.Release()
}

func TestRefreshSecondaryOwnerCancelsAndDrainsTwoFileJob(t *testing.T) {
	service := NewFileService()
	first, err := service.OpenFile(writeTempFile(t, "two-file-refresh-a.txt", []byte("alpha\n")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.OpenFile(writeTempFile(t, "two-file-refresh-b.txt", []byte("beta\n")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = service.CloseFile(first.FileID)
		_ = service.CloseFile(second.FileID)
	})
	before, ok := service.reg.Get(second.FileID)
	if !ok {
		t.Fatal("secondary file is unavailable")
	}
	beforeGeneration := before.Generation
	before.Release()

	ready, jobDone := startTwoFileLeaseJob(service, first.FileID, second.FileID)
	waitForTwoFileLeaseJob(t, ready)
	refreshDone := make(chan error, 1)
	go func() {
		_, refreshErr := service.RefreshFile(second.FileID)
		refreshDone <- refreshErr
	}()
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("refreshing the secondary job owner deadlocked")
	}
	waitForCancelledTwoFileJob(t, jobDone)
	after, ok := service.reg.Get(second.FileID)
	if !ok {
		t.Fatal("refreshed secondary file is unavailable")
	}
	defer after.Release()
	if after.Generation <= beforeGeneration {
		t.Fatalf("secondary generation = %d, want > %d", after.Generation, beforeGeneration)
	}
}

func TestShutdownCancelsAndDrainsTwoFileJob(t *testing.T) {
	service := NewFileService()
	first, err := service.OpenFile(writeTempFile(t, "two-file-shutdown-a.txt", []byte("alpha\n")))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.OpenFile(writeTempFile(t, "two-file-shutdown-b.txt", []byte("beta\n")))
	if err != nil {
		t.Fatal(err)
	}
	ready, jobDone := startTwoFileLeaseJob(service, first.FileID, second.FileID)
	waitForTwoFileLeaseJob(t, ready)
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- service.ServiceShutdown() }()
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown deadlocked on two-file job leases")
	}
	waitForCancelledTwoFileJob(t, jobDone)
	if paths := service.reg.Paths(); len(paths) != 0 {
		t.Fatalf("shutdown retained two-file job paths: %v", paths)
	}
}

func TestCloseBlocksRegistrationBeforeCancellingAndDraining(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "close-job-barrier.txt", []byte("alpha\n"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	held, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("missing held lease")
	}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(held.Release) }
	t.Cleanup(release)

	closeDone := make(chan error, 1)
	go func() { closeDone <- service.CloseFile(meta.FileID) }()
	waitForFileJobBlock(t, service, meta.FileID)
	if _, err := service.withFileJobResult(meta.FileID, "late job", jobKindTransform, func(context.Context, func(int64, string)) (int, error) {
		return 1, nil
	}); !errors.Is(err, ErrFileJobsBlocked) {
		t.Fatalf("late job error = %v, want ErrFileJobsBlocked", err)
	}
	select {
	case err := <-closeDone:
		t.Fatalf("close returned before held lease release: %v", err)
	default:
	}
	release()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("close did not drain after lease release")
	}
}

func TestFirstEditFingerprintIsJobOwnedAndCancellable(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "cancel-edit-prepare.txt", []byte("alpha\n"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })

	previous := captureEditSourceExpectation
	started := make(chan struct{})
	captureEditSourceExpectation = func(ctx context.Context, _ *document.FileDocument, _ string, _ func(int64, int64)) (*documentSourceExpectation, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	t.Cleanup(func() { captureEditSourceExpectation = previous })

	editDone := make(chan error, 1)
	go func() {
		_, err := service.PrepareEditSession(meta.FileID)
		editDone <- err
	}()
	<-started
	jobID := activeBigFileJobID(t, service)
	if err := service.CancelJob(jobID); err != nil {
		t.Fatal(err)
	}
	if err := <-editDone; !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled prepare error = %v", err)
	}
	state, err := service.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if state.EditCount != 0 {
		t.Fatalf("canceled preparation installed edits: %+v", state)
	}
	if _, err := service.StageEdit(meta.FileID, 0, 5, "ALPHA"); !errors.Is(err, ErrEditPreparationRequired) {
		t.Fatalf("StageEdit after canceled prepare = %v, want ErrEditPreparationRequired", err)
	}
}

func TestSaveCopyUsesPreparedGenerationAndRejectsRestoredMetadataRewrite(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "prepared-generation.txt", []byte("alpha bravo"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	prepareEditSessionForTest(t, service, meta.FileID)
	if _, err := service.StageEdit(meta.FileID, 6, 5, "delta"); err != nil {
		t.Fatal(err)
	}
	stateBefore, err := service.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(path, []byte("ALPHA bravo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "must-not-publish.txt")
	if _, err := service.saveCopy(meta.FileID, output); !errors.Is(err, manualedit.ErrSessionSourceChanged) {
		t.Fatalf("SaveCopy error = %v, want prepared-generation rejection", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected SaveCopy destination state = %v, want absent", err)
	}
	if _, err := service.StageEdit(meta.FileID, 0, 5, "omega"); !errors.Is(err, manualedit.ErrSessionSourceChanged) {
		t.Fatalf("later StageEdit error = %v, want prepared-generation rejection", err)
	}
	stateAfter, err := service.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if stateAfter.EditCount != stateBefore.EditCount || stateAfter.EditedSize != stateBefore.EditedSize {
		t.Fatalf("rejected rewrite changed staging: before=%+v after=%+v", stateBefore, stateAfter)
	}
}

func TestStageEditRequiresExplicitPreparationAndCleanReleaseIsNonDestructive(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "explicit-prepare.txt", []byte("alpha\n"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })

	if _, err := service.StageEdit(meta.FileID, 0, 5, "ALPHA"); !errors.Is(err, ErrEditPreparationRequired) {
		t.Fatalf("unprepared StageEdit error = %v, want ErrEditPreparationRequired", err)
	}
	prepareEditSessionForTest(t, service, meta.FileID)
	if state, err := service.ReleaseCleanEditSession(meta.FileID); err != nil || state.EditCount != 0 {
		t.Fatalf("clean release = %+v, %v", state, err)
	}
	if _, err := service.StageEdit(meta.FileID, 0, 5, "ALPHA"); !errors.Is(err, ErrEditPreparationRequired) {
		t.Fatalf("StageEdit after clean release error = %v", err)
	}

	prepareEditSessionForTest(t, service, meta.FileID)
	if _, err := service.StageEdit(meta.FileID, 0, 5, "ALPHA"); err != nil {
		t.Fatal(err)
	}
	before, err := service.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReleaseCleanEditSession(meta.FileID); !errors.Is(err, ErrEditSessionDirty) {
		t.Fatalf("dirty release error = %v, want ErrEditSessionDirty", err)
	}
	after, err := service.GetStagingState(meta.FileID)
	if err != nil || after.EditCount != before.EditCount || after.EditedSize != before.EditedSize {
		t.Fatalf("dirty release changed staging: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestDiscardEditsSerializesWithInFlightStageEdit(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "discard-stage-race.txt", []byte("alpha\n"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })

	for iteration := 0; iteration < 64; iteration++ {
		prepareEditSessionForTest(t, service, meta.FileID)
		start := make(chan struct{})
		stageDone := make(chan error, 1)
		discardDone := make(chan error, 1)
		go func() {
			<-start
			_, err := service.StageEdit(meta.FileID, 0, 5, "ALPHA")
			stageDone <- err
		}()
		go func() {
			<-start
			_, err := service.DiscardEdits(meta.FileID)
			discardDone <- err
		}()
		close(start)

		if err := <-stageDone; err != nil && !errors.Is(err, ErrEditPreparationRequired) {
			t.Fatalf("iteration %d StageEdit: %v", iteration, err)
		}
		if err := <-discardDone; err != nil {
			t.Fatalf("iteration %d DiscardEdits: %v", iteration, err)
		}
		state, err := service.GetStagingState(meta.FileID)
		if err != nil {
			t.Fatalf("iteration %d GetStagingState: %v", iteration, err)
		}
		if state.EditCount != 0 {
			t.Fatalf("iteration %d retained %d edits after discard", iteration, state.EditCount)
		}
	}
}

func TestPreparedEditSessionsHaveAggregateAdmissionLimit(t *testing.T) {
	service := NewFileService()
	ids := make([]string, 0, maxPreparedEditSessions+1)
	for index := 0; index < maxPreparedEditSessions+1; index++ {
		path := writeTempFile(t, "prepared-limit-"+string(rune('a'+index))+".txt", []byte("alpha\n"))
		meta, err := service.OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, meta.FileID)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_ = service.CloseFile(id)
		}
	})
	for _, id := range ids[:maxPreparedEditSessions] {
		prepareEditSessionForTest(t, service, id)
	}
	if _, err := service.PrepareEditSession(ids[maxPreparedEditSessions]); !errors.Is(err, ErrPreparedEditLimit) {
		t.Fatalf("overflow prepare error = %v, want ErrPreparedEditLimit", err)
	}
	if _, err := service.ReleaseCleanEditSession(ids[0]); err != nil {
		t.Fatal(err)
	}
	prepareEditSessionForTest(t, service, ids[maxPreparedEditSessions])
}

func TestStageEditStrictRequestAndAuthenticatedRangeValidation(t *testing.T) {
	service := NewFileService()
	path := writeTempFile(t, "strict-stage.txt", []byte("alpha\n"))
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	for _, test := range []struct {
		name    string
		start   int64
		length  int64
		text    string
		wantErr error
	}{
		{"negative start", -1, 0, "", ErrEditRequestTooLarge},
		{"negative length", 0, -1, "", ErrEditRequestTooLarge},
		{"overflow", int64(^uint64(0) >> 1), 1, "", ErrEditRequestTooLarge},
		{"CR text", 0, 0, "\r", ErrFileNotEditable},
		{"NUL text", 0, 0, "\x00", ErrFileNotEditable},
		{"invalid UTF-8", 0, 0, string([]byte{0xff}), ErrFileNotEditable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.StageEdit(meta.FileID, test.start, test.length, test.text); !errors.Is(err, test.wantErr) {
				t.Fatalf("StageEdit error = %v, want %v", err, test.wantErr)
			}
		})
	}

	lateInvalid := make([]byte, (1<<20)+8)
	for index := range lateInvalid {
		lateInvalid[index] = 'a'
	}
	lateInvalid[1<<20+2] = '\r'
	latePath := writeTempFile(t, "late-invalid.txt", lateInvalid)
	lateMeta, err := service.OpenFile(latePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(lateMeta.FileID) })
	prepareEditSessionForTest(t, service, lateMeta.FileID)
	if _, err := service.StageEdit(lateMeta.FileID, 1<<20+2, 1, "x"); !errors.Is(err, ErrFileNotEditable) {
		t.Fatalf("authenticated CR range error = %v, want ErrFileNotEditable", err)
	}
}

func TestSaveCopyDialogApprovalRejectsChangedOrRepreparedSession(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *FileService, string)
	}{
		{
			name: "revision changed",
			mutate: func(t *testing.T, service *FileService, fileID string) {
				if _, err := service.StageEdit(fileID, 0, 5, "OMEGA"); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "discarded and reprepared",
			mutate: func(t *testing.T, service *FileService, fileID string) {
				if _, err := service.DiscardEdits(fileID); err != nil {
					t.Fatal(err)
				}
				prepareEditSessionForTest(t, service, fileID)
				if _, err := service.StageEdit(fileID, 0, 5, "ALPHA"); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := NewFileService()
			path := writeTempFile(t, "dialog-approval.txt", []byte("alpha\n"))
			meta, err := service.OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
			prepareEditSessionForTest(t, service, meta.FileID)
			if _, err := service.StageEdit(meta.FileID, 0, 5, "ALPHA"); err != nil {
				t.Fatal(err)
			}

			output := filepath.Join(t.TempDir(), "must-not-save.txt")
			entered := make(chan struct{})
			release := make(chan struct{})
			previous := saveCopySaveDialog
			saveCopySaveDialog = func() (string, error) {
				close(entered)
				<-release
				return output, nil
			}
			t.Cleanup(func() { saveCopySaveDialog = previous })

			saveDone := make(chan error, 1)
			go func() {
				_, err := service.SaveCopyViaDialog(meta.FileID)
				saveDone <- err
			}()
			<-entered
			test.mutate(t, service, meta.FileID)
			close(release)
			if err := <-saveDone; !errors.Is(err, ErrSaveCopyStateChanged) {
				t.Fatalf("SaveCopyViaDialog error = %v, want ErrSaveCopyStateChanged", err)
			}
			if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("approval race published output: %v", err)
			}
		})
	}
}

func TestDiffWindowMapsLengthChangingEditsToOriginalCoordinates(t *testing.T) {
	tests := []struct {
		name        string
		replacement string
	}{
		{"insert", "BRAVO EXPANDED\n"},
		{"delete", "B\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const source = "alpha\nbravo\ncharlie\n"
			service := NewFileService()
			path := writeTempFile(t, "diff-map.txt", []byte(source))
			meta, err := service.OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
			prepareEditSessionForTest(t, service, meta.FileID)
			if _, err := service.StageEdit(meta.FileID, 6, int64(len("bravo\n")), test.replacement); err != nil {
				t.Fatal(err)
			}
			window, err := service.GetDiffWindow(meta.FileID, 0, 1024)
			if err != nil {
				t.Fatal(err)
			}
			wantEdited := "alpha\n" + test.replacement + "charlie\n"
			if window.Original != source || window.Edited != wantEdited {
				t.Fatalf("diff contents original=%q edited=%q", window.Original, window.Edited)
			}
			if window.OriginalStartByte != 0 || window.OriginalNextByte != int64(len(source)) ||
				window.EditedStartByte != 0 || window.EditedNextByte != int64(len(wantEdited)) {
				t.Fatalf("diff coordinates = %+v", window)
			}
		})
	}
}
