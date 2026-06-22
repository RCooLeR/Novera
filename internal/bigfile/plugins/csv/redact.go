package csv

import (
	"bufio"
	"context"
	"crypto/sha256"
	stdcsv "encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
)

// RedactMode is how a column's values are masked.
type RedactMode string

const (
	RedactNull  RedactMode = "null"  // empty value
	RedactFixed RedactMode = "fixed" // a constant replacement
	RedactHash  RedactMode = "hash"  // stable short hash (pseudonym)
	RedactEmail RedactMode = "email" // keep first char + domain: a***@x.com
)

type RedactOptions struct {
	Delimiter   rune
	HasHeader   bool
	Columns     map[int]RedactMode // 0-based source column → mask
	Replacement string             // used by RedactFixed (default "REDACTED")
	Progress    func(records int64, bytes int64)
}

type RedactSummary struct {
	RecordsRead    int64
	RecordsWritten int64
	CellsMasked    int64
	Delimiter      rune
}

func maskValue(value string, mode RedactMode, replacement string) string {
	switch mode {
	case RedactNull:
		return ""
	case RedactFixed:
		return replacement
	case RedactHash:
		if value == "" {
			return ""
		}
		sum := sha256.Sum256([]byte(value))
		return hex.EncodeToString(sum[:4]) // 8 hex chars — stable pseudonym
	case RedactEmail:
		at := strings.LastIndexByte(value, '@')
		if at <= 0 {
			if value == "" {
				return ""
			}
			return value[:1] + "***"
		}
		return value[:1] + "***" + value[at:]
	default:
		return value
	}
}

// RedactColumnsFile streams src to dst (CSV), masking the configured columns.
// The header row (if any) is passed through unchanged.
func RedactColumnsFile(ctx context.Context, srcPath, dstPath string, opts RedactOptions) (RedactSummary, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if len(opts.Columns) == 0 {
		return RedactSummary{}, errors.New("select at least one column to redact")
	}
	if opts.Delimiter == 0 {
		opts.Delimiter = ','
	}
	if opts.Replacement == "" {
		opts.Replacement = "REDACTED"
	}
	if same, err := sameFilePath(srcPath, dstPath); err != nil {
		return RedactSummary{}, err
	} else if same {
		return RedactSummary{}, errors.New("output path must be different from input path")
	}

	in, err := os.Open(srcPath)
	if err != nil {
		return RedactSummary{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return RedactSummary{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = out.Close()
			_ = os.Remove(dstPath)
		}
	}()

	br := bufio.NewReader(in)
	if err := skipInputBOM(br); err != nil {
		return RedactSummary{}, err
	}
	reader := stdcsv.NewReader(br)
	reader.Comma = opts.Delimiter
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	reader.ReuseRecord = false

	bw := bufio.NewWriter(out)
	defer func() { _ = bw.Flush() }()
	writer := stdcsv.NewWriter(bw)
	writer.Comma = opts.Delimiter

	summary := RedactSummary{Delimiter: opts.Delimiter}
	first := true
	for {
		if err := ctx.Err(); err != nil {
			return summary, err
		}
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return summary, err
		}
		summary.RecordsRead++
		if first && opts.HasHeader {
			first = false
			if err := writer.Write(record); err != nil {
				return summary, err
			}
			summary.RecordsWritten++
			continue
		}
		first = false
		for i := range record {
			if mode, ok := opts.Columns[i]; ok {
				record[i] = maskValue(record[i], mode, opts.Replacement)
				summary.CellsMasked++
			}
		}
		if err := writer.Write(record); err != nil {
			return summary, err
		}
		summary.RecordsWritten++
		if opts.Progress != nil && summary.RecordsRead%5000 == 0 {
			opts.Progress(summary.RecordsRead, 0)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return summary, err
	}
	if err := bw.Flush(); err != nil {
		return summary, err
	}
	if err := out.Sync(); err != nil {
		return summary, err
	}
	if err := out.Close(); err != nil {
		return summary, err
	}
	cleanup = false
	return summary, nil
}
