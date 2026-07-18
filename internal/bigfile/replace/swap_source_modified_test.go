package replace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReplaceRegexpFileFailsIfSourceChangesBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("ab12 ", 2048)), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := false
	summary, err := ReplaceRegexpFile(context.Background(), srcPath, outPath, []byte(`([a-z]{2})([0-9]{2})`), []byte(`${2}-${1}`), FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			if changed {
				return
			}
			changed = true
			time.Sleep(10 * time.Millisecond)
			if writeErr := os.WriteFile(srcPath, []byte("source changed externally"), 0o600); writeErr != nil {
				t.Fatalf("mutate source: %v", writeErr)
			}
		},
	}, RegexOptions{
		ChunkSize:      8,
		MaxMatchWindow: 8,
	})
	assertSourceModifiedBeforeSwap(t, summary.ManifestPath, srcPath, outPath, backupPath, err)
}

func TestReplaceBatchPlainFileFailsIfSourceChangesBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("alpha beta ", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte("alpha"), Replace: []byte("omega"), Priority: 0},
		{Name: "Rule 2", Find: []byte("beta"), Replace: []byte(""), Priority: 1},
	}

	changed := false
	summary, err := ReplaceBatchPlainFile(context.Background(), srcPath, outPath, rules, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			if changed {
				return
			}
			changed = true
			time.Sleep(10 * time.Millisecond)
			if writeErr := os.WriteFile(srcPath, []byte("source changed externally"), 0o600); writeErr != nil {
				t.Fatalf("mutate source: %v", writeErr)
			}
		},
	}, BatchOptions{
		ChunkSize: 16,
	})
	assertSourceModifiedBeforeSwap(t, summary.ManifestPath, srcPath, outPath, backupPath, err)
}

func TestReplaceBatchRegexpFileFailsIfSourceChangesBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("id=41 id=42 ", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	rules := []BatchRule{
		{Name: "Rule 1", Find: []byte(`id=(\d+)`), Replace: []byte(`row-$1`), Priority: 0},
	}

	changed := false
	summary, err := ReplaceBatchRegexpFile(context.Background(), srcPath, outPath, rules, FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			if changed {
				return
			}
			changed = true
			time.Sleep(10 * time.Millisecond)
			if writeErr := os.WriteFile(srcPath, []byte("source changed externally"), 0o600); writeErr != nil {
				t.Fatalf("mutate source: %v", writeErr)
			}
		},
	}, RegexOptions{
		ChunkSize:      16,
		MaxMatchWindow: 16,
	})
	assertSourceModifiedBeforeSwap(t, summary.ManifestPath, srcPath, outPath, backupPath, err)
}

func TestConvertLineEndingsFileFailsIfSourceChangesBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("a\r\nb\r\n", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := false
	summary, err := ConvertLineEndingsFile(context.Background(), srcPath, outPath, "LF", FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			if changed {
				return
			}
			changed = true
			time.Sleep(10 * time.Millisecond)
			if writeErr := os.WriteFile(srcPath, []byte("source changed externally"), 0o600); writeErr != nil {
				t.Fatalf("mutate source: %v", writeErr)
			}
		},
	})
	assertSourceModifiedBeforeSwap(t, summary.ManifestPath, srcPath, outPath, backupPath, err)
}

func TestConvertEncodingFileFailsIfSourceChangesBeforeSwap(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "source.txt")
	outPath := filepath.Join(dir, "output.txt")
	backupPath := filepath.Join(dir, "source.txt.bak")
	if err := os.WriteFile(srcPath, []byte(strings.Repeat("alpha beta gamma\n", 4096)), 0o600); err != nil {
		t.Fatal(err)
	}

	changed := false
	summary, err := ConvertEncodingFile(context.Background(), srcPath, outPath, "UTF-16LE", FileOptions{
		SwapOriginal: true,
		BackupPath:   backupPath,
		Progress: func(Progress) {
			if changed {
				return
			}
			changed = true
			time.Sleep(10 * time.Millisecond)
			if writeErr := os.WriteFile(srcPath, []byte("source changed externally"), 0o600); writeErr != nil {
				t.Fatalf("mutate source: %v", writeErr)
			}
		},
	})
	assertSourceModifiedBeforeSwap(t, summary.ManifestPath, srcPath, outPath, backupPath, err)
}

func assertSourceModifiedBeforeSwap(t *testing.T, manifestPath string, sourcePath string, outputPath string, backupPath string, runErr error) {
	t.Helper()
	if !errors.Is(runErr, ErrSourceModifiedDuringOperation) {
		t.Fatalf("err = %v, want %v", runErr, ErrSourceModifiedDuringOperation)
	}
	if _, statErr := os.Stat(backupPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("backup should not exist, stat err = %v", statErr)
	}
	if _, statErr := os.Stat(outputPath); statErr != nil {
		t.Fatalf("output should remain for recovery, stat err = %v", statErr)
	}
	got, readErr := os.ReadFile(sourcePath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "source changed externally" {
		t.Fatalf("source = %q", string(got))
	}

	manifest := readManifest(t, manifestPath)
	if manifest.Status != "failed" {
		t.Fatalf("manifest status = %q", manifest.Status)
	}
	if manifest.Phase != "output_written" {
		t.Fatalf("manifest phase = %q, want durable output_written checkpoint", manifest.Phase)
	}
	if manifest.Error != ErrSourceModifiedDuringOperation.Error() {
		t.Fatalf("manifest error = %q", manifest.Error)
	}
}
