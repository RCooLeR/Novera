package extract

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/plugins/sql/analyze"
)

func TestSplitByTableWritesOutputsAndManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "header\nCREATE TABLE users(id int);\nINSERT INTO users VALUES (1);\nCREATE TABLE orders(id int);\nINSERT INTO orders VALUES (2);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	createUsers := int64(strings.Index(src, "CREATE TABLE users"))
	createOrders := int64(strings.Index(src, "CREATE TABLE orders"))
	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: createUsers, InsertOffset: -1},
		{Name: "orders", CreateOffset: createOrders, InsertOffset: -1},
	}}, WriteOptions{OutputDir: outDir})
	if err != nil {
		t.Fatal(err)
	}

	if summary.Operation != "sql-split-by-table" || len(summary.Outputs) != 2 {
		t.Fatalf("summary = %#v", summary)
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, src[createUsers:createOrders])
	assertFileContent(t, summary.Outputs[1].OutputPath, src[createOrders:])

	data, err := os.ReadFile(summary.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest WriteSummary
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Operation != summary.Operation || len(manifest.Outputs) != 2 {
		t.Fatalf("manifest = %#v", manifest)
	}
}

func TestSplitByTableWritesSHA256EvidenceWhenRequested(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\nINSERT INTO users VALUES (1);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
	}}, WriteOptions{OutputDir: outDir, ComputeSHA256: true})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(src))
	want := hex.EncodeToString(sum[:])
	if summary.ChecksumAlgorithm != "sha256" {
		t.Fatalf("algorithm = %q, want sha256", summary.ChecksumAlgorithm)
	}
	if got := summary.Outputs[0].SHA256; got != want {
		t.Fatalf("summary sha256 = %q, want %q", got, want)
	}

	data, err := os.ReadFile(summary.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest WriteSummary
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.ChecksumAlgorithm != "sha256" || manifest.Outputs[0].SHA256 != want {
		t.Fatalf("manifest checksums = %#v, want %q", manifest, want)
	}
}

func TestSplitByTableUsesWholeSourceWhileEditableSliceIsDirty(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "header\nCREATE TABLE users(id int);\nINSERT INTO users VALUES ('original');\nCREATE TABLE orders(id int);\nINSERT INTO orders VALUES ('outside');\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	sliceStart := int64(strings.Index(src, "INSERT INTO users"))
	sliceEnd := sliceStart + int64(len("INSERT INTO users VALUES ('original');\n"))
	buf, err := document.LoadInMemoryWindow(doc, sliceStart, sliceEnd, sliceEnd-sliceStart)
	if err != nil {
		t.Fatal(err)
	}
	editedSlice := strings.Replace(buf.Text, "original", "edited", 1)
	if _, _, encoded, err := buf.EncodedReplacement(editedSlice); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(encoded), "original") {
		t.Fatalf("edited replacement still contains original marker: %q", string(encoded))
	}

	createUsers := int64(strings.Index(src, "CREATE TABLE users"))
	createOrders := int64(strings.Index(src, "CREATE TABLE orders"))
	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: createUsers, InsertOffset: sliceStart},
		{Name: "orders", CreateOffset: createOrders, InsertOffset: -1},
	}}, WriteOptions{OutputDir: outDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Outputs) != 2 {
		t.Fatalf("outputs = %d, want 2", len(summary.Outputs))
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, src[createUsers:createOrders])
	if got, err := os.ReadFile(summary.Outputs[0].OutputPath); err != nil {
		t.Fatal(err)
	} else if strings.Contains(string(got), "edited") {
		t.Fatalf("SQL extract leaked dirty editable-slice text: %q", string(got))
	}
	sourceAfter, err := os.ReadFile(srcPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(sourceAfter) != src {
		t.Fatalf("source changed to %q, want immutable original", string(sourceAfter))
	}
}

func TestExtractTableWritesSelectedTableOnly(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\nCREATE TABLE orders(id int);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	outDir := filepath.Join(dir, "extract")
	summary, err := ExtractTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
		{Name: "orders", CreateOffset: int64(strings.Index(src, "CREATE TABLE orders")), InsertOffset: -1},
	}}, "orders", WriteOptions{OutputDir: outDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.Outputs) != 1 || summary.Outputs[0].Name != "orders" {
		t.Fatalf("summary = %#v", summary)
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, "CREATE TABLE orders(id int);\n")
}

func TestSplitByTableRejectsMissingOutputDir(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	if err := os.WriteFile(srcPath, []byte("CREATE TABLE users(id int);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	_, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{{Name: "users", CreateOffset: 0, InsertOffset: -1}}}, WriteOptions{})
	if err == nil || !strings.Contains(err.Error(), "output directory is required") {
		t.Fatalf("err = %v, want output directory error", err)
	}
}

func TestSplitByTableRejectsExistingManifestBeforeWritingOutputs(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	if err := os.WriteFile(srcPath, []byte("CREATE TABLE users(id int);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(outDir, defaultManifestName)
	if err := os.WriteFile(manifestPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{{Name: "users", CreateOffset: 0, InsertOffset: -1}}}, WriteOptions{OutputDir: outDir})
	if !errors.Is(err, fileio.ErrExists) {
		t.Fatalf("err = %v, want ErrExists", err)
	}
	if matches, globErr := filepath.Glob(filepath.Join(outDir, "*.sql")); globErr != nil {
		t.Fatal(globErr)
	} else if len(matches) != 0 {
		t.Fatalf("expected no table outputs after manifest preflight failure, got %v", matches)
	}
}

func TestPreflightWritePathsReportsSourceAliasBeforeExistingOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.sql")
	if err := os.WriteFile(sourcePath, []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "manifest.json")

	t.Run("direct path", func(t *testing.T) {
		err := preflightWritePaths(sourcePath, manifestPath, []TableRange{{
			Name:       "direct",
			OutputPath: sourcePath,
		}})
		if !errors.Is(err, fileio.ErrSourceAlias) {
			t.Fatalf("err = %v, want ErrSourceAlias", err)
		}
		if errors.Is(err, fileio.ErrExists) {
			t.Fatalf("source alias was misclassified as ErrExists: %v", err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		aliasPath := filepath.Join(dir, "source-hardlink.sql")
		if err := os.Link(sourcePath, aliasPath); err != nil {
			t.Skipf("hard links unavailable: %v", err)
		}
		err := preflightWritePaths(sourcePath, manifestPath, []TableRange{{
			Name:       "hard-link",
			OutputPath: aliasPath,
		}})
		if !errors.Is(err, fileio.ErrSourceAlias) {
			t.Fatalf("err = %v, want ErrSourceAlias", err)
		}
		if errors.Is(err, fileio.ErrExists) {
			t.Fatalf("source hard link was misclassified as ErrExists: %v", err)
		}
	})
}

func TestSplitByTableExistingLaterOutputIsRejectedBeforeEarlierOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\nCREATE TABLE orders(id int);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	outDir := filepath.Join(dir, "out")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conflict := filepath.Join(outDir, "orders.sql")
	if err := os.WriteFile(conflict, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
		{Name: "orders", CreateOffset: int64(strings.Index(src, "CREATE TABLE orders")), InsertOffset: -1},
	}}, WriteOptions{OutputDir: outDir})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if _, statErr := os.Stat(filepath.Join(outDir, "users.sql")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("created first output should be deleted, stat err = %v", statErr)
	}
	assertFileContent(t, conflict, "existing")
}

func TestSplitByTableCancelPreservesCommittedOutputsAndReportsIncomplete(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	first := "CREATE TABLE users(id int);\n" + strings.Repeat("a", 2*1024*1024)
	second := "CREATE TABLE orders(id int);\n" + strings.Repeat("b", 2*1024*1024)
	src := first + second
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	var canceled bool
	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(ctx, doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
		{Name: "orders", CreateOffset: int64(len(first)), InsertOffset: -1},
	}}, WriteOptions{
		OutputDir: outDir,
		Progress: func(done int64, total int64, outputs int) {
			if !canceled && outputs >= 2 && done > int64(len(first)) {
				canceled = true
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if _, ok := errors.AsType[*IncompleteWriteError](err); !ok {
		t.Fatalf("err = %T %v, want IncompleteWriteError", err, err)
	}
	if summary.Complete || len(summary.Outputs) != 1 || summary.Failure == "" {
		t.Fatalf("summary = %#v, want one preserved incomplete output", summary)
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, first)
	if _, statErr := os.Lstat(filepath.Join(outDir, "orders.sql")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled in-flight output became visible: %v", statErr)
	}
	if _, statErr := os.Lstat(summary.ManifestPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("incomplete operation published success manifest: %v", statErr)
	}
}

func TestSplitByTableManifestRacePreservesOutputsAndCompetitor(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	outDir := filepath.Join(dir, "out")
	manifestPath := filepath.Join(outDir, defaultManifestName)
	createdCompetitor := false
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
	}}, WriteOptions{
		OutputDir: outDir,
		Progress: func(done int64, total int64, outputs int) {
			if createdCompetitor || done == 0 {
				return
			}
			createdCompetitor = true
			if writeErr := os.WriteFile(manifestPath, []byte("competitor"), 0o600); writeErr != nil {
				t.Fatalf("create competing manifest: %v", writeErr)
			}
		},
	})
	if _, ok := errors.AsType[*IncompleteWriteError](err); !ok {
		t.Fatalf("err = %T %v, want IncompleteWriteError", err, err)
	}
	if summary.Complete || len(summary.Outputs) != 1 {
		t.Fatalf("summary = %#v, want preserved output", summary)
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, src)
	assertFileContent(t, manifestPath, "competitor")
}

func TestSplitByTableSourceValidationFailurePreventsFirstOutput(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	validationErr := errors.New("analysis source generation expired")
	validationCalls := 0
	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
	}}, WriteOptions{
		OutputDir: outDir,
		ValidateSource: func(context.Context) error {
			validationCalls++
			return validationErr
		},
	})
	if !errors.Is(err, validationErr) {
		t.Fatalf("err = %v, want validation failure", err)
	}
	if validationCalls != 1 {
		t.Fatalf("validation calls = %d, want 1", validationCalls)
	}
	if summary.Complete || len(summary.Outputs) != 0 || summary.Failure == "" {
		t.Fatalf("summary = %#v, want no published outputs", summary)
	}
	if _, statErr := os.Lstat(filepath.Join(outDir, "users.sql")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source-invalid table output became visible: %v", statErr)
	}
	if _, statErr := os.Lstat(filepath.Join(outDir, defaultManifestName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source-invalid completion manifest became visible: %v", statErr)
	}
}

func TestSplitByTableManifestValidationFailureRetainsValidatedOutputOnly(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	validationErr := errors.New("analysis changed before manifest publication")
	validationCalls := 0
	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
	}}, WriteOptions{
		OutputDir: outDir,
		ValidateSource: func(context.Context) error {
			validationCalls++
			if validationCalls == 1 {
				return nil
			}
			return validationErr
		},
	})
	if !errors.Is(err, validationErr) {
		t.Fatalf("err = %v, want manifest validation failure", err)
	}
	if _, ok := errors.AsType[*IncompleteWriteError](err); !ok {
		t.Fatalf("err = %T %v, want IncompleteWriteError", err, err)
	}
	if validationCalls != 2 {
		t.Fatalf("validation calls = %d, want output and manifest validation", validationCalls)
	}
	if summary.Complete || len(summary.Outputs) != 1 || summary.Failure == "" {
		t.Fatalf("summary = %#v, want one retained validated output", summary)
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, src)
	if _, statErr := os.Lstat(summary.ManifestPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("source-invalid completion manifest became visible: %v", statErr)
	}
}

func TestSplitByTablePreparedValidationRunsOnceForCompletionManifest(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "dump.sql")
	src := "CREATE TABLE users(id int);\n"
	if err := os.WriteFile(srcPath, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	doc := openTestDocument(t, srcPath)
	defer doc.Close()

	prepareCalls := 0
	operationCalls := 0
	preparedCalls := 0
	outDir := filepath.Join(dir, "out")
	summary, err := SplitByTable(context.Background(), doc, srcPath, analyze.Summary{Tables: []analyze.Table{
		{Name: "users", CreateOffset: 0, InsertOffset: -1},
	}}, WriteOptions{
		OutputDir: outDir,
		PrepareSourceValidation: func(context.Context) (func(context.Context) error, error) {
			prepareCalls++
			return func(context.Context) error {
				preparedCalls++
				if operationCalls != 2 {
					t.Fatalf("prepared validator ran after %d operation validations, want output and manifest checks", operationCalls)
				}
				return nil
			}, nil
		},
		ValidateSource: func(context.Context) error {
			operationCalls++
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.Complete || prepareCalls != 1 || operationCalls != 2 || preparedCalls != 1 {
		t.Fatalf("summary=%#v prepare=%d operation=%d prepared=%d", summary, prepareCalls, operationCalls, preparedCalls)
	}
	assertFileContent(t, summary.Outputs[0].OutputPath, src)
	if _, err := os.Stat(summary.ManifestPath); err != nil {
		t.Fatalf("completion manifest missing: %v", err)
	}
}

func TestFinishWriteClassifiesCompletionManifestPublication(t *testing.T) {
	sentinel := errors.New("directory sync failed")
	base := WriteSummary{
		ManifestPath: filepath.Join(t.TempDir(), "manifest.json"),
		Outputs:      []TableRange{{Name: "users", OutputPath: "users.sql"}},
		Complete:     true,
	}

	t.Run("known visible manifest remains complete", func(t *testing.T) {
		publication := &fileio.PublicationError{
			FinalPath: base.ManifestPath,
			Durable:   false,
			Err:       sentinel,
		}
		summary, err := finishWrite(base, publication)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want publication warning", err)
		}
		if _, ok := errors.AsType[*IncompleteWriteError](err); ok {
			t.Fatalf("known visible completion manifest was classified incomplete: %v", err)
		}
		if !summary.Complete || summary.PublicationUncertain || summary.Failure != "" {
			t.Fatalf("summary = %#v, want complete known publication", summary)
		}
	})

	t.Run("uncertain manifest location is incomplete", func(t *testing.T) {
		publication := &fileio.PublicationError{
			FinalPath:         base.ManifestPath,
			LocationUncertain: true,
			Err:               sentinel,
		}
		summary, err := finishWrite(base, publication)
		if !errors.Is(err, sentinel) {
			t.Fatalf("err = %v, want publication warning", err)
		}
		if _, ok := errors.AsType[*IncompleteWriteError](err); !ok {
			t.Fatalf("err = %T %v, want IncompleteWriteError", err, err)
		}
		if summary.Complete || !summary.PublicationUncertain || summary.Failure == "" {
			t.Fatalf("summary = %#v, want uncertain incomplete publication", summary)
		}
	})
}

func openTestDocument(t *testing.T, path string) *document.FileDocument {
	t.Helper()
	doc, err := document.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}
