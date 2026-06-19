package datatools

import (
	"regexp"
	"strings"
)

// reInsertInto matches the head of a mysqldump-style INSERT, capturing the table
// (1) and an optional column list (3). The VALUES tuples follow (parsed separately).
var reInsertInto = regexp.MustCompile("(?i)^\\s*INSERT\\s+(?:IGNORE\\s+)?INTO\\s+`?([\\w.$]+)`?\\s*(\\(([^)]*)\\))?\\s+VALUES")

var reValuesKw = regexp.MustCompile(`(?i)\bVALUES\b`)

// statementComplete reports whether s contains a ';' outside a single-quoted
// string — i.e. a full SQL statement has been accumulated.
func statementComplete(s string) bool {
	inStr := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inStr && c == '\\':
			i++ // skip the escaped char
		case inStr && c == '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				i++ // doubled '' inside the string
			} else {
				inStr = false
			}
		case !inStr && c == '\'':
			inStr = true
		case !inStr && c == ';':
			return true
		}
	}
	return false
}

// parseColList splits an INSERT/COPY column list into trimmed, unquoted names.
func parseColList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.Trim(strings.TrimSpace(p), "`\""))
	}
	return out
}

// parseSQLValues parses the tuple list after VALUES in an INSERT statement into
// rows of decoded field strings. SQL NULL and empty strings both become "" (CSV
// has no null). Handles ” and backslash string escapes; assumes dump values are
// plain literals (no nested function calls), as mysqldump emits.
func parseSQLValues(s string) [][]string {
	loc := reValuesKw.FindStringIndex(s)
	if loc == nil {
		return nil
	}
	s = s[loc[1]:]

	var tuples [][]string
	var cur []string
	var field []byte
	inTuple := false
	inStr := false
	quoted := false

	flush := func() {
		cur = append(cur, finalizeField(field, quoted))
		field = field[:0]
		quoted = false
	}

	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					field = append(field, '\n')
				case 't':
					field = append(field, '\t')
				case 'r':
					field = append(field, '\r')
				case '0':
					field = append(field, 0)
				case 'b':
					field = append(field, '\b')
				case 'Z':
					field = append(field, 26)
				default:
					field = append(field, s[i]) // \' \\ \" → literal char
				}
				continue
			}
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' { // '' → literal quote
					field = append(field, '\'')
					i++
					continue
				}
				inStr = false
				continue
			}
			field = append(field, c)
			continue
		}
		switch c {
		case '(':
			if !inTuple {
				inTuple = true
				cur = nil
				field = field[:0]
				quoted = false
			} else {
				field = append(field, c)
			}
		case ')':
			if inTuple {
				flush()
				tuples = append(tuples, cur)
				cur = nil
				inTuple = false
			} else {
				field = append(field, c)
			}
		case ',':
			if inTuple {
				flush()
			}
		case '\'':
			if inTuple {
				inStr = true
				quoted = true
			}
		case ';':
			if !inTuple {
				return tuples // end of statement
			}
		default:
			if inTuple {
				field = append(field, c)
			}
		}
	}
	return tuples
}

// finalizeField decodes a parsed field: a quoted field's bytes are already
// unescaped; a bare token is trimmed, and unquoted NULL becomes "".
func finalizeField(b []byte, quoted bool) string {
	if quoted {
		return string(b)
	}
	t := strings.TrimSpace(string(b))
	if strings.EqualFold(t, "NULL") {
		return ""
	}
	return t
}
