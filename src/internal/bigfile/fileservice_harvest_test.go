package bigfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/regexutil"
)

func TestHarvestMatchesToPathPublishesCompleteOutput(t *testing.T) {
	path := writeTempFile(t, "source.txt", []byte("one id=12\ntwo id=34\n"))
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	file, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("opened file disappeared")
	}
	defer file.Release()
	re, _, err := regexutil.CompileBounded([]byte(`id=\d{2}`), false, harvestRegexMatchWindow)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "matches.txt")

	result, err := service.harvestMatchesToPath(context.Background(), file, re, false, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.RecordsWritten != 2 || result.OutputPath != dst {
		t.Fatalf("result = %+v", result)
	}
	if got, err := os.ReadFile(dst); err != nil || string(got) != "id=12\nid=34\n" {
		t.Fatalf("output = %q, err %v", got, err)
	}
}

func TestFlattenHarvestMatchNewlinesCollapsesEveryLineEnding(t *testing.T) {
	input := []byte("alpha\r\nbeta\rgamma\ndelta")
	got := flattenHarvestMatchNewlines(input)
	if string(got) != "alpha beta gamma delta" {
		t.Fatalf("flattened match = %q", got)
	}
}

func TestHarvestRejectsNonExactRegexBeforeDialogOrOutputCreation(t *testing.T) {
	path := writeTempFile(t, "source.txt", []byte("id=12\nid=34\n"))
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	previousDialog := harvestSaveDialog
	dialogCalls := 0
	harvestSaveDialog = func(string, string) (string, error) {
		dialogCalls++
		return filepath.Join(t.TempDir(), "unused.txt"), nil
	}
	t.Cleanup(func() { harvestSaveDialog = previousDialog })

	if _, err := service.HarvestMatchesViaDialog(meta.FileID, `id=\d+`, false); !errors.Is(err, regexutil.ErrUnboundedRegex) {
		t.Fatalf("dialog preflight error = %v, want ErrUnboundedRegex", err)
	}
	if dialogCalls != 0 {
		t.Fatalf("invalid regex opened save dialog %d times", dialogCalls)
	}

	file, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("opened file disappeared")
	}
	defer file.Release()
	re, err := regexutil.Compile([]byte(`id=\d+`), false)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "matches.txt")
	if _, err := service.harvestMatchesToPath(context.Background(), file, re, false, dst, nil); !errors.Is(err, regexutil.ErrUnboundedRegex) {
		t.Fatalf("direct preflight error = %v, want ErrUnboundedRegex", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid regex published output: %v", err)
	}
}

func TestHarvestMatchesToPathRejectsChangedSource(t *testing.T) {
	path := writeTempFile(t, "source.txt", []byte("id=12\nid=34\n"))
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	file, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("opened file disappeared")
	}
	defer file.Release()
	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	re, _, err := regexutil.CompileBounded([]byte(`id=\d{2}`), false, harvestRegexMatchWindow)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "matches.txt")

	if _, err := service.harvestMatchesToPath(context.Background(), file, re, false, dst, nil); !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want ErrOutputSourceChanged", err)
	}
	if _, err := os.Stat(dst); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("changed source published output: %v", err)
	}
}
