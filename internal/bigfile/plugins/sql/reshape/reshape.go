// Package reshape rewrites the row layout of SQL INSERT statements without
// touching the data: an extended (multi-row) INSERT can be exploded into one
// INSERT per row (so a line diff between two dumps is meaningful), or a run of
// single-row INSERTs can be batched back into extended INSERTs (so a dump
// re-imports faster). Everything that is not an INSERT…VALUES statement —
// comments, CREATE TABLE, SET, locks — is copied through byte-for-byte.
package reshape

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	sqldelimiter "novera/internal/bigfile/plugins/sql/delimiter"
	"novera/internal/bigfile/regularfile"
)

// Mode selects the reshape direction.
type Mode string

const (
	// ModeSingleRow explodes extended INSERTs into one row per statement.
	ModeSingleRow Mode = "single"
	// ModeMultiRow batches consecutive same-prefix single-row INSERTs.
	ModeMultiRow Mode = "multi"
)

// Options configures a reshape run.
type Options struct {
	Mode      Mode
	BatchSize int // ModeMultiRow: max rows per output INSERT (default 100)
}

// Summary reports what a reshape produced.
type Summary struct {
	StatementsRead    int64
	InsertsRewritten  int64
	RowsSeen          int64
	StatementsWritten int64
}

// maxStatementBytes caps how much of a single statement we buffer before giving
// up and streaming it through verbatim. A normal mysqldump extended INSERT is
// well under this; the cap only guards against a pathological single statement.
const maxStatementBytes = 64 << 20

const (
	MaxBatchRows  = 10_000
	maxBatchBytes = 8 << 20
)

var (
	// ErrUnsupportedDelimiter is returned for mysql-client DELIMITER scripts,
	// whose statement boundaries cannot be inferred from ordinary semicolons.
	ErrUnsupportedDelimiter = sqldelimiter.ErrUnsupported
	// ErrUnsupportedInsert rejects INSERT shapes whose semantics cannot be
	// preserved by duplicating or coalescing their VALUES tuples.
	ErrUnsupportedInsert = errors.New("INSERT form is not safe to reshape")
	// ErrUnsupportedCompoundStatement prevents routine bodies and client-owned
	// raw-data sections from being mistaken for top-level INSERT statements.
	ErrUnsupportedCompoundStatement = errors.New("compound SQL body or client-managed raw-data payload is not safe to reshape")
	// ErrUnsupportedLexicalConstruct rejects dialect quoting/comment syntax the
	// intentionally small reshape lexer cannot prove safe.
	ErrUnsupportedLexicalConstruct = errors.New("unsupported SQL dialect quoting or comment construct")
)

// ReshapeInsertsFile streams src to dst, reshaping INSERT row layout per opts.
// The source is never modified.
func ReshapeInsertsFile(ctx context.Context, srcPath, dstPath string, opts Options) (Summary, error) {
	opts, err := validateOptions(opts)
	if err != nil {
		return Summary{}, err
	}
	if same, err := sameFile(srcPath, dstPath); err != nil {
		return Summary{}, err
	} else if same {
		return Summary{}, errors.New("output path must be different from input path")
	}
	in, err := regularfile.Open(srcPath)
	if err != nil {
		return Summary{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return Summary{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = out.Close()
			_ = os.Remove(dstPath)
		}
	}()

	sum, err := ReshapeInserts(ctx, in, out, opts)
	if err != nil {
		return sum, err
	}
	if err := out.Sync(); err != nil {
		return sum, err
	}
	if err := out.Close(); err != nil {
		return sum, err
	}
	cleanup = false
	return sum, nil
}

// ReshapeInserts streams SQL from source to destination without owning either
// endpoint. Callers can therefore combine the parser with their own atomic
// publication and exact-source validation.
func ReshapeInserts(ctx context.Context, source io.Reader, destination io.Writer, opts Options) (Summary, error) {
	if source == nil {
		return Summary{}, errors.New("SQL reshape source is required")
	}
	if destination == nil {
		return Summary{}, errors.New("SQL reshape destination is required")
	}
	opts, err := validateOptions(opts)
	if err != nil {
		return Summary{}, err
	}
	reader := bufio.NewReaderSize(source, 1<<20)
	writer := bufio.NewWriterSize(destination, 1<<20)
	summary, err := reshapeStream(ctx, reader, writer, opts)
	if err != nil {
		return summary, err
	}
	if err := writer.Flush(); err != nil {
		return summary, err
	}
	return summary, nil
}

func validateOptions(opts Options) (Options, error) {
	switch opts.Mode {
	case ModeSingleRow, ModeMultiRow:
	default:
		return Options{}, fmt.Errorf("unsupported reshape mode %q", opts.Mode)
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 100
	}
	if opts.BatchSize > MaxBatchRows {
		return Options{}, fmt.Errorf("reshape batch size %d exceeds limit %d", opts.BatchSize, MaxBatchRows)
	}
	return opts, nil
}

// out is a write sink that remembers the last byte written, so a reshaped
// statement can guarantee it starts on a fresh line (otherwise a verbatim
// statement ending in ';' would run into the next reshaped INSERT).
type out struct {
	w    *bufio.Writer
	last byte
}

func (o *out) str(s string) error {
	if len(s) == 0 {
		return nil
	}
	if _, err := o.w.WriteString(s); err != nil {
		return err
	}
	o.last = s[len(s)-1]
	return nil
}

func (o *out) bytes(p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if _, err := o.w.Write(p); err != nil {
		return err
	}
	o.last = p[len(p)-1]
	return nil
}

func (o *out) ensureNL() error {
	if o.last != '\n' {
		return o.str("\n")
	}
	return nil
}

// batcher coalesces single-row INSERTs that share a prefix into extended ones.
type batcher struct {
	o         *out
	batchSize int
	prefix    string
	tuples    []string
	bytes     int
	sum       *Summary
}

func (b *batcher) flush() error {
	if len(b.tuples) == 0 {
		return nil
	}
	if err := b.o.ensureNL(); err != nil {
		return err
	}
	if err := b.o.str(b.prefix); err != nil {
		return err
	}
	if err := b.o.str(strings.Join(b.tuples, ",")); err != nil {
		return err
	}
	if err := b.o.str(";\n"); err != nil {
		return err
	}
	b.sum.StatementsWritten++
	b.sum.InsertsRewritten++
	b.tuples = b.tuples[:0]
	b.prefix = ""
	b.bytes = 0
	return nil
}

func (b *batcher) add(prefix string, tuples []string) error {
	for _, t := range tuples {
		if len(prefix)+len(t) > maxBatchBytes {
			return fmt.Errorf("one reshaped INSERT row exceeds %d-byte batch memory limit", maxBatchBytes)
		}
		needed := len(t)
		if b.prefix == "" {
			needed += len(prefix)
		} else {
			needed++ // tuple separator
		}
		if b.prefix != "" && (b.prefix != prefix || len(b.tuples) >= b.batchSize || b.bytes+needed > maxBatchBytes) {
			if err := b.flush(); err != nil {
				return err
			}
			needed = len(prefix) + len(t)
		}
		b.prefix = prefix
		b.tuples = append(b.tuples, t)
		b.bytes += needed
	}
	return nil
}

func reshapeStream(ctx context.Context, r *bufio.Reader, w *bufio.Writer, opts Options) (Summary, error) {
	var sum Summary
	o := &out{w: w, last: '\n'}
	bat := &batcher{o: o, batchSize: opts.BatchSize, sum: &sum}
	var stmt bytes.Buffer
	lex := lexer{}
	safety := reshapeSafetyGuard{}
	overflow := false // current statement exceeded the buffer cap; pass through

	emitVerbatim := func(p []byte) error {
		if opts.Mode == ModeMultiRow {
			if err := bat.flush(); err != nil {
				return err
			}
		}
		return o.bytes(p)
	}

	flushStatement := func() error {
		defer stmt.Reset()
		if stmt.Len() == 0 {
			return nil
		}
		sum.StatementsRead++
		parsed, isInsert, err := parseInsert(stmt.Bytes())
		if err != nil {
			return fmt.Errorf("statement %d: %w", sum.StatementsRead, err)
		}
		if !isInsert {
			return emitVerbatim(stmt.Bytes())
		}
		sum.RowsSeen += int64(len(parsed.tuples))
		switch opts.Mode {
		case ModeMultiRow:
			if len(bytes.TrimSpace(parsed.leading)) > 0 {
				if err := bat.flush(); err != nil {
					return err
				}
				if err := o.bytes(parsed.leading); err != nil {
					return err
				}
			}
			return bat.add(parsed.prefix, parsed.tuples)
		default: // ModeSingleRow
			if err := o.bytes(parsed.leading); err != nil {
				return err
			}
			for _, t := range parsed.tuples {
				if err := o.ensureNL(); err != nil {
					return err
				}
				if err := o.str(parsed.prefix); err != nil {
					return err
				}
				if err := o.str(t); err != nil {
					return err
				}
				if err := o.str(";\n"); err != nil {
					return err
				}
				sum.StatementsWritten++
			}
			sum.InsertsRewritten++
			return nil
		}
	}

	buf := make([]byte, 256*1024)
	for {
		if err := ctxErr(ctx); err != nil {
			return sum, err
		}
		n, err := r.Read(buf)
		for i := 0; i < n; i++ {
			c := buf[i]
			if guardErr := safety.Step(c); guardErr != nil {
				return sum, guardErr
			}
			atTop := lex.step(c)
			if overflow {
				if err := o.bytes(buf[i : i+1]); err != nil {
					return sum, err
				}
				if atTop && c == ';' {
					overflow = false
					sum.StatementsRead++
				}
				continue
			}
			stmt.WriteByte(c)
			if atTop && c == ';' {
				if ferr := flushStatement(); ferr != nil {
					return sum, ferr
				}
			} else if stmt.Len() >= maxStatementBytes {
				// A target INSERT that exceeds the parser budget must fail;
				// silently copying it would produce a partially reshaped dump.
				switch classifyStatementPrefix(stmt.Bytes()) {
				case statementPrefixInsert:
					return sum, fmt.Errorf("%w: statement exceeds %d-byte parser limit", ErrUnsupportedInsert, maxStatementBytes)
				case statementPrefixUnknown:
					return sum, fmt.Errorf("%w: statement prefix is unresolved at the %d-byte parser limit", ErrUnsupportedInsert, maxStatementBytes)
				}
				if werr := emitVerbatim(stmt.Bytes()); werr != nil {
					return sum, werr
				}
				stmt.Reset()
				overflow = true
			}
		}
		if err == io.EOF {
			if guardErr := safety.Finish(); guardErr != nil {
				return sum, guardErr
			}
			break
		}
		if err != nil {
			return sum, err
		}
	}
	// Trailing bytes with no terminating ';'.
	if stmt.Len() > 0 {
		if ferr := flushStatement(); ferr != nil {
			return sum, ferr
		}
	}
	if opts.Mode == ModeMultiRow {
		if err := bat.flush(); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// lexer tracks SQL string/comment/identifier state so statement and tuple
// boundaries are only recognized at the top level. step returns true when, after
// consuming c, the lexer is back at normal (non-string, non-comment) state — i.e.
// c was a structural byte that counts toward statement/tuple parsing.
type lexer struct {
	inSingle   bool
	inDouble   bool
	inBacktick bool
	inLine     bool
	inBlock    bool
	esc        bool
	prevStar   bool
	prevDash   bool
	prevSlash  bool
}

func (l *lexer) inLiteral() bool {
	return l.inSingle || l.inDouble || l.inBacktick || l.inLine || l.inBlock
}

func (l *lexer) step(c byte) bool {
	switch {
	case l.inLine:
		if c == '\n' || c == '\r' {
			l.inLine = false
		}
		return false
	case l.inBlock:
		if c == '/' && l.prevStar {
			l.inBlock = false
			l.prevStar = false
			return false
		}
		l.prevStar = c == '*'
		return false
	case l.inSingle:
		if l.esc {
			l.esc = false
		} else if c == '\\' {
			l.esc = true
		} else if c == '\'' {
			l.inSingle = false
		}
		return false
	case l.inDouble:
		if l.esc {
			l.esc = false
		} else if c == '\\' {
			l.esc = true
		} else if c == '"' {
			l.inDouble = false
		}
		return false
	case l.inBacktick:
		if c == '`' {
			l.inBacktick = false
		}
		return false
	}
	// Normal state. Treat every "--" pair as a line comment. Some dialects
	// require following whitespace, but accepting the pair as operators would
	// let a semicolon inside a valid SQL comment become a false statement
	// boundary. The safety guard rejects ambiguous constructs before output is
	// published, so conservative tokenisation is the safe choice here.
	l.prevStar = false
	switch c {
	case '\'':
		l.inSingle = true
		l.prevDash, l.prevSlash = false, false
		return false
	case '"':
		l.inDouble = true
		l.prevDash, l.prevSlash = false, false
		return false
	case '`':
		l.inBacktick = true
		l.prevDash, l.prevSlash = false, false
		return false
	case '#':
		l.inLine = true
		l.prevDash, l.prevSlash = false, false
		return false
	case '-':
		if l.prevDash {
			l.inLine = true
			l.prevDash = false
			return false
		}
		l.prevDash = true
		l.prevSlash = false
		return true
	case '*':
		if l.prevSlash {
			l.inBlock = true
			l.prevSlash = false
			l.prevDash = false
			return false
		}
		l.prevDash, l.prevSlash = false, false
		return true
	case '/':
		l.prevSlash = true
		l.prevDash = false
		return true
	default:
		l.prevDash, l.prevSlash = false, false
		return true
	}
}

func ctxErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func sameFile(a, b string) (bool, error) {
	if a == b {
		return true, nil
	}
	ai, err := os.Stat(a)
	if err != nil {
		return false, nil // a must exist; let the open fail later if not
	}
	bi, err := os.Stat(b)
	if err != nil {
		return false, nil
	}
	return os.SameFile(ai, bi), nil
}

// parseInsert splits a single SQL statement into the INSERT prefix (everything
// up to and including "VALUES ") and the list of value tuples ("(...)"). It
// returns ok=false for anything that is not an INSERT…VALUES statement, which
// the caller then copies through unchanged.
type parsedInsert struct {
	leading []byte
	prefix  string
	tuples  []string
}

func parseInsert(stmt []byte) (parsed parsedInsert, isInsert bool, err error) {
	// mysqldump provenance comments are buffered with the following INSERT
	// because they have no semicolon. Preserve that leading trivia exactly once.
	triviaEnd := skipLeadingTrivia(stmt)
	rest := stmt[triviaEnd:]
	if !matchKeyword(rest, 0, "insert") {
		return parsedInsert{}, false, nil
	}
	if containsExecutableSQLComment(stmt) {
		return parsedInsert{}, true, fmt.Errorf("%w: executable or optimizer comment in target INSERT", ErrUnsupportedInsert)
	}
	vi := findValues(rest)
	if vi < 0 {
		return parsedInsert{}, true, fmt.Errorf("%w: expected a top-level VALUES tuple list", ErrUnsupportedInsert)
	}
	if containsTopLevelKeyword(rest[:vi], "overwrite") {
		return parsedInsert{}, true, fmt.Errorf("%w: INSERT OVERWRITE is not row-additive", ErrUnsupportedInsert)
	}
	pre := string(rest[:vi])
	pre = strings.TrimRight(pre, " \t\r\n")
	parsed.prefix = pre + " "
	parsed.leading = append([]byte(nil), stmt[:triviaEnd]...)
	parsed.tuples, err = splitTuplesStrict(rest[vi:])
	if err != nil {
		return parsedInsert{}, true, fmt.Errorf("%w: %v", ErrUnsupportedInsert, err)
	}
	return parsed, true, nil
}

func containsExecutableSQLComment(statement []byte) bool {
	lex := lexer{}
	for index := 0; index+2 < len(statement); index++ {
		if !lex.inLiteral() && statement[index] == '/' && statement[index+1] == '*' {
			marker := statement[index+2]
			if marker == '!' || marker == '+' ||
				((marker == 'm' || marker == 'M') && index+3 < len(statement) && statement[index+3] == '!') {
				return true
			}
		}
		lex.step(statement[index])
	}
	return false
}

func containsTopLevelKeyword(statement []byte, keyword string) bool {
	lex := lexer{}
	for index := 0; index < len(statement); index++ {
		top := lex.step(statement[index])
		if top && !lex.inLiteral() && matchKeyword(statement, index, keyword) {
			return true
		}
	}
	return false
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v'
}

// scanLeadingTrivia returns the first live byte. resolved is false when a
// bounded prefix ends before a first live token is known.
func scanLeadingTrivia(b []byte) (index int, resolved bool) {
	i := 0
	if len(b) < 3 && len(b) > 0 && b[0] == 0xef {
		if len(b) == 1 || b[1] == 0xbb {
			return len(b), false
		}
	}
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		i = 3
	}
	for i < len(b) {
		c := b[i]
		switch {
		case isSpace(c):
			i++
		case c == '#':
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
			if i == len(b) {
				return i, false
			}
		case c == '-' && i+1 < len(b) && b[i+1] == '-':
			i += 2
			for i < len(b) && b[i] != '\n' && b[i] != '\r' {
				i++
			}
			if i == len(b) {
				return i, false
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			if i+1 < len(b) {
				i += 2 // consume the closing */
			} else {
				return len(b), false
			}
		case (c == '-' || c == '/') && i+1 == len(b):
			return i, false
		default:
			return i, true
		}
	}
	return i, false
}

func skipLeadingTrivia(b []byte) int {
	index, _ := scanLeadingTrivia(b)
	return index
}

type statementPrefixClass uint8

const (
	statementPrefixUnknown statementPrefixClass = iota
	statementPrefixInsert
	statementPrefixOther
)

func classifyStatementPrefix(statement []byte) statementPrefixClass {
	start, resolved := scanLeadingTrivia(statement)
	if !resolved {
		return statementPrefixUnknown
	}
	rest := statement[start:]
	const keyword = "insert"
	limit := len(rest)
	if limit > len(keyword) {
		limit = len(keyword)
	}
	for offset := 0; offset < limit; offset++ {
		if lower(rest[offset]) != keyword[offset] {
			return statementPrefixOther
		}
	}
	if len(rest) < len(keyword) {
		return statementPrefixUnknown
	}
	if len(rest) == len(keyword) || !isWordByte(rest[len(keyword)]) {
		return statementPrefixInsert
	}
	return statementPrefixOther
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// findValues returns the index just AFTER the top-level VALUES (or VALUE)
// keyword in an INSERT statement, or -1. It uses a lexer so a column named
// `values` (backtick-quoted) or the word inside a string isn't matched.
func findValues(b []byte) int {
	lex := lexer{}
	for i := 0; i < len(b); i++ {
		top := lex.step(b[i])
		if !top || lex.inLiteral() {
			continue
		}
		// match VALUES at a word boundary
		if matchKeyword(b, i, "values") {
			return i + len("values")
		}
		if matchKeyword(b, i, "value") {
			return i + len("value")
		}
	}
	return -1
}

// matchKeyword reports whether b[i:] begins with word (case-insensitive) at a
// word boundary on both sides.
func matchKeyword(b []byte, i int, word string) bool {
	if i+len(word) > len(b) {
		return false
	}
	if i > 0 && isWordByte(b[i-1]) {
		return false
	}
	for j := 0; j < len(word); j++ {
		if lower(b[i+j]) != word[j] {
			return false
		}
	}
	after := i + len(word)
	if after < len(b) && isWordByte(b[after]) {
		return false
	}
	return true
}

func isWordByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
		c == '_' || c == '$' || c >= 0x80
}

// splitTuplesStrict accepts only whitespace-separated tuples joined by commas,
// followed by a semicolon and optional whitespace. Every trailing clause is
// rejected because duplicating it per tuple is not proven semantics-preserving.
func splitTuplesStrict(b []byte) ([]string, error) {
	skipSpace := func(i int) int {
		for i < len(b) && isSpace(b[i]) {
			i++
		}
		return i
	}

	var tuples []string
	i := skipSpace(0)
	for {
		if i >= len(b) || b[i] != '(' {
			return nil, fmt.Errorf("expected value tuple at byte %d", i)
		}
		start := i
		depth := 0
		lex := lexer{}
		closed := false
		for i < len(b) {
			top := lex.step(b[i])
			if top && !lex.inLiteral() {
				switch b[i] {
				case '(':
					depth++
				case ')':
					if depth == 0 {
						return nil, fmt.Errorf("unexpected closing parenthesis at byte %d", i)
					}
					depth--
					if depth == 0 {
						i++
						tuples = append(tuples, string(b[start:i]))
						closed = true
					}
				}
			}
			if closed {
				break
			}
			i++
		}
		if !closed || lex.inLiteral() {
			return nil, errors.New("unterminated value tuple")
		}

		i = skipSpace(i)
		if i >= len(b) {
			return nil, errors.New("missing statement terminator")
		}
		switch b[i] {
		case ',':
			i = skipSpace(i + 1)
			continue
		case ';':
			i = skipSpace(i + 1)
			if i != len(b) {
				return nil, fmt.Errorf("unsupported trailing tokens at byte %d", i)
			}
			return tuples, nil
		default:
			return nil, fmt.Errorf("unsupported trailing clause or malformed tuple separator at byte %d", i)
		}
	}
}

// FormatNote returns a short human description of a reshape result.
func FormatNote(mode Mode, sum Summary) string {
	switch mode {
	case ModeMultiRow:
		return fmt.Sprintf("%d rows batched into %d INSERTs", sum.RowsSeen, sum.InsertsRewritten)
	default:
		return fmt.Sprintf("%d extended INSERTs → %d single-row INSERTs", sum.InsertsRewritten, sum.StatementsWritten)
	}
}
