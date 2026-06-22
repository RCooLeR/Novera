package analyze

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
)

type ReaderAtSize interface {
	io.ReaderAt
	Size() int64
}

type Progress struct {
	BytesProcessed int64
	BytesTotal     int64
}

type Table struct {
	Name         string
	CreateOffset int64
	InsertOffset int64
}

type Summary struct {
	Tables          []Table
	CreateTables    int
	InsertTables    int
	DefinerCount    int
	Charsets        map[string]int
	Collations      map[string]int
	MysqldumpHeader bool
}

type Options struct {
	ChunkSize int
	Progress  func(Progress)
}

// sqlIdent matches one MySQL identifier: a backtick-quoted name (with doubled
// `` escapes, so it may contain dots/spaces) or a bare run of identifier bytes
// (no '.', so a db-qualified name splits into qualifier + table).
const sqlIdent = "(?:`(?:``|[^`])*`|[A-Za-z0-9_$-]+)"

var (
	// CREATE TABLE [`db`.]`tbl` — capture the TABLE segment, not the database.
	createTableRe = regexp.MustCompile("(?i)CREATE(?:\\s+DEFINER\\s*=\\s*`?[^`\\s]+`?@`?[^`\\s]+`?)?\\s+TABLE(?:\\s+IF\\s+NOT\\s+EXISTS)?\\s+(?:" + sqlIdent + "\\s*\\.\\s*)?(" + sqlIdent + ")")
	definerRe     = regexp.MustCompile("(?i)DEFINER\\s*=\\s*`?[^`\\s]+`?@`?[^`\\s]+`?")
	charsetRe     = regexp.MustCompile("(?i)(?:DEFAULT\\s+)?(?:CHARSET|CHARACTER\\s+SET)\\s*=?\\s*`?([a-zA-Z0-9_]+)`?")
	collationRe   = regexp.MustCompile("(?i)COLLATE\\s*=?\\s*`?([a-zA-Z0-9_]+)`?")
	mysqldumpRe   = regexp.MustCompile("(?i)mysqldump|mysql\\s+dump")
)

const analyzerCarrySize = 16 * 1024

func AnalyzeFile(ctx context.Context, path string, opts Options) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Summary{}, err
	}
	return Analyze(ctx, fileReaderAtSize{File: f, size: st.Size()}, opts)
}

type fileReaderAtSize struct {
	*os.File
	size int64
}

func (f fileReaderAtSize) Size() int64 {
	return f.size
}

func Analyze(ctx context.Context, r ReaderAtSize, opts Options) (Summary, error) {
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 16 * 1024 * 1024
	}
	buf := make([]byte, opts.ChunkSize)
	carry := make([]byte, 0, analyzerCarrySize)
	window := make([]byte, 0, opts.ChunkSize+analyzerCarrySize)
	scratch := make([]byte, 0, opts.ChunkSize+analyzerCarrySize)
	var maskSt maskState
	processed := int64(0)
	total := r.Size()

	summary := Summary{
		Charsets:   make(map[string]int),
		Collations: make(map[string]int),
	}
	tables := make(map[string]*Table)

	for processed < total {
		select {
		case <-ctx.Done():
			return Summary{}, ctx.Err()
		default:
		}

		want := opts.ChunkSize
		if remaining := total - processed; remaining < int64(want) {
			want = int(remaining)
		}
		n, err := r.ReadAt(buf[:want], processed)
		if err != nil && !errors.Is(err, io.EOF) {
			return Summary{}, err
		}
		if n == 0 {
			break
		}

		window = window[:0]
		window = append(window, carry...)
		window = append(window, buf[:n]...)
		windowStart := processed - int64(len(carry))
		processLimit := len(window) - analyzerCarrySize
		if errors.Is(err, io.EOF) {
			processLimit = len(window)
		}
		if processLimit < 0 {
			processLimit = 0
		}

		maskSt, scratch = processWindow(&summary, tables, window, scratch, windowStart, processLimit, maskSt)

		if processLimit < len(window) {
			carry = append(carry[:0], window[processLimit:]...)
		} else {
			carry = carry[:0]
		}
		processed += int64(n)
		if opts.Progress != nil {
			opts.Progress(Progress{BytesProcessed: processed, BytesTotal: total})
		}
	}

	if len(carry) > 0 {
		processWindow(&summary, tables, carry, scratch, total-int64(len(carry)), len(carry), maskSt)
	}

	for _, table := range tables {
		summary.Tables = append(summary.Tables, *table)
		if table.CreateOffset >= 0 {
			summary.CreateTables++
		}
		if table.InsertOffset >= 0 {
			summary.InsertTables++
		}
	}
	sort.Slice(summary.Tables, func(i, j int) bool {
		left := tablePrimaryOffset(summary.Tables[i])
		right := tablePrimaryOffset(summary.Tables[j])
		if left != right {
			return left < right
		}
		return summary.Tables[i].Name < summary.Tables[j].Name
	})
	return summary, nil
}

func processWindow(summary *Summary, tables map[string]*Table, window, scratch []byte, windowStart int64, processLimit int, st maskState) (maskState, []byte) {
	scan, nextState := maskSQLLiteralsAndComments(window, scratch, st, processLimit)
	if processLimit > len(scan) {
		processLimit = len(scan)
	}
	if processLimit < 0 {
		processLimit = 0
	}
	if !summary.MysqldumpHeader && containsFold(window, "mysql") {
		if match := mysqldumpRe.FindIndex(window); matchStartsBeforeLimit(match, processLimit) {
			summary.MysqldumpHeader = true
		}
	}

	// INSERT INTO is the hot path on data dumps: a fast ASCII fold scan + a
	// hand-rolled parse instead of a case-insensitive RE2 scan (which dominated
	// CPU profiling). Equivalent to insertIntoRe over the masked window.
	for pos := 0; pos < processLimit; {
		k := indexFold(scan, "insert", pos)
		if k < 0 || k >= processLimit {
			break
		}
		ns, ne, end, ok := parseInsertInto(scan, k)
		if ok {
			table := ensureTable(tables, normalizeIdentifier(window[ns:ne]))
			if table.InsertOffset < 0 {
				table.InsertOffset = windowStart + int64(k)
			}
			pos = end
		} else {
			pos = k + 1
		}
	}

	// Rarer statements: only run the precise regex when its keyword is present
	// in the window (a cheap fold scan), so INSERT-only windows skip them all.
	if containsFold(scan, "create") {
		for _, match := range createTableRe.FindAllSubmatchIndex(scan, -1) {
			if !matchStartsBeforeLimit(match, processLimit) {
				continue
			}
			name := normalizeIdentifier(window[match[2]:match[3]])
			offset := windowStart + int64(match[0])
			table := ensureTable(tables, name)
			if table.CreateOffset < 0 {
				table.CreateOffset = offset
			}
		}
	}
	if containsFold(scan, "definer") {
		for _, match := range definerRe.FindAllIndex(scan, -1) {
			if matchStartsBeforeLimit(match, processLimit) {
				summary.DefinerCount++
			}
		}
	}
	if containsFold(scan, "char") { // CHARSET or CHARACTER SET
		for _, match := range charsetRe.FindAllSubmatchIndex(scan, -1) {
			if !matchStartsBeforeLimit(match, processLimit) {
				continue
			}
			name := normalizeStatsName(window[match[2]:match[3]])
			if name != "" {
				summary.Charsets[name]++
			}
		}
	}
	if containsFold(scan, "collate") {
		for _, match := range collationRe.FindAllSubmatchIndex(scan, -1) {
			if !matchStartsBeforeLimit(match, processLimit) {
				continue
			}
			name := normalizeStatsName(window[match[2]:match[3]])
			if name != "" {
				summary.Collations[name]++
			}
		}
	}
	// Return the masked buffer so the caller can reuse it as scratch, and the
	// lexer state at processLimit so the next window resumes mid-literal correctly.
	return nextState, scan
}

func asciiLower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// indexFold returns the index >= from of the first case-insensitive ASCII match
// of word (which must be lowercase) in b, or -1.
func indexFold(b []byte, word string, from int) int {
	m := len(word)
	if m == 0 {
		return from
	}
	c0 := word[0]
	last := len(b) - m
	for i := from; i <= last; i++ {
		if asciiLower(b[i]) != c0 {
			continue
		}
		j := 1
		for ; j < m; j++ {
			if asciiLower(b[i+j]) != word[j] {
				break
			}
		}
		if j == m {
			return i
		}
	}
	return -1
}

func containsFold(b []byte, word string) bool { return indexFold(b, word, 0) >= 0 }

func matchFold(b []byte, pos int, word string) bool {
	if pos+len(word) > len(b) {
		return false
	}
	for j := 0; j < len(word); j++ {
		if asciiLower(b[pos+j]) != word[j] {
			return false
		}
	}
	return true
}

func skipSQLSpace(b []byte, pos int) int {
	for pos < len(b) {
		switch b[pos] {
		case ' ', '\t', '\r', '\n', '\f', '\v':
			pos++
		default:
			return pos
		}
	}
	return pos
}

func isBareIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '$' || c == '-'
}

// parseSQLIdentifier parses one identifier at b[pos]: a backtick-quoted name
// (with doubled `` escapes) or a bare run of identifier bytes. It returns the
// raw token range [start,end) (backticks included; normalizeIdentifier strips
// them) and the position after it. ok is false if no identifier is present or a
// backtick quote is left unterminated (truncated at a window edge).
func parseSQLIdentifier(b []byte, pos int) (start, end, next int, ok bool) {
	if pos >= len(b) {
		return 0, 0, 0, false
	}
	if b[pos] == '`' {
		for i := pos + 1; i < len(b); i++ {
			if b[i] == '`' {
				if i+1 < len(b) && b[i+1] == '`' { // doubled backtick escape
					i++
					continue
				}
				return pos, i + 1, i + 1, true
			}
		}
		return 0, 0, 0, false
	}
	i := pos
	for i < len(b) && isBareIdentByte(b[i]) {
		i++
	}
	if i == pos {
		return 0, 0, 0, false
	}
	return pos, i, i, true
}

// parseInsertInto parses "INSERT\s+INTO\s+[`?db`?\s*.\s*]`?name`?" starting at
// b[k] (where b[k:] already folds to "insert"). It returns the TABLE name byte
// range [ns,ne) (skipping any db. qualifier), the end position, and whether it
// matched — equivalent to insertIntoRe with db-qualified support.
func parseInsertInto(b []byte, k int) (ns, ne, end int, ok bool) {
	p := k + len("insert")
	q := skipSQLSpace(b, p)
	if q == p {
		return 0, 0, 0, false
	}
	p = q
	if !matchFold(b, p, "into") {
		return 0, 0, 0, false
	}
	p += len("into")
	q = skipSQLSpace(b, p)
	if q == p {
		return 0, 0, 0, false
	}
	p = q
	s1, e1, n1, ok1 := parseSQLIdentifier(b, p)
	if !ok1 {
		return 0, 0, 0, false
	}
	// Optional `db`. qualifier — keep only the table segment after the dot.
	q = skipSQLSpace(b, n1)
	if q < len(b) && b[q] == '.' {
		p = skipSQLSpace(b, q+1)
		s2, e2, n2, ok2 := parseSQLIdentifier(b, p)
		if !ok2 {
			return 0, 0, 0, false
		}
		return s2, e2, n2, true
	}
	return s1, e1, n1, true
}

func matchStartsBeforeLimit(match []int, processLimit int) bool {
	return len(match) >= 2 && match[0] >= 0 && match[0] < processLimit
}

// maskMode is the SQL lexer state carried across analysis windows. Without it,
// a string literal or comment larger than one chunk would leave the next window
// starting mid-literal with the opening quote out of view, so blob contents
// (e.g. text that looks like CREATE TABLE / INSERT INTO, or a stray apostrophe)
// would be scanned as live SQL — producing phantom tables or dropping real ones,
// which then corrupts the byte ranges used by extract/split.
type maskMode uint8

const (
	modeNormal maskMode = iota
	modeSingleQuote
	modeDoubleQuote
	modeLineComment
	modeBlockComment
)

type maskState struct {
	mode maskMode
	// escaped: inside a quote, the previous byte was an escaping backslash, so
	// this byte is literal and cannot close the quote.
	escaped bool
	// prevStar: inside a block comment, the previous byte was '*', so a '/' now
	// closes the comment.
	prevStar bool
	// justExited: the quote byte that just closed a string; if the next byte
	// repeats it, it was a doubled-quote escape ('' or "") and we re-enter.
	justExited byte
}

// maskSQLLiteralsAndComments masks SQL string literals and comments in `in`,
// writing into `scratch` (reused and grown as needed, eliminating a per-chunk
// allocation). It resumes from `st` and returns the masked slice plus the lexer
// state as of index `stateAt` (clamped), which the caller carries to the next
// window so constructs that span the chunk boundary stay masked. Newlines are
// preserved so offset/line bookkeeping is unaffected.
func maskSQLLiteralsAndComments(in, scratch []byte, st maskState, stateAt int) ([]byte, maskState) {
	out := append(scratch[:0], in...)
	if stateAt < 0 {
		stateAt = 0
	}
	mode := st.mode
	escaped := st.escaped
	prevStar := st.prevStar
	justExited := st.justExited

	atState := st
	captured := stateAt == 0

	for i := 0; i < len(out); i++ {
		if !captured && i >= stateAt {
			atState = maskState{mode: mode, escaped: escaped, prevStar: prevStar, justExited: justExited}
			captured = true
		}
		c := out[i]
		switch mode {
		case modeNormal:
			if justExited != 0 {
				q := justExited
				justExited = 0
				if c == q { // doubled quote: re-enter the string
					if c != '\n' && c != '\r' {
						out[i] = ' '
					}
					if q == '\'' {
						mode = modeSingleQuote
					} else {
						mode = modeDoubleQuote
					}
					continue
				}
			}
			switch c {
			case '\'':
				out[i] = ' '
				mode, escaped = modeSingleQuote, false
			case '"':
				out[i] = ' '
				mode, escaped = modeDoubleQuote, false
			case '#':
				out[i] = ' '
				mode = modeLineComment
			case '-':
				if i+1 < len(out) && out[i+1] == '-' &&
					(i+2 >= len(out) || out[i+2] == ' ' || out[i+2] == '\t' || out[i+2] == '\r' || out[i+2] == '\n') {
					out[i] = ' '
					mode = modeLineComment
				}
			case '/':
				if i+1 < len(out) && out[i+1] == '*' {
					out[i] = ' '
					mode, prevStar = modeBlockComment, false
				}
			}
		case modeSingleQuote, modeDoubleQuote:
			q := byte('\'')
			if mode == modeDoubleQuote {
				q = '"'
			}
			if c != '\n' && c != '\r' {
				out[i] = ' '
			}
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == q:
				mode, justExited = modeNormal, q
			}
		case modeLineComment:
			if c == '\n' || c == '\r' {
				mode = modeNormal
			} else {
				out[i] = ' '
			}
		case modeBlockComment:
			if c != '\n' && c != '\r' {
				out[i] = ' '
			}
			if c == '/' && prevStar {
				mode, prevStar = modeNormal, false
			} else {
				prevStar = c == '*'
			}
		}
	}
	if !captured {
		atState = maskState{mode: mode, escaped: escaped, prevStar: prevStar, justExited: justExited}
	}
	return out, atState
}

func ensureTable(tables map[string]*Table, name string) *Table {
	table, ok := tables[name]
	if ok {
		return table
	}
	table = &Table{Name: name, CreateOffset: -1, InsertOffset: -1}
	tables[name] = table
	return table
}

func normalizeIdentifier(value []byte) string {
	trimmed := bytes.TrimSpace(value)
	trimmed = bytes.Trim(trimmed, "`")
	return strings.TrimSpace(string(trimmed))
}

func normalizeStatsName(value []byte) string {
	return strings.ToLower(normalizeIdentifier(value))
}

func tablePrimaryOffset(table Table) int64 {
	switch {
	case table.CreateOffset >= 0:
		return table.CreateOffset
	case table.InsertOffset >= 0:
		return table.InsertOffset
	default:
		return 1<<62 - 1
	}
}
