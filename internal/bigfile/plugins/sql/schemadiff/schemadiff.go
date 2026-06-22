// Package schemadiff compares the structure of two SQL dumps: which tables were
// added or dropped, and for tables present in both, which columns were
// added/removed or had their definition changed. It parses CREATE TABLE column
// lists rather than executing anything, so it works on dump text alone.
package schemadiff

import (
	"sort"
	"strings"
)

// Column is one parsed column of a CREATE TABLE.
type Column struct {
	Name       string `json:"name"`
	Definition string `json:"definition"` // type + modifiers, normalized
}

// Table is a parsed table schema.
type Table struct {
	Name    string   `json:"name"`
	Columns []Column `json:"columns"`
}

// ColumnChange describes a column that exists in both tables but differs.
type ColumnChange struct {
	Name string `json:"name"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// TableDiff is the per-table column-level diff for a table in both dumps.
type TableDiff struct {
	Name          string         `json:"name"`
	AddedColumns  []Column       `json:"addedColumns"`
	RemovedColumns []Column      `json:"removedColumns"`
	ChangedColumns []ColumnChange `json:"changedColumns"`
}

// Changed reports whether this table actually differs.
func (d TableDiff) Changed() bool {
	return len(d.AddedColumns) > 0 || len(d.RemovedColumns) > 0 || len(d.ChangedColumns) > 0
}

// Result is the full schema diff between two dumps (A = old, B = new).
type Result struct {
	AddedTables   []string    `json:"addedTables"`   // in B, not in A
	RemovedTables []string    `json:"removedTables"` // in A, not in B
	ChangedTables []TableDiff `json:"changedTables"` // in both, column changes
	UnchangedCount int        `json:"unchangedCount"`
}

// Diff compares two sets of parsed tables (a = old, b = new).
func Diff(a, b []Table) Result {
	am := indexTables(a)
	bm := indexTables(b)
	var res Result
	for key, bt := range bm {
		if _, ok := am[key]; !ok {
			res.AddedTables = append(res.AddedTables, bt.Name)
		}
	}
	for key, at := range am {
		if _, ok := bm[key]; !ok {
			res.RemovedTables = append(res.RemovedTables, at.Name)
		}
	}
	for key, at := range am {
		bt, ok := bm[key]
		if !ok {
			continue
		}
		td := diffTable(at.Name, at, bt)
		if td.Changed() {
			res.ChangedTables = append(res.ChangedTables, td)
		} else {
			res.UnchangedCount++
		}
	}
	sort.Strings(res.AddedTables)
	sort.Strings(res.RemovedTables)
	sort.Slice(res.ChangedTables, func(i, j int) bool { return res.ChangedTables[i].Name < res.ChangedTables[j].Name })
	return res
}

func diffTable(name string, a, b Table) TableDiff {
	td := TableDiff{Name: name}
	ac := indexColumns(a.Columns)
	bc := indexColumns(b.Columns)
	for _, col := range b.Columns {
		if _, ok := ac[strings.ToLower(col.Name)]; !ok {
			td.AddedColumns = append(td.AddedColumns, col)
		}
	}
	for _, col := range a.Columns {
		bcol, ok := bc[strings.ToLower(col.Name)]
		if !ok {
			td.RemovedColumns = append(td.RemovedColumns, col)
			continue
		}
		if !strings.EqualFold(col.Definition, bcol.Definition) {
			td.ChangedColumns = append(td.ChangedColumns, ColumnChange{Name: col.Name, Old: col.Definition, New: bcol.Definition})
		}
	}
	return td
}

// indexTables keys by lower-cased name so two dumps that differ only in table
// name case are compared as the same table (matching the column-level contract).
func indexTables(ts []Table) map[string]Table {
	m := make(map[string]Table, len(ts))
	for _, t := range ts {
		m[strings.ToLower(t.Name)] = t
	}
	return m
}

func indexColumns(cs []Column) map[string]Column {
	m := make(map[string]Column, len(cs))
	for _, c := range cs {
		m[strings.ToLower(c.Name)] = c
	}
	return m
}

// constraintKeywords are the leading words of a table-constraint clause, not a
// column definition.
var constraintKeywords = map[string]bool{
	"primary":    true,
	"unique":     true,
	"key":        true,
	"index":      true,
	"constraint": true,
	"foreign":    true,
	"fulltext":   true,
	"spatial":    true,
	"check":      true,
}

// ParseColumns extracts column definitions from a CREATE TABLE statement's DDL
// text. It reads the first top-level (...) body and splits its depth-1
// comma-separated entries, returning those that begin with a column identifier.
func ParseColumns(ddl []byte) []Column {
	body, ok := tableBody(ddl)
	if !ok {
		return nil
	}
	entries := splitTopLevel(body)
	cols := make([]Column, 0, len(entries))
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		name, rest, quoted, ok := leadingIdentifier(e)
		if !ok {
			continue
		}
		// A bare leading keyword (KEY, PRIMARY, UNIQUE, …) is a table constraint,
		// not a column — but a backtick-quoted identifier with the same spelling
		// (e.g. a column literally named `key`) IS a column.
		if !quoted && constraintKeywords[strings.ToLower(name)] {
			continue
		}
		cols = append(cols, Column{Name: name, Definition: normalizeDef(rest)})
	}
	return cols
}

// tableBody returns the text inside the first balanced top-level (...) of a
// CREATE TABLE statement, quote/backtick-aware.
func tableBody(ddl []byte) (string, bool) {
	depth := 0
	start := -1
	var inS, inD, inB, esc bool
	for i := 0; i < len(ddl); i++ {
		c := ddl[i]
		switch {
		case inS:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '\'' {
				inS = false
			}
			continue
		case inD:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inD = false
			}
			continue
		case inB:
			if c == '`' {
				inB = false
			}
			continue
		}
		switch c {
		case '\'':
			inS = true
		case '"':
			inD = true
		case '`':
			inB = true
		case '(':
			if depth == 0 {
				start = i + 1
			}
			depth++
		case ')':
			depth--
			if depth == 0 && start >= 0 {
				return string(ddl[start:i]), true
			}
		}
	}
	return "", false
}

// splitTopLevel splits s on commas that sit at paren depth 0, quote-aware.
func splitTopLevel(s string) []string {
	var out []string
	depth := 0
	start := 0
	var inS, inD, inB, esc bool
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inS:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '\'' {
				inS = false
			}
			continue
		case inD:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inD = false
			}
			continue
		case inB:
			if c == '`' {
				inB = false
			}
			continue
		}
		switch c {
		case '\'':
			inS = true
		case '"':
			inD = true
		case '`':
			inB = true
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	out = append(out, s[start:])
	return out
}

// leadingIdentifier reads the first identifier (backtick-quoted or bare) of a
// column entry and returns it, the remainder (type/modifiers), and whether it
// was backtick-quoted (a quoted leading token is always a column, never a
// table-constraint clause).
func leadingIdentifier(e string) (name, rest string, quoted, ok bool) {
	e = strings.TrimSpace(e)
	if e == "" {
		return "", "", false, false
	}
	if e[0] == '`' {
		end := strings.IndexByte(e[1:], '`')
		if end < 0 {
			return "", "", false, false
		}
		name = e[1 : 1+end]
		rest = strings.TrimSpace(e[2+end:])
		return name, rest, true, true
	}
	i := 0
	for i < len(e) && isIdentByte(e[i]) {
		i++
	}
	if i == 0 {
		return "", "", false, false
	}
	return e[:i], strings.TrimSpace(e[i:]), false, true
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$'
}

// normalizeDef collapses internal whitespace so cosmetic reformatting doesn't
// read as a column change.
func normalizeDef(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
