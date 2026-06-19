package datatools

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
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
func sqlStr(s string) string   { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

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
// values are emitted as quoted strings except NULLs, which keeps generation
// dialect-safe; the column TYPES still come from inference for the CREATE.
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
			break // tolerate a malformed tail
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

// DumpTableRange is a byte range [Start,End) holding one table's statements.
type DumpTableRange struct {
	Name  string `json:"name"`
	Start int64  `json:"start"`
	End   int64  `json:"end"`
}

// PlanDumpRanges turns a DumpSummary into contiguous per-table byte ranges: each
// table runs from its first statement to the start of the next table (the last
// to end-of-file), so a table can be extracted by copying its byte range.
func PlanDumpRanges(sum DumpSummary, sourceSize int64) []DumpTableRange {
	type start struct {
		name string
		off  int64
	}
	starts := make([]start, 0, len(sum.Tables))
	for _, t := range sum.Tables {
		off := t.CreateOffset
		if off < 0 || (t.InsertOffset >= 0 && t.InsertOffset < off) {
			if t.InsertOffset >= 0 {
				off = t.InsertOffset
			}
		}
		if off < 0 {
			continue
		}
		starts = append(starts, start{t.Name, off})
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].off < starts[j].off })
	out := make([]DumpTableRange, len(starts))
	for i, s := range starts {
		end := sourceSize
		if i+1 < len(starts) {
			end = starts[i+1].off
		}
		out[i] = DumpTableRange{Name: s.name, Start: s.off, End: end}
	}
	return out
}
