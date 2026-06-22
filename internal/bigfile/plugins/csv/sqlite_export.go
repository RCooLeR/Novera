package csv

import (
	"bufio"
	"context"
	"database/sql"
	stdcsv "encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo)
)

// SQLiteOptions configures a CSV → SQLite (.db) export.
type SQLiteOptions struct {
	Delimiter  rune
	HasHeader  bool
	TableName  string
	BatchSize  int
	TypedCells bool // bind numeric cells as INTEGER/REAL (else everything TEXT)
	Progress   func(records int64)
}

// ExportSQLiteFile streams a CSV into a new SQLite database file with one table.
// Columns are created untyped (BLOB affinity) so dynamically-bound numbers keep
// their storage class. Rows are inserted in batched transactions.
func ExportSQLiteFile(ctx context.Context, srcPath, dstPath string, opts SQLiteOptions) (ExportSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 1000
	}
	table := strings.TrimSpace(opts.TableName)
	if table == "" {
		table = "data"
	}
	if same, err := sameFilePath(srcPath, dstPath); err != nil {
		return ExportSummary{}, err
	} else if same {
		return ExportSummary{}, errors.New("output path must be different from input path")
	}
	// Build the DB at a temp path, then rename over dstPath on success — so an
	// existing destination is replaced only when the export completes, and a
	// leftover temp from a prior crash never corrupts a new DB.
	tmpPath := tempOutputPath(dstPath)
	if err := os.Remove(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ExportSummary{}, err
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return ExportSummary{}, err
	}
	defer in.Close()

	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	br := bufio.NewReader(in)
	if err := skipInputBOM(br); err != nil {
		return ExportSummary{}, err
	}
	reader := stdcsv.NewReader(br)
	reader.Comma = opts.Delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false

	// Read the first row to learn the column count / names.
	firstRow, err := reader.Read()
	if errors.Is(err, io.EOF) {
		return ExportSummary{}, errors.New("input has no rows")
	}
	if err != nil {
		return ExportSummary{}, err
	}
	var header []string
	var sum ExportSummary
	sum.RecordsRead++
	if opts.HasHeader {
		header = append([]string(nil), firstRow...)
		firstRow = nil
	}
	colCount := len(header)
	if colCount == 0 {
		colCount = len(firstRow)
	}
	if colCount == 0 {
		return ExportSummary{}, errors.New("input has no columns")
	}
	colNames := make([]string, colCount)
	for i := 0; i < colCount; i++ {
		colNames[i] = columnKey(header, i)
	}

	db, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return ExportSummary{}, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=OFF; PRAGMA synchronous=OFF;"); err != nil {
		return ExportSummary{}, err
	}
	if _, err := db.ExecContext(ctx, createTableSQL(table, colNames)); err != nil {
		return ExportSummary{}, err
	}
	insertSQL := insertSQL(table, colNames)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ExportSummary{}, err
	}
	stmt, err := tx.PrepareContext(ctx, insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return ExportSummary{}, err
	}

	args := make([]any, colCount)
	insert := func(rec []string) error {
		for i := 0; i < colCount; i++ {
			cell := ""
			if i < len(rec) {
				cell = rec[i]
			}
			if opts.TypedCells {
				args[i] = typedCell(cell)
			} else {
				args[i] = cell
			}
		}
		_, e := stmt.ExecContext(ctx, args...)
		return e
	}

	fail := func(e error) (ExportSummary, error) {
		_ = stmt.Close()
		_ = tx.Rollback()
		return sum, e
	}

	if firstRow != nil { // no header: the first row is data
		if err := insert(firstRow); err != nil {
			return fail(err)
		}
		sum.RecordsWritten++
	}

	inBatch := int(sum.RecordsWritten)
	for {
		if err := contextErr(ctx); err != nil {
			return fail(err)
		}
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(err)
		}
		sum.RecordsRead++
		reportEvery(opts.Progress, sum.RecordsRead, 50000)
		if err := insert(rec); err != nil {
			return fail(err)
		}
		sum.RecordsWritten++
		inBatch++
		if inBatch >= opts.BatchSize {
			if err := stmt.Close(); err != nil {
				return fail(err)
			}
			if err := tx.Commit(); err != nil {
				return sum, err
			}
			tx, err = db.BeginTx(ctx, nil)
			if err != nil {
				return sum, err
			}
			stmt, err = tx.PrepareContext(ctx, insertSQL)
			if err != nil {
				_ = tx.Rollback()
				return sum, err
			}
			inBatch = 0
		}
	}
	if err := stmt.Close(); err != nil {
		_ = tx.Rollback()
		return sum, err
	}
	if err := tx.Commit(); err != nil {
		return sum, err
	}
	if err := db.Close(); err != nil {
		return sum, err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		return sum, err
	}
	cleanup = false
	return sum, nil
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func createTableSQL(table string, cols []string) string {
	parts := make([]string, len(cols))
	for i, c := range cols {
		parts[i] = quoteIdent(c) // untyped → BLOB affinity, keeps native storage class
	}
	return fmt.Sprintf("CREATE TABLE %s (%s);", quoteIdent(table), strings.Join(parts, ", "))
}

func insertSQL(table string, cols []string) string {
	names := make([]string, len(cols))
	ph := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c)
		ph[i] = "?"
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s);", quoteIdent(table), strings.Join(names, ", "), strings.Join(ph, ", "))
}

// typedCell binds numeric-looking cells as int64/float64 so SQLite stores them
// with numeric storage class; everything else stays text. See numericCell for
// the (shared) rules on leading zeros, int64 overflow, and non-finite values.
func typedCell(s string) any { return numericCell(s) }
