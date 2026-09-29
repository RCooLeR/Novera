package exportx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
)

func TestExportOutputIsInvisibleUntilComplete(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.txt")
	outputPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(sourcePath, []byte("alpha\nbravo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	var visibleDuringWrite bool
	_, err = ExportByteRange(context.Background(), doc, sourcePath, outputPath, 0, doc.Size(), Options{
		Progress: func(done int64, total int64) {
			if done > 0 {
				_, statErr := os.Lstat(outputPath)
				visibleDuringWrite = statErr == nil
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if visibleDuringWrite {
		t.Fatal("final output became visible while bytes were still being written")
	}
}

func TestExportPublicationRacePreservesCompetingOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.txt")
	outputPath := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(sourcePath, []byte("operation"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	created := false
	_, err = ExportByteRange(context.Background(), doc, sourcePath, outputPath, 0, doc.Size(), Options{
		Progress: func(done int64, total int64) {
			if !created && done == total {
				created = true
				if writeErr := os.WriteFile(outputPath, []byte("competitor"), 0o600); writeErr != nil {
					t.Fatalf("create competing output: %v", writeErr)
				}
			}
		},
	})
	if !errors.Is(err, fileio.ErrExists) {
		t.Fatalf("error = %v, want ErrExists", err)
	}
	if got, readErr := os.ReadFile(outputPath); readErr != nil || string(got) != "competitor" {
		t.Fatalf("competing output = %q, %v", got, readErr)
	}
}

func TestExportRejectsHardLinkAliasOfSource(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.txt")
	aliasPath := filepath.Join(dir, "alias.txt")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(sourcePath, aliasPath); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()

	if _, err := ExportByteRange(context.Background(), doc, sourcePath, aliasPath, 0, doc.Size(), Options{}); !errors.Is(err, fileio.ErrSourceAlias) {
		t.Fatalf("error = %v, want ErrSourceAlias", err)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || string(got) != "source" {
		t.Fatalf("source changed: %q, %v", got, err)
	}
}

func TestExportRejectsShortSourceWithoutPublishing(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output.txt")
	reader := shortReaderAt{data: []byte("short"), size: 20}
	summary, err := ExportByteRange(context.Background(), reader, "", outputPath, 0, reader.Size(), Options{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
	}
	if summary.BytesWritten != 0 {
		t.Fatalf("reported bytes = %d, want zero on failed export", summary.BytesWritten)
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short source published output: %v", err)
	}
}

func TestTextExportRejectsShortSourceWithoutPublishing(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "output.txt")
	reader := shortReaderAt{data: []byte("short"), size: 20}
	summary, err := ExportByteRangeText(
		context.Background(),
		reader,
		"",
		outputPath,
		0,
		reader.Size(),
		"UTF-8",
		"UTF-8",
		Options{},
	)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("error = %v, want io.ErrUnexpectedEOF", err)
	}
	if summary.BytesWritten != 0 {
		t.Fatalf("reported bytes = %d, want zero on failed text export", summary.BytesWritten)
	}
	if _, err := os.Lstat(outputPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("short text source published output: %v", err)
	}
}

type shortReaderAt struct {
	data []byte
	size int64
}

func (r shortReaderAt) Size() int64 { return r.size }

func (r shortReaderAt) ReadAt(dst []byte, offset int64) (int, error) {
	if offset >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(dst, r.data[offset:])
	return n, io.EOF
}
