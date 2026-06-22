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

// ReshapeInsertsFile streams src to dst, reshaping INSERT row layout per opts.
// The source is never modified.
func ReshapeInsertsFile(ctx context.Context, srcPath, dstPath string, opts Options) (Summary, error) {
	if same, err := sameFile(srcPath, dstPath); err != nil {
		return Summary{}, err
	} else if same {
		return Summary{}, errors.New("output path must be different from input path")
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 100
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return Summary{}, err
	}
	defer in.Close()
	out, err := os.OpenFile(dstPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err != nil {
		return Summary{}, err
	}
	bw := bufio.NewWriterSize(out, 1<<20)
	cleanup := true
	defer func() {
		if cleanup {
			_ = out.Close()
			_ = os.Remove(dstPath)
		}
	}()

	sum, err := reshapeStream(ctx, bufio.NewReaderSize(in, 1<<20), bw, opts)
	if err != nil {
		return sum, err
	}
	if err := bw.Flush(); err != nil {
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
	return nil
}

func (b *batcher) add(prefix string, tuples []string) error {
	for _, t := range tuples {
		if b.prefix != "" && (b.prefix != prefix || len(b.tuples) >= b.batchSize) {
			if err := b.flush(); err != nil {
				return err
			}
		}
		b.prefix = prefix
		b.tuples = append(b.tuples, t)
	}
	return nil
}

func reshapeStream(ctx context.Context, r *bufio.Reader, w *bufio.Writer, opts Options) (Summary, error) {
	var sum Summary
	o := &out{w: w, last: '\n'}
	bat := &batcher{o: o, batchSize: opts.BatchSize, sum: &sum}
	var stmt bytes.Buffer
	lex := lexer{}
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
		prefix, tuples, ok := parseInsert(stmt.Bytes())
		if !ok {
			return emitVerbatim(stmt.Bytes())
		}
		sum.RowsSeen += int64(len(tuples))
		switch opts.Mode {
		case ModeMultiRow:
			return bat.add(prefix, tuples)
		default: // ModeSingleRow
			for _, t := range tuples {
				if err := o.ensureNL(); err != nil {
					return err
				}
				if err := o.str(prefix); err != nil {
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
				// Too large to reshape safely — stream it through verbatim.
				if werr := emitVerbatim(stmt.Bytes()); werr != nil {
					return sum, werr
				}
				stmt.Reset()
				overflow = true
			}
		}
		if err == io.EOF {
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
	inSingle  bool
	inDouble  bool
	inBacktick bool
	inLine    bool
	inBlock   bool
	esc       bool
	prevStar  bool
	prevDash  bool
	prevSlash bool
}

func (l *lexer) inLiteral() bool {
	return l.inSingle || l.inDouble || l.inBacktick || l.inLine || l.inBlock
}

func (l *lexer) step(c byte) bool {
	switch {
	case l.inLine:
		if c == '\n' {
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
	// normal state
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
func parseInsert(stmt []byte) (prefix string, tuples []string, ok bool) {
	// Skip leading whitespace AND comments. mysqldump prefixes each table's data
	// with a "-- Dumping data for table `x`" comment block that gets buffered
	// together with the following INSERT (comments have no ';' terminator), so
	// without skipping them the keyword test below would fail and the INSERT —
	// usually the largest one — would be copied through unreshaped.
	rest := stmt[skipLeadingTrivia(stmt):]
	if !hasFold(rest, "insert") {
		return "", nil, false
	}
	// Locate the VALUES keyword at top level.
	vi := findValues(rest)
	if vi < 0 {
		return "", nil, false
	}
	// prefix = "INSERT ... VALUES " with a single normalized trailing space.
	// Leading whitespace from the source is dropped; each rewritten statement is
	// newline-terminated, so rows stay one-per-line.
	pre := string(rest[:vi])
	pre = strings.TrimRight(pre, " \t\r\n")
	prefix = pre + " "

	// Parse tuples from after VALUES.
	body := rest[vi:]
	tuples = splitTuples(body)
	if len(tuples) == 0 {
		return "", nil, false
	}
	return prefix, tuples, true
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' || c == '\v'
}

// skipLeadingTrivia returns the index of the first byte that is not leading
// whitespace, a '--' or '#' line comment, or a '/* */' block comment.
func skipLeadingTrivia(b []byte) int {
	i := 0
	for i < len(b) {
		c := b[i]
		switch {
		case isSpace(c):
			i++
		case c == '#':
			for i < len(b) && b[i] != '\n' {
				i++
			}
		case c == '-' && i+1 < len(b) && b[i+1] == '-':
			i += 2
			for i < len(b) && b[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			if i+1 < len(b) {
				i += 2 // consume the closing */
			} else {
				i = len(b)
			}
		default:
			return i
		}
	}
	return i
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 32
	}
	return c
}

// hasFold reports whether b begins with word (case-insensitive), allowing
// leading whitespace already stripped by the caller.
func hasFold(b []byte, word string) bool {
	if len(b) < len(word) {
		return false
	}
	for j := 0; j < len(word); j++ {
		if lower(b[j]) != word[j] {
			return false
		}
	}
	return true
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
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'
}

// splitTuples returns each top-level "(...)" group from the VALUES body, with a
// trailing ";" stripped. Quote/escape/comment-aware via the lexer.
func splitTuples(b []byte) []string {
	var tuples []string
	lex := lexer{}
	depth := 0
	start := -1
	for i := 0; i < len(b); i++ {
		top := lex.step(b[i])
		if !top || lex.inLiteral() {
			continue
		}
		c := b[i]
		switch c {
		case '(':
			if depth == 0 {
				start = i
			}
			depth++
		case ')':
			if depth > 0 {
				depth--
				if depth == 0 && start >= 0 {
					tuples = append(tuples, string(b[start:i+1]))
					start = -1
				}
			}
		}
	}
	return tuples
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
