package analyze

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	sqldelimiter "novera/internal/bigfile/plugins/sql/delimiter"
	"novera/internal/bigfile/regularfile"
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
	// Name is the stable display and selection identity. Unqualified tables keep
	// their table name. Qualified tables use an unambiguous, escaped
	// `database`.`table` spelling so two databases cannot collapse in service
	// DTOs that historically exposed only this field.
	Name         string
	Database     string
	TableName    string
	CreateOffset int64
	InsertOffset int64
	// Regions contains every recognized CREATE/INSERT/REPLACE-led block for
	// this identity. Each region ends immediately after its own lexically
	// top-level semicolon; unsupported compound statements fail analysis rather
	// than borrowing a later boundary. Consumers must use these regions instead
	// of treating the first/last offsets as one contiguous range.
	Regions []Region
}

type RegionKind string

const (
	RegionCreate  RegionKind = "create"
	RegionInsert  RegionKind = "insert"
	RegionReplace RegionKind = "replace"
)

type Region struct {
	Kind        RegionKind
	StartOffset int64
	EndOffset   int64
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

const (
	// DefaultChunkBytes is the streaming read size used when Options.ChunkSize
	// is zero. It is also the largest supported chunk: analysis keeps a read
	// buffer, a carry window, and a masked scratch window alive concurrently.
	DefaultChunkBytes = 16 * 1024 * 1024
	// MaxChunkBytes prevents a caller-controlled allocation from defeating the
	// analyzer's bounded-memory contract.
	MaxChunkBytes = DefaultChunkBytes
	// MaxTableCount bounds retained table names and offsets. Repeated
	// observations of an existing table do not consume additional slots.
	MaxTableCount = 10_000
	// MaxRegionCount bounds retained CREATE/INSERT/REPLACE block metadata.
	// Repeated statements for one table therefore remain memory-bounded too.
	MaxRegionCount = 1_000_000
	// MaxDistinctCharsetNames bounds distinct normalized charset names retained
	// in a Summary. Occurrence counts for already-known names remain exact.
	MaxDistinctCharsetNames = 256
	// MaxDistinctCollationNames bounds distinct normalized collation names
	// retained in a Summary. MySQL's ordinary built-in set is well below this.
	MaxDistinctCollationNames = 2_048
	// MaxIdentifierBytes bounds every table, database qualifier, charset, and
	// collation identifier accepted into analysis metadata.
	MaxIdentifierBytes = 1_024
)

// LimitKind classifies a hard analysis limit without requiring callers to
// parse an error string.
type LimitKind string

const (
	LimitChunkBytes      LimitKind = "chunk-bytes"
	LimitTables          LimitKind = "tables"
	LimitRegions         LimitKind = "regions"
	LimitCharsetNames    LimitKind = "charset-names"
	LimitCollationNames  LimitKind = "collation-names"
	LimitIdentifierBytes LimitKind = "identifier-bytes"
)

var (
	// ErrLimitExceeded is matched by every controlled hard-limit failure.
	ErrLimitExceeded = errors.New("SQL analysis limit exceeded")
	// ErrInvalidOptions reports an invalid option before the analyzer reads the
	// source or allocates its streaming buffers.
	ErrInvalidOptions = errors.New("invalid SQL analysis options")
	// ErrUnsupportedDelimiter prevents cache consumers from treating offsets
	// derived with ordinary semicolon boundaries as safe for a routine dump
	// whose mysql-client DELIMITER commands change those boundaries.
	ErrUnsupportedDelimiter = sqldelimiter.ErrUnsupported
	// ErrUnterminatedStatement prevents extraction from guessing an end offset
	// for a recognized CREATE/INSERT/REPLACE statement that has no top-level
	// semicolon in the source.
	ErrUnterminatedStatement = errors.New("recognized SQL statement has no top-level semicolon")
	// ErrAmbiguousStatement prevents malformed or unsupported nesting from
	// assigning one semicolon to multiple recognized table statements.
	ErrAmbiguousStatement = errors.New("recognized SQL statement boundaries are ambiguous")
	// ErrUnsupportedCompoundStatement rejects stored routines, triggers, rules,
	// anonymous procedural blocks, client-managed COPY payloads, and PostgreSQL
	// dollar-quoted bodies whose internal semicolons are not safe extraction
	// boundaries for this deliberately small streaming analyzer.
	ErrUnsupportedCompoundStatement = errors.New("compound SQL body or client-managed COPY payload is not supported by table analysis")
	// ErrUnsupportedLexicalConstruct rejects dialect syntax that this analyzer
	// cannot mask without risking phantom or incomplete extraction offsets.
	ErrUnsupportedLexicalConstruct = errors.New("unsupported SQL dialect quoting or comment construct")
	// ErrUnresolvedStatementPrefix reports statement-leading INSERT/REPLACE
	// syntax that is outside the bounded analyzer grammar, including otherwise
	// supported prefixes that outgrow its fixed lookahead. Returning an empty
	// Summary is safer than silently omitting an extraction region.
	ErrUnresolvedStatementPrefix = errors.New("recognized SQL table statement prefix is unresolved by the bounded analyzer grammar")
	// ErrInvalidIdentifierEncoding prevents invalid source bytes from becoming
	// replacement-normalized JSON names that can collide or become unselectable.
	ErrInvalidIdentifierEncoding = errors.New("SQL identifier is not valid UTF-8")
)

// LimitError reports the first hard limit that analysis would exceed. Analyze
// returns an empty Summary with this error; partial metadata is never returned.
type LimitError struct {
	Kind     LimitKind
	Limit    int
	Observed int
}

func (e *LimitError) Error() string {
	if e == nil {
		return ErrLimitExceeded.Error()
	}
	return fmt.Sprintf("%s: %s observed %d, limit %d", ErrLimitExceeded, e.Kind, e.Observed, e.Limit)
}

func (e *LimitError) Unwrap() error { return ErrLimitExceeded }

// sqlIdent matches one identifier: a backtick- or double-quote-delimited name
// (with doubled delimiter escapes, so it may contain dots/spaces) or a bare run
// of identifier bytes (no '.', so a db-qualified name splits into segments).
const sqlIdent = "(?:\"(?:\"\"|[^\"])*\"|`(?:``|[^`])*`|[A-Za-z0-9_$\\-\\p{L}\\p{N}\\p{M}]+)"

var (
	// CREATE TABLE [`db`.]`tbl` — capture the TABLE segment, not the database.
	definerIdent  = "(?:\"(?:\"\"|[^\"])*\"|`(?:``|[^`])*`|[^`\"\\s@]+)"
	definerClause = "DEFINER\\s*=\\s*" + definerIdent + "\\s*@\\s*" + definerIdent
	createTableRe = regexp.MustCompile("(?i)CREATE(?:\\s+" + definerClause + ")?\\s+(?:TEMPORARY\\s+)?TABLE(?:\\s+IF\\s+NOT\\s+EXISTS)?\\s+(?:(" + sqlIdent + ")\\s*\\.\\s*)?(" + sqlIdent + ")")
	definerRe     = regexp.MustCompile("(?i)" + definerClause)
	charsetRe     = regexp.MustCompile("(?i)(?:DEFAULT\\s+)?(?:CHARSET|CHARACTER\\s+SET)\\s*=?\\s*(" + sqlIdent + ")")
	collationRe   = regexp.MustCompile("(?i)COLLATE\\s*=?\\s*(" + sqlIdent + ")")
	mysqldumpRe   = regexp.MustCompile(`(?i)mysqldump|mysql\s+dump`)
)

const analyzerCarrySize = 16 * 1024

func AnalyzeFile(ctx context.Context, path string, opts Options) (Summary, error) {
	var err error
	opts, err = normalizeOptions(opts)
	if err != nil {
		return Summary{}, err
	}
	if ctx == nil {
		return Summary{}, fmt.Errorf("%w: context is nil", ErrInvalidOptions)
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	f, err := regularfile.Open(path)
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

type tableIdentity struct {
	database string
	table    string
}

func Analyze(ctx context.Context, r ReaderAtSize, opts Options) (Summary, error) {
	var err error
	opts, err = normalizeOptions(opts)
	if err != nil {
		return Summary{}, err
	}
	if ctx == nil {
		return Summary{}, fmt.Errorf("%w: context is nil", ErrInvalidOptions)
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if r == nil {
		return Summary{}, fmt.Errorf("%w: source is nil", ErrInvalidOptions)
	}
	total := r.Size()
	if total < 0 {
		return Summary{}, fmt.Errorf("%w: source size %d is negative", ErrInvalidOptions, total)
	}
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	summary := Summary{
		Charsets:   make(map[string]int),
		Collations: make(map[string]int),
	}
	if total == 0 {
		return summary, nil
	}
	bufferBytes := opts.ChunkSize
	if total < int64(bufferBytes) {
		bufferBytes = int(total)
	}
	buf := make([]byte, bufferBytes)
	carry := make([]byte, 0, analyzerCarrySize)
	window := make([]byte, 0, bufferBytes+analyzerCarrySize)
	scratch := make([]byte, 0, bufferBytes+analyzerCarrySize)
	var maskSt maskState
	var delimiterProbe sqldelimiter.Detector
	processed := int64(0)
	tables := make(map[tableIdentity]*Table)
	regionCount := 0
	var regionBoundaries regionBoundaryState

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
		if n < 0 || n > want {
			return Summary{}, fmt.Errorf("SQL analyzer source returned invalid byte count %d for a %d-byte read", n, want)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return Summary{}, err
		}
		if n == 0 {
			if processed < total {
				if errors.Is(err, io.EOF) {
					return Summary{}, io.ErrUnexpectedEOF
				}
				return Summary{}, io.ErrNoProgress
			}
			break
		}
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		for i, c := range buf[:n] {
			if i&((64*1024)-1) == 0 {
				if err := ctx.Err(); err != nil {
					return Summary{}, err
				}
			}
			if delimiterProbe.Step(c) {
				return Summary{}, ErrUnsupportedDelimiter
			}
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

		maskSt, scratch, err = processWindow(ctx, &summary, tables, &regionCount, &regionBoundaries, window, scratch, windowStart, processLimit, maskSt)
		if err != nil {
			return Summary{}, err
		}

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
	if delimiterProbe.Finish() {
		return Summary{}, ErrUnsupportedDelimiter
	}

	if len(carry) > 0 {
		if err := ctx.Err(); err != nil {
			return Summary{}, err
		}
		maskSt, _, err = processWindow(ctx, &summary, tables, &regionCount, &regionBoundaries, carry, scratch, total-int64(len(carry)), len(carry), maskSt)
		if err != nil {
			return Summary{}, err
		}
	}
	if maskSt.hasUnterminatedConstruct() {
		return Summary{}, fmt.Errorf("%w: unterminated quoted literal, identifier, or block comment", ErrUnsupportedLexicalConstruct)
	}
	if regionBoundaries.compound.Finish() {
		return Summary{}, ErrUnsupportedCompoundStatement
	}
	if err := clientHazardError(regionBoundaries.client.Finish(), total); err != nil {
		return Summary{}, err
	}
	if proof, ok := regionBoundaries.target.Finish(); ok {
		if err := verifyTargetPrefixOwnership(&regionBoundaries, proof); err != nil {
			return Summary{}, err
		}
	}
	if proof, ok := regionBoundaries.target.UnresolvedData(); ok {
		return Summary{}, unresolvedTargetPrefixError(proof)
	}
	if regionBoundaries.active {
		region := regionBoundaries.ref.table.Regions[regionBoundaries.ref.index]
		return Summary{}, fmt.Errorf("%w: %s statement at byte %d", ErrUnterminatedStatement, region.Kind, region.StartOffset)
	}

	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if err := finalizeRegions(ctx, tables, regionCount, total); err != nil {
		return Summary{}, err
	}
	summary.Tables = make([]Table, 0, len(tables))
	for _, table := range tables {
		if len(summary.Tables)&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return Summary{}, err
			}
		}
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
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func normalizeOptions(opts Options) (Options, error) {
	switch {
	case opts.ChunkSize < 0:
		return Options{}, fmt.Errorf("%w: chunk size %d is negative", ErrInvalidOptions, opts.ChunkSize)
	case opts.ChunkSize == 0:
		opts.ChunkSize = DefaultChunkBytes
	case opts.ChunkSize > MaxChunkBytes:
		return Options{}, &LimitError{Kind: LimitChunkBytes, Limit: MaxChunkBytes, Observed: opts.ChunkSize}
	}
	return opts, nil
}

func processWindow(ctx context.Context, summary *Summary, tables map[tableIdentity]*Table, regionCount *int, boundaries *regionBoundaryState, window, scratch []byte, windowStart int64, processLimit int, st maskState) (maskState, []byte, error) {
	scan, nextState, err := maskSQLLiteralsAndComments(ctx, window, scratch, st, processLimit)
	if err != nil {
		return st, scan, err
	}
	if processLimit > len(scan) {
		processLimit = len(scan)
	}
	if processLimit < 0 {
		processLimit = 0
	}
	candidates := make([]regionCandidate, 0, 8)
	if !summary.MysqldumpHeader && containsFold(window, "mysql") {
		if match := mysqldumpRe.FindIndex(window); matchStartsBeforeLimit(match, processLimit) {
			summary.MysqldumpHeader = true
		}
	}

	// INSERT/REPLACE is the hot path on data dumps: a fast ASCII fold scan plus
	// a strict MySQL prefix parser. Both keyword boundaries are checked before
	// accepting a match, so identifier substrings cannot invent table ranges.
	for _, verb := range []struct {
		word string
		kind RegionKind
	}{
		{word: "insert", kind: RegionInsert},
		{word: "replace", kind: RegionReplace},
	} {
		for pos := 0; pos < processLimit; {
			if err := ctx.Err(); err != nil {
				return st, scan, err
			}
			k := indexFold(scan, verb.word, pos)
			if k < 0 || k >= processLimit {
				break
			}
			ref, end, ok, err := parseDataStatement(scan, k, verb.word)
			if err != nil {
				return st, scan, err
			}
			if ok {
				database, name, err := normalizeTableReference(window, ref)
				if err != nil {
					return st, scan, err
				}
				offset := windowStart + int64(k)
				candidates = append(candidates, regionCandidate{database: database, table: name, kind: verb.kind, offset: offset})
				pos = end
			} else {
				pos = k + 1
			}
		}
	}

	// Rarer statements: only run the precise regex when its keyword is present
	// in the window (a cheap fold scan), so INSERT-only windows skip them all.
	if containsFold(scan, "create") {
		for searchAt := 0; searchAt < len(scan); {
			if err := ctx.Err(); err != nil {
				return st, scan, err
			}
			match := createTableRe.FindSubmatchIndex(scan[searchAt:])
			if match == nil {
				break
			}
			matchStart := searchAt + match[0]
			if matchStart >= processLimit {
				break
			}
			if !hasKeywordBoundaries(scan, matchStart, len("create")) {
				searchAt = nextRegexSearchOffset(searchAt, match)
				continue
			}
			// Do not finalize an apparently unqualified first segment while its
			// following byte is outside the bounded lookahead. A distant `.table`
			// must fail closed instead of being retained under the database name.
			afterName := skipSQLSpace(scan, searchAt+match[1])
			if afterName >= len(scan) || scan[afterName] == '.' {
				searchAt = nextRegexSearchOffset(searchAt, match)
				continue
			}
			ref := parsedTableRef{tableStart: searchAt + match[4], tableEnd: searchAt + match[5]}
			if match[2] >= 0 {
				ref.databaseStart = searchAt + match[2]
				ref.databaseEnd = searchAt + match[3]
				ref.qualified = true
			}
			database, name, err := normalizeTableReference(window, ref)
			if err != nil {
				return st, scan, err
			}
			offset := windowStart + int64(matchStart)
			candidates = append(candidates, regionCandidate{database: database, table: name, kind: RegionCreate, offset: offset})
			searchAt = nextRegexSearchOffset(searchAt, match)
		}
	}
	if containsFold(scan, "definer") {
		for searchAt := 0; searchAt < len(scan); {
			if err := ctx.Err(); err != nil {
				return st, scan, err
			}
			match := definerRe.FindIndex(scan[searchAt:])
			if match == nil || searchAt+match[0] >= processLimit {
				break
			}
			matchStart := searchAt + match[0]
			if !hasKeywordBoundaries(scan, matchStart, len("definer")) {
				searchAt = nextRegexSearchOffset(searchAt, match)
				continue
			}
			summary.DefinerCount++
			searchAt = nextRegexSearchOffset(searchAt, match)
		}
	}
	if containsFold(scan, "char") { // CHARSET or CHARACTER SET
		for searchAt := 0; searchAt < len(scan); {
			if err := ctx.Err(); err != nil {
				return st, scan, err
			}
			match := charsetRe.FindSubmatchIndex(scan[searchAt:])
			if match == nil || searchAt+match[0] >= processLimit {
				break
			}
			matchStart := searchAt + match[0]
			wordLen := len("charset")
			if matchFold(scan, matchStart, "default") {
				wordLen = len("default")
			} else if matchFold(scan, matchStart, "character") {
				wordLen = len("character")
			}
			if !hasKeywordBoundaries(scan, matchStart, wordLen) {
				searchAt = nextRegexSearchOffset(searchAt, match)
				continue
			}
			name, err := normalizeStatsName(window[searchAt+match[2] : searchAt+match[3]])
			if err != nil {
				return st, scan, err
			}
			if name != "" {
				if err := incrementNamedCount(summary.Charsets, name, LimitCharsetNames, MaxDistinctCharsetNames); err != nil {
					return st, scan, err
				}
			}
			searchAt = nextRegexSearchOffset(searchAt, match)
		}
	}
	if containsFold(scan, "collate") {
		for searchAt := 0; searchAt < len(scan); {
			if err := ctx.Err(); err != nil {
				return st, scan, err
			}
			match := collationRe.FindSubmatchIndex(scan[searchAt:])
			if match == nil || searchAt+match[0] >= processLimit {
				break
			}
			matchStart := searchAt + match[0]
			if !hasKeywordBoundaries(scan, matchStart, len("collate")) {
				searchAt = nextRegexSearchOffset(searchAt, match)
				continue
			}
			name, err := normalizeStatsName(window[searchAt+match[2] : searchAt+match[3]])
			if err != nil {
				return st, scan, err
			}
			if name != "" {
				if err := incrementNamedCount(summary.Collations, name, LimitCollationNames, MaxDistinctCollationNames); err != nil {
					return st, scan, err
				}
			}
			searchAt = nextRegexSearchOffset(searchAt, match)
		}
	}
	if err := resolveRegionBoundaries(scan[:processLimit], windowStart, candidates, boundaries, tables, regionCount); err != nil {
		return st, scan, err
	}
	// Return the masked buffer so the caller can reuse it as scratch, and the
	// lexer state at processLimit so the next window resumes mid-literal correctly.
	return nextState, scan, nil
}

func nextRegexSearchOffset(searchAt int, match []int) int {
	if len(match) < 2 || match[1] <= 0 {
		return searchAt + 1
	}
	return searchAt + match[1]
}

func incrementNamedCount(counts map[string]int, name string, kind LimitKind, limit int) error {
	if count, ok := counts[name]; ok {
		counts[name] = count + 1
		return nil
	}
	if len(counts) >= limit {
		return &LimitError{Kind: kind, Limit: limit, Observed: len(counts) + 1}
	}
	counts[name] = 1
	return nil
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
		c == '_' || c == '$' || c == '-' || c >= 0x80
}

// parseSQLIdentifier parses one identifier at b[pos]: a backtick- or
// double-quote-delimited name (with doubled delimiter escapes) or a bare run of
// identifier bytes. It returns the raw token range [start,end), delimiters
// included, and the position after it. ok is false if no identifier is present
// or a quoted token is truncated at a window edge.
func parseSQLIdentifier(b []byte, pos int) (start, end, next int, ok bool, err error) {
	if pos >= len(b) {
		return 0, 0, 0, false, nil
	}
	if quote := b[pos]; quote == '`' || quote == '"' {
		contentBytes := 0
		for i := pos + 1; i < len(b); i++ {
			if b[i] == quote {
				if i+1 < len(b) && b[i+1] == quote { // doubled delimiter escape
					contentBytes += 2
					if contentBytes > MaxIdentifierBytes {
						return 0, 0, 0, false, identifierLimitError(contentBytes)
					}
					i++
					continue
				}
				return pos, i + 1, i + 1, true, nil
			}
			contentBytes++
			if contentBytes > MaxIdentifierBytes {
				return 0, 0, 0, false, identifierLimitError(contentBytes)
			}
		}
		return 0, 0, 0, false, nil
	}
	i := pos
	for i < len(b) && isBareIdentByte(b[i]) {
		if i-pos >= MaxIdentifierBytes {
			return 0, 0, 0, false, identifierLimitError(i - pos + 1)
		}
		i++
	}
	if i == pos {
		return 0, 0, 0, false, nil
	}
	return pos, i, i, true, nil
}

type parsedTableRef struct {
	databaseStart int
	databaseEnd   int
	tableStart    int
	tableEnd      int
	qualified     bool
}

// parseDataStatement accepts the MySQL forms emitted by Quarry and mysqldump:
//
//	INSERT [LOW_PRIORITY|DELAYED|HIGH_PRIORITY] [IGNORE] INTO table
//	REPLACE [LOW_PRIORITY|DELAYED] INTO table
//
// INTO remains required by the analyzer contract. Requiring it prevents a
// misspelled modifier from being reinterpreted as a table name and changing
// extraction offsets. Every keyword is matched with both word boundaries.
func parseDataStatement(b []byte, k int, verb string) (parsedTableRef, int, bool, error) {
	if verb != "insert" && verb != "replace" {
		return parsedTableRef{}, 0, false, nil
	}
	if !hasKeywordBoundaries(b, k, len(verb)) || !matchFold(b, k, verb) {
		return parsedTableRef{}, 0, false, nil
	}
	p, ok := requireSQLSpaceAfter(b, k+len(verb))
	if !ok {
		return parsedTableRef{}, 0, false, nil
	}

	if verb == "insert" {
		if next, matched := matchAnyKeyword(b, p, "low_priority", "delayed", "high_priority"); matched {
			p, ok = requireSQLSpaceAfter(b, next)
			if !ok {
				return parsedTableRef{}, 0, false, nil
			}
		}
		if next, matched := matchKeyword(b, p, "ignore"); matched {
			p, ok = requireSQLSpaceAfter(b, next)
			if !ok {
				return parsedTableRef{}, 0, false, nil
			}
		}
	} else if next, matched := matchAnyKeyword(b, p, "low_priority", "delayed"); matched {
		p, ok = requireSQLSpaceAfter(b, next)
		if !ok {
			return parsedTableRef{}, 0, false, nil
		}
	}

	next, matched := matchKeyword(b, p, "into")
	if !matched {
		return parsedTableRef{}, 0, false, nil
	}
	p, ok = requireSQLSpaceAfter(b, next)
	if !ok {
		return parsedTableRef{}, 0, false, nil
	}
	if verb == "insert" {
		if _, reserved := matchAnyKeyword(b, p, "only", "table", "directory"); reserved {
			// PostgreSQL ONLY is ambiguous with a valid unquoted MySQL table named
			// ONLY, while Hive TABLE/DIRECTORY are target introducers rather than
			// identities. Fail through target-prefix ownership instead of retaining
			// any of these keywords as the wrong table name.
			return parsedTableRef{}, 0, false, nil
		}
	}

	s1, e1, n1, ok1, err := parseSQLIdentifier(b, p)
	if err != nil {
		return parsedTableRef{}, 0, false, err
	}
	if !ok1 {
		return parsedTableRef{}, 0, false, nil
	}
	ref := parsedTableRef{tableStart: s1, tableEnd: e1}
	q := skipSQLSpace(b, n1)
	if q >= len(b) {
		return parsedTableRef{}, 0, false, nil
	}
	if b[q] != '.' {
		return ref, n1, true, nil
	}
	p = skipSQLSpace(b, q+1)
	s2, e2, n2, ok2, err := parseSQLIdentifier(b, p)
	if err != nil {
		return parsedTableRef{}, 0, false, err
	}
	if !ok2 {
		return parsedTableRef{}, 0, false, nil
	}
	ref.databaseStart = s1
	ref.databaseEnd = e1
	ref.tableStart = s2
	ref.tableEnd = e2
	ref.qualified = true
	q = skipSQLSpace(b, n2)
	if q >= len(b) || b[q] == '.' {
		return parsedTableRef{}, 0, false, nil
	}
	return ref, n2, true, nil
}

func requireSQLSpaceAfter(b []byte, pos int) (int, bool) {
	next := skipSQLSpace(b, pos)
	return next, next > pos
}

func matchKeyword(b []byte, pos int, word string) (int, bool) {
	if !matchFold(b, pos, word) || !hasKeywordBoundaries(b, pos, len(word)) {
		return pos, false
	}
	return pos + len(word), true
}

func matchAnyKeyword(b []byte, pos int, words ...string) (int, bool) {
	for _, word := range words {
		if next, ok := matchKeyword(b, pos, word); ok {
			return next, true
		}
	}
	return pos, false
}

func hasKeywordBoundaries(b []byte, pos int, length int) bool {
	if pos < 0 || length <= 0 || pos+length > len(b) {
		return false
	}
	return (pos == 0 || !isBareIdentByte(b[pos-1])) &&
		(pos+length == len(b) || !isBareIdentByte(b[pos+length]))
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
	modeBacktick
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
	// blockJustOpened prevents the '*' in the opening delimiter from also being
	// reused as the '*' in a closing delimiter (the invalid overlap in "/*/").
	blockJustOpened bool
	// justExited: the quote byte that just closed a string; if the next byte
	// repeats it, it was a doubled-quote escape ('', "", or ``) and we re-enter.
	justExited byte
	// prevQ records an immediately preceding q/Q in normal SQL. Oracle's q'...'
	// quoting cannot be safely masked without remembering its chosen delimiter,
	// so the opener is rejected explicitly even when split across windows.
	prevQ bool
	// lineHasContent and statementHasContent disambiguate MySQL's '#' comments
	// from PostgreSQL's live '#' operators. Hash comments are accepted only on
	// an otherwise blank physical line between top-level statements.
	lineHasContent      bool
	statementHasContent bool
}

// maskSQLLiteralsAndComments masks SQL string literals and comments in `in`,
// writing into `scratch` (reused and grown as needed, eliminating a per-chunk
// allocation). It resumes from `st` and returns the masked slice plus the lexer
// state as of index `stateAt` (clamped), which the caller carries to the next
// window so constructs that span the chunk boundary stay masked. Newlines are
// preserved so offset/line bookkeeping is unaffected.
func maskSQLLiteralsAndComments(ctx context.Context, in, scratch []byte, st maskState, stateAt int) ([]byte, maskState, error) {
	out := append(scratch[:0], in...)
	if err := ctx.Err(); err != nil {
		return out, st, err
	}
	if stateAt < 0 {
		stateAt = 0
	}
	mode := st.mode
	escaped := st.escaped
	prevStar := st.prevStar
	blockJustOpened := st.blockJustOpened
	justExited := st.justExited
	prevQ := st.prevQ
	lineHasContent := st.lineHasContent
	statementHasContent := st.statementHasContent

	atState := st
	captured := stateAt == 0

	for i := 0; i < len(out); i++ {
		if i&((64*1024)-1) == 0 {
			if err := ctx.Err(); err != nil {
				return out, st, err
			}
		}
		if !captured && i >= stateAt {
			atState = maskState{
				mode: mode, escaped: escaped, prevStar: prevStar, blockJustOpened: blockJustOpened,
				justExited: justExited, prevQ: prevQ,
				lineHasContent: lineHasContent, statementHasContent: statementHasContent,
			}
			captured = true
		}
		c := out[i]
		lineLeading := !lineHasContent
		if c == '\n' || c == '\r' {
			lineHasContent = false
		} else if !isSQLSpace(c) {
			lineHasContent = true
		}
		switch mode {
		case modeNormal:
			if justExited != 0 {
				q := justExited
				justExited = 0
				if c == q { // doubled quote: re-enter the string
					// Backtick delimiters stay visible so the statement parsers can
					// recover target ranges. A doubled pair is identifier content,
					// though, so hide both bytes when they share this window. Across
					// a carry seam, justExited still keeps following content masked.
					if (q == '`' || q == '"') && i > 0 {
						out[i-1] = ' '
					}
					if c != '\n' && c != '\r' {
						out[i] = ' '
					}
					switch q {
					case '\'':
						mode = modeSingleQuote
					case '"':
						mode = modeDoubleQuote
					default:
						mode = modeBacktick
					}
					prevQ = false
					continue
				}
			}
			if c == '\'' && prevQ {
				return out, st, fmt.Errorf("%w: Oracle q-quoted literal", ErrUnsupportedLexicalConstruct)
			}
			prevQ = false
			switch c {
			case '\'':
				statementHasContent = true
				out[i] = ' '
				mode, escaped = modeSingleQuote, false
			case '"':
				// Preserve structural double quotes so PostgreSQL/SQLite-style
				// identifiers remain parseable; their contents are still masked.
				statementHasContent = true
				mode, escaped = modeDoubleQuote, false
			case '`':
				// Preserve structural ticks but mask all identifier contents.
				statementHasContent = true
				mode = modeBacktick
			case '[':
				return out, st, fmt.Errorf("%w: bracket-quoted construct", ErrUnsupportedLexicalConstruct)
			case '#':
				if !lineLeading || statementHasContent {
					return out, st, fmt.Errorf("%w: ambiguous inline '#' comment or operator", ErrUnsupportedLexicalConstruct)
				}
				out[i] = ' '
				mode = modeLineComment
			case '-':
				if i+1 < len(out) && out[i+1] == '-' {
					if i+2 < len(out) && !isSQLSpace(out[i+2]) {
						return out, st, fmt.Errorf("%w: '--' without separating whitespace", ErrUnsupportedLexicalConstruct)
					}
					out[i] = ' '
					mode = modeLineComment
				} else {
					statementHasContent = true
				}
			case '/':
				if i+1 < len(out) && out[i+1] == '*' {
					// Bytes at/after stateAt are retained as lookahead and will be
					// rescanned. Defer classification there so a safe directive arriving
					// one byte at a time is not rejected before its closing */ is visible.
					if i < stateAt {
						switch {
						case i+2 < len(out) && out[i+2] == '+':
							if !statementHasContent {
								return out, st, fmt.Errorf("%w: detached optimizer comment", ErrUnsupportedLexicalConstruct)
							}
						case i+2 < len(out) && out[i+2] == '!':
							if !safeExecutableCommentPrefix(out, i+3) {
								return out, st, fmt.Errorf("%w: executable SQL comment can own table data", ErrUnsupportedLexicalConstruct)
							}
						case i+3 < len(out) && (out[i+2] == 'm' || out[i+2] == 'M') && out[i+3] == '!':
							if !safeExecutableCommentPrefix(out, i+4) {
								return out, st, fmt.Errorf("%w: MariaDB executable SQL comment can own table data", ErrUnsupportedLexicalConstruct)
							}
						}
					}
					out[i] = ' '
					mode, prevStar, blockJustOpened = modeBlockComment, false, true
				} else {
					statementHasContent = true
				}
			case ';':
				statementHasContent = false
			default:
				prevQ = c == 'q' || c == 'Q'
				if !isSQLSpace(c) {
					statementHasContent = true
				}
			}
		case modeSingleQuote:
			if c != '\n' && c != '\r' {
				out[i] = ' '
			}
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '\'':
				mode, justExited = modeNormal, c
			}
		case modeDoubleQuote:
			// Leave only a possible terminal delimiter visible. If the next byte
			// is another quote, normal-mode handling masks both as escaped content.
			switch {
			case escaped:
				escaped = false
				if c != '\n' && c != '\r' {
					out[i] = ' '
				}
			case c == '\\':
				escaped = true
				out[i] = ' '
			case c == '"':
				mode, justExited = modeNormal, c
			default:
				if c != '\n' && c != '\r' {
					out[i] = ' '
				}
			}
		case modeBacktick:
			if c == '`' {
				mode, justExited = modeNormal, c
			} else if c != '\n' && c != '\r' {
				out[i] = ' '
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
			if blockJustOpened {
				blockJustOpened = false
				prevStar = false
				continue
			}
			if c == '/' && prevStar {
				mode, prevStar, blockJustOpened = modeNormal, false, false
			} else if c == '/' && i+1 < len(out) && out[i+1] == '*' {
				return out, st, fmt.Errorf("%w: nested block comment", ErrUnsupportedLexicalConstruct)
			} else {
				prevStar = c == '*'
			}
		}
	}
	if !captured {
		atState = maskState{
			mode: mode, escaped: escaped, prevStar: prevStar, blockJustOpened: blockJustOpened,
			justExited: justExited, prevQ: prevQ,
			lineHasContent: lineHasContent, statementHasContent: statementHasContent,
		}
	}
	return out, atState, nil
}

// safeExecutableCommentPrefix permits the common one-statement mysqldump SET
// and ALTER directives that do not own CREATE/INSERT/REPLACE data regions.
// Unknown/overlong blocks, live internal semicolons, and later target keywords
// fail closed. Quoted marker text remains payload. The scan is bounded by the
// caller's fixed carry window and retains only constant lexer/token state.
func safeExecutableCommentPrefix(b []byte, pos int) bool {
	for pos < len(b) && b[pos] >= '0' && b[pos] <= '9' {
		pos++
	}
	for pos < len(b) && isSQLSpace(b[pos]) {
		pos++
	}
	start := pos
	for pos < len(b) && isCompoundWordByte(b[pos]) {
		pos++
		if pos-start > 8 {
			return false
		}
	}
	if start == pos || pos == len(b) {
		return false
	}
	token := b[start:pos]
	if !bytes.EqualFold(token, []byte("set")) && !bytes.EqualFold(token, []byte("alter")) {
		return false
	}

	var quote byte
	escaped := false
	justExited := byte(0)
	var word [16]byte
	wordLen := 0
	overflow := false
	finishWord := func() bool {
		if wordLen == 0 && !overflow {
			return false
		}
		hazard := !overflow && (string(word[:wordLen]) == "create" || string(word[:wordLen]) == "insert" || string(word[:wordLen]) == "replace")
		wordLen = 0
		overflow = false
		return hazard
	}

	for ; pos < len(b); pos++ {
		c := b[pos]
		if quote != 0 {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == quote:
				quote = 0
				justExited = c
			}
			continue
		}
		if justExited != 0 {
			closed := justExited
			justExited = 0
			if c == closed {
				quote = closed
				continue
			}
		}
		if c == '*' && pos+1 < len(b) && b[pos+1] == '/' {
			return !finishWord()
		}
		if c == ';' || (c == '/' && pos+1 < len(b) && b[pos+1] == '*') {
			return false
		}
		switch c {
		case '\'', '"', '`':
			if finishWord() {
				return false
			}
			quote = c
			escaped = false
		default:
			if isCompoundWordByte(c) {
				if wordLen < len(word) {
					word[wordLen] = asciiLower(c)
					wordLen++
				} else {
					overflow = true
				}
			} else if finishWord() {
				return false
			}
		}
	}
	return false
}

func (s maskState) hasUnterminatedConstruct() bool {
	switch s.mode {
	case modeSingleQuote, modeDoubleQuote, modeBacktick, modeBlockComment:
		return true
	default:
		return false
	}
}

func ensureTable(tables map[tableIdentity]*Table, database string, name string) (*Table, error) {
	identity := tableIdentity{database: database, table: name}
	table, ok := tables[identity]
	if ok {
		return table, nil
	}
	if len(tables) >= MaxTableCount {
		return nil, &LimitError{Kind: LimitTables, Limit: MaxTableCount, Observed: len(tables) + 1}
	}
	table = &Table{
		Name:         displayTableName(database, name),
		Database:     database,
		TableName:    name,
		CreateOffset: -1,
		InsertOffset: -1,
	}
	tables[identity] = table
	return table, nil
}

type regionRef struct {
	table *Table
	index int
}

type regionCandidate struct {
	database string
	table    string
	kind     RegionKind
	offset   int64
}

type regionBoundaryState struct {
	active     bool
	ref        regionRef
	hasContent bool
	prevLess   bool
	bomState   uint8 // 0: undecided; 1: EF; 2: EF BB; 3: resolved
	client     clientCommandDetector
	compound   compoundPrefixDetector
	dollar     dollarQuoteDetector
	target     targetPrefixDetector
}

func appendRegion(table *Table, kind RegionKind, offset int64, count *int) (regionRef, error) {
	if table == nil || count == nil {
		return regionRef{}, errors.New("SQL analyzer region state is unavailable")
	}
	if *count >= MaxRegionCount {
		return regionRef{}, &LimitError{Kind: LimitRegions, Limit: MaxRegionCount, Observed: *count + 1}
	}
	index := len(table.Regions)
	table.Regions = append(table.Regions, Region{Kind: kind, StartOffset: offset, EndOffset: -1})
	*count++
	return regionRef{table: table, index: index}, nil
}

func resolveRegionBoundaries(scan []byte, windowStart int64, candidates []regionCandidate, state *regionBoundaryState, tables map[tableIdentity]*Table, count *int) error {
	if state == nil || tables == nil || count == nil {
		return errors.New("SQL analyzer boundary state is unavailable")
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].offset != candidates[j].offset {
			return candidates[i].offset < candidates[j].offset
		}
		return candidates[i].kind < candidates[j].kind
	})
	activate := func(candidate regionCandidate) (bool, error) {
		if state.client.AmbiguousSameLine() {
			// Candidate activation precedes consumption of its first byte. Refuse
			// immediately when an unknown client-owned segment earlier on this
			// physical line made SQL semicolon ownership ambiguous; do not rely on
			// the client detector reaching the candidate's trailing word boundary.
			return false, fmt.Errorf("%w: %s candidate at byte %d follows an ambiguous client segment",
				ErrUnsupportedCompoundStatement, candidate.kind, candidate.offset)
		}
		if state.active {
			active := state.ref.table.Regions[state.ref.index]
			return false, fmt.Errorf("%w: %s statement at byte %d contains %s statement at byte %d before its terminator",
				ErrAmbiguousStatement, active.Kind, active.StartOffset, candidate.kind, candidate.offset)
		}
		// INSERT/REPLACE text inside a CREATE TRIGGER body or after a WITH
		// prefix does not own the surrounding statement's byte range. Only a
		// small explicitly safe set of wrapper prefixes may ignore such a nested
		// candidate; unknown prefixes fail closed rather than risking ownership.
		if state.hasContent {
			if state.compound.PotentialCompoundPrefix() {
				return false, fmt.Errorf("%w: %s candidate at byte %d is nested in a compound prefix",
					ErrUnsupportedCompoundStatement, candidate.kind, candidate.offset)
			}
			// A candidate that starts on a new physical line while an unknown,
			// unterminated statement is still open may be client-script payload
			// (for example SQL*Plus SPOOL output followed by dumped SQL). Treat
			// that composition like other client-managed bodies. Activation runs
			// before the first candidate byte is consumed, so classify it here
			// rather than waiting for the line detector to finish the keyword.
			if state.compound.Irrelevant() && state.client.state == clientLineLeading {
				return false, fmt.Errorf("%w: %s candidate at byte %d follows an unterminated client line",
					ErrUnsupportedCompoundStatement, candidate.kind, candidate.offset)
			}
			return false, fmt.Errorf("%w: %s candidate at byte %d does not own the statement prefix",
				ErrAmbiguousStatement, candidate.kind, candidate.offset)
		}
		table, err := ensureTable(tables, candidate.database, candidate.table)
		if err != nil {
			return false, err
		}
		if candidate.kind == RegionCreate {
			if table.CreateOffset < 0 || candidate.offset < table.CreateOffset {
				table.CreateOffset = candidate.offset
			}
		} else if table.InsertOffset < 0 || candidate.offset < table.InsertOffset {
			table.InsertOffset = candidate.offset
		}
		ref, err := appendRegion(table, candidate.kind, candidate.offset, count)
		if err != nil {
			return false, err
		}
		state.active = true
		state.ref = ref
		return true, nil
	}

	candidateAt := 0
	for i, c := range scan {
		offset := windowStart + int64(i)
		for candidateAt < len(candidates) && candidates[candidateAt].offset <= offset {
			if _, err := activate(candidates[candidateAt]); err != nil {
				return err
			}
			candidateAt++
		}
		if state.consumeLeadingBOM(c, offset) {
			continue
		}
		if state.prevLess && c == '<' {
			return fmt.Errorf("%w at byte %d: Oracle block label", ErrUnsupportedLexicalConstruct, offset-1)
		}
		state.prevLess = c == '<'
		if err := clientHazardError(state.client.Step(c, state.hasContent, state.compound.Irrelevant()), offset); err != nil {
			return err
		}
		if state.compound.Step(c) {
			// The precise bounded CREATE TABLE parser may already have proven
			// ownership of the legacy CREATE DEFINER=... TABLE spelling accepted
			// by this analyzer. That exact active region is safe; every other
			// DEFINER-bearing CREATE remains fail-closed because an unquoted
			// account name can otherwise hide a later routine object token.
			if state.compound.DefinerHazard() && state.active &&
				state.ref.table.Regions[state.ref.index].Kind == RegionCreate {
				state.compound.AllowParsedCreateTable()
			} else {
				return fmt.Errorf("%w at byte %d", ErrUnsupportedCompoundStatement, offset)
			}
		}
		if state.dollar.Step(c) {
			return fmt.Errorf("%w at byte %d", ErrUnsupportedCompoundStatement, offset)
		}
		if proof, ok := state.target.Step(c, offset); ok {
			if err := verifyTargetPrefixOwnership(state, proof); err != nil {
				return err
			}
		}
		if c == ';' {
			if proof, ok := state.target.UnresolvedData(); ok {
				return unresolvedTargetPrefixError(proof)
			}
			if state.active {
				region := &state.ref.table.Regions[state.ref.index]
				region.EndOffset = offset + 1
				state.active = false
				state.ref = regionRef{}
			}
			state.hasContent = false
			state.compound.Reset()
			state.dollar.Reset()
			state.target.Reset()
			continue
		}
		if !isSQLSpace(c) {
			state.hasContent = true
		}
	}
	for candidateAt < len(candidates) {
		if _, err := activate(candidates[candidateAt]); err != nil {
			return err
		}
		candidateAt++
	}
	return nil
}

type targetPrefixProof struct {
	kind  RegionKind
	start int64
}

// targetPrefixDetector independently tracks the first statement tokens so a
// valid INSERT/REPLACE ... INTO or CREATE ... TABLE prefix cannot disappear
// merely because its keywords are separated by more than analyzerCarrySize.
// It retains only fixed-size token state; the table name still remains subject
// to MaxIdentifierBytes in the precise parser.
type targetPrefixDetector struct {
	stage      uint8 // 0:first token; 1:CREATE-like; 2:INSERT/REPLACE; 3:irrelevant
	token      [16]byte
	tokenLen   uint8
	overflow   bool
	tokenStart int64
	start      int64
	kind       RegionKind
}

func (d *targetPrefixDetector) Step(c byte, offset int64) (targetPrefixProof, bool) {
	if isCompoundWordByte(c) {
		if d.tokenLen == 0 && !d.overflow {
			d.tokenStart = offset
		}
		if int(d.tokenLen) < len(d.token) {
			d.token[d.tokenLen] = asciiLower(c)
			d.tokenLen++
		} else {
			d.overflow = true
		}
		return targetPrefixProof{}, false
	}
	return d.finishToken()
}

func (d *targetPrefixDetector) finishToken() (targetPrefixProof, bool) {
	if d.tokenLen == 0 && !d.overflow {
		return targetPrefixProof{}, false
	}
	token := ""
	if !d.overflow {
		token = string(d.token[:d.tokenLen])
	}
	tokenStart := d.tokenStart
	d.tokenLen = 0
	d.overflow = false

	switch d.stage {
	case 0:
		switch token {
		case "create", "recreate":
			d.stage = 1
			d.start = tokenStart
			d.kind = RegionCreate
		case "insert":
			d.stage = 2
			d.start = tokenStart
			d.kind = RegionInsert
		case "replace":
			d.stage = 2
			d.start = tokenStart
			d.kind = RegionReplace
		default:
			d.stage = 3
		}
	case 1:
		if token == "table" {
			d.stage = 3
			return targetPrefixProof{kind: d.kind, start: d.start}, true
		}
		switch token {
		case "view", "index", "database", "schema", "sequence", "type", "domain",
			"trigger", "procedure", "proc", "function", "event", "rule", "package", "module", "macro":
			d.stage = 3
		}
	case 2:
		if token == "into" {
			d.stage = 3
			return targetPrefixProof{kind: d.kind, start: d.start}, true
		}
	}
	return targetPrefixProof{}, false
}

func (d *targetPrefixDetector) Finish() (targetPrefixProof, bool) { return d.finishToken() }

func (d *targetPrefixDetector) UnresolvedData() (targetPrefixProof, bool) {
	if d.stage != 2 {
		return targetPrefixProof{}, false
	}
	return targetPrefixProof{kind: d.kind, start: d.start}, true
}

func (d *targetPrefixDetector) Reset() { *d = targetPrefixDetector{} }

func verifyTargetPrefixOwnership(state *regionBoundaryState, proof targetPrefixProof) error {
	if state != nil && state.active {
		region := state.ref.table.Regions[state.ref.index]
		if region.Kind == proof.kind && region.StartOffset == proof.start {
			return nil
		}
	}
	return fmt.Errorf("%w: %s prefix at byte %d was not resolved within the %d-byte lookahead",
		ErrUnresolvedStatementPrefix, proof.kind, proof.start, analyzerCarrySize)
}

func unresolvedTargetPrefixError(proof targetPrefixProof) error {
	return fmt.Errorf("%w: unsupported %s prefix at byte %d", ErrUnresolvedStatementPrefix, proof.kind, proof.start)
}

type clientHazard uint8

const (
	clientNoHazard clientHazard = iota
	clientBatchHazard
	clientDelimiterHazard
)

func clientHazardError(hazard clientHazard, offset int64) error {
	switch hazard {
	case clientBatchHazard:
		return fmt.Errorf("%w at byte %d: client-side batch or meta-command", ErrUnsupportedCompoundStatement, offset)
	case clientDelimiterHazard:
		return fmt.Errorf("%w at byte %d: client-side terminator command", ErrUnsupportedDelimiter, offset)
	default:
		return nil
	}
}

type clientLineState uint8

const (
	clientLineLeading clientLineState = iota
	clientLineToken
	clientLineAfterSet
	clientLineSecondToken
	clientLineSlash
	clientLineSlashTrailing
	clientLineBang
	clientLineDot
	clientLineIgnored
)

type clientStatementStarterClass uint8

const (
	clientStatementStarterUndecided clientStatementStarterClass = iota
	clientStatementStarterServerSQL
	clientStatementStarterUnknown
)

// clientCommandDetector consumes the committed, literal/comment-masked byte
// stream. It recognizes only line-oriented commands that can change statement
// ownership outside SQL grammar. Token and line state are fixed-sized, so a
// huge command line or a command split across read windows remains O(1) state.
type clientCommandDetector struct {
	state              clientLineState
	token              [16]byte
	tokenLen           uint8
	overflow           bool
	continuedAmbiguous bool
	continuedStatement bool
	capturingStarter   bool
	statementStarter   clientStatementStarterClass
	ambiguousSameLine  bool
}

func (d *clientCommandDetector) Step(c byte, statementHasContent, compoundPrefixIrrelevant bool) clientHazard {
	if c == '\r' || c == '\n' {
		hazard := d.finishLine()
		d.resetLine()
		return hazard
	}
	// Literal and comment bytes have already been replaced with spaces by the
	// streaming masker, so every remaining semicolon is lexically live.
	if c == ';' {
		return d.finishStatementSegment(statementHasContent)
	}

	switch d.state {
	case clientLineLeading:
		if isHorizontalSQLSpace(c) {
			return clientNoHazard
		}
		switch c {
		case '/':
			d.state = clientLineSlash
		case '\\':
			return clientBatchHazard
		case '@':
			// SQL*Plus @file and @@file execute another script. Inside an
			// already-started multiline statement, line-leading @ can instead be
			// SQL Server variable syntax, so only a new statement is hazardous.
			if !statementHasContent {
				return clientBatchHazard
			}
			d.state = clientLineIgnored
		case '?':
			// mysql's statement-leading ? command is HELP shorthand. Preserve
			// placeholder-like punctuation inside an already-started statement.
			if !statementHasContent {
				return clientBatchHazard
			}
			d.state = clientLineIgnored
		case ':':
			return clientBatchHazard
		case '!':
			d.state = clientLineBang
		case '.':
			d.state = clientLineDot
		default:
			if isClientTokenByte(c) {
				d.state = clientLineToken
				d.continuedAmbiguous = statementHasContent && compoundPrefixIrrelevant
				d.continuedStatement = statementHasContent
				d.capturingStarter = !statementHasContent && d.statementStarter == clientStatementStarterUndecided
				d.appendToken(c)
			} else {
				d.state = clientLineIgnored
			}
		}
	case clientLineToken:
		if isClientTokenByte(c) {
			d.appendToken(c)
			return clientNoHazard
		}
		return d.finishFirstToken(c)
	case clientLineAfterSet:
		if isHorizontalSQLSpace(c) {
			return clientNoHazard
		}
		if isClientTokenByte(c) {
			d.clearToken()
			d.state = clientLineSecondToken
			d.appendToken(c)
		} else {
			d.state = clientLineIgnored
		}
	case clientLineSecondToken:
		if isClientTokenByte(c) {
			d.appendToken(c)
			return clientNoHazard
		}
		if d.tokenString() == "term" && isHorizontalSQLSpace(c) {
			return clientDelimiterHazard
		}
		d.state = clientLineIgnored
	case clientLineSlash:
		if isHorizontalSQLSpace(c) {
			d.state = clientLineSlashTrailing
		} else {
			d.state = clientLineIgnored
		}
	case clientLineSlashTrailing:
		if !isHorizontalSQLSpace(c) {
			d.state = clientLineIgnored
		}
	case clientLineBang:
		if c == '!' || isASCIIAlpha(c) {
			return clientBatchHazard
		}
		d.state = clientLineIgnored
	case clientLineDot:
		if isASCIIAlpha(c) {
			return clientBatchHazard
		}
		d.state = clientLineIgnored
	}
	return clientNoHazard
}

func (d *clientCommandDetector) finishFirstToken(boundary byte) clientHazard {
	token := d.tokenString()
	if d.capturingStarter {
		if isKnownServerStatementStarter(token) {
			d.statementStarter = clientStatementStarterServerSQL
		} else {
			d.statementStarter = clientStatementStarterUnknown
		}
		d.capturingStarter = false
	}
	if isAlwaysClientToken(token) || (!d.continuedStatement && isTopLevelClientToken(token)) {
		return clientBatchHazard
	}
	if token == "set" {
		if isHorizontalSQLSpace(boundary) {
			d.state = clientLineAfterSet
			d.clearToken()
			return clientNoHazard
		}
	}
	if (d.continuedAmbiguous || d.ambiguousSameLine) && isAmbiguousContinuedStarter(token) {
		return clientBatchHazard
	}
	d.state = clientLineIgnored
	return clientNoHazard
}

// isKnownServerStatementStarter is intentionally conservative. Tokens shared
// by server SQL and client command languages (SET, START, USE, CALL, EXECUTE,
// COPY, LOAD, SHOW, and similar commands) stay unknown. If such a physical-line
// segment is followed by a semicolon and a target statement on that same line,
// failing closed is safer than treating client-owned payload as independent SQL.
func isKnownServerStatementStarter(token string) bool {
	switch token {
	case "abort", "alter", "analyze", "attach", "audit", "backup", "begin", "binlog", "bulk", "cache",
		"check", "checkpoint", "checksum", "clone", "close", "cluster", "comment", "commit", "create",
		"dbcc", "deallocate", "declare", "delete", "deny", "detach", "discard", "disassociate", "do",
		"drop", "explain", "export", "fetch", "flashback", "flush", "grant", "handler", "import",
		"insert", "install", "kill", "listen", "lock", "merge", "move", "noaudit", "notify", "optimize",
		"pivot", "pragma", "prepare", "purge", "reassign", "refresh", "reindex", "release", "remove",
		"rename", "repair", "replace", "reset", "restart", "restore", "revoke", "rollback", "savepoint",
		"security", "select", "summarize", "truncate", "uncache", "undrop", "uninstall",
		"unlisten", "unlock", "unpivot", "update", "upsert", "vacuum", "values", "with":
		return true
	default:
		return false
	}
}

func (d *clientCommandDetector) finishStatementSegment(statementHasContent bool) clientHazard {
	var hazard clientHazard
	switch d.state {
	case clientLineToken:
		hazard = d.finishFirstToken(';')
	case clientLineSecondToken:
		if d.tokenString() == "term" {
			hazard = clientDelimiterHazard
		}
	}
	if hazard != clientNoHazard {
		return hazard
	}
	if statementHasContent && d.statementStarter == clientStatementStarterUnknown {
		// Sticky until the physical newline: an unknown client command owns its
		// whole line, so an intervening harmless-looking SQL segment cannot make
		// a later INSERT/CREATE safe.
		d.ambiguousSameLine = true
	}
	d.statementStarter = clientStatementStarterUndecided
	d.resetSegment()
	return clientNoHazard
}

func (d *clientCommandDetector) resetSegment() {
	d.state = clientLineLeading
	d.continuedAmbiguous = false
	d.continuedStatement = false
	d.capturingStarter = false
	d.clearToken()
}

func isAlwaysClientToken(token string) bool {
	switch token {
	case "go", "prompt", "rem", "remark":
		return true
	default:
		return false
	}
}

func isTopLevelClientToken(token string) bool {
	switch token {
	case "spool", "source", "host", "system", "tee", "pager", "input", "output":
		return true
	default:
		return false
	}
}

func (d *clientCommandDetector) finishLine() clientHazard {
	switch d.state {
	case clientLineToken:
		return d.finishFirstToken('\n')
	case clientLineSecondToken:
		if d.tokenString() == "term" {
			return clientDelimiterHazard
		}
	case clientLineSlash, clientLineSlashTrailing:
		return clientBatchHazard
	}
	return clientNoHazard
}

func (d *clientCommandDetector) Finish() clientHazard { return d.finishLine() }

func (d *clientCommandDetector) appendToken(c byte) {
	if int(d.tokenLen) < len(d.token) {
		d.token[d.tokenLen] = asciiLower(c)
		d.tokenLen++
	} else {
		d.overflow = true
	}
}

func (d *clientCommandDetector) tokenString() string {
	if d.overflow {
		return ""
	}
	return string(d.token[:d.tokenLen])
}

func (d *clientCommandDetector) clearToken() {
	d.tokenLen = 0
	d.overflow = false
}

func (d *clientCommandDetector) resetLine() {
	d.resetSegment()
	d.ambiguousSameLine = false
}

func (d *clientCommandDetector) AmbiguousSameLine() bool { return d.ambiguousSameLine }

func isAmbiguousContinuedStarter(token string) bool {
	switch token {
	case "insert", "create", "alter", "recreate", "replace", "begin", "declare", "do", "execute", "copy",
		"if", "loop", "while", "for", "repeat":
		return true
	default:
		return false
	}
}

func isClientTokenByte(c byte) bool {
	return isASCIIWord(c) || c == '$' || c >= 0x80
}

func isHorizontalSQLSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\f' || c == '\v'
}

type compoundPrefixDetector struct {
	stage         uint8 // 0:first; 1:DDL object; 2:after BEGIN; 3:irrelevant; 4:CREATE TYPE; 5:EXECUTE; 6:SET
	token         [16]byte
	tokenLen      uint8
	overflow      bool
	create        bool
	definerHazard bool
}

func (d *compoundPrefixDetector) Step(c byte) bool {
	d.definerHazard = false
	if d.stage == 3 {
		return false
	}
	if isCompoundWordByte(c) {
		if int(d.tokenLen) < len(d.token) {
			d.token[d.tokenLen] = asciiLower(c)
			d.tokenLen++
		} else {
			d.overflow = true
		}
		return false
	}
	return d.finishToken()
}

func (d *compoundPrefixDetector) finishToken() bool {
	if d.tokenLen == 0 && !d.overflow {
		return false
	}
	token := ""
	if !d.overflow {
		token = string(d.token[:d.tokenLen])
	}
	d.tokenLen = 0
	d.overflow = false
	if d.stage == 0 {
		switch token {
		case "create", "recreate":
			d.create = true
			d.stage = 1
		case "alter", "replace":
			d.stage = 1
		case "execute":
			d.stage = 5
		case "set":
			d.stage = 6
		case "begin":
			d.stage = 2
		case "declare", "copy", "do", "if", "loop", "while", "for", "repeat":
			return true
		default:
			d.stage = 3
		}
		return false
	}
	if d.stage == 2 {
		switch token {
		case "transaction", "work", "deferred", "immediate", "exclusive":
			d.stage = 3
			return false
		default:
			return true
		}
	}
	if d.stage == 4 {
		if token == "body" {
			return true
		}
		d.stage = 3
		return false
	}
	if d.stage == 5 {
		if token == "block" {
			return true
		}
		d.stage = 3
		return false
	}
	if d.stage == 6 {
		if token == "term" {
			return true
		}
		d.stage = 3
		return false
	}
	switch token {
	case "definer":
		// An unquoted MySQL account such as user@host can otherwise hit the
		// ordinary CREATE USER stop token before PROCEDURE/TRIGGER/FUNCTION/EVENT
		// appears, hiding the routine body and its internal semicolons.
		d.definerHazard = true
		return true
	case "begin", "trigger", "procedure", "proc", "function", "event", "rule", "package", "module", "macro":
		return true
	case "type":
		if d.create {
			d.stage = 4
		} else {
			d.stage = 3
		}
	case "into", "table", "view", "index", "database", "schema", "sequence",
		"user", "role", "server", "tablespace", "extension", "collation", "domain",
		"operator", "aggregate", "policy", "publication", "subscription":
		// These object kinds cannot own table extraction regions. Stop probing so
		// an object name or expression token cannot be mistaken for a routine.
		d.stage = 3
	}
	return false
}

func (d *compoundPrefixDetector) Reset() {
	*d = compoundPrefixDetector{}
}

func (d *compoundPrefixDetector) Finish() bool {
	d.definerHazard = false
	return d.finishToken()
}

func (d *compoundPrefixDetector) PotentialCompoundPrefix() bool {
	return d.stage == 1 || d.stage == 2 || d.stage == 4 || d.stage == 5 || d.stage == 6
}

func (d *compoundPrefixDetector) Irrelevant() bool { return d.stage == 3 }

func (d *compoundPrefixDetector) DefinerHazard() bool { return d.definerHazard }

func (d *compoundPrefixDetector) AllowParsedCreateTable() {
	d.stage = 3
	d.definerHazard = false
}

type dollarQuoteDetector struct {
	state     uint8 // 0: idle; 1: saw boundary '$'; 2: valid tag body
	prevIdent bool
}

func (d *dollarQuoteDetector) Step(c byte) bool {
	switch d.state {
	case 0:
		if c == '$' && !d.prevIdent {
			d.state = 1
			d.prevIdent = false
		} else {
			d.prevIdent = isDollarIdentByte(c)
		}
	case 1:
		switch {
		case c == '$':
			return true
		case isASCIIAlpha(c) || c == '_' || c >= 0x80:
			d.state = 2
		default:
			d.state = 0
			d.prevIdent = isDollarIdentByte(c)
		}
	case 2:
		switch {
		case c == '$':
			return true
		case isASCIIWord(c) || c >= 0x80:
		default:
			d.state = 0
			d.prevIdent = isDollarIdentByte(c)
		}
	}
	return false
}

func isDollarIdentByte(c byte) bool {
	return isASCIIWord(c) || c == '$' || c >= 0x80
}

func (d *dollarQuoteDetector) Reset() {
	*d = dollarQuoteDetector{}
}

func isASCIIAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isASCIIWord(c byte) bool {
	return isASCIIAlpha(c) || (c >= '0' && c <= '9') || c == '_'
}

func isCompoundWordByte(c byte) bool {
	return isASCIIWord(c) || c == '$' || c >= 0x80
}

func (s *regionBoundaryState) consumeLeadingBOM(c byte, offset int64) bool {
	if s.bomState == 3 || offset > 2 || s.hasContent {
		s.bomState = 3
		return false
	}
	switch s.bomState {
	case 0:
		if offset == 0 && c == 0xef {
			s.bomState = 1
			return true
		}
		s.bomState = 3
		return false
	case 1:
		if offset == 1 && c == 0xbb {
			s.bomState = 2
			return true
		}
	case 2:
		if offset == 2 && c == 0xbf {
			s.bomState = 3
			return true
		}
	}
	// An incomplete/malformed BOM is live content, so a later keyword cannot
	// silently be promoted to statement ownership.
	s.bomState = 3
	s.hasContent = true
	return false
}

func isSQLSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '\f', '\v':
		return true
	default:
		return false
	}
}

func finalizeRegions(ctx context.Context, tables map[tableIdentity]*Table, count int, sourceSize int64) error {
	observed := 0
	for _, table := range tables {
		for _, region := range table.Regions {
			if observed&1023 == 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
			}
			observed++
			if region.StartOffset < 0 || region.EndOffset <= region.StartOffset || region.EndOffset > sourceSize {
				return fmt.Errorf("invalid exact SQL table region [%d,%d) for source size %d", region.StartOffset, region.EndOffset, sourceSize)
			}
		}
		sort.Slice(table.Regions, func(i, j int) bool {
			if table.Regions[i].StartOffset != table.Regions[j].StartOffset {
				return table.Regions[i].StartOffset < table.Regions[j].StartOffset
			}
			return table.Regions[i].Kind < table.Regions[j].Kind
		})
	}
	if observed != count {
		return fmt.Errorf("SQL analyzer region count changed: retained %d, expected %d", observed, count)
	}
	return ctx.Err()
}

func normalizeTableReference(window []byte, ref parsedTableRef) (string, string, error) {
	if ref.tableStart < 0 || ref.tableEnd < ref.tableStart || ref.tableEnd > len(window) {
		return "", "", errors.New("invalid SQL table identifier range")
	}
	name, err := normalizeIdentifier(window[ref.tableStart:ref.tableEnd])
	if err != nil {
		return "", "", err
	}
	database := ""
	if ref.qualified {
		if ref.databaseStart < 0 || ref.databaseEnd < ref.databaseStart || ref.databaseEnd > len(window) {
			return "", "", errors.New("invalid SQL database identifier range")
		}
		database, err = normalizeIdentifier(window[ref.databaseStart:ref.databaseEnd])
		if err != nil {
			return "", "", err
		}
	}
	return database, name, nil
}

func displayTableName(database string, table string) string {
	if database == "" {
		return table
	}
	return quoteDisplayIdentifier(database) + "." + quoteDisplayIdentifier(table)
}

func quoteDisplayIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func normalizeIdentifier(value []byte) (string, error) {
	raw := bytes.TrimSpace(value)
	content := raw
	quote := byte(0)
	if len(raw) >= 2 && (raw[0] == '`' || raw[0] == '"') && raw[len(raw)-1] == raw[0] {
		content = raw[1 : len(raw)-1]
		quote = raw[0]
	}
	if len(content) > MaxIdentifierBytes {
		return "", identifierLimitError(len(content))
	}
	if !utf8.Valid(content) {
		return "", fmt.Errorf("%w: %w", ErrUnsupportedLexicalConstruct, ErrInvalidIdentifierEncoding)
	}
	if quote != 0 {
		return string(bytes.ReplaceAll(content, []byte{quote, quote}, []byte{quote})), nil
	}
	return string(content), nil
}

func normalizeStatsName(value []byte) (string, error) {
	name, err := normalizeIdentifier(value)
	if err != nil {
		return "", err
	}
	return strings.ToLower(name), nil
}

func identifierLimitError(observed int) error {
	return &LimitError{Kind: LimitIdentifierBytes, Limit: MaxIdentifierBytes, Observed: observed}
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
