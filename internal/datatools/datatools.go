// Package datatools provides pure-Go inspection utilities for tabular data and
// SQL dumps — CSV column-schema inference and SQL-dump table analysis — adapted
// from the sibling Quarry project. Everything streams or works off a bounded
// sample, so the functions stay memory-safe on multi-GB inputs.
package datatools

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// --- CSV schema inference ---

// SchemaColumn describes one inferred column.
type SchemaColumn struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"` // BIGINT | DOUBLE | BOOLEAN | TEXT
	NonNull int      `json:"nonNull"`
	Null    int      `json:"null"`
	Samples []string `json:"samples"` // up to 3 example values
}

// SchemaResult is the inferred schema of a CSV/TSV file.
type SchemaResult struct {
	Columns     []SchemaColumn `json:"columns"`
	RowsScanned int            `json:"rowsScanned"`
	Truncated   bool           `json:"truncated"` // stopped at the row cap
}

type kind int

const (
	kEmpty kind = iota
	kInt
	kFloat
	kBool
	kText
)

// classifyValue returns the value's kind (int/float/bool/text).
func classifyValue(v string) kind {
	t := strings.TrimSpace(v)
	if t == "" {
		return kEmpty
	}
	if _, err := strconv.ParseInt(t, 10, 64); err == nil {
		return kInt
	}
	if f, err := strconv.ParseFloat(t, 64); err == nil && !math.IsInf(f, 0) && !math.IsNaN(f) {
		return kFloat
	}
	switch strings.ToLower(t) {
	case "true", "false":
		return kBool
	}
	return kText
}

// promote widens two kinds: int+float→float; any disagreement→text.
func promote(a, b kind) kind {
	if a == kEmpty {
		return b
	}
	if b == kEmpty || a == b {
		return a
	}
	if (a == kInt && b == kFloat) || (a == kFloat && b == kInt) {
		return kFloat
	}
	return kText
}

func sqlType(k kind) string {
	switch k {
	case kInt:
		return "BIGINT"
	case kFloat:
		return "DOUBLE"
	case kBool:
		return "BOOLEAN"
	default:
		return "TEXT"
	}
}

func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// InferCSVSchema reads up to maxRows data rows (after a header row) and infers a
// SQL-ish type per column, with null counts and a few sample values.
func InferCSVSchema(r io.Reader, comma rune, maxRows int) (SchemaResult, error) {
	if maxRows <= 0 {
		maxRows = 200
	}
	cr := csv.NewReader(r)
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err == io.EOF {
		return SchemaResult{Columns: []SchemaColumn{}}, nil
	}
	if err != nil {
		return SchemaResult{}, err
	}
	n := len(header)
	kinds := make([]kind, n)
	nonNull := make([]int, n)
	nulls := make([]int, n)
	samples := make([][]string, n)

	scanned := 0
	truncated := false
	for {
		if scanned >= maxRows {
			truncated = true
			break
		}
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			break // tolerate a malformed tail; report what we have
		}
		for i := 0; i < n; i++ {
			v := ""
			if i < len(rec) {
				v = rec[i]
			}
			if strings.TrimSpace(v) == "" {
				nulls[i]++
				continue
			}
			nonNull[i]++
			kinds[i] = promote(kinds[i], classifyValue(v))
			if len(samples[i]) < 3 {
				samples[i] = append(samples[i], clipRunes(v, 48))
			}
		}
		scanned++
	}

	cols := make([]SchemaColumn, n)
	for i := 0; i < n; i++ {
		name := strings.TrimSpace(header[i])
		if name == "" {
			name = fmt.Sprintf("col%d", i+1)
		}
		if samples[i] == nil {
			samples[i] = []string{}
		}
		cols[i] = SchemaColumn{Name: name, Type: sqlType(kinds[i]), NonNull: nonNull[i], Null: nulls[i], Samples: samples[i]}
	}
	return SchemaResult{Columns: cols, RowsScanned: scanned, Truncated: truncated}, nil
}

// --- SQL dump analysis ---

// DumpTable is a table discovered in a SQL dump.
type DumpTable struct {
	Name         string `json:"name"`
	CreateOffset int64  `json:"createOffset"` // byte offset of CREATE TABLE, or -1
	InsertOffset int64  `json:"insertOffset"` // byte offset of first INSERT INTO, or -1
}

// DumpSummary inventories a SQL dump.
type DumpSummary struct {
	Tables       []DumpTable `json:"tables"`
	CreateTables int         `json:"createTables"` // count of CREATE TABLE statements
	InsertTables int         `json:"insertTables"` // count of INSERT INTO statements
	CopyBlocks   int         `json:"copyBlocks"`   // count of COPY … FROM stdin blocks (pg_dump)
	Mysqldump    bool        `json:"mysqldump"`
	Truncated    bool        `json:"truncated"` // stopped at the byte cap
}

var (
	reCreate     = regexp.MustCompile("(?i)^\\s*CREATE\\s+(?:DEFINER\\s*=\\s*\\S+\\s+)?TABLE(?:\\s+IF\\s+NOT\\s+EXISTS)?\\s+`?([A-Za-z0-9_.$-]+)`?")
	reInsert     = regexp.MustCompile("(?i)^\\s*INSERT\\s+(?:IGNORE\\s+)?INTO\\s+`?([A-Za-z0-9_.$-]+)`?")
	reCopyHeader = regexp.MustCompile(`(?i)^\s*COPY\s+([\w.$"]+)`)
)

// AnalyzeSQLDump scans a SQL dump line-by-line (comment-aware) and inventories
// its tables and CREATE/INSERT statements. It only runs the regexes on lines
// that begin with CREATE/INSERT, so even million-line dumps scan quickly.
// maxBytes > 0 caps the scan (Truncated is set if hit); 0 means the whole file.
func AnalyzeSQLDump(r io.Reader, maxBytes int64) (DumpSummary, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	var sum DumpSummary
	idx := map[string]int{}
	var offset int64
	inBlock := false // inside a /* ... */ block comment
	inCopy := false  // inside a pg_dump COPY … FROM stdin data block

	for {
		line, err := br.ReadString('\n')
		lineOff := offset
		offset += int64(len(line))
		if offset < 4096 && strings.Contains(strings.ToLower(line), "mysqldump") {
			sum.Mysqldump = true
		}

		trimmed := strings.TrimSpace(line)
		switch {
		case inCopy:
			// Skip COPY data rows entirely; the block ends at a lone "\.".
			if trimmed == `\.` {
				inCopy = false
			}
		case inBlock:
			if strings.Contains(line, "*/") {
				inBlock = false
			}
		case strings.HasPrefix(trimmed, "--"), strings.HasPrefix(trimmed, "#"):
			// line comment — skip
		case strings.HasPrefix(trimmed, "/*") && !strings.Contains(trimmed, "*/"):
			inBlock = true
		default:
			// Cheap prefix gate before the regex (avoids regex on data-value lines).
			switch {
			case len(trimmed) >= 6 && strings.EqualFold(trimmed[:6], "CREATE"):
				if m := reCreate.FindStringSubmatch(line); m != nil {
					sum.CreateTables++
					recordTable(&sum, idx, m[1], lineOff, true)
				}
			case len(trimmed) >= 6 && strings.EqualFold(trimmed[:6], "INSERT"):
				if m := reInsert.FindStringSubmatch(line); m != nil {
					sum.InsertTables++
					recordTable(&sum, idx, m[1], lineOff, false)
				}
			case len(trimmed) >= 5 && strings.EqualFold(trimmed[:5], "COPY "):
				if m := reCopyHeader.FindStringSubmatch(line); m != nil {
					sum.CopyBlocks++
					recordTable(&sum, idx, m[1], lineOff, false)
					inCopy = true
				}
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return sum, err
		}
		if maxBytes > 0 && offset >= maxBytes {
			sum.Truncated = true
			break
		}
	}
	return sum, nil
}

func recordTable(sum *DumpSummary, idx map[string]int, name string, off int64, isCreate bool) {
	i, ok := idx[name]
	if !ok {
		i = len(sum.Tables)
		idx[name] = i
		sum.Tables = append(sum.Tables, DumpTable{Name: name, CreateOffset: -1, InsertOffset: -1})
	}
	if isCreate {
		if sum.Tables[i].CreateOffset < 0 {
			sum.Tables[i].CreateOffset = off
		}
	} else if sum.Tables[i].InsertOffset < 0 {
		sum.Tables[i].InsertOffset = off
	}
}
