package datatools

import (
	"bufio"
	"encoding/csv"
	"io"
	"regexp"
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
	reDefiner = regexp.MustCompile(`(?i)\s*DEFINER\s*=\s*[^\s]+@[^\s]+`)
	reAutoInc = regexp.MustCompile(`(?i)\s+AUTO_INCREMENT\s*=\s*\d+`)
	reEngine  = regexp.MustCompile(`(?i)(ENGINE\s*=\s*)\w+`)
	reCharset = regexp.MustCompile(`(?i)((?:DEFAULT\s+)?(?:CHARSET|CHARACTER\s+SET)\s*=?\s*)\w+`)
	reCollate = regexp.MustCompile(`(?i)(COLLATE\s*=?\s*)\w+`)
)

func subCount(line string, re *regexp.Regexp, tmpl string, count *int) string {
	if m := re.FindAllStringIndex(line, -1); len(m) > 0 {
		*count += len(m)
		return re.ReplaceAllString(line, tmpl)
	}
	return line
}

// TransformDump streams a SQL dump applying the selected cleanup presets line by
// line (so it stays memory-safe on multi-GB dumps).
func TransformDump(r io.Reader, w io.Writer, t DumpTransform) (DumpTransformSummary, error) {
	br := bufio.NewReaderSize(r, 256<<10)
	cw := &countWriter{w: w}
	bw := bufio.NewWriterSize(cw, 256<<10)
	var in int64
	count := 0

	// Per-call database-rename regexes (FromDatabase escaped).
	var reUse, reQual *regexp.Regexp
	if t.FromDatabase != "" && t.ToDatabase != "" {
		q := regexp.QuoteMeta(t.FromDatabase)
		reUse = regexp.MustCompile(`(?i)(USE\s+|CREATE\s+DATABASE\s+(?:IF\s+NOT\s+EXISTS\s+)?|DROP\s+DATABASE\s+(?:IF\s+EXISTS\s+)?)` + "`" + q + "`")
		reQual = regexp.MustCompile("`" + q + "`\\.")
	}

	for {
		line, err := br.ReadString('\n')
		in += int64(len(line))
		if line != "" {
			if t.RemoveDefiner {
				line = subCount(line, reDefiner, "", &count)
			}
			if t.DropAutoIncrement {
				line = subCount(line, reAutoInc, "", &count)
			}
			if t.Engine != "" {
				line = subCount(line, reEngine, "${1}"+t.Engine, &count)
			}
			if t.Charset != "" {
				line = subCount(line, reCharset, "${1}"+t.Charset, &count)
			}
			if t.Collation != "" {
				line = subCount(line, reCollate, "${1}"+t.Collation, &count)
			}
			if reUse != nil {
				line = subCount(line, reUse, "${1}`"+t.ToDatabase+"`", &count)
				line = subCount(line, reQual, "`"+t.ToDatabase+"`.", &count)
			}
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
			break
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
			break
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
