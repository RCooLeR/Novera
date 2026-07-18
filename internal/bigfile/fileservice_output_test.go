package bigfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"novera/internal/bigfile/document"
)

func openOutputTestDocument(t *testing.T, data string) (string, *document.FileDocument) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = doc.Close() })
	return path, doc
}

func TestWriteSafeOutputRejectsOpenedSourceIdentity(t *testing.T) {
	source, doc := openOutputTestDocument(t, "source-data")
	producerCalled := false
	_, err := writeSafeOutput(doc, source, source, func(w io.Writer) error {
		producerCalled = true
		_, writeErr := w.Write([]byte("destroyed"))
		return writeErr
	})
	if !errors.Is(err, ErrOutputAliasesSource) {
		t.Fatalf("error = %v, want ErrOutputAliasesSource", err)
	}
	if producerCalled {
		t.Fatal("producer ran for source-alias destination")
	}
	got, readErr := os.ReadFile(source)
	if readErr != nil || string(got) != "source-data" {
		t.Fatalf("source changed: %q, %v", got, readErr)
	}
}

func TestWriteSafeOutputRejectsHardLinkAlias(t *testing.T) {
	source, doc := openOutputTestDocument(t, "source-data")
	alias := filepath.Join(filepath.Dir(source), "alias.txt")
	if err := os.Link(source, alias); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("hard links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	_, err := writeSafeOutput(doc, source, alias, func(w io.Writer) error {
		_, writeErr := w.Write([]byte("destroyed"))
		return writeErr
	})
	if !errors.Is(err, ErrOutputAliasesSource) {
		t.Fatalf("error = %v, want ErrOutputAliasesSource", err)
	}
	got, readErr := os.ReadFile(source)
	if readErr != nil || string(got) != "source-data" {
		t.Fatalf("source changed: %q, %v", got, readErr)
	}
}

func TestWriteSafeOutputProducerFailurePreservesDestination(t *testing.T) {
	source, doc := openOutputTestDocument(t, "source-data")
	destination := filepath.Join(filepath.Dir(source), "output.txt")
	if err := os.WriteFile(destination, []byte("old-output"), 0o640); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected producer failure")
	written, err := writeSafeOutput(doc, source, destination, func(w io.Writer) error {
		if _, err := w.Write([]byte("partial-new-output")); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want injected failure", err)
	}
	if written != int64(len("partial-new-output")) {
		t.Fatalf("written = %d", written)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "old-output" {
		t.Fatalf("destination changed: %q, %v", got, readErr)
	}
	temps, globErr := filepath.Glob(filepath.Join(filepath.Dir(source), ".novera-bigfile-*.tmp"))
	if globErr != nil {
		t.Fatal(globErr)
	}
	if len(temps) != 0 {
		t.Fatalf("partial temps remain: %v", temps)
	}
}

func TestWriteSafeOutputAtomicallyReplacesCompleteDestination(t *testing.T) {
	source, doc := openOutputTestDocument(t, "source-data")
	destination := filepath.Join(filepath.Dir(source), "output.txt")
	if err := os.WriteFile(destination, []byte("old-output"), 0o640); err != nil {
		t.Fatal(err)
	}
	written, err := writeSafeOutput(doc, source, destination, func(w io.Writer) error {
		_, err := w.Write([]byte("complete-new-output"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if written != int64(len("complete-new-output")) {
		t.Fatalf("written = %d", written)
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "complete-new-output" {
		t.Fatalf("destination = %q, %v", got, readErr)
	}
}
