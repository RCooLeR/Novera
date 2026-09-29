package datatools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	reAlterTable = regexp.MustCompile("(?i)^\\s*ALTER\\s+TABLE(?:\\s+ONLY)?\\s+`?([\\w.$]+)`?")
	reIndexOn    = regexp.MustCompile("(?i)^\\s*CREATE\\s+(?:UNIQUE\\s+)?INDEX\\b.*?\\s+ON\\s+`?([\\w.$]+)`?")
)

// ExtractSQLTable writes every ordered statement/data span owned by table. It
// does not assume the table's DDL and data are adjacent, so all-DDL-then-all-data
// dumps and repeated INSERT/COPY blocks remain complete.
func ExtractSQLTable(r io.Reader, table string, w io.Writer) (bytes int64, found bool, err error) {
	return ExtractSQLTableContext(context.Background(), r, table, w)
}

func ExtractSQLTableContext(ctx context.Context, r io.Reader, table string, w io.Writer) (bytes int64, found bool, err error) {
	br := bufio.NewReaderSize(r, 256<<10)
	cw := &countWriter{w: w}
	want := normTable(table)
	if want == "" {
		return 0, false, errors.New("table name is required")
	}

	matchedTable := ""
	match := func(candidate string) (bool, error) {
		canonical := normTable(candidate)
		if !tableMatches(canonical, want) {
			return false, nil
		}
		if matchedTable != "" && matchedTable != canonical {
			return false, fmt.Errorf("table name %q is ambiguous between %q and %q; use a schema-qualified name", table, matchedTable, canonical)
		}
		matchedTable = canonical
		return true, nil
	}

	var statement statementTerminator
	inStatement := false
	writeStatement := false
	inCopy := false
	writeCopy := false
	copyTable := ""

	for {
		line, readErr := readBoundedSQLLine(ctx, br, maxSQLLogicalLineBytes)
		text := string(line)
		trimmed := strings.TrimSpace(text)

		switch {
		case inCopy:
			if writeCopy {
				if err := writeAll(cw, line); err != nil {
					return cw.n, found, err
				}
			}
			if trimmed == `\.` {
				inCopy = false
				writeCopy = false
				copyTable = ""
			}

		case inStatement:
			if writeStatement {
				if err := writeAll(cw, line); err != nil {
					return cw.n, found, err
				}
			}
			if statement.Feed(text) {
				inStatement = false
				writeStatement = false
			}

		default:
			if copyMatch := reCopy.FindStringSubmatch(text); copyMatch != nil {
				copyTable = normTable(copyMatch[1])
				selected, matchErr := match(copyMatch[1])
				if matchErr != nil {
					return cw.n, found, matchErr
				}
				inCopy = true
				writeCopy = selected
				if selected {
					found = true
					if err := writeAll(cw, line); err != nil {
						return cw.n, found, err
					}
				}
				break
			}

			candidate := ownedStatementTable(text)
			selected := false
			if candidate != "" {
				var matchErr error
				selected, matchErr = match(candidate)
				if matchErr != nil {
					return cw.n, found, matchErr
				}
			}
			if candidate != "" || startsSQLStatement(trimmed) {
				statement = statementTerminator{}
				writeStatement = selected
				if selected {
					found = true
					if err := writeAll(cw, line); err != nil {
						return cw.n, found, err
					}
				}
				inStatement = !statement.Feed(text)
			}
		}

		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return cw.n, found, readErr
		}
	}

	if inCopy {
		return cw.n, found, fmt.Errorf("truncated COPY block for table %q: missing \\. terminator", copyTable)
	}
	if inStatement && writeStatement {
		return cw.n, found, fmt.Errorf("truncated SQL statement for table %q", matchedTable)
	}
	return cw.n, found, nil
}

func ownedStatementTable(line string) string {
	for _, re := range []*regexp.Regexp{reCreate, reInsert, reAlterTable, reIndexOn} {
		if match := re.FindStringSubmatch(line); match != nil {
			return match[1]
		}
	}
	return ""
}

func startsSQLStatement(trimmed string) bool {
	if trimmed == "" || strings.HasPrefix(trimmed, "--") || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "/*") {
		return false
	}
	for _, keyword := range []string{"CREATE", "ALTER", "INSERT", "DROP", "SET", "USE", "LOCK", "UNLOCK", "GRANT", "REVOKE", "COMMENT", "SELECT"} {
		if len(trimmed) >= len(keyword) && strings.EqualFold(trimmed[:len(keyword)], keyword) {
			return true
		}
	}
	return false
}
