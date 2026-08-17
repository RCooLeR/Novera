package csv

import (
	"bytes"
	"context"
	stdcsv "encoding/csv"
	"errors"
	"io"
)

// CompleteRecordPrefix returns a prefix ending on a parser-confirmed logical
// record boundary when a bounded source sample was truncated. It is intended
// for preview/schema/profile paths that already have their own row handling.
func CompleteRecordPrefix(ctx context.Context, data []byte, delimiter rune, maxRecordBytes int64, sourceTruncated bool) ([]byte, bool, error) {
	if !sourceTruncated || len(data) == 0 {
		return data, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reader, logical, err := newBoundedCSVReaderWithState(ctx, bytes.NewReader(data), csvReaderConfig{
		Delimiter: delimiter, MaxRecordBytes: maxRecordBytes,
		FieldsPerRecord: -1, LazyQuotes: false,
	})
	if err != nil {
		return nil, false, err
	}
	lastEnd := 0
	for {
		_, readErr := reader.Read()
		inputEnd := int(reader.InputOffset())
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			// A missing closing quote at the end of a known-truncated sample
			// is ambiguous and belongs to the omitted partial record. Definite
			// syntax failures such as ErrBareQuote remain errors even when the
			// parser consumed the final retained byte.
			if inputEnd >= len(data) &&
				errors.Is(readErr, stdcsv.ErrQuote) &&
				logical.inQuotes &&
				!logical.afterQuote {
				break
			}
			return nil, false, readErr
		}
		if inputEnd > 0 && data[inputEnd-1] == '\n' {
			lastEnd = inputEnd
		}
		if inputEnd >= len(data) {
			break
		}
	}
	return data[:lastEnd:lastEnd], lastEnd < len(data), nil
}

// trimTrailingPartialRecord remains for the SQL preview path until that
// root-owned implementation can migrate to parser-confirmed record boundaries.
func trimTrailingPartialRecord(data []byte) ([]byte, bool) {
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' || data[i] == '\r' {
			return data[:i+1], true
		}
	}
	return nil, false
}
