package bigfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sqlanalyze "novera/internal/bigfile/plugins/sql/analyze"
	sqlschemadiff "novera/internal/bigfile/plugins/sql/schemadiff"
)

func TestSqlAnalysisFailsClosedOnIncompleteDDL(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.sql")
	newPath := filepath.Join(dir, "new.sql")
	if err := os.WriteFile(oldPath, []byte("CREATE TABLE t (id int);\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("CREATE TABLE t (id int\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewFileService()
	oldMeta, err := service.OpenFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(oldMeta.FileID) })
	newMeta, err := service.OpenFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(newMeta.FileID) })
	if _, err := service.SqlAnalyze(oldMeta.FileID); err != nil {
		t.Fatal(err)
	}
	result, err := service.SqlAnalyze(newMeta.FileID)
	if !errors.Is(err, sqlanalyze.ErrUnterminatedStatement) {
		t.Fatalf("result = %+v, error = %v; want unterminated-statement error", result, err)
	}
	if len(result.Tables) != 0 || result.CreateTables != 0 || result.InsertTables != 0 ||
		result.DefinerCount != 0 || result.Header {
		t.Fatalf("failed analysis leaked a partial summary: %+v", result)
	}
	service.sqlMu.Lock()
	_, cached := service.sqlSummary[newMeta.FileID]
	service.sqlMu.Unlock()
	if cached {
		t.Fatal("failed analysis was cached")
	}
}

func TestSqlSchemaDiffPreservesLiteralCaseAndSpacing(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old.sql")
	newPath := filepath.Join(dir, "new.sql")
	if err := os.WriteFile(oldPath, []byte("CREATE TABLE t (note varchar(20) DEFAULT 'A  B');\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("CREATE TABLE t (note VARCHAR ( 20 ) DEFAULT 'a b');\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	service := NewFileService()
	oldMeta, err := service.OpenFile(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(oldMeta.FileID) })
	newMeta, err := service.OpenFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.CloseFile(newMeta.FileID) })
	if _, err := service.SqlAnalyze(oldMeta.FileID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.SqlAnalyze(newMeta.FileID); err != nil {
		t.Fatal(err)
	}

	result, err := service.SqlSchemaDiff(oldMeta.FileID, newMeta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedTables) != 1 || len(result.ChangedTables[0].ChangedColumns) != 1 ||
		result.UnchangedCount != 0 {
		t.Fatalf("literal change was hidden: %+v", result)
	}
}

func TestSqlSchemaDiffCarriesFullSemanticChangesThroughService(t *testing.T) {
	service := NewFileService()
	oldID := openAndAnalyzeSchemaDiffTestFixture(t, service, "old.sql", `CREATE TABLE users (
 id int NOT NULL DEFAULT 'ABC',
 name varchar(20),
 PRIMARY KEY(id)
) ENGINE=InnoDB;`)
	newID := openAndAnalyzeSchemaDiffTestFixture(t, service, "new.sql", `CREATE TABLE users (
 name varchar(40),
 id int NOT NULL DEFAULT 'abc',
 UNIQUE KEY uq_name(name)
) ENGINE=MyISAM;`)

	result, err := service.SqlSchemaDiff(oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if result.UnchangedCount != 0 || len(result.ChangedTables) != 1 {
		t.Fatalf("service diff = %#v, want one changed table", result)
	}
	change := result.ChangedTables[0]
	if change.Status != sqlschemadiff.DiffChanged ||
		len(change.ChangedColumns) != 2 ||
		!change.ColumnOrderChanged ||
		len(change.RemovedConstraints) != 1 ||
		len(change.AddedIndexes) != 1 ||
		len(change.ChangedOptions) != 1 {
		t.Fatalf("service lost semantic dimensions: %#v", change)
	}
	assertNoSchemaDiffJob(t, service)
}

func TestSqlSchemaDiffSurfacesUnsupportedDDLAsUnknownNotUnchanged(t *testing.T) {
	service := NewFileService()
	unsupported := "CREATE TABLE t (id int MASKED WITH (FUNCTION='mask')) ENGINE=InnoDB;"
	oldID := openAndAnalyzeSchemaDiffTestFixture(t, service, "old-unknown.sql", unsupported)
	newID := openAndAnalyzeSchemaDiffTestFixture(t, service, "new-unknown.sql", unsupported)

	result, err := service.SqlSchemaDiff(oldID, newID)
	if err != nil {
		t.Fatal(err)
	}
	if result.UnchangedCount != 0 || len(result.ChangedTables) != 1 {
		t.Fatalf("unsupported service diff = %#v, want explicit unknown table", result)
	}
	if result.ChangedTables[0].Status != sqlschemadiff.DiffUnknown || result.ChangedTables[0].Reason == "" {
		t.Fatalf("unsupported DDL was not surfaced as unknown: %#v", result.ChangedTables[0])
	}
}

func TestSqlSchemaDiffSameFileUsesOneLeaseAndReturnsUnchanged(t *testing.T) {
	service := NewFileService()
	fileID := openAndAnalyzeSchemaDiffTestFixture(t, service, "same.sql", "CREATE TABLE users (id int PRIMARY KEY);")
	leaseCount := 0
	restore := installSQLSchemaDiffLeaseHook(func(hookedFileID string) {
		if hookedFileID == fileID {
			leaseCount++
		}
	})
	defer restore()

	result, err := service.SqlSchemaDiff(fileID, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if leaseCount != 1 {
		t.Fatalf("same-file schema diff acquired %d leases, want exactly one", leaseCount)
	}
	if result.UnchangedCount != 1 ||
		len(result.AddedTables) != 0 ||
		len(result.RemovedTables) != 0 ||
		len(result.ChangedTables) != 0 {
		t.Fatalf("same-file schema diff = %#v, want one unchanged table", result)
	}
	assertNoSchemaDiffJob(t, service)
}

func TestSqlSchemaDiffAcquiresBothArgumentOrdersCanonically(t *testing.T) {
	service := NewFileService()
	fileA := openAndAnalyzeSchemaDiffTestFixture(t, service, "a.sql", "CREATE TABLE a (id int);")
	fileB := openAndAnalyzeSchemaDiffTestFixture(t, service, "b.sql", "CREATE TABLE b (id int);")
	want := []string{fileA, fileB}
	sort.Strings(want)

	for _, args := range [][2]string{{fileA, fileB}, {fileB, fileA}} {
		var acquired []string
		restore := installSQLSchemaDiffLeaseHook(func(fileID string) {
			acquired = append(acquired, fileID)
		})
		result, err := service.SqlSchemaDiff(args[0], args[1])
		restore()
		if err != nil {
			t.Fatalf("SqlSchemaDiff(%q, %q): %v", args[0], args[1], err)
		}
		if len(acquired) != 2 || acquired[0] != want[0] || acquired[1] != want[1] {
			t.Fatalf("SqlSchemaDiff(%q, %q) lease order = %v, want %v", args[0], args[1], acquired, want)
		}
		if result.FileA == result.FileB {
			t.Fatalf("SqlSchemaDiff(%q, %q) lost argument mapping: %#v", args[0], args[1], result)
		}
	}
}

func TestSqlSchemaDiffRejectsAggregateBudgetsBeforeAnyDDLRead(t *testing.T) {
	t.Run("input", func(t *testing.T) {
		service := NewFileService()
		data := append([]byte("CREATE TABLE t (id int);"), bytes.Repeat([]byte{' '}, sqlschemadiff.MaxStatementBytes+128)...)
		fileID := openSchemaDiffTestFixture(t, service, "input-budget.sql", data)
		tables := make([]sqlanalyze.Table, 33)
		for index := range tables {
			tables[index] = sqlanalyze.Table{
				Name:         fmt.Sprintf("t%d", index),
				CreateOffset: 0,
				InsertOffset: -1,
				Regions: []sqlanalyze.Region{{
					Kind:        sqlanalyze.RegionCreate,
					StartOffset: 0,
					EndOffset:   int64(len(data)),
				}},
			}
		}
		installSchemaDiffTestSummary(t, service, fileID, sqlanalyze.Summary{Tables: tables, CreateTables: len(tables)})

		reads := 0
		restore := installSQLSchemaDiffReadHook(func(context.Context, string, int64, int64) error {
			reads++
			return nil
		})
		defer restore()
		if _, err := service.SqlSchemaDiff(fileID, fileID); !errors.Is(err, ErrSQLSchemaDiffBudget) {
			t.Fatalf("SqlSchemaDiff error = %v, want ErrSQLSchemaDiffBudget", err)
		}
		if reads != 0 {
			t.Fatalf("over-budget plan performed %d DDL reads, want zero", reads)
		}
		assertNoSchemaDiffJob(t, service)
	})

	t.Run("retained state", func(t *testing.T) {
		service := NewFileService()
		fileID := openSchemaDiffTestFixture(t, service, "retained-budget.sql", []byte("x"))
		name := strings.Repeat("n", 8<<10)
		tables := make([]sqlanalyze.Table, sqlschemadiff.MaxDiffTableCount)
		for index := range tables {
			tables[index] = sqlanalyze.Table{
				Name:         name,
				CreateOffset: -1,
				InsertOffset: 0,
				Regions: []sqlanalyze.Region{{
					Kind:        sqlanalyze.RegionInsert,
					StartOffset: 0,
					EndOffset:   1,
				}},
			}
		}
		installSchemaDiffTestSummary(t, service, fileID, sqlanalyze.Summary{Tables: tables, InsertTables: len(tables)})

		reads := 0
		restore := installSQLSchemaDiffReadHook(func(context.Context, string, int64, int64) error {
			reads++
			return nil
		})
		defer restore()
		if _, err := service.SqlSchemaDiff(fileID, fileID); !errors.Is(err, ErrSQLSchemaDiffBudget) {
			t.Fatalf("SqlSchemaDiff error = %v, want retained-memory budget rejection", err)
		}
		if reads != 0 {
			t.Fatalf("retained-state rejection performed %d DDL reads, want zero", reads)
		}
		assertNoSchemaDiffJob(t, service)
	})
}

func TestSqlSchemaDiffCapsEveryStatementReadAtParserLimitPlusOne(t *testing.T) {
	service := NewFileService()
	data := append([]byte("CREATE TABLE t (id int);"), bytes.Repeat([]byte{' '}, sqlschemadiff.MaxStatementBytes+128)...)
	fileID := openSchemaDiffTestFixture(t, service, "statement-cap.sql", data)
	installSchemaDiffTestSummary(t, service, fileID, sqlanalyze.Summary{
		Tables: []sqlanalyze.Table{{
			Name:         "t",
			CreateOffset: 0,
			InsertOffset: -1,
			Regions: []sqlanalyze.Region{{
				Kind:        sqlanalyze.RegionCreate,
				StartOffset: 0,
				EndOffset:   int64(len(data)),
			}},
		}},
		CreateTables: 1,
	})

	var requested []int64
	restore := installSQLSchemaDiffReadHook(func(_ context.Context, hookedFileID string, start, end int64) error {
		if hookedFileID != fileID {
			t.Fatalf("read hook file = %q, want %q", hookedFileID, fileID)
		}
		requested = append(requested, end-start)
		return nil
	})
	defer restore()
	result, err := service.SqlSchemaDiff(fileID, fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(requested) != 1 || requested[0] != int64(sqlschemadiff.MaxStatementBytes+1) {
		t.Fatalf("statement reads = %v, want exactly [%d]", requested, sqlschemadiff.MaxStatementBytes+1)
	}
	if result.UnchangedCount != 0 ||
		len(result.ChangedTables) != 1 ||
		result.ChangedTables[0].Status != sqlschemadiff.DiffUnknown {
		t.Fatalf("over-limit DDL result = %#v, want explicit unknown", result)
	}
}

func TestSqlSchemaDiffBindsCachedAnalysisToExactDigest(t *testing.T) {
	service := NewFileService()
	path := filepath.Join(t.TempDir(), "digest.sql")
	original := []byte("CREATE TABLE t (id int);")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSchemaDiffTestFile(service, meta.FileID) })
	if _, err := service.SqlAnalyze(meta.FileID); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mutated := []byte("CREATE TABLE t (id bit);")
	if len(mutated) != len(original) {
		t.Fatal("test mutation must preserve size")
	}
	if err := os.WriteFile(path, mutated, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}

	if _, err := service.SqlSchemaDiff(meta.FileID, meta.FileID); !errors.Is(err, ErrSQLAnalysisStale) ||
		!errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("schema diff error = %v, want stale exact-digest rejection", err)
	}
	service.sqlMu.Lock()
	_, cached := service.sqlSummary[meta.FileID]
	service.sqlMu.Unlock()
	if cached {
		t.Fatal("digest-mismatched analysis remained cached")
	}
}

func TestSqlSchemaDiffCancelJobReleasesJobAndLeaseOwnership(t *testing.T) {
	service := NewFileService()
	fileID := openAndAnalyzeSchemaDiffTestFixture(t, service, "cancel.sql", "CREATE TABLE t (id int);")
	entered := make(chan struct{})
	var once sync.Once
	restore := installSQLSchemaDiffReadHook(func(ctx context.Context, _ string, _, _ int64) error {
		once.Do(func() { close(entered) })
		<-ctx.Done()
		return ctx.Err()
	})

	done := make(chan error, 1)
	go func() {
		_, err := service.SqlSchemaDiff(fileID, fileID)
		done <- err
	}()
	waitSchemaDiffTestSignal(t, entered, "schema diff did not reach its bounded read")
	jobID := activeSchemaDiffJobID(t, service)
	if err := service.CancelJob(jobID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled diff error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled schema diff did not return")
	}
	restore()
	assertNoSchemaDiffJob(t, service)
	if _, err := service.SqlSchemaDiff(fileID, fileID); err != nil {
		t.Fatalf("schema diff after cancellation: %v", err)
	}
}

func TestCommitSQLSchemaDiffRejectsEstablishedLifecycleBarrier(t *testing.T) {
	service := NewFileService()
	fileID := openAndAnalyzeSchemaDiffTestFixture(t, service, "barrier.sql", "CREATE TABLE t (id int);")

	_, err := runServiceJob(service, jobSpec{
		Title:   "schema barrier test",
		Kind:    "sql-schema-diff",
		FileID:  fileID,
		FileIDs: []string{fileID},
	}, func(ctx context.Context, _ func(int64, int64, string)) (struct{}, error) {
		lease, err := service.acquireSQLSchemaLease(fileID)
		if err != nil {
			return struct{}{}, err
		}
		handle, err := service.reg.BeginClose(fileID)
		if err != nil {
			lease.file.Release()
			return struct{}{}, err
		}
		commitErr := commitSQLSchemaDiff(ctx, service, &lease, &lease, true)
		lease.file.Release()
		if finishErr := handle.Finish(); finishErr != nil {
			return struct{}{}, finishErr
		}
		return struct{}{}, commitErr
	})
	if !errors.Is(err, ErrSQLAnalysisStale) || !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("commit after lifecycle barrier error = %v, want stale source rejection", err)
	}
	assertNoSchemaDiffJob(t, service)
}

func TestSqlSchemaDiffLifecycleCancelsEitherInput(t *testing.T) {
	for _, operation := range []string{"close", "refresh"} {
		for _, targetIndex := range []int{0, 1} {
			operation, targetIndex := operation, targetIndex
			t.Run(fmt.Sprintf("%s-input-%d", operation, targetIndex+1), func(t *testing.T) {
				service := NewFileService()
				fileIDs := []string{
					openAndAnalyzeSchemaDiffTestFixture(t, service, "old.sql", "CREATE TABLE t (id int);"),
					openAndAnalyzeSchemaDiffTestFixture(t, service, "new.sql", "CREATE TABLE t (id bigint);"),
				}
				entered := make(chan struct{})
				var once sync.Once
				restore := installSQLSchemaDiffReadHook(func(ctx context.Context, _ string, _, _ int64) error {
					once.Do(func() { close(entered) })
					<-ctx.Done()
					return ctx.Err()
				})
				defer restore()

				diffDone := make(chan error, 1)
				go func() {
					_, err := service.SqlSchemaDiff(fileIDs[0], fileIDs[1])
					diffDone <- err
				}()
				waitSchemaDiffTestSignal(t, entered, "schema diff did not reach its bounded read")

				lifecycleDone := make(chan error, 1)
				go func() {
					if operation == "close" {
						lifecycleDone <- service.CloseFile(fileIDs[targetIndex])
						return
					}
					_, err := service.RefreshFile(fileIDs[targetIndex])
					lifecycleDone <- err
				}()
				select {
				case err := <-diffDone:
					if !errors.Is(err, ErrJobCancelled) || !errors.Is(err, context.Canceled) {
						t.Fatalf("lifecycle-canceled diff error = %v", err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("schema diff was not canceled by input lifecycle")
				}
				select {
				case err := <-lifecycleDone:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("lifecycle operation did not drain schema diff")
				}
				assertNoSchemaDiffJob(t, service)
			})
		}
	}
}

func openAndAnalyzeSchemaDiffTestFixture(t *testing.T, service *FileService, name, ddl string) string {
	t.Helper()
	fileID := openSchemaDiffTestFixture(t, service, name, []byte(ddl))
	result, err := service.SqlAnalyze(fileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Tables) != 1 {
		t.Fatalf("analysis tables = %#v, want one", result.Tables)
	}
	return fileID
}

func openSchemaDiffTestFixture(t *testing.T, service *FileService, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeSchemaDiffTestFile(service, meta.FileID) })
	return meta.FileID
}

func closeSchemaDiffTestFile(service *FileService, fileID string) {
	if service == nil {
		return
	}
	lease, ok := service.reg.Get(fileID)
	if !ok {
		return
	}
	lease.Release()
	_ = service.CloseFile(fileID)
}

func installSchemaDiffTestSummary(
	t *testing.T,
	service *FileService,
	fileID string,
	summary sqlanalyze.Summary,
) {
	t.Helper()
	lease, ok := service.reg.Get(fileID)
	if !ok {
		t.Fatalf("unknown fixture file %q", fileID)
	}
	expected, err := captureDocumentSourceExpectation(context.Background(), lease.Doc, lease.Path, nil)
	if err != nil {
		lease.Release()
		t.Fatal(err)
	}
	generation := lease.Generation
	lease.Release()
	service.sqlMu.Lock()
	service.sqlUseSeq++
	service.sqlSummary[fileID] = cachedSQLSummary{
		Generation: generation,
		Summary:    summary,
		Digest:     expected.digest,
		Verified:   true,
		LastUsed:   service.sqlUseSeq,
	}
	service.sqlMu.Unlock()
}

func activeSchemaDiffJobID(t *testing.T, service *FileService) string {
	t.Helper()
	manager := service.jobs()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active == nil {
		t.Fatal("schema diff has no active job")
	}
	return manager.active.id
}

func assertNoSchemaDiffJob(t *testing.T, service *FileService) {
	t.Helper()
	manager := service.jobs()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != nil {
		t.Fatalf("schema diff retained active job %q", manager.active.id)
	}
}

func waitSchemaDiffTestSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}
