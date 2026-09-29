package datatools

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// --- SQL dump cleanup presets (streaming regex transforms) ---

// DumpTransform selects which cleanup transforms to apply to a SQL dump. Empty
// string fields mean "leave unchanged".
type DumpTransform struct {
	RemoveDefiner     bool   `json:"removeDefiner"`     // strip DEFINER=user@host clauses
	DropAutoIncrement bool   `json:"dropAutoIncrement"` // strip table-option AUTO_INCREMENT=N
	Engine            string `json:"engine"`            // rewrite ENGINE=… (e.g. "InnoDB")
	Charset           string `json:"charset"`           // rewrite (DEFAULT) CHARSET/CHARACTER SET
	Collation         string `json:"collation"`         // rewrite COLLATE …
	FromDatabase      string `json:"fromDatabase"`      // rename this database…
	ToDatabase        string `json:"toDatabase"`        // …to this (both required for the rename)
}

// DumpTransformSummary reports a transform run.
type DumpTransformSummary struct {
	BytesIn      int64 `json:"bytesIn"`
	BytesOut     int64 `json:"bytesOut"`
	Replacements int   `json:"replacements"`
}

var (
	reDefinerQuoted = regexp.MustCompile("(?i)\\s+DEFINER\\s*=\\s*`(?:``|[^`])+`\\s*@\\s*`(?:``|[^`])+`")
	reDefinerBare   = regexp.MustCompile(`(?i)\s+DEFINER\s*=\s*[A-Za-z0-9_.$%-]+\s*@\s*[A-Za-z0-9_.$%-]+`)
	reAutoInc       = regexp.MustCompile(`(?i)\s+AUTO_INCREMENT\s*=\s*\d+`)
	reEngine        = regexp.MustCompile(`(?i)(ENGINE\s*=\s*)\w+`)
	reCharset       = regexp.MustCompile(`(?i)((?:DEFAULT\s+)?(?:CHARSET|CHARACTER\s+SET)\s*=?\s*)\w+`)
	reCollate       = regexp.MustCompile(`(?i)(COLLATE\s*=?\s*)\w+`)
	reTableDDL      = regexp.MustCompile(`(?i)^\s*(?:CREATE\s+(?:TEMPORARY\s+)?TABLE|ALTER\s+TABLE)\b`)
	reCreateStmt    = regexp.MustCompile(`(?i)^\s*CREATE\b`)
)

func replaceLiteralSuffix(s string, re *regexp.Regexp, value string, count *int) string {
	return re.ReplaceAllStringFunc(s, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		*count++
		return parts[1] + value
	})
}

func removeMatches(s string, re *regexp.Regexp, count *int) string {
	return re.ReplaceAllStringFunc(s, func(string) string {
		*count++
		return ""
	})
}

// transformOutsideBackticks applies table-option rewrites only outside quoted
// identifiers. A column literally named `ENGINE=MyISAM` is data, not a table
// option.
func transformOutsideBackticks(s string, transform func(string) string) string {
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		start := i
		for i < len(s) && s[i] != '`' {
			i++
		}
		out.WriteString(transform(s[start:i]))
		if i >= len(s) {
			break
		}
		start = i
		i++
		for i < len(s) {
			if s[i] != '`' {
				i++
				continue
			}
			if i+1 < len(s) && s[i+1] == '`' {
				i += 2
				continue
			}
			i++
			break
		}
		out.WriteString(s[start:i])
	}
	return out.String()
}

func validateSQLName(label, value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) || len(value) > 128 {
		return fmt.Errorf("%s must be a valid SQL identifier of at most 128 bytes", label)
	}
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '$' || r == '-' {
			continue
		}
		return fmt.Errorf("%s contains unsupported SQL identifier character %q", label, r)
	}
	return nil
}

func validateSQLToken(label, value string) error {
	if value == "" {
		return nil
	}
	first := value[0]
	startsWithLetter := first >= 'A' && first <= 'Z' || first >= 'a' && first <= 'z'
	if len(value) > 64 || !startsWithLetter {
		return fmt.Errorf("%s must begin with an ASCII letter and contain at most 64 characters", label)
	}
	for _, r := range value {
		if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return fmt.Errorf("%s contains unsupported SQL token character %q", label, r)
	}
	return nil
}

func validateDumpTransform(t DumpTransform) error {
	for _, item := range []struct {
		label string
		value string
	}{
		{"engine", t.Engine},
		{"charset", t.Charset},
		{"collation", t.Collation},
	} {
		if err := validateSQLToken(item.label, item.value); err != nil {
			return err
		}
	}
	if err := validateSQLName("source database", t.FromDatabase); err != nil {
		return err
	}
	if err := validateSQLName("destination database", t.ToDatabase); err != nil {
		return err
	}
	if (t.FromDatabase == "") != (t.ToDatabase == "") {
		return errors.New("source and destination database must be provided together")
	}
	return nil
}

type dumpLexState struct {
	inBlockComment bool
	tableDDL       bool
	quote          byte
	escaped        bool
}

type sqlSegment struct {
	text string
	code bool
}

// splitSQLSegments marks strings and comments as protected data. Backtick
// identifiers deliberately remain code because MySQL dump clauses and database
// qualifiers use them.
func splitSQLSegments(line string, state *dumpLexState) ([]sqlSegment, string) {
	segments := make([]sqlSegment, 0, 8)
	var codeOnly strings.Builder
	appendSegment := func(text string, code bool) {
		if text == "" {
			return
		}
		segments = append(segments, sqlSegment{text: text, code: code})
		if code {
			codeOnly.WriteString(text)
		} else {
			for range text {
				codeOnly.WriteByte(' ')
			}
		}
	}

	for i := 0; i < len(line); {
		if state.quote != 0 {
			start := i
			for i < len(line) {
				if state.escaped {
					state.escaped = false
					i++
					continue
				}
				if line[i] == '\\' {
					state.escaped = true
					i++
					continue
				}
				if line[i] == state.quote {
					if i+1 < len(line) && line[i+1] == state.quote {
						i += 2
						continue
					}
					i++
					state.quote = 0
					break
				}
				i++
			}
			appendSegment(line[start:i], false)
			if state.quote != 0 {
				break
			}
			continue
		}
		if state.inBlockComment {
			end := strings.Index(line[i:], "*/")
			if end < 0 {
				appendSegment(line[i:], false)
				break
			}
			end += i + 2
			appendSegment(line[i:end], false)
			state.inBlockComment = false
			i = end
			continue
		}

		start := i
		for i < len(line) {
			if strings.HasPrefix(line[i:], "/*") || strings.HasPrefix(line[i:], "--") || line[i] == '#' || line[i] == '\'' || line[i] == '"' {
				break
			}
			i++
		}
		appendSegment(line[start:i], true)
		if i >= len(line) {
			break
		}
		switch {
		case strings.HasPrefix(line[i:], "/*"):
			state.inBlockComment = true
		case strings.HasPrefix(line[i:], "--"), line[i] == '#':
			appendSegment(line[i:], false)
			return segments, codeOnly.String()
		case line[i] == '\'' || line[i] == '"':
			state.quote = line[i]
			state.escaped = false
			appendSegment(line[i:i+1], false)
			i++
			continue
		}
	}
	return segments, codeOnly.String()
}

func transformSQLLine(line string, state *dumpLexState, t DumpTransform, reUse, reQual *regexp.Regexp, count *int) string {
	segments, codeOnly := splitSQLSegments(line, state)
	trimmedCode := strings.TrimSpace(codeOnly)
	if !state.tableDDL && reTableDDL.MatchString(trimmedCode) {
		state.tableDDL = true
	}
	createStatement := reCreateStmt.MatchString(trimmedCode)

	var out strings.Builder
	out.Grow(len(line))
	for _, segment := range segments {
		text := segment.text
		if segment.code {
			if t.RemoveDefiner && createStatement {
				text = removeMatches(text, reDefinerQuoted, count)
				text = transformOutsideBackticks(text, func(code string) string {
					return removeMatches(code, reDefinerBare, count)
				})
			}
			if state.tableDDL {
				text = transformOutsideBackticks(text, func(code string) string {
					if t.DropAutoIncrement {
						code = removeMatches(code, reAutoInc, count)
					}
					if t.Engine != "" {
						code = replaceLiteralSuffix(code, reEngine, t.Engine, count)
					}
					if t.Charset != "" {
						code = replaceLiteralSuffix(code, reCharset, t.Charset, count)
					}
					if t.Collation != "" {
						code = replaceLiteralSuffix(code, reCollate, t.Collation, count)
					}
					return code
				})
			}
			if reUse != nil {
				text = replaceLiteralSuffix(text, reUse, "`"+t.ToDatabase+"`", count)
				text = reQual.ReplaceAllStringFunc(text, func(string) string {
					*count++
					return "`" + t.ToDatabase + "`."
				})
			}
		}
		out.WriteString(text)
	}
	if strings.Contains(codeOnly, ";") {
		state.tableDDL = false
	}
	return out.String()
}

// TransformDump streams a SQL dump applying cleanup presets with bounded
// logical-line acquisition. Only SQL code is transformed; literals and comments
// are copied byte-for-byte.
func TransformDump(r io.Reader, w io.Writer, t DumpTransform) (DumpTransformSummary, error) {
	return TransformDumpContext(context.Background(), r, w, t)
}

func TransformDumpContext(ctx context.Context, r io.Reader, w io.Writer, t DumpTransform) (DumpTransformSummary, error) {
	if err := validateDumpTransform(t); err != nil {
		return DumpTransformSummary{}, err
	}
	br := bufio.NewReaderSize(r, 256<<10)
	cw := &countWriter{w: w}
	bw := bufio.NewWriterSize(cw, 256<<10)
	var in int64
	count := 0
	var lex dumpLexState

	// Per-call database-rename regexes (FromDatabase escaped).
	var reUse, reQual *regexp.Regexp
	if t.FromDatabase != "" && t.ToDatabase != "" {
		q := regexp.QuoteMeta(t.FromDatabase)
		reUse = regexp.MustCompile(`(?i)(USE\s+|CREATE\s+DATABASE\s+(?:IF\s+NOT\s+EXISTS\s+)?|DROP\s+DATABASE\s+(?:IF\s+EXISTS\s+)?)` + "`" + q + "`")
		reQual = regexp.MustCompile("`" + q + "`\\.")
	}

	for {
		lineBytes, err := readBoundedSQLLine(ctx, br, maxSQLLogicalLineBytes)
		line := string(lineBytes)
		in += int64(len(line))
		if line != "" {
			line = transformSQLLine(line, &lex, t, reUse, reQual, &count)
			if _, werr := bw.WriteString(line); werr != nil {
				return DumpTransformSummary{}, werr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return DumpTransformSummary{}, err
		}
	}
	if err := bw.Flush(); err != nil {
		return DumpTransformSummary{}, err
	}
	return DumpTransformSummary{BytesIn: in, BytesOut: cw.n, Replacements: count}, nil
}

// --- CSV column tools ---

// ProjectCSV rewrites a CSV/TSV keeping only the named columns, in the given
// order. Streaming. Returns the number of data rows written.
func ProjectCSV(r io.Reader, w io.Writer, comma rune, keep []string) (int64, error) {
	cr := csv.NewReader(r)
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err == io.EOF {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pos := map[string]int{}
	for i, h := range header {
		if _, ok := pos[h]; !ok {
			pos[h] = i
		}
	}
	idx := make([]int, 0, len(keep))
	for _, name := range keep {
		i, ok := pos[name]
		if !ok {
			return 0, &columnError{name}
		}
		idx = append(idx, i)
	}
	cw := csv.NewWriter(w)
	cw.Comma = comma
	if err := cw.Write(keep); err != nil {
		return 0, err
	}
	var rows int64
	out := make([]string, len(idx))
	for {
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return rows, fmt.Errorf("read CSV data record %d: %w", rows+1, e)
		}
		for j, i := range idx {
			if i < len(rec) {
				out[j] = rec[i]
			} else {
				out[j] = ""
			}
		}
		if err := cw.Write(out); err != nil {
			return rows, err
		}
		rows++
	}
	cw.Flush()
	return rows, cw.Error()
}

// AddCSVColumn appends a constant-valued column to every row of a CSV/TSV.
func AddCSVColumn(r io.Reader, w io.Writer, comma rune, name, value string) (int64, error) {
	cr := csv.NewReader(r)
	cr.Comma = comma
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	header, err := cr.Read()
	if err == io.EOF {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cw := csv.NewWriter(w)
	cw.Comma = comma
	if err := cw.Write(append(append([]string{}, header...), name)); err != nil {
		return 0, err
	}
	var rows int64
	for {
		rec, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return rows, fmt.Errorf("read CSV data record %d: %w", rows+1, e)
		}
		if err := cw.Write(append(rec, value)); err != nil {
			return rows, err
		}
		rows++
	}
	cw.Flush()
	return rows, cw.Error()
}

type columnError struct{ name string }

func (e *columnError) Error() string { return "column not found: " + e.name }
