package inplace

import (
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

func TestRecoverCorruptSidecarDeletes(t *testing.T) {
	path := writeFile(t, "data")
	sidecar := path + ".qrp"
	if err := os.WriteFile(sidecar, []byte("not a real sidecar"), 0o600); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := Recover(path, sidecar)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if rolledBack {
		t.Fatal("corrupt sidecar must not roll back")
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatal("corrupt sidecar should be removed")
	}
	if got := read(t, path); got != "data" {
		t.Fatalf("file must be untouched, got %q", got)
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
