package csv

import (
	"context"
	"errors"
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

// ErrSQLiteExportSecurePublicationUnavailable reports that SQLite export is
// disabled until the writer can publish output without attacker-controlled
// scratch paths or unsafe destination replacement.
var ErrSQLiteExportSecurePublicationUnavailable = errors.New(
	"SQLite export is temporarily unavailable because secure atomic publication is not supported by the SQLite writer",
)

// ExportSQLiteFile fails before opening the source or touching the destination.
// Keep the public API available so callers can surface an explicit containment
// error instead of silently using the unsafe publication path.
func ExportSQLiteFile(context.Context, string, string, SQLiteOptions) (ExportSummary, error) {
	return ExportSummary{}, ErrSQLiteExportSecurePublicationUnavailable
}
