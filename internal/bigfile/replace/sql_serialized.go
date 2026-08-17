package replace

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"novera/internal/bigfile/asciifold"
)

const (
	// SQL replacement is intentionally bounded per decoded literal. The whole
	// dump remains streaming, but one SQL value must be retained long enough to
	// validate and, when necessary, rebuild its PHP serialization structure.
	MaxSQLSerializedLiteralBytes       = 32 * 1024 * 1024
	MaxSQLSerializedOutputLiteralBytes = 64 * 1024 * 1024
	MaxSQLReplacePatternBytes          = 1024 * 1024
	MaxSQLReplaceReplacementBytes      = 16 * 1024 * 1024

	maxPHPSerializedDepth = 128
	maxPHPSerializedNodes = 1_000_000
	maxSQLLexerNesting    = 64
)

var (
	ErrUnsupportedPHPSerializedData = errors.New("replacement stopped: unsupported or malformed PHP/WordPress serialized data; no output was published")
	ErrUnsupportedSQLReplaceContext = errors.New("replacement stopped: unsupported SQL dump context detected; comments and identifiers are preserved, while routines, custom delimiters, opaque literals, and ambiguous escape modes are not rewritten; no output was published")
	ErrSQLReplaceResourceLimit      = errors.New("SQL replacement resource limit exceeded")
)

type sqlLexState uint8

const (
	sqlLexNormal sqlLexState = iota
	sqlLexLineComment
	sqlLexBlockComment
	sqlLexBacktickIdentifier
	sqlLexDoubleQuotedIdentifier
	sqlLexBracketIdentifier
)

// ReplaceSQLPlain streams a SQL dump from src to dst and applies plain
// replacement only to decoded single-quoted SQL values. It preserves the exact
// source spelling of every untouched literal, comment, and identifier.
//
// Native PHP/WordPress serialized values are parsed structurally and their
// byte lengths are recalculated. Ambiguous SQL dialect constructs fail closed;
// callers must publish dst atomically only after this function succeeds.
func ReplaceSQLPlain(ctx context.Context, src io.Reader, dst io.Writer, total int64, find []byte, replacement []byte, opts BatchOptions) (int64, error) {
	if src == nil || dst == nil {
		return 0, errors.New("SQL replacement source and destination are required")
	}
	if len(find) == 0 {
		return 0, errors.New("empty pattern")
	}
	if len(find) > MaxSQLReplacePatternBytes {
		return 0, fmt.Errorf("%w: pattern has %d bytes, maximum is %d", ErrSQLReplaceResourceLimit, len(find), MaxSQLReplacePatternBytes)
	}
	if len(replacement) > MaxSQLReplaceReplacementBytes {
		return 0, fmt.Errorf("%w: replacement has %d bytes, maximum is %d", ErrSQLReplaceResourceLimit, len(replacement), MaxSQLReplaceReplacementBytes)
	}
	// Without an explicit dialect, emitting backslash/control escapes is
	// ambiguous between MySQL and standard-conforming SQL. Quotes are safe
	// because the output always uses the portable doubled-quote spelling.
	if bytes.IndexByte(replacement, '\\') >= 0 ||
		bytes.IndexByte(replacement, 0) >= 0 ||
		bytes.IndexByte(replacement, 26) >= 0 {
		return 0, fmt.Errorf("%w: replacement contains dialect-dependent escape bytes", ErrUnsupportedSQLReplaceContext)
	}
	if total < 0 {
		return 0, errors.New("negative SQL source size")
	}
	if opts.ChunkSize < 0 || opts.ChunkSize > 64*1024*1024 {
		return 0, fmt.Errorf("%w: invalid reader buffer size %d", ErrSQLReplaceResourceLimit, opts.ChunkSize)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	readerSize := opts.ChunkSize
	if readerSize == 0 {
		readerSize = 64 * 1024
	}
	if readerSize < 4*1024 {
		readerSize = 4 * 1024
	}
	reader := bufio.NewReaderSize(src, readerSize)
	writer := bufio.NewWriterSize(dst, plainReplaceWriteBufferSize)
	detector := sqlReplaceContextDetector{}
	code := make([]byte, 0, 64*1024)
	history := make([]byte, 0, 128)
	state := sqlLexNormal
	blockDepth := 0
	var processed int64
	var matches int64
	var nextProgress int64 = 4 * 1024 * 1024

	appendHistory := func(p []byte) {
		const maxHistory = 128
		if len(p) >= maxHistory {
			history = append(history[:0], p[len(p)-maxHistory:]...)
			return
		}
		if len(history)+len(p) > maxHistory {
			drop := len(history) + len(p) - maxHistory
			copy(history, history[drop:])
			history = history[:len(history)-drop]
		}
		history = append(history, p...)
	}
	flushCode := func() error {
		if len(code) == 0 {
			return nil
		}
		if detector.Inspect(code) {
			return ErrUnsupportedSQLReplaceContext
		}
		code = code[:0]
		return nil
	}
	appendCode := func(p ...byte) error {
		code = append(code, p...)
		appendHistory(p)
		if len(code) >= 64*1024 {
			return flushCode()
		}
		return nil
	}
	writeByte := func(b byte) error {
		return writer.WriteByte(b)
	}
	reportProgress := func(force bool) {
		if opts.Progress == nil {
			return
		}
		if force || processed >= nextProgress {
			opts.Progress(Progress{BytesProcessed: processed, BytesTotal: total, Matches: matches})
			for nextProgress <= processed {
				nextProgress += 4 * 1024 * 1024
			}
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return matches, err
		}
		b, readErr := reader.ReadByte()
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return matches, readErr
			}
			switch state {
			case sqlLexBlockComment, sqlLexBacktickIdentifier, sqlLexDoubleQuotedIdentifier, sqlLexBracketIdentifier:
				return matches, ErrUnsupportedSQLReplaceContext
			}
			if err := flushCode(); err != nil {
				return matches, err
			}
			if total != processed {
				return matches, fmt.Errorf("SQL source size changed while reading: processed %d of %d bytes", processed, total)
			}
			reportProgress(true)
			if err := writer.Flush(); err != nil {
				return matches, err
			}
			return matches, nil
		}
		processed++

		switch state {
		case sqlLexLineComment:
			if err := writeByte(b); err != nil {
				return matches, err
			}
			if b == '\n' || b == '\r' {
				state = sqlLexNormal
				if err := appendCode('\n'); err != nil {
					return matches, err
				}
			}

		case sqlLexBlockComment:
			if err := writeByte(b); err != nil {
				return matches, err
			}
			if b == '/' {
				if next, peekErr := reader.Peek(1); peekErr == nil && next[0] == '*' {
					nested, _ := reader.ReadByte()
					processed++
					if err := writeByte(nested); err != nil {
						return matches, err
					}
					blockDepth++
					if blockDepth > maxSQLLexerNesting {
						return matches, ErrUnsupportedSQLReplaceContext
					}
				}
			} else if b == '*' {
				if next, peekErr := reader.Peek(1); peekErr == nil && next[0] == '/' {
					end, _ := reader.ReadByte()
					processed++
					if err := writeByte(end); err != nil {
						return matches, err
					}
					blockDepth--
					if blockDepth == 0 {
						state = sqlLexNormal
						if err := appendCode(' '); err != nil {
							return matches, err
						}
					}
				}
			}

		case sqlLexBacktickIdentifier, sqlLexDoubleQuotedIdentifier, sqlLexBracketIdentifier:
			if err := writeByte(b); err != nil {
				return matches, err
			}
			closeByte := byte('`')
			if state == sqlLexDoubleQuotedIdentifier {
				closeByte = '"'
			} else if state == sqlLexBracketIdentifier {
				closeByte = ']'
			}
			if b == '\\' && state != sqlLexBracketIdentifier {
				if escaped, escapeErr := reader.ReadByte(); escapeErr == nil {
					processed++
					if err := writeByte(escaped); err != nil {
						return matches, err
					}
				} else if !errors.Is(escapeErr, io.EOF) {
					return matches, escapeErr
				}
				break
			}
			if b == closeByte {
				if next, peekErr := reader.Peek(1); peekErr == nil && next[0] == closeByte {
					doubled, _ := reader.ReadByte()
					processed++
					if err := writeByte(doubled); err != nil {
						return matches, err
					}
					break
				}
				state = sqlLexNormal
				if err := appendCode(' '); err != nil {
					return matches, err
				}
			}

		default:
			switch b {
			case '-':
				if next, peekErr := reader.Peek(1); peekErr == nil && next[0] == '-' {
					if err := writeByte(b); err != nil {
						return matches, err
					}
					second, _ := reader.ReadByte()
					processed++
					if err := writeByte(second); err != nil {
						return matches, err
					}
					if err := appendCode(' '); err != nil {
						return matches, err
					}
					state = sqlLexLineComment
					break
				}
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(b); err != nil {
					return matches, err
				}

			case '#':
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(' '); err != nil {
					return matches, err
				}
				state = sqlLexLineComment

			case '/':
				if next, peekErr := reader.Peek(1); peekErr == nil && next[0] == '*' {
					if err := writeByte(b); err != nil {
						return matches, err
					}
					second, _ := reader.ReadByte()
					processed++
					if err := writeByte(second); err != nil {
						return matches, err
					}
					if err := appendCode(' '); err != nil {
						return matches, err
					}
					state = sqlLexBlockComment
					blockDepth = 1
					break
				}
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(b); err != nil {
					return matches, err
				}

			case '`':
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(' '); err != nil {
					return matches, err
				}
				state = sqlLexBacktickIdentifier

			case '"':
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(' '); err != nil {
					return matches, err
				}
				state = sqlLexDoubleQuotedIdentifier

			case '[':
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(' '); err != nil {
					return matches, err
				}
				state = sqlLexBracketIdentifier

			case '$':
				if beginsSQLDollarQuote(reader) {
					return matches, ErrUnsupportedSQLReplaceContext
				}
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(b); err != nil {
					return matches, err
				}

			case '\'':
				if err := flushCode(); err != nil {
					return matches, err
				}
				prefix := classifySQLLiteralPrefix(history)
				decoded, raw, consumed, hasBackslash, literalErr := readSQLSingleQuotedLiteral(reader)
				processed += consumed
				if literalErr != nil {
					return matches, literalErr
				}
				if bytes.Contains(bytes.ToUpper(decoded), []byte("NO_BACKSLASH_ESCAPES")) {
					return matches, ErrUnsupportedSQLReplaceContext
				}
				if prefix == sqlLiteralUnsupported {
					return matches, ErrUnsupportedSQLReplaceContext
				}
				if prefix == sqlLiteralOpaque {
					if _, err := writer.Write(raw); err != nil {
						return matches, err
					}
					if err := appendCode(' '); err != nil {
						return matches, err
					}
					break
				}
				next, count, replaceErr := replaceSQLLiteralDecoded(decoded, find, replacement, opts)
				if replaceErr != nil {
					return matches, replaceErr
				}
				if count == 0 {
					if _, err := writer.Write(raw); err != nil {
						return matches, err
					}
				} else {
					if hasBackslash {
						return matches, ErrUnsupportedSQLReplaceContext
					}
					encoded, encodeErr := appendSQLSingleQuotedLiteral(nil, next)
					if encodeErr != nil {
						return matches, encodeErr
					}
					if _, err := writer.Write(encoded); err != nil {
						return matches, err
					}
					matches += int64(count)
				}
				if err := appendCode(' '); err != nil {
					return matches, err
				}
				reportProgress(false)

			default:
				if err := writeByte(b); err != nil {
					return matches, err
				}
				if err := appendCode(b); err != nil {
					return matches, err
				}
			}
		}
		reportProgress(false)
	}
}

type sqlLiteralPrefix uint8

const (
	sqlLiteralText sqlLiteralPrefix = iota
	sqlLiteralOpaque
	sqlLiteralUnsupported
)

func classifySQLLiteralPrefix(history []byte) sqlLiteralPrefix {
	if len(history) == 0 || isSQLSpace(history[len(history)-1]) {
		return sqlLiteralText
	}
	end := len(history)
	start := end
	for start > 0 && isSQLIdentifierByte(history[start-1]) {
		start--
	}
	if start == end {
		return sqlLiteralText
	}
	token := strings.ToUpper(string(history[start:end]))
	switch token {
	case "X", "B":
		return sqlLiteralOpaque
	case "E":
		return sqlLiteralUnsupported
	case "N":
		return sqlLiteralText
	}
	if strings.HasPrefix(token, "_") {
		return sqlLiteralText
	}
	return sqlLiteralUnsupported
}

func beginsSQLDollarQuote(reader *bufio.Reader) bool {
	const maxTagBytes = 64
	peek, err := reader.Peek(maxTagBytes)
	if err != nil && len(peek) == 0 {
		return false
	}
	for index, b := range peek {
		if b == '$' {
			return index == 0 || isSQLDollarTagStart(peek[0])
		}
		if !isSQLIdentifierByte(b) {
			return false
		}
	}
	return false
}

func isSQLDollarTagStart(b byte) bool {
	return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func isSQLIdentifierByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func isSQLSpace(b byte) bool {
	switch b {
	case ' ', '\t', '\r', '\n', '\f':
		return true
	default:
		return false
	}
}

// readSQLSingleQuotedLiteral is called after the opening quote has already
// been consumed. raw includes both delimiters exactly as read.
func readSQLSingleQuotedLiteral(reader *bufio.Reader) (decoded []byte, raw []byte, consumed int64, hasBackslash bool, err error) {
	raw = append(raw, '\'')
	for {
		b, readErr := reader.ReadByte()
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil, nil, consumed, hasBackslash, ErrUnsupportedSQLReplaceContext
			}
			return nil, nil, consumed, hasBackslash, readErr
		}
		consumed++
		raw = append(raw, b)
		if b == '\'' {
			next, peekErr := reader.Peek(1)
			if peekErr == nil && next[0] == '\'' {
				doubled, _ := reader.ReadByte()
				consumed++
				raw = append(raw, doubled)
				decoded = append(decoded, '\'')
				continue
			}
			return decoded, raw, consumed, hasBackslash, nil
		}
		if b == '\\' {
			hasBackslash = true
			escaped, escapeErr := reader.ReadByte()
			if escapeErr != nil {
				return nil, nil, consumed, hasBackslash, ErrUnsupportedSQLReplaceContext
			}
			consumed++
			raw = append(raw, escaped)
			decoded = append(decoded, decodeSQLBackslashEscape(escaped))
		} else {
			decoded = append(decoded, b)
		}
		if len(decoded) > MaxSQLSerializedLiteralBytes || len(raw) > 2*MaxSQLSerializedLiteralBytes+2 {
			return nil, nil, consumed, hasBackslash, fmt.Errorf("%w: SQL string literal exceeds %d decoded bytes", ErrSQLReplaceResourceLimit, MaxSQLSerializedLiteralBytes)
		}
	}
}

func decodeSQLBackslashEscape(b byte) byte {
	switch b {
	case '0':
		return 0
	case 'b':
		return '\b'
	case 'n':
		return '\n'
	case 'r':
		return '\r'
	case 't':
		return '\t'
	case 'Z':
		return 26
	default:
		return b
	}
}

func appendSQLSingleQuotedLiteral(dst []byte, value []byte) ([]byte, error) {
	if len(value) > MaxSQLSerializedOutputLiteralBytes {
		return nil, fmt.Errorf("%w: rewritten SQL literal exceeds %d bytes", ErrSQLReplaceResourceLimit, MaxSQLSerializedOutputLiteralBytes)
	}
	dst = append(dst, '\'')
	for _, b := range value {
		switch b {
		case '\'':
			dst = append(dst, '\'', '\'')
		case '\\', 0, 26:
			return nil, ErrUnsupportedSQLReplaceContext
		default:
			dst = append(dst, b)
		}
	}
	dst = append(dst, '\'')
	return dst, nil
}

func replaceSQLLiteralDecoded(value []byte, find []byte, replacement []byte, opts BatchOptions) ([]byte, int, error) {
	if !hasSQLPlainMatch(value, find, opts) {
		return value, 0, nil
	}
	if !containsPHPSerialization(value) {
		if encodedPHPSerializationHazard(value, find, opts) {
			return nil, 0, ErrUnsupportedPHPSerializedData
		}
		return replaceBytesBounded(value, find, replacement, opts)
	}
	parser := phpSerializedRewriter{
		input:           value,
		find:            find,
		replacement:     replacement,
		caseInsensitive: opts.CaseInsensitive,
		wholeWord:       opts.WholeWord,
	}
	out, err := parser.parseValue(0)
	if err != nil {
		return nil, 0, err
	}
	if parser.pos != len(value) {
		return nil, 0, ErrUnsupportedPHPSerializedData
	}
	return out, parser.matches, nil
}

func hasSQLPlainMatch(value []byte, find []byte, opts BatchOptions) bool {
	needle := find
	if opts.CaseInsensitive {
		needle = asciifold.Fold(find)
	}
	searchPos := 0
	for searchPos < len(value) {
		index := indexPlain(value[searchPos:], needle, opts.CaseInsensitive)
		if index < 0 {
			return false
		}
		start := searchPos + index
		if replaceWordBoundaryOK(value, start, len(find), 0, int64(len(value)), opts.WholeWord) {
			return true
		}
		searchPos = start + 1
	}
	return false
}

func replaceBytesBounded(value []byte, find []byte, replacement []byte, opts BatchOptions) ([]byte, int, error) {
	needle := find
	if opts.CaseInsensitive {
		needle = asciifold.Fold(find)
	}
	positions := make([]int, 0, 8)
	for searchPos := 0; searchPos < len(value); {
		index := indexPlain(value[searchPos:], needle, opts.CaseInsensitive)
		if index < 0 {
			break
		}
		start := searchPos + index
		if replaceWordBoundaryOK(value, start, len(find), 0, int64(len(value)), opts.WholeWord) {
			positions = append(positions, start)
			searchPos = start + len(find)
		} else {
			searchPos = start + 1
		}
	}
	if len(positions) == 0 {
		return value, 0, nil
	}
	outputLength := int64(len(value))
	delta := int64(len(replacement)) - int64(len(find))
	if delta > 0 {
		count := int64(len(positions))
		limit := int64(MaxSQLSerializedOutputLiteralBytes)
		if outputLength > limit || count > (limit-outputLength)/delta {
			return nil, 0, fmt.Errorf("%w: rewritten SQL literal exceeds %d bytes", ErrSQLReplaceResourceLimit, MaxSQLSerializedOutputLiteralBytes)
		}
		outputLength += count * delta
	} else {
		outputLength += int64(len(positions)) * delta
	}
	if outputLength < 0 || outputLength > int64(MaxSQLSerializedOutputLiteralBytes) {
		return nil, 0, fmt.Errorf("%w: invalid rewritten SQL literal size", ErrSQLReplaceResourceLimit)
	}
	out := make([]byte, 0, int(outputLength))
	writePos := 0
	for _, start := range positions {
		out = append(out, value[writePos:start]...)
		out = append(out, replacement...)
		writePos = start + len(find)
	}
	out = append(out, value[writePos:]...)
	return out, len(positions), nil
}

func encodedPHPSerializationHazard(value []byte, find []byte, opts BatchOptions) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) < 8 {
		return false
	}
	if decoded, ok := decodeLikelyHex(trimmed); ok && containsPHPSerialization(decoded) {
		return true
	}
	if decoded, ok := decodeLikelyBase64(trimmed); ok && containsPHPSerialization(decoded) {
		return true
	}
	return false
}

func decodeLikelyHex(value []byte) ([]byte, bool) {
	text := string(value)
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		text = text[2:]
	}
	if len(text) < 8 || len(text)%2 != 0 {
		return nil, false
	}
	for _, char := range text {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return nil, false
		}
	}
	decoded, err := hex.DecodeString(text)
	return decoded, err == nil
}

func decodeLikelyBase64(value []byte) ([]byte, bool) {
	text := string(value)
	if len(text) < 12 || len(text)%4 != 0 {
		return nil, false
	}
	for _, char := range text {
		if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') ||
			(char >= '0' && char <= '9') || char == '+' || char == '/' || char == '=') {
			return nil, false
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(text)
	return decoded, err == nil
}

type sqlReplaceContextDetector struct {
	tail string
}

var unsupportedSQLReplaceContextMarkers = []string{
	"DELIMITER",
	"NO_BACKSLASH_ESCAPES",
	"CREATE PROCEDURE",
	"CREATE FUNCTION",
	"CREATE TRIGGER",
	"CREATE EVENT",
	"CREATE OR REPLACE PROCEDURE",
	"CREATE OR REPLACE FUNCTION",
	"CREATE OR REPLACE TRIGGER",
	"CREATE OR REPLACE EVENT",
	"CREATE DEFINER",
	"DO $$",
	"COPY ",
	"\\COPY ",
	"SET TERM",
	"\nGO\n",
	"\r\nGO\r\n",
}

func (detector *sqlReplaceContextDetector) Inspect(chunk []byte) bool {
	if len(chunk) == 0 {
		return false
	}
	text := strings.ToUpper(detector.tail + string(chunk))
	for _, marker := range unsupportedSQLReplaceContextMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	const maxTail = 128
	if len(text) > maxTail {
		detector.tail = text[len(text)-maxTail:]
	} else {
		detector.tail = text
	}
	return false
}

type phpSerializationScanState uint8

const (
	phpSerializationIdle phpSerializationScanState = iota
	phpSerializationWantColon
	phpSerializationWantDigit
	phpSerializationDigits
	phpSerializationWantValueStart
	phpSerializationEscapedQuote
)

// phpSerializationDetector recognizes high-confidence native PHP
// serialization prefixes in constant memory, including across scan chunks.
type phpSerializationDetector struct {
	state      phpSerializationScanState
	kind       byte
	nextOffset int64
	detected   bool
}

func (detector *phpSerializationDetector) Inspect(data []byte) {
	if detector.detected {
		detector.nextOffset += int64(len(data))
		return
	}
	for index, b := range data {
		detector.inspectByte(b, detector.nextOffset+int64(index))
		if detector.detected {
			break
		}
	}
	detector.nextOffset += int64(len(data))
}

func (detector *phpSerializationDetector) inspectByte(b byte, offset int64) {
	switch detector.state {
	case phpSerializationIdle:
		detector.restartAt(b)
	case phpSerializationWantColon:
		if b == ':' {
			detector.state = phpSerializationWantDigit
			return
		}
		detector.restartAt(b)
	case phpSerializationWantDigit:
		if isASCIIDigit(b) {
			detector.state = phpSerializationDigits
			return
		}
		detector.restartAt(b)
	case phpSerializationDigits:
		if isASCIIDigit(b) {
			return
		}
		if b == ':' {
			detector.state = phpSerializationWantValueStart
			return
		}
		detector.restartAt(b)
	case phpSerializationWantValueStart:
		if detector.kind == 'a' {
			if b == '{' {
				detector.detected = true
				return
			}
			detector.restartAt(b)
			return
		}
		if b == '"' {
			detector.detected = true
			return
		}
		if b == '\\' {
			detector.state = phpSerializationEscapedQuote
			return
		}
		detector.restartAt(b)
	case phpSerializationEscapedQuote:
		if b == '\\' {
			return
		}
		if b == '"' {
			detector.detected = true
			return
		}
		detector.restartAt(b)
	default:
		detector.state = phpSerializationIdle
		detector.restartAt(b)
	}
	_ = offset
}

func (detector *phpSerializationDetector) restartAt(b byte) {
	if isPHPSerializationLengthKind(b) {
		detector.kind = b
		detector.state = phpSerializationWantColon
		return
	}
	detector.kind = 0
	detector.state = phpSerializationIdle
}

func containsPHPSerialization(value []byte) bool {
	var detector phpSerializationDetector
	detector.Inspect(value)
	return detector.detected
}

func isPHPSerializationLengthKind(b byte) bool {
	switch b {
	case 'a', 's', 'S', 'O', 'C', 'E':
		return true
	default:
		return false
	}
}

func isASCIIDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

type phpSerializedRewriter struct {
	input           []byte
	pos             int
	find            []byte
	replacement     []byte
	caseInsensitive bool
	wholeWord       bool
	matches         int
	nodes           int
}

func (parser *phpSerializedRewriter) parseValue(depth int) ([]byte, error) {
	if depth > maxPHPSerializedDepth || parser.nodes >= maxPHPSerializedNodes || parser.pos >= len(parser.input) {
		return nil, ErrUnsupportedPHPSerializedData
	}
	parser.nodes++
	switch parser.input[parser.pos] {
	case 'N':
		return parser.copyNull()
	case 'b', 'i', 'd', 'r', 'R':
		return parser.copyScalar()
	case 's', 'E':
		return parser.parseByteString(parser.input[parser.pos])
	case 'S':
		// Upper-case S uses escaped-byte semantics that cannot be treated as a
		// raw byte string without a dedicated decoder.
		return nil, ErrUnsupportedPHPSerializedData
	case 'a':
		return parser.parseArray(depth)
	case 'O':
		return parser.parseObject(depth)
	case 'C':
		// Serializable::serialize payloads are opaque application-defined data.
		return nil, ErrUnsupportedPHPSerializedData
	default:
		return nil, ErrUnsupportedPHPSerializedData
	}
}

func (parser *phpSerializedRewriter) copyNull() ([]byte, error) {
	if len(parser.input)-parser.pos < 2 || parser.input[parser.pos] != 'N' || parser.input[parser.pos+1] != ';' {
		return nil, ErrUnsupportedPHPSerializedData
	}
	parser.pos += 2
	return []byte{'N', ';'}, nil
}

func (parser *phpSerializedRewriter) copyScalar() ([]byte, error) {
	start := parser.pos
	kind := parser.input[parser.pos]
	if len(parser.input)-parser.pos < 3 || parser.input[parser.pos+1] != ':' {
		return nil, ErrUnsupportedPHPSerializedData
	}
	parser.pos += 2
	valueStart := parser.pos
	for parser.pos < len(parser.input) && parser.input[parser.pos] != ';' {
		parser.pos++
	}
	if parser.pos >= len(parser.input) || parser.pos == valueStart {
		return nil, ErrUnsupportedPHPSerializedData
	}
	value := parser.input[valueStart:parser.pos]
	if !validPHPSerializedScalar(kind, value) {
		return nil, ErrUnsupportedPHPSerializedData
	}
	parser.pos++
	return append([]byte(nil), parser.input[start:parser.pos]...), nil
}

func validPHPSerializedScalar(kind byte, value []byte) bool {
	switch kind {
	case 'b':
		return bytes.Equal(value, []byte{'0'}) || bytes.Equal(value, []byte{'1'})
	case 'i', 'r', 'R':
		start := 0
		if kind == 'i' && len(value) > 1 && (value[0] == '-' || value[0] == '+') {
			start = 1
		}
		if start == len(value) {
			return false
		}
		for _, b := range value[start:] {
			if !isASCIIDigit(b) {
				return false
			}
		}
		return true
	case 'd':
		_, err := strconv.ParseFloat(string(value), 64)
		return err == nil || bytes.Equal(value, []byte("NAN")) || bytes.Equal(value, []byte("INF")) || bytes.Equal(value, []byte("-INF"))
	default:
		return false
	}
}

func (parser *phpSerializedRewriter) parseByteString(kind byte) ([]byte, error) {
	parser.pos++
	length, err := parser.readLengthColon()
	if err != nil || !parser.consume('"') || length < 0 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	if len(parser.input)-parser.pos < 2 || length > len(parser.input)-parser.pos-2 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	value := parser.input[parser.pos : parser.pos+length]
	parser.pos += length
	if !parser.consume('"') || !parser.consume(';') {
		return nil, ErrUnsupportedPHPSerializedData
	}
	next, count, err := replaceBytesBounded(value, parser.find, parser.replacement, BatchOptions{
		CaseInsensitive: parser.caseInsensitive,
		WholeWord:       parser.wholeWord,
	})
	if err != nil {
		return nil, err
	}
	parser.matches += count
	out := []byte{kind, ':'}
	out = strconv.AppendInt(out, int64(len(next)), 10)
	out = append(out, ':', '"')
	out = append(out, next...)
	out = append(out, '"', ';')
	return out, nil
}

func (parser *phpSerializedRewriter) parseArray(depth int) ([]byte, error) {
	parser.pos++
	count, err := parser.readLengthColon()
	if err != nil || !parser.consume('{') || count < 0 || count > maxPHPSerializedNodes/2 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	valueCount := count * 2
	if valueCount > maxPHPSerializedNodes-parser.nodes || valueCount > (len(parser.input)-parser.pos)/2 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	out := []byte{'a', ':'}
	out = strconv.AppendInt(out, int64(count), 10)
	out = append(out, ':', '{')
	for index := 0; index < valueCount; index++ {
		value, valueErr := parser.parseValue(depth + 1)
		if valueErr != nil {
			return nil, valueErr
		}
		if len(out) > MaxSQLSerializedOutputLiteralBytes-len(value) {
			return nil, fmt.Errorf("%w: rewritten serialized value exceeds %d bytes", ErrSQLReplaceResourceLimit, MaxSQLSerializedOutputLiteralBytes)
		}
		out = append(out, value...)
	}
	if !parser.consume('}') {
		return nil, ErrUnsupportedPHPSerializedData
	}
	out = append(out, '}')
	return out, nil
}

func (parser *phpSerializedRewriter) parseObject(depth int) ([]byte, error) {
	parser.pos++
	class, err := parser.readLengthDelimitedBytes()
	if err != nil {
		return nil, err
	}
	properties, err := parser.readLengthColon()
	if err != nil || !parser.consume('{') || properties < 0 || properties > maxPHPSerializedNodes/2 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	valueCount := properties * 2
	if valueCount > maxPHPSerializedNodes-parser.nodes || valueCount > (len(parser.input)-parser.pos)/2 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	nextClass, classMatches, err := replaceBytesBounded(class, parser.find, parser.replacement, BatchOptions{
		CaseInsensitive: parser.caseInsensitive,
		WholeWord:       parser.wholeWord,
	})
	if err != nil {
		return nil, err
	}
	parser.matches += classMatches
	out := []byte{'O', ':'}
	out = strconv.AppendInt(out, int64(len(nextClass)), 10)
	out = append(out, ':', '"')
	out = append(out, nextClass...)
	out = append(out, '"', ':')
	out = strconv.AppendInt(out, int64(properties), 10)
	out = append(out, ':', '{')
	for index := 0; index < valueCount; index++ {
		value, valueErr := parser.parseValue(depth + 1)
		if valueErr != nil {
			return nil, valueErr
		}
		if len(out) > MaxSQLSerializedOutputLiteralBytes-len(value) {
			return nil, fmt.Errorf("%w: rewritten serialized value exceeds %d bytes", ErrSQLReplaceResourceLimit, MaxSQLSerializedOutputLiteralBytes)
		}
		out = append(out, value...)
	}
	if !parser.consume('}') {
		return nil, ErrUnsupportedPHPSerializedData
	}
	out = append(out, '}')
	return out, nil
}

func (parser *phpSerializedRewriter) readLengthDelimitedBytes() ([]byte, error) {
	length, err := parser.readLengthColon()
	if err != nil || !parser.consume('"') || length < 0 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	if len(parser.input)-parser.pos < 2 || length > len(parser.input)-parser.pos-2 {
		return nil, ErrUnsupportedPHPSerializedData
	}
	value := parser.input[parser.pos : parser.pos+length]
	parser.pos += length
	if !parser.consume('"') || !parser.consume(':') {
		return nil, ErrUnsupportedPHPSerializedData
	}
	return value, nil
}

func (parser *phpSerializedRewriter) readLengthColon() (int, error) {
	if !parser.consume(':') {
		return 0, ErrUnsupportedPHPSerializedData
	}
	start := parser.pos
	value := 0
	for parser.pos < len(parser.input) && isASCIIDigit(parser.input[parser.pos]) {
		digit := int(parser.input[parser.pos] - '0')
		if value > (int(^uint(0)>>1)-digit)/10 {
			return 0, ErrUnsupportedPHPSerializedData
		}
		value = value*10 + digit
		parser.pos++
	}
	if start == parser.pos || !parser.consume(':') {
		return 0, ErrUnsupportedPHPSerializedData
	}
	return value, nil
}

func (parser *phpSerializedRewriter) consume(want byte) bool {
	if parser.pos >= len(parser.input) || parser.input[parser.pos] != want {
		return false
	}
	parser.pos++
	return true
}
