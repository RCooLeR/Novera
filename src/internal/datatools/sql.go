package datatools

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"strings"
)

// SQLConvertOptions configures CSV→SQL generation.
type SQLConvertOptions struct {
	TableName     string
	Comma         rune
	Columns       []string // column names (from the header / schema)
	ColumnTypes   []string // parallel to Columns; "" → TEXT
	IncludeCreate bool
	BatchSize     int      // rows per INSERT statement (default 500)
	NullValues    []string // values treated as SQL NULL (empty string always is)
}

// SQLConvertSummary reports how much was generated.
type SQLConvertSummary struct {
	Rows  int64 `json:"rows"`
	Bytes int64 `json:"bytes"`
}

func sqlIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// sqlStr emits a MySQL-compatible string literal. Escaping backslashes before
// quotes is essential: under MySQL's default SQL mode, an input backslash can
// otherwise consume the first quote of a doubled-quote escape and let the
// remainder of an untrusted CSV value escape the literal.
func sqlStr(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('\'')
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 0:
			b.WriteString(`\0`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case 0x1a:
			b.WriteString(`\Z`)
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString("''")
		default:
			b.WriteByte(s[i])
		}
	}
	b.WriteByte('\'')
	return b.String()
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

// ConvertCSVToSQL streams a CSV/TSV into CREATE TABLE + batched INSERT
// statements. The header row is skipped (opts.Columns is authoritative). All
// values are emitted as MySQL-escaped strings except NULLs; the column TYPES
// still come from inference for the CREATE.
func ConvertCSVToSQL(r io.Reader, w io.Writer, opts SQLConvertOptions) (SQLConvertSummary, error) {
	return writeCSVAsSQL(r, w, opts, 0)
}

// PreviewCSVToSQL returns the SQL that ConvertCSVToSQL would generate for the
// first maxRows data rows (for an at-a-glance preview before writing a file).
func PreviewCSVToSQL(r io.Reader, opts SQLConvertOptions, maxRows int) (string, error) {
	var sb strings.Builder
	if _, err := writeCSVAsSQL(r, &sb, opts, maxRows); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func writeCSVAsSQL(r io.Reader, w io.Writer, opts SQLConvertOptions, maxRows int) (SQLConvertSummary, error) {
	comma := opts.Comma
	if comma == 0 {
		comma = ','
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = 500
	}
	if strings.TrimSpace(opts.TableName) == "" {
		opts.TableName = "data"
	}
	cr := csv.NewReader(r)
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	if _, err := cr.Read(); err != nil { // consume the header (opts.Columns is used)
		if err == io.EOF {
			return SQLConvertSummary{}, nil
		}
		return SQLConvertSummary{}, err
	}

	cw := &countWriter{w: w}
	bw := bufio.NewWriterSize(cw, 64<<10)
	table := sqlIdent(opts.TableName)
	cols := opts.Columns

	if opts.IncludeCreate && len(cols) > 0 {
		fmt.Fprintf(bw, "CREATE TABLE IF NOT EXISTS %s (\n", table)
		for i, c := range cols {
			typ := "TEXT"
			if i < len(opts.ColumnTypes) && opts.ColumnTypes[i] != "" {
				typ = opts.ColumnTypes[i]
			}
			tail := ","
			if i == len(cols)-1 {
				tail = ""
			}
			fmt.Fprintf(bw, "  %s %s%s\n", sqlIdent(c), typ, tail)
		}
		bw.WriteString(");\n")
	}

	idents := make([]string, len(cols))
	for i, c := range cols {
		idents[i] = sqlIdent(c)
	}
	colClause := "(" + strings.Join(idents, ", ") + ")"
	nullset := map[string]bool{}
	for _, n := range opts.NullValues {
		nullset[n] = true
	}

	var rows int64
	inBatch := 0
	open := false
	for {
		if maxRows > 0 && rows >= int64(maxRows) {
			break
		}
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return SQLConvertSummary{}, fmt.Errorf("read CSV data record %d: %w", rows+1, e)
		}
		if !open {
			fmt.Fprintf(bw, "INSERT INTO %s %s VALUES\n", table, colClause)
			open = true
			inBatch = 0
		} else if inBatch > 0 {
			bw.WriteString(",\n")
		}
		bw.WriteString("  (")
		for i := range cols {
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			if v == "" || nullset[v] {
				bw.WriteString("NULL")
			} else {
				bw.WriteString(sqlStr(v))
			}
			if i < len(cols)-1 {
				bw.WriteString(", ")
			}
		}
		bw.WriteString(")")
		rows++
		inBatch++
		if inBatch >= batch {
			bw.WriteString(";\n")
			open = false
		}
	}
	if open {
		bw.WriteString(";\n")
	}
	if err := bw.Flush(); err != nil {
		return SQLConvertSummary{}, err
	}
	return SQLConvertSummary{Rows: rows, Bytes: cw.n}, nil
}
