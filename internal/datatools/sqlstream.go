package datatools

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// SQL dump operations are streaming, but a single logical record still has
	// to be retained by transforms and the legacy INSERT-to-CSV parser. Keep the
	// budget immutable at the backend boundary so callers cannot disable it.
	maxSQLLogicalLineBytes = 16 << 20
	maxSQLStatementBytes   = 32 << 20
	maxSQLHeaderBytes      = 256 << 10
)

var (
	ErrSQLLogicalLineTooLong = errors.New("SQL dump logical line exceeds byte limit")
	ErrSQLStatementTooLong   = errors.New("SQL dump statement exceeds byte limit")
)

func checkContext(ctx context.Context) error {
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

// readBoundedSQLLine reads one newline-terminated logical line without allowing
// bufio.Reader.ReadString to grow an attacker-controlled allocation.
func readBoundedSQLLine(ctx context.Context, br *bufio.Reader, max int) ([]byte, error) {
	line := make([]byte, 0, min(max, br.Size()))
	for {
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		fragment, err := br.ReadSlice('\n')
		if len(fragment) > max-len(line) {
			return nil, fmt.Errorf("%w (%d bytes)", ErrSQLLogicalLineTooLong, max)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// readSQLLinePrefix drains one complete logical line while retaining only the
// prefix needed to classify a dump statement. It lets analysis handle huge
// extended INSERT rows without retaining their values.
func readSQLLinePrefix(ctx context.Context, br *bufio.Reader) (prefix []byte, bytesRead int64, err error) {
	prefix = make([]byte, 0, min(maxSQLHeaderBytes, br.Size()))
	for {
		if ctxErr := checkContext(ctx); ctxErr != nil {
			return nil, bytesRead, ctxErr
		}
		fragment, readErr := br.ReadSlice('\n')
		bytesRead += int64(len(fragment))
		if len(prefix) < maxSQLHeaderBytes {
			take := min(len(fragment), maxSQLHeaderBytes-len(prefix))
			prefix = append(prefix, fragment[:take]...)
		}
		if errors.Is(readErr, bufio.ErrBufferFull) {
			continue
		}
		return prefix, bytesRead, readErr
	}
}

func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func appendBoundedSQLStatement(dst *strings.Builder, text string, max int) error {
	if len(text) > max-dst.Len() {
		return fmt.Errorf("%w (%d bytes)", ErrSQLStatementTooLong, max)
	}
	dst.WriteString(text)
	return nil
}
