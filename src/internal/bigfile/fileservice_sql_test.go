package bigfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openSQLServiceFile(t *testing.T) (*FileService, FileMeta) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dump.sql")
	data := "CREATE TABLE `users` (`id` int);\nINSERT INTO `users` VALUES (1),(2);\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	svc := NewFileService()
	meta, err := svc.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.CloseFile(meta.FileID) })
	return svc, meta
}

func TestSQLSummaryInvalidatedOnRefreshAndClose(t *testing.T) {
	svc, meta := openSQLServiceFile(t)
	if _, err := svc.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}
	svc.sqlMu.Lock()
	_, cached := svc.sqlSummary[meta.FileID]
	svc.sqlMu.Unlock()
	if !cached {
		t.Fatal("analysis was not cached")
	}

	if _, err := svc.RefreshFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	svc.sqlMu.Lock()
	_, cached = svc.sqlSummary[meta.FileID]
	svc.sqlMu.Unlock()
	if cached {
		t.Fatal("refresh retained stale SQL summary")
	}
	if _, _, err := svc.sqlSummaryFor(meta.FileID); err == nil || !strings.Contains(err.Error(), "analyze") {
		t.Fatalf("summary lookup after refresh = %v", err)
	}

	if _, err := svc.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}
	if err := svc.CloseFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	svc.sqlMu.Lock()
	_, cached = svc.sqlSummary[meta.FileID]
	svc.sqlMu.Unlock()
	if cached {
		t.Fatal("close retained SQL summary")
	}
}

func TestSQLSummaryGenerationRejectsStaleOffsets(t *testing.T) {
	svc, meta := openSQLServiceFile(t)
	if _, err := svc.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}
	// Reopen through the registry to prove generation validation independently
	// of the facade's explicit invalidation.
	reopened, err := svc.reg.Reopen(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Release()
	f, _, err := svc.sqlSummaryFor(meta.FileID)
	if f != nil {
		f.Release()
	}
	if err == nil || !strings.Contains(err.Error(), "analyze") {
		t.Fatalf("stale generation lookup = %v", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected filesystem error: %v", err)
	}
}

func TestSampleInsertRows(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		maxRows int
		want    string
	}{
		{
			name:    "first two of four tuples",
			data:    "INSERT INTO `t` VALUES (1,'a'),(2,'b'),(3,'c');\nINSERT INTO `t` VALUES (4,'d');\n",
			maxRows: 2,
			want:    "INSERT INTO `t` VALUES (1,'a'),(2,'b');\n",
		},
		{
			name:    "across statements",
			data:    "INSERT INTO `t` VALUES (1,'a'),(2,'b');\nINSERT INTO `t` VALUES (3,'c'),(4,'d');\n",
			maxRows: 3,
			want:    "INSERT INTO `t` VALUES (1,'a'),(2,'b');\nINSERT INTO `t` VALUES (3,'c');\n",
		},
		{
			name:    "paren inside string is ignored",
			data:    "INSERT INTO `t` VALUES (1,'a)b'),(2,'c');\n",
			maxRows: 1,
			want:    "INSERT INTO `t` VALUES (1,'a)b');\n",
		},
		{
			name:    "escaped quote inside string",
			data:    "INSERT INTO `t` VALUES (1,'a\\'b'),(2,'c');\n",
			maxRows: 1,
			want:    "INSERT INTO `t` VALUES (1,'a\\'b');\n",
		},
		{
			name:    "fewer rows than requested keeps terminator",
			data:    "INSERT INTO `t` VALUES (1,'a');\n",
			maxRows: 10,
			want:    "INSERT INTO `t` VALUES (1,'a');\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.data)
			got, _, err := sampleInsertRows(context.Background(), r, 0, int64(len(tc.data)), tc.maxRows)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", string(got), tc.want)
			}
		})
	}
}
