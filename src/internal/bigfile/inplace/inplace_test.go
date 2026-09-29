package inplace

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.bin")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApplyOverwritesBytesAndRemovesSidecar(t *testing.T) {
	path := writeFile(t, "hello world, hello there")
	sidecar := path + ".qrp"
	patches := []Patch{
		{Offset: 6, Old: []byte("world"), New: []byte("WORLD")},
		{Offset: 19, Old: []byte("there"), New: []byte("THERE")},
	}
	if err := Apply(path, patches, sidecar); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := read(t, path); got != "hello WORLD, hello THERE" {
		t.Fatalf("content = %q", got)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar should be removed, stat err = %v", err)
	}
}

func TestApplyUnsortedPatches(t *testing.T) {
	path := writeFile(t, "0123456789")
	patches := []Patch{
		{Offset: 8, Old: []byte("89"), New: []byte("YZ")},
		{Offset: 0, Old: []byte("01"), New: []byte("AB")},
	}
	if err := Apply(path, patches, path+".qrp"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := read(t, path); got != "AB234567YZ" {
		t.Fatalf("content = %q", got)
	}
}

func TestApplyRejectsLengthChange(t *testing.T) {
	path := writeFile(t, "hello world")
	err := Apply(path, []Patch{{Offset: 6, Old: []byte("world"), New: []byte("worlds")}}, path+".qrp")
	if err != ErrNotLengthPreserving {
		t.Fatalf("want ErrNotLengthPreserving, got %v", err)
	}
	if got := read(t, path); got != "hello world" {
		t.Fatalf("file must be unchanged, got %q", got)
	}
}

func TestApplyRejectsDrift(t *testing.T) {
	path := writeFile(t, "hello world")
	err := Apply(path, []Patch{{Offset: 6, Old: []byte("WORLD"), New: []byte("xxxxx")}}, path+".qrp")
	if err != ErrDrift {
		t.Fatalf("want ErrDrift, got %v", err)
	}
	if got := read(t, path); got != "hello world" {
		t.Fatalf("file must be unchanged, got %q", got)
	}
	if _, err := os.Stat(path + ".qrp"); !os.IsNotExist(err) {
		t.Fatalf("no sidecar should remain on drift")
	}
}

func TestApplyRejectsOutOfRange(t *testing.T) {
	path := writeFile(t, "short")
	err := Apply(path, []Patch{{Offset: 4, Old: []byte("tXXXX"), New: []byte("xXXXX")}}, path+".qrp")
	if err != ErrOutOfRange {
		t.Fatalf("want ErrOutOfRange, got %v", err)
	}
}

func TestApplyRejectsOffsetOverflow(t *testing.T) {
	path := writeFile(t, "short")
	err := Apply(path, []Patch{{Offset: maxInt64, Old: []byte("x"), New: []byte("y")}}, path+".qrp")
	if !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("want ErrOutOfRange, got %v", err)
	}
	if got := read(t, path); got != "short" {
		t.Fatalf("file must be unchanged, got %q", got)
	}
}

func TestApplyRejectsSourceAsSidecar(t *testing.T) {
	path := writeFile(t, "hello world")
	err := Apply(path, []Patch{{Offset: 6, Old: []byte("world"), New: []byte("WORLD")}}, path)
	if !errors.Is(err, ErrRecoveryPathAlias) {
		t.Fatalf("Apply error = %v, want ErrRecoveryPathAlias", err)
	}
	if got := read(t, path); got != "hello world" {
		t.Fatalf("source changed to %q", got)
	}
}

func TestApplyRejectsOverlap(t *testing.T) {
	path := writeFile(t, "abcdefgh")
	patches := []Patch{
		{Offset: 2, Old: []byte("cde"), New: []byte("CDE")},
		{Offset: 3, Old: []byte("de"), New: []byte("DE")},
	}
	if err := Apply(path, patches, path+".qrp"); err != ErrOverlap {
		t.Fatalf("want ErrOverlap, got %v", err)
	}
}

func TestApplyPreservesExistingRecoverySidecar(t *testing.T) {
	path := writeFile(t, "hello world")
	sidecar := path + ".qrp"
	evidence := []byte("existing recovery evidence")
	if err := os.WriteFile(sidecar, evidence, 0o600); err != nil {
		t.Fatal(err)
	}

	err := Apply(path, []Patch{{Offset: 6, Old: []byte("world"), New: []byte("WORLD")}}, sidecar)
	if !errors.Is(err, ErrRecoveryPending) {
		t.Fatalf("Apply error = %v, want ErrRecoveryPending", err)
	}
	if got := read(t, path); got != "hello world" {
		t.Fatalf("file must be unchanged, got %q", got)
	}
	gotEvidence, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotEvidence) != string(evidence) {
		t.Fatalf("sidecar was modified: got %q, want %q", gotEvidence, evidence)
	}
}

func TestRecoverRollsBackPendingSidecar(t *testing.T) {
	// Simulate a crash: write the pending sidecar (original bytes), then apply
	// the byte changes directly, but never commit/remove the sidecar.
	path := writeFile(t, "hello world")
	sidecar := path + ".qrp"
	patches := []Patch{{Offset: 6, Old: []byte("world"), New: []byte("WORLD")}}
	if err := writeSidecar(sidecar, 11, patches); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("hello WORLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	rolledBack, err := Recover(path, sidecar)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if !rolledBack {
		t.Fatal("expected rollback")
	}
	if got := read(t, path); got != "hello world" {
		t.Fatalf("rollback content = %q", got)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatal("sidecar should be removed after recover")
	}
}

func TestRecoverCommittedJustDeletes(t *testing.T) {
	path := writeFile(t, "hello WORLD") // already-applied state
	sidecar := path + ".qrp"
	if err := writeSidecar(sidecar, 11, []Patch{{Offset: 6, Old: []byte("world"), New: []byte("WORLD")}}); err != nil {
		t.Fatal(err)
	}
	if err := markCommitted(sidecar); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := Recover(path, sidecar)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rolledBack {
		t.Fatal("committed sidecar must NOT roll back")
	}
	if got := read(t, path); got != "hello WORLD" {
		t.Fatalf("content must be unchanged, got %q", got)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatal("sidecar should be removed")
	}
}

func TestRecoverNoSidecarIsNoop(t *testing.T) {
	path := writeFile(t, "data")
	rolledBack, err := Recover(path, path+".qrp")
	if err != nil || rolledBack {
		t.Fatalf("want no-op, got rolledBack=%v err=%v", rolledBack, err)
	}
}

func TestRecoverRejectsSourceAsSidecarWithoutDeletingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source-and-sidecar")
	evidence := encodeRecoverySidecar(t, 1, sidecarHeaderBytes, 0, nil, nil)
	if err := os.WriteFile(path, evidence, 0o600); err != nil {
		t.Fatal(err)
	}

	rolledBack, err := Recover(path, path)
	if rolledBack || !errors.Is(err, ErrRecoveryPathAlias) {
		t.Fatalf("Recover = (%v, %v), want false and ErrRecoveryPathAlias", rolledBack, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("aliased source was deleted: %v", err)
	}
	if !bytes.Equal(got, evidence) {
		t.Fatal("aliased source was modified")
	}
}

func TestRecoverRejectsHardLinkAlias(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "journal.qrp")
	source := filepath.Join(dir, "source.bin")
	evidence := encodeRecoverySidecar(t, 1, sidecarHeaderBytes, 0, nil, nil)
	if err := os.WriteFile(sidecar, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sidecar, source); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}

	rolledBack, err := Recover(source, sidecar)
	if rolledBack || !errors.Is(err, ErrRecoveryPathAlias) {
		t.Fatalf("Recover = (%v, %v), want false and ErrRecoveryPathAlias", rolledBack, err)
	}
	for _, path := range []string{source, sidecar} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, evidence) {
			t.Fatalf("alias %q changed or disappeared: bytes=%q err=%v", path, got, readErr)
		}
	}
}

func TestRecoverRejectsSymbolicLinkAlias(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "journal.qrp")
	source := filepath.Join(dir, "source.bin")
	evidence := encodeRecoverySidecar(t, 1, sidecarHeaderBytes, 0, nil, nil)
	if err := os.WriteFile(sidecar, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sidecar, source); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}

	rolledBack, err := Recover(source, sidecar)
	if rolledBack || !errors.Is(err, ErrRecoveryPathAlias) {
		t.Fatalf("Recover = (%v, %v), want false and ErrRecoveryPathAlias", rolledBack, err)
	}
	got, readErr := os.ReadFile(sidecar)
	if readErr != nil || !bytes.Equal(got, evidence) {
		t.Fatalf("sidecar changed or disappeared: bytes=%q err=%v", got, readErr)
	}
}

func TestRecoverCorruptSidecarPreservesEvidence(t *testing.T) {
	path := writeFile(t, "data")
	sidecar := path + ".qrp"
	evidence := []byte("not a real sidecar")
	if err := os.WriteFile(sidecar, evidence, 0o600); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := Recover(path, sidecar)
	if !errors.Is(err, ErrInvalidSidecar) {
		t.Fatalf("Recover error = %v, want ErrInvalidSidecar", err)
	}
	if rolledBack {
		t.Fatal("corrupt sidecar must not roll back")
	}
	gotEvidence, readErr := os.ReadFile(sidecar)
	if readErr != nil {
		t.Fatalf("corrupt sidecar must be preserved: %v", readErr)
	}
	if !bytes.Equal(gotEvidence, evidence) {
		t.Fatalf("sidecar changed: got %q, want %q", gotEvidence, evidence)
	}
	if got := read(t, path); got != "data" {
		t.Fatalf("file must be untouched, got %q", got)
	}
}

func TestRecoverRejectsMaliciousSidecarsBeforeMutation(t *testing.T) {
	tests := []struct {
		name       string
		fileSize   int64
		flag       byte
		count      int64
		entries    []encodedRecoveryEntry
		trailing   []byte
		wantTooBig bool
	}{
		{name: "invalid committed flag", fileSize: 8, flag: 2},
		{name: "negative file size", fileSize: -1},
		{name: "entry count limit", fileSize: 8, count: maxRecoveryEntries + 1, wantTooBig: true},
		{name: "negative offset", fileSize: 8, count: 1, entries: []encodedRecoveryEntry{{offset: -1, length: 1, data: []byte("x")}}},
		{name: "range overflow", fileSize: int64(^uint64(0) >> 1), count: 1, entries: []encodedRecoveryEntry{{offset: int64(^uint64(0) >> 1), length: 1, data: []byte("x")}}},
		{name: "out of range", fileSize: 8, count: 1, entries: []encodedRecoveryEntry{{offset: 7, length: 2, data: []byte("xx")}}},
		{name: "overlap", fileSize: 8, count: 2, entries: []encodedRecoveryEntry{{offset: 1, length: 3, data: []byte("abc")}, {offset: 2, length: 1, data: []byte("d")}}},
		{name: "unsorted", fileSize: 8, count: 2, entries: []encodedRecoveryEntry{{offset: 5, length: 1, data: []byte("a")}, {offset: 1, length: 1, data: []byte("b")}}},
		{name: "truncated payload", fileSize: 8, count: 1, entries: []encodedRecoveryEntry{{offset: 1, length: 3, data: []byte("a")}}},
		{name: "trailing bytes", fileSize: 8, trailing: []byte("unexpected")},
		{name: "rollback byte limit", fileSize: int64(^uint64(0) >> 1), count: 1, entries: []encodedRecoveryEntry{{offset: 0, length: maxRecoveryRollback + 1}}, wantTooBig: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, "original")
			sidecar := path + ".qrp"
			evidence := encodeRecoverySidecar(t, tc.flag, tc.fileSize, tc.count, tc.entries, tc.trailing)
			if err := os.WriteFile(sidecar, evidence, 0o600); err != nil {
				t.Fatal(err)
			}

			rolledBack, err := Recover(path, sidecar)
			if rolledBack {
				t.Fatal("malicious sidecar must not roll back")
			}
			if tc.wantTooBig {
				if !errors.Is(err, ErrRecoveryTooLarge) {
					t.Fatalf("Recover error = %v, want ErrRecoveryTooLarge", err)
				}
			} else if !errors.Is(err, ErrInvalidSidecar) {
				t.Fatalf("Recover error = %v, want ErrInvalidSidecar", err)
			}
			if got := read(t, path); got != "original" {
				t.Fatalf("source changed to %q", got)
			}
			gotEvidence, readErr := os.ReadFile(sidecar)
			if readErr != nil {
				t.Fatalf("sidecar must be preserved: %v", readErr)
			}
			if !bytes.Equal(gotEvidence, evidence) {
				t.Fatal("sidecar evidence changed")
			}
		})
	}
}

func TestRecoverRejectsChangedSourceSizeAndPreservesSidecar(t *testing.T) {
	path := writeFile(t, "hello world")
	sidecar := path + ".qrp"
	if err := writeSidecar(sidecar, 11, []Patch{{Offset: 6, Old: []byte("world"), New: []byte("WORLD")}}); err != nil {
		t.Fatal(err)
	}
	evidence, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("different-size"), 0o644); err != nil {
		t.Fatal(err)
	}

	rolledBack, err := Recover(path, sidecar)
	if rolledBack || !errors.Is(err, ErrRecoverySourceMismatch) {
		t.Fatalf("Recover = (%v, %v), want false and ErrRecoverySourceMismatch", rolledBack, err)
	}
	if got := read(t, path); got != "different-size" {
		t.Fatalf("source changed to %q", got)
	}
	gotEvidence, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatalf("sidecar must be preserved: %v", err)
	}
	if !bytes.Equal(gotEvidence, evidence) {
		t.Fatal("sidecar evidence changed")
	}
}

func TestApplyThenReopenCleanState(t *testing.T) {
	// After a successful Apply, Recover must be a no-op (no sidecar left).
	path := writeFile(t, "aaaa bbbb")
	sidecar := path + ".qrp"
	if err := Apply(path, []Patch{{Offset: 5, Old: []byte("bbbb"), New: []byte("BBBB")}}, sidecar); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := Recover(path, sidecar)
	if err != nil || rolledBack {
		t.Fatalf("post-apply recover should be no-op, got rolledBack=%v err=%v", rolledBack, err)
	}
	if got := read(t, path); got != "aaaa BBBB" {
		t.Fatalf("content = %q", got)
	}
}

type encodedRecoveryEntry struct {
	offset int64
	length int64
	data   []byte
}

func encodeRecoverySidecar(t *testing.T, flag byte, fileSize, count int64, entries []encodedRecoveryEntry, trailing []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(sidecarMagic[:])
	buf.WriteByte(flag)
	for _, value := range []int64{fileSize, count} {
		if err := binary.Write(&buf, binary.LittleEndian, value); err != nil {
			t.Fatal(err)
		}
	}
	for _, entry := range entries {
		for _, value := range []int64{entry.offset, entry.length} {
			if err := binary.Write(&buf, binary.LittleEndian, value); err != nil {
				t.Fatal(err)
			}
		}
		buf.Write(entry.data)
	}
	buf.Write(trailing)
	return buf.Bytes()
}
