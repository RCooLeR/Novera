package csv

import (
	"context"
	"errors"
)

// XLSXOptions configures a CSV → Excel (.xlsx) export.
type XLSXOptions struct {
	Delimiter  rune
	HasHeader  bool
	SheetName  string
	TypedCells bool // write numeric cells as numbers, not text
	Progress   func(records int64)
}

// XLSXSummary reports an .xlsx export, including whether the row cap truncated.
type XLSXSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	Truncated      bool
}

// ErrXLSXExportSecureScratchUnavailable reports that XLSX export is disabled
// until the streaming writer can securely own and clean all scratch files.
var ErrXLSXExportSecureScratchUnavailable = errors.New(
	"XLSX export is temporarily unavailable because the streaming writer cannot securely own and clean its scratch files",
)

// ExportXLSXFile fails before opening the source or touching the destination.
// Keep the public API available so callers can surface an explicit containment
// error instead of silently using the unsafe scratch-file path.
func ExportXLSXFile(context.Context, string, string, XLSXOptions) (XLSXSummary, error) {
	return XLSXSummary{}, ErrXLSXExportSecureScratchUnavailable
}
