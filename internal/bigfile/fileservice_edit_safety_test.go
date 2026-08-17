package bigfile

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSaveCopyUsesExactCopyOnlyPublicationAndPreservesStaging(t *testing.T) {
	sourcePath := writeTempFile(t, "save-copy-source.txt", []byte("alpha bravo charlie"))
	outputPath := filepath.Join(t.TempDir(), "edited.txt")
	if err := os.WriteFile(outputPath, []byte("existing destination"), 0o600); err != nil {
		t.Fatal(err)
	}

	svc := NewFileService()
	meta, err := svc.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.CloseFile(meta.FileID) }()

	prepareEditSessionForTest(t, svc, meta.FileID)
	if _, err := svc.StageEdit(meta.FileID, 6, 5, "delta"); err != nil {
		t.Fatal(err)
	}
	result, err := svc.saveCopy(meta.FileID, outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "copy" || result.OutputPath != outputPath || result.BytesWritten != int64(len("alpha delta charlie")) {
		t.Fatalf("result = %+v", result)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || string(got) != "alpha bravo charlie" {
		t.Fatalf("source changed: %q, %v", got, err)
	}
	if got, err := os.ReadFile(outputPath); err != nil || string(got) != "alpha delta charlie" {
		t.Fatalf("output = %q, %v", got, err)
	}
	state, err := svc.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if state.EditCount != 1 || state.InPlaceEligible {
		t.Fatalf("staging state = %+v", state)
	}
}

func TestSaveCopyRejectsSourceAliasWithoutMutation(t *testing.T) {
	sourcePath := writeTempFile(t, "save-copy-alias.txt", []byte("alpha bravo"))
	svc := NewFileService()
	meta, err := svc.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = svc.CloseFile(meta.FileID) }()

	prepareEditSessionForTest(t, svc, meta.FileID)
	if _, err := svc.StageEdit(meta.FileID, 6, 5, "delta"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.saveCopy(meta.FileID, sourcePath); !errors.Is(err, ErrOutputAliasesSource) {
		t.Fatalf("SaveCopy error = %v, want ErrOutputAliasesSource", err)
	}
	if got, err := os.ReadFile(sourcePath); err != nil || string(got) != "alpha bravo" {
		t.Fatalf("source changed: %q, %v", got, err)
	}
	state, err := svc.GetStagingState(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if state.EditCount != 1 {
		t.Fatalf("staging state = %+v", state)
	}
}

func TestBoundedEditWindowEndCannotOverflow(t *testing.T) {
	for _, test := range []struct {
		start  int64
		size   int64
		budget int64
		want   int64
	}{
		{start: math.MaxInt64 - 8, size: math.MaxInt64, budget: 1 << 20, want: math.MaxInt64},
		{start: 10, size: 100, budget: 20, want: 30},
		{start: 100, size: 100, budget: 20, want: 100},
	} {
		if got := boundedEditWindowEnd(test.start, test.size, test.budget); got != test.want {
			t.Fatalf("boundedEditWindowEnd(%d,%d,%d) = %d, want %d", test.start, test.size, test.budget, got, test.want)
		}
	}
}

func TestEditRPCsRejectReadOnlyFormats(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "CRLF", data: []byte("alpha\r\nbeta\r\n")},
		{name: "CR", data: []byte("alpha\rbeta\r")},
		{name: "mixed line endings", data: []byte("alpha\nbeta\r\ngamma\r")},
		{name: "binary", data: []byte{'a', 0, 'b', 0, 'c'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeTempFile(t, "read-only.dat", test.data)
			service := NewFileService()
			meta, err := service.OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
			if meta.Editable {
				t.Fatalf("metadata unexpectedly marks %s input editable", test.name)
			}
			if _, err := service.GetEditWindow(meta.FileID, 0, 1024); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("GetEditWindow error = %v, want ErrFileNotEditable", err)
			}
			if _, err := service.GetDiffWindow(meta.FileID, 0, 1024); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("GetDiffWindow error = %v, want ErrFileNotEditable", err)
			}
			if _, err := service.PrepareEditSession(meta.FileID); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("PrepareEditSession error = %v, want ErrFileNotEditable", err)
			}
			if _, err := service.StageEdit(meta.FileID, 0, 1, "x"); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("StageEdit error = %v, want ErrFileNotEditable", err)
			}
		})
	}
}

func TestEditRPCsRejectUnsupportedBytesBeyondMetadataSample(t *testing.T) {
	tests := []struct {
		name   string
		suffix []byte
	}{
		{name: "CRLF", suffix: []byte{'\r', '\n', 'z'}},
		{name: "NUL", suffix: []byte{0, 'z'}},
		{name: "invalid UTF-8", suffix: []byte{0xff, 'z'}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prefix := append(bytes.Repeat([]byte{'a'}, 1<<20), '\n')
			lateOffset := int64(len(prefix))
			data := append(append([]byte(nil), prefix...), test.suffix...)
			path := writeTempFile(t, "late-unsupported.txt", data)
			service := NewFileService()
			meta, err := service.OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
			if !meta.Editable {
				t.Fatal("bounded metadata prefix should remain editable for this regression")
			}
			if _, err := service.GetEditWindow(meta.FileID, lateOffset, 1024); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("GetEditWindow error = %v, want ErrFileNotEditable", err)
			}
			if _, err := service.GetDiffWindow(meta.FileID, lateOffset, 1024); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("GetDiffWindow error = %v, want ErrFileNotEditable", err)
			}
			prepareEditSessionForTest(t, service, meta.FileID)
			if _, err := service.StageEdit(meta.FileID, lateOffset, int64(len(test.suffix)), strings.Repeat("x", len(test.suffix))); !errors.Is(err, ErrFileNotEditable) {
				t.Fatalf("StageEdit error = %v, want ErrFileNotEditable", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("rejected edit changed source bytes")
			}
		})
	}
}

func TestEditWindowRequestBudgetsFailClosed(t *testing.T) {
	service := NewFileService()
	meta, err := service.OpenFile(writeTempFile(t, "bounded-edit.txt", []byte("alpha\n")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	for _, request := range []struct {
		name  string
		start int64
		max   int
	}{
		{name: "negative start", start: -1, max: 16},
		{name: "negative budget", start: 0, max: -1},
		{name: "oversized budget", start: 0, max: maxEditWindowBytes + 1},
		{name: "one byte", start: 0, max: 1},
		{name: "two bytes", start: 0, max: 2},
		{name: "three bytes", start: 0, max: 3},
	} {
		t.Run(request.name, func(t *testing.T) {
			if _, err := service.GetEditWindow(meta.FileID, request.start, request.max); !errors.Is(err, ErrEditRequestTooLarge) {
				t.Fatalf("GetEditWindow error = %v, want ErrEditRequestTooLarge", err)
			}
			if _, err := service.GetDiffWindow(meta.FileID, request.start, request.max); !errors.Is(err, ErrEditRequestTooLarge) {
				t.Fatalf("GetDiffWindow error = %v, want ErrEditRequestTooLarge", err)
			}
		})
	}
	if _, err := service.GetEditWindow(meta.FileID, 0, 0); err != nil {
		t.Fatalf("default edit budget: %v", err)
	}
}

func TestEditWindowsRejectIsolatedContinuationAndSkipValidatedRuneTail(t *testing.T) {
	t.Run("isolated continuation", func(t *testing.T) {
		prefix := append(bytes.Repeat([]byte{'a'}, 1<<20), '\n')
		offset := int64(len(prefix))
		data := append(append([]byte(nil), prefix...), 0x80, 'x', '\n')
		service := NewFileService()
		meta, err := service.OpenFile(writeTempFile(t, "isolated-continuation.txt", data))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
		if _, err := service.GetEditWindow(meta.FileID, offset, 1024); !errors.Is(err, ErrFileNotEditable) {
			t.Fatalf("GetEditWindow error = %v, want ErrFileNotEditable", err)
		}
		if _, err := service.GetDiffWindow(meta.FileID, offset, 1024); !errors.Is(err, ErrFileNotEditable) {
			t.Fatalf("GetDiffWindow error = %v, want ErrFileNotEditable", err)
		}
	})

	t.Run("validated rune tail", func(t *testing.T) {
		data := []byte("aa\u20acx\n")
		service := NewFileService()
		meta, err := service.OpenFile(writeTempFile(t, "valid-continuation.txt", data))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
		window, err := service.GetEditWindow(meta.FileID, 3, 4)
		if err != nil {
			t.Fatal(err)
		}
		if window.StartByte != 5 || window.Text != "x\n" {
			t.Fatalf("window = start %d text %q, want start 5 text %q", window.StartByte, window.Text, "x\n")
		}
	})
}

func TestEditWindowsPreserveUTF8AcrossByteBudgetBoundary(t *testing.T) {
	data := []byte("aa\u20acx\n")
	service := NewFileService()
	meta, err := service.OpenFile(writeTempFile(t, "utf8-boundary.txt", data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })

	for _, test := range []struct {
		name string
		read func(int64) (string, int64, bool, error)
	}{
		{
			name: "edit",
			read: func(start int64) (string, int64, bool, error) {
				window, err := service.GetEditWindow(meta.FileID, start, utf8.UTFMax)
				return window.Text, window.NextByte, window.AtEOF, err
			},
		},
		{
			name: "diff",
			read: func(start int64) (string, int64, bool, error) {
				window, err := service.GetDiffWindow(meta.FileID, start, utf8.UTFMax)
				if err == nil && window.Original != window.Edited {
					return "", 0, false, errors.New("unedited diff sides differ")
				}
				return window.Edited, window.NextByte, window.AtEOF, err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var reconstructed []byte
			next := int64(0)
			for windows := 0; windows < 4; windows++ {
				text, following, atEOF, err := test.read(next)
				if err != nil {
					t.Fatal(err)
				}
				if !utf8.ValidString(text) {
					t.Fatal("window contains invalid UTF-8")
				}
				if !atEOF && following <= next {
					t.Fatalf("next byte %d did not advance beyond %d", following, next)
				}
				reconstructed = append(reconstructed, text...)
				next = following
				if atEOF {
					break
				}
			}
			if !bytes.Equal(reconstructed, data) {
				t.Fatalf("reconstructed %q, want %q", reconstructed, data)
			}
		})
	}
}

func TestEditWindowLongLineMakesForwardProgress(t *testing.T) {
	data := bytes.Repeat([]byte{'a'}, 3*editWindowBytes)
	service := NewFileService()
	meta, err := service.OpenFile(writeTempFile(t, "long-line.txt", data))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(meta.FileID) })
	requested := int64(editWindowBytes)
	window, err := service.GetEditWindow(meta.FileID, requested, editWindowBytes)
	if err != nil {
		t.Fatal(err)
	}
	if window.StartByte != requested || window.NextByte <= requested || len(window.Text) > editWindowBytes {
		t.Fatalf("long-line window = start %d next %d bytes %d", window.StartByte, window.NextByte, len(window.Text))
	}
}
