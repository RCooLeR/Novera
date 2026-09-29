//go:build !windows

package extract

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/plugins/sql/analyze"
)

func TestSplitByTableRejectsSymlinkDotDotOutputDirectory(t *testing.T) {
	root := t.TempDir()
	physical := filepath.Join(root, "physical")
	child := filepath.Join(physical, "child")
	if err := os.MkdirAll(child, 0o700); err != nil {
		t.Fatal(err)
	}
	visible := filepath.Join(root, "visible")
	if err := os.Symlink(child, visible); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sourcePath := filepath.Join(root, "source.sql")
	contents := []byte("CREATE TABLE orders (id INT);\n")
	if err := os.WriteFile(sourcePath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	rawDir := visible + string(os.PathSeparator) + ".."
	summary := analyze.Summary{Tables: []analyze.Table{{Name: "orders", CreateOffset: 0, InsertOffset: -1}}}

	_, err = SplitByTable(context.Background(), doc, sourcePath, summary, WriteOptions{OutputDir: rawDir})
	if !errors.Is(err, fileio.ErrInvalidExactPath) {
		t.Fatalf("split error = %v, want ErrInvalidExactPath", err)
	}
	for _, path := range []string{
		filepath.Join(root, "orders.sql"),
		filepath.Join(physical, "orders.sql"),
		filepath.Join(root, defaultManifestName),
		filepath.Join(physical, defaultManifestName),
	} {
		if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("rejected SQL split created %q: %v", path, statErr)
		}
	}
}

func TestSplitByTablePreservesSpacedOutputDirectory(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.sql")
	contents := []byte("CREATE TABLE orders (id INT);\n")
	if err := os.WriteFile(sourcePath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := document.OpenFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer doc.Close()
	outputDir := filepath.Join(root, " output directory ")
	analysis := analyze.Summary{Tables: []analyze.Table{{Name: "orders", CreateOffset: 0, InsertOffset: -1}}}

	summary, err := SplitByTable(context.Background(), doc, sourcePath, analysis, WriteOptions{OutputDir: outputDir})
	if err != nil {
		t.Fatal(err)
	}
	wantOutput := outputDir + string(os.PathSeparator) + "orders.sql"
	wantManifest := outputDir + string(os.PathSeparator) + defaultManifestName
	if len(summary.Outputs) != 1 || summary.Outputs[0].OutputPath != wantOutput || summary.ManifestPath != wantManifest {
		t.Fatalf("summary paths = output %#v manifest %q", summary.Outputs, summary.ManifestPath)
	}
	for _, path := range []string{wantOutput, wantManifest} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected exact artifact %q: %v", path, err)
		}
	}
}
