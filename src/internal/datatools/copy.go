package datatools

import (
	"bufio"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// reCopy matches a pg_dump "COPY <table> (cols…) FROM stdin" data-block header.
var reCopy = regexp.MustCompile(`(?i)^\s*COPY\s+([\w.$"]+)\s*(?:\(([^)]*)\))?\s+FROM\s+stdin`)

// DumpTableToCSV extracts one table's data from a SQL dump into CSV, supporting
// both pg_dump COPY … FROM stdin blocks (tab-separated, pg escapes, \N) and
// mysqldump-style INSERT INTO … VALUES (…),(…) statements (SQL literals, NULL).
// The COPY/INSERT column list becomes the header (or col1..N if absent). found is
// false if neither form for the table is present. Streaming.
func DumpTableToCSV(r io.Reader, table string, w io.Writer) (rows int64, found bool, err error) {
	return DumpTableToCSVContext(context.Background(), r, table, w)
}

func DumpTableToCSVContext(ctx context.Context, r io.Reader, table string, w io.Writer) (rows int64, found bool, err error) {
	br := bufio.NewReaderSize(r, 256<<10)
	cw := csv.NewWriter(w)
	want := normTable(table)

	inCopy := false
	copySelected := false
	copyTable := ""
	collecting := false // accumulating a multi-line INSERT statement for the target
	headerWritten := false
	matchedTable := ""
	var stmt strings.Builder
	var terminator statementTerminator
	var insertCols []string
	appendStatement := func(line string) error {
		return appendBoundedSQLStatement(&stmt, line, maxSQLStatementBytes)
	}

	writeHeader := func(cols []string) error {
		if headerWritten || len(cols) == 0 {
			headerWritten = headerWritten || len(cols) > 0
			return nil
		}
		headerWritten = true
		return cw.Write(cols)
	}
	flushInsert := func() error {
		tuples := parseSQLValues(stmt.String())
		stmt.Reset()
		if !headerWritten {
			cols := insertCols
			if len(cols) == 0 && len(tuples) > 0 {
				cols = make([]string, len(tuples[0]))
				for i := range cols {
					cols[i] = fmt.Sprintf("col%d", i+1)
				}
			}
			if err := writeHeader(cols); err != nil {
				return err
			}
		}
		for _, t := range tuples {
			if err := cw.Write(t); err != nil {
				return err
			}
			rows++
		}
		return nil
	}

	matchTable := func(candidate string) (bool, error) {
		canonical := normTable(candidate)
		if !tableMatches(canonical, want) {
			return false, nil
		}
		// A qualified request names one exact table. A bare request may match a
		// qualified dump name, but it must not silently merge same-named tables
		// from different schemas into one CSV.
		if matchedTable != "" && matchedTable != canonical {
			return false, fmt.Errorf("table name %q is ambiguous between %q and %q; use a schema-qualified name", table, matchedTable, canonical)
		}
		matchedTable = canonical
		return true, nil
	}

	for {
		lineBytes, e := readBoundedSQLLine(ctx, br, maxSQLLogicalLineBytes)
		line := string(lineBytes)
		if line != "" {
			switch {
			case inCopy:
				if t := strings.TrimRight(line, "\r\n"); t == `\.` {
					inCopy = false
					copySelected = false
					copyTable = ""
				} else if copySelected {
					fields := strings.Split(t, "\t")
					for i := range fields {
						fields[i] = decodeCopyField(fields[i])
					}
					if werr := cw.Write(fields); werr != nil {
						return rows, found, werr
					}
					rows++
				}
			case collecting:
				if appendErr := appendStatement(line); appendErr != nil {
					return rows, found, appendErr
				}
				if terminator.Feed(line) {
					if ferr := flushInsert(); ferr != nil {
						return rows, found, ferr
					}
					collecting = false
				}
			default:
				if cm := reCopy.FindStringSubmatch(line); cm != nil {
					matches, matchErr := matchTable(cm[1])
					if matchErr != nil {
						return rows, found, matchErr
					}
					// Track every COPY block, including non-target tables. COPY data
					// is opaque text and may itself look like a SQL statement; parsing
					// it as SQL could create a false target match and corrupt output.
					inCopy = true
					copySelected = matches
					copyTable = normTable(cm[1])
					if matches {
						found = true
						if werr := writeHeader(parseCopyCols(cm[2])); werr != nil {
							return rows, found, werr
						}
					}
				} else if im := reInsertInto.FindStringSubmatch(line); im != nil {
					matches, matchErr := matchTable(im[1])
					if matchErr != nil {
						return rows, found, matchErr
					}
					if matches {
						found = true
						insertCols = parseColList(im[3])
						stmt.Reset()
						terminator = statementTerminator{}
						if appendErr := appendStatement(line); appendErr != nil {
							return rows, found, appendErr
						}
						if terminator.Feed(line) {
							if ferr := flushInsert(); ferr != nil {
								return rows, found, ferr
							}
						} else {
							collecting = true
						}
					}
				}
			}
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return rows, found, e
		}
	}
	if inCopy {
		return rows, found, fmt.Errorf("truncated COPY block for table %q: missing \\. terminator", copyTable)
	}
	if collecting {
		return rows, found, fmt.Errorf("truncated INSERT statement for table %q: missing complete statement terminator", matchedTable)
	}
	cw.Flush()
	if cwErr := cw.Error(); cwErr != nil {
		return rows, found, cwErr
	}
	return rows, found, nil
}

// decodeCopyField preserves PostgreSQL's \N null marker in the CSV instead of
// collapsing it into an empty field. A real text value equal to "\N" is
// emitted as "\\N", retaining the same marker/escape distinction PostgreSQL
// COPY text uses and making null versus empty versus literal-\N reversible.
func decodeCopyField(raw string) string {
	if raw == `\N` {
		return `\N`
	}
	value := pgUnescape(raw)
	if value == `\N` {
		return `\\N`
	}
	return value
}

func normTable(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, `"`, "")
	s = strings.ReplaceAll(s, "`", "")
	return strings.ToLower(s)
}

func lastSegment(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 {
		return s[i+1:]
	}
	return s
}

// tableMatches requires exact identity for a qualified request. A bare request
// may match a qualified dump table; the caller detects ambiguity across schemas.
func tableMatches(copyName, want string) bool {
	c := normTable(copyName)
	if strings.Contains(want, ".") {
		return c == want
	}
	return lastSegment(c) == want
}

func parseCopyCols(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.Trim(strings.TrimSpace(p), `"`))
	}
	return out
}

// pgUnescape decodes PostgreSQL COPY text-format backslash escapes.
func pgUnescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++
			switch s[i] {
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'v':
				b.WriteByte('\v')
			case '\\':
				b.WriteByte('\\')
			default:
				if s[i] >= '0' && s[i] <= '7' {
					start := i
					for i+1 < len(s) && i-start < 2 && s[i+1] >= '0' && s[i+1] <= '7' {
						i++
					}
					value, _ := strconv.ParseUint(s[start:i+1], 8, 8)
					b.WriteByte(byte(value))
				} else if s[i] == 'x' && i+1 < len(s) && isHex(s[i+1]) {
					start := i + 1
					i++
					if i+1 < len(s) && isHex(s[i+1]) {
						i++
					}
					value, _ := strconv.ParseUint(s[start:i+1], 16, 8)
					b.WriteByte(byte(value))
				} else {
					// PostgreSQL documents only a finite escape set. Preserve an
					// unknown sequence byte-for-byte instead of silently deleting
					// the backslash and corrupting the value.
					b.WriteByte('\\')
					b.WriteByte(s[i])
				}
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}

func isHex(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
}
