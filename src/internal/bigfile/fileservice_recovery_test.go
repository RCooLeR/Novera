package bigfile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"novera/internal/bigfile/session"
)

func TestOpenFileDoesNotReplayAdjacentPendingRecoverySidecar(t *testing.T) {
	original := []byte("trusted-current-data\n")
	path := writeTempFile(t, "data.txt", original)
	sidecar := path + ".qrp"

	// This is a syntactically valid pending sidecar. The recovery primitive
	// would overwrite the first eight source bytes with attacker-controlled
	// rollback bytes if OpenFile trusted adjacency as proof of provenance.
	sidecarData := pendingSidecarForTest(t, int64(len(original)), 0, []byte("ATTACK!!"))
	if err := os.WriteFile(sidecar, sidecarData, 0o600); err != nil {
		t.Fatal(err)
	}

	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() {
		if err := svc.CloseFile(meta.FileID); err != nil {
			t.Errorf("CloseFile: %v", err)
		}
	}()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("OpenFile mutated source: got %q, want %q", got, original)
	}
	gotSidecar, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("recovery evidence was not preserved: %v", err)
	}
	if !bytes.Equal(gotSidecar, sidecarData) {
		t.Fatal("OpenFile modified adjacent recovery evidence")
	}
}

func TestOpenFilePreservesMalformedRecoveryEvidence(t *testing.T) {
	original := []byte("current data\n")
	path := writeTempFile(t, "malformed.txt", original)
	sidecar := path + ".qrp"
	evidence := []byte("incomplete recovery evidence")
	if err := os.WriteFile(sidecar, evidence, 0o600); err != nil {
		t.Fatal(err)
	}

	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() {
		if err := svc.CloseFile(meta.FileID); err != nil {
			t.Errorf("CloseFile: %v", err)
		}
	}()

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("OpenFile mutated source: got %q, want %q", got, original)
	}
	gotSidecar, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("recovery evidence was not preserved: %v", err)
	}
	if !bytes.Equal(gotSidecar, evidence) {
		t.Fatal("OpenFile modified malformed recovery evidence")
	}
}

func TestSavePatchIsDisabledAndPreservesSourceStagingAndRecoveryEvidence(t *testing.T) {
	original := []byte("trusted-current-data\n")
	path := writeTempFile(t, "pending-save.txt", original)
	sidecar := path + ".qrp"
	sidecarData := pendingSidecarForTest(t, int64(len(original)), 0, []byte("ATTACK!!"))
	if err := os.WriteFile(sidecar, sidecarData, 0o600); err != nil {
		t.Fatal(err)
	}

	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}
	defer func() {
		if err := svc.CloseFile(meta.FileID); err != nil {
			t.Errorf("CloseFile: %v", err)
		}
	}()

	replacement := []byte("TRUSTED-current-data\n")
	prepareEditSessionForTest(t, svc, meta.FileID)
	state, err := svc.StageEdit(meta.FileID, 0, int64(len(original)), string(replacement))
	if err != nil {
		t.Fatalf("StageEdit: %v", err)
	}
	if state.InPlaceEligible || state.EditCount == 0 {
		t.Fatalf("expected copy-only staged edit, got %+v", state)
	}

	if _, err := svc.SavePatch(meta.FileID); !errors.Is(err, ErrInPlaceSaveDisabled) {
		t.Fatalf("SavePatch error = %v, want ErrInPlaceSaveDisabled", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("SavePatch mutated source: got %q, want %q", got, original)
	}
	gotSidecar, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("recovery evidence was not preserved: %v", err)
	}
	if !bytes.Equal(gotSidecar, sidecarData) {
		t.Fatal("SavePatch modified pending recovery evidence")
	}
	remaining, err := svc.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatalf("GetStagingState: %v", err)
	}
	if remaining.EditCount == 0 {
		t.Fatal("failed SavePatch discarded the staged edit")
	}
}

func TestSavePatchFailsClosedBeforeFileLookup(t *testing.T) {
	svc := NewFileService()
	for _, fileID := range []string{"", "unknown-file-id"} {
		if _, err := svc.SavePatch(fileID); !errors.Is(err, ErrInPlaceSaveDisabled) {
			t.Fatalf("SavePatch(%q) error = %v, want ErrInPlaceSaveDisabled", fileID, err)
		}
	}
}

func TestRefreshFileRefusesToDiscardStagedEdits(t *testing.T) {
	original := []byte("alpha\nbeta\n")
	path := writeTempFile(t, "refresh-staged.txt", original)
	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.CloseFile(meta.FileID) }()

	prepareEditSessionForTest(t, svc, meta.FileID)
	if _, err := svc.StageEdit(meta.FileID, 0, 5, "ALPHA"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefreshFile(meta.FileID); !errors.Is(err, session.ErrStagedEdits) {
		t.Fatalf("RefreshFile error = %v, want ErrStagedEdits", err)
	}
	state, err := svc.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if state.EditCount != 1 {
		t.Fatalf("staged edits after failed refresh = %d, want 1", state.EditCount)
	}
}

func pendingSidecarForTest(t *testing.T, fileSize, offset int64, rollback []byte) []byte {
	t.Helper()
	var sidecar bytes.Buffer
	sidecar.Write([]byte{'Q', 'R', 'Y', 'R', 'P', 0, 0, 1})
	sidecar.WriteByte(0) // pending, not committed
	for _, value := range []int64{fileSize, 1, offset, int64(len(rollback))} {
		if err := binary.Write(&sidecar, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	sidecar.Write(rollback)
	return sidecar.Bytes()
}
