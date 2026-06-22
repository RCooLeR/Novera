package csv

import (
	"bufio"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/xuri/excelize/v2"
)

// xlsxMaxRows is the hard per-sheet row limit of the .xlsx format.
const xlsxMaxRows = 1048576

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

// ExportXLSXFile streams a CSV into a new .xlsx using excelize's StreamWriter,
// so it stays memory-bounded. The .xlsx format caps a sheet at ~1,048,576 rows;
// beyond that the export stops and reports Truncated.
func ExportXLSXFile(ctx context.Context, srcPath, dstPath string, opts XLSXOptions) (XLSXSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	sheet := strings.TrimSpace(opts.SheetName)
	if sheet == "" {
		sheet = "Sheet1"
	}
	if same, err := sameFilePath(srcPath, dstPath); err != nil {
		return XLSXSummary{}, err
	} else if same {
		return XLSXSummary{}, errors.New("output path must be different from input path")
	}
	// Write to a temp path and rename over dstPath on success, so the Save
	// dialog's confirmed overwrite replaces the target only on completion. The
	// temp path keeps an .xlsx extension because excelize picks the workbook
	// format from the file name.
	tmpPath := dstPath + ".quarry-part.xlsx"

	in, err := os.Open(srcPath)
	if err != nil {
		return XLSXSummary{}, err
	}
	defer in.Close()
	br := bufio.NewReader(in)
	if err := skipInputBOM(br); err != nil {
		return XLSXSummary{}, err
	}
	reader := stdcsv.NewReader(br)
	reader.Comma = opts.Delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false

	fx := excelize.NewFile()
	defer fx.Close()
	if sheet != "Sheet1" {
		if err := fx.SetSheetName("Sheet1", sheet); err != nil {
			return XLSXSummary{}, err
		}
	}
	sw, err := fx.NewStreamWriter(sheet)
	if err != nil {
		return XLSXSummary{}, err
	}

	var sum XLSXSummary
	rowNum := 1
	for {
		if err := contextErr(ctx); err != nil {
			return sum, err
		}
		rec, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return sum, err
		}
		sum.RecordsRead++
		reportEvery(opts.Progress, sum.RecordsRead, 50000)
		if rowNum > xlsxMaxRows {
			sum.Truncated = true
			break
		}
		cell, err := excelize.CoordinatesToCellName(1, rowNum)
		if err != nil {
			return sum, err
		}
		row := make([]any, len(rec))
		for i, v := range rec {
			if opts.TypedCells {
				row[i] = typedCellXLSX(v)
			} else {
				row[i] = v
			}
		}
		if err := sw.SetRow(cell, row); err != nil {
			return sum, err
		}
		sum.RecordsWritten++
		rowNum++
	}
	if err := sw.Flush(); err != nil {
		return sum, err
	}
	if err := fx.SaveAs(tmpPath); err != nil {
		return sum, err
	}
	if err := os.Rename(tmpPath, dstPath); err != nil {
		_ = os.Remove(tmpPath)
		return sum, err
	}
	return sum, nil
}

func typedCellXLSX(s string) any { return numericCell(s) }
