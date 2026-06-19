package datatools

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"regexp"
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
	br := bufio.NewReaderSize(r, 256<<10)
	cw := csv.NewWriter(w)
	want := normTable(table)

	inCopy := false
	collecting := false // accumulating a multi-line INSERT statement for the target
	headerWritten := false
	var stmt strings.Builder
	var insertCols []string

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

	for {
		line, e := br.ReadString('\n')
		switch {
		case inCopy:
			if t := strings.TrimRight(line, "\r\n"); t == `\.` {
				inCopy = false
			} else {
				fields := strings.Split(t, "\t")
				for i := range fields {
					if fields[i] == `\N` {
						fields[i] = ""
					} else {
						fields[i] = pgUnescape(fields[i])
					}
				}
				if werr := cw.Write(fields); werr != nil {
					return rows, found, werr
				}
				rows++
			}
		case collecting:
			stmt.WriteString(line)
			if statementComplete(stmt.String()) {
				if ferr := flushInsert(); ferr != nil {
					return rows, found, ferr
				}
				collecting = false
			}
		default:
			if cm := reCopy.FindStringSubmatch(line); cm != nil && tableMatches(cm[1], want) {
				found = true
				inCopy = true
				if werr := writeHeader(parseCopyCols(cm[2])); werr != nil {
					return rows, found, werr
				}
			} else if im := reInsertInto.FindStringSubmatch(line); im != nil && tableMatches(im[1], want) {
				found = true
				insertCols = parseColList(im[3])
				stmt.Reset()
				stmt.WriteString(line)
				if statementComplete(stmt.String()) {
					if ferr := flushInsert(); ferr != nil {
						return rows, found, ferr
					}
				} else {
					collecting = true
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
	if collecting && stmt.Len() > 0 {
		if ferr := flushInsert(); ferr != nil {
			return rows, found, ferr
		}
	}
	cw.Flush()
	if cwErr := cw.Error(); cwErr != nil {
		return rows, found, cwErr
	}
	return rows, found, nil
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

// tableMatches accepts an exact match or a match on the final dotted segment, so
// the caller can pass "users" or "public.users".
func tableMatches(copyName, want string) bool {
	c := normTable(copyName)
	return c == want || lastSegment(c) == lastSegment(want)
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
				b.WriteByte(s[i])
			}
		} else {
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
