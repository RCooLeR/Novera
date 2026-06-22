package replace

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"

	"novera/internal/bigfile/asciifold"
)

const (
	plainReplaceWriteBufferSize        = 1024 * 1024
	plainReplaceMaxBufferedOutputBytes = 128 * 1024 * 1024
)

type syncWriter interface {
	io.Writer
	Sync() error
}

type syncWriteCloser interface {
	syncWriter
	io.Closer
}

// Progress describes a long-running replace operation.
type Progress struct {
	BytesProcessed int64
	BytesTotal     int64
	Matches        int64
}

// PlainOptions controls streaming replacement.
type PlainOptions struct {
	ChunkSize       int
	CaseInsensitive bool
	WholeWord       bool
	Progress        func(Progress)
}

// ReplacePlain streams src to dst while replacing pattern with repl.
// It never loads the full file into memory.
func ReplacePlain(ctx context.Context, src *os.File, dst syncWriter, pattern []byte, repl []byte, opts PlainOptions) (int64, error) {
	if len(pattern) == 0 {
		return 0, errors.New("empty pattern")
	}
	if opts.ChunkSize <= 0 {
		opts.ChunkSize = 64 * 1024 * 1024
	}
	if opts.WholeWord && opts.ChunkSize < len(pattern)+2 {
		opts.ChunkSize = len(pattern) + 2
	}

	st, err := src.Stat()
	if err != nil {
		return 0, err
	}

	total := st.Size()
	buf := make([]byte, opts.ChunkSize)
	bufferedDst := bufio.NewWriterSize(dst, plainReplaceWriteBufferSize)
	keepSize := len(pattern) - 1
	if opts.WholeWord {
		keepSize = len(pattern) + 1
	}
	carry := make([]byte, 0, keepSize)
	window := make([]byte, 0, opts.ChunkSize+keepSize)
	needle := pattern
	if opts.CaseInsensitive {
		needle = asciifold.Fold(pattern)
	}

	var processed int64
	var matches int64

	for {
		select {
		case <-ctx.Done():
			return matches, ctx.Err()
		default:
		}

		n, readErr := src.Read(buf)
		if n > 0 {
			carryLen := len(carry)
			window = window[:0]
			window = append(window, carry...)
			window = append(window, buf[:n]...)

			// Process match starts that are now safe. A match may start inside the
			// old carry and finish inside the new chunk, so we must search the full
			// window but only finalize bytes that cannot be affected by future reads.
			processLimit := len(window) - keepSize
			if readErr == io.EOF {
				processLimit = len(window)
			}
			if processLimit < 0 {
				processLimit = 0
			}

			consumed, count, err := writeReplacedPrefix(bufferedDst, window, processLimit, needle, pattern, repl, opts.CaseInsensitive, opts.WholeWord, processed-int64(carryLen), total)
			if err != nil {
				return matches, err
			}
			matches += int64(count)

			carry = append(carry[:0], window[consumed:]...)
			processed += int64(n)

			if opts.Progress != nil {
				opts.Progress(Progress{
					BytesProcessed: processed,
					BytesTotal:     total,
					Matches:        matches,
				})
			}
		}

		if errors.Is(readErr, io.EOF) {
			if len(carry) > 0 {
				_, count, err := writeReplacedPrefix(bufferedDst, carry, len(carry), needle, pattern, repl, opts.CaseInsensitive, opts.WholeWord, processed-int64(len(carry)), total)
				if err != nil {
					return matches, err
				}
				matches += int64(count)
			}
			if err := bufferedDst.Flush(); err != nil {
				return matches, err
			}
			return matches, dst.Sync()
		}

		if readErr != nil {
			return matches, readErr
		}
	}
}

func writeReplacedPrefix(dst io.Writer, window []byte, processLimit int, needle []byte, old []byte, new []byte, caseInsensitive bool, wholeWord bool, windowStart int64, total int64) (consumed int, count int, err error) {
	if !caseInsensitive && !wholeWord {
		return writeReplacedPrefixFastPath(dst, window, processLimit, old, new)
	}
	writePos := 0
	searchPos := 0
	for searchPos < processLimit {
		idx := indexPlain(window[searchPos:], needle, caseInsensitive)
		if idx < 0 {
			break
		}

		matchStart := searchPos + idx
		if matchStart >= processLimit {
			break
		}
		if !replaceWordBoundaryOK(window, matchStart, len(old), windowStart, total, wholeWord) {
			searchPos = matchStart + 1
			continue
		}

		if _, err := dst.Write(window[writePos:matchStart]); err != nil {
			return consumed, count, err
		}
		if _, err := dst.Write(new); err != nil {
			return consumed, count, err
		}

		count++
		writePos = matchStart + len(old)
		searchPos = writePos
	}

	if writePos < processLimit {
		if _, err := dst.Write(window[writePos:processLimit]); err != nil {
			return consumed, count, err
		}
		writePos = processLimit
	}

	return writePos, count, nil
}

func writeReplacedPrefixFastPath(dst io.Writer, window []byte, processLimit int, old []byte, new []byte) (consumed int, count int, err error) {
	consumed, count = countPlainFastPath(window, processLimit, old)
	if count == 0 {
		if _, err := dst.Write(window[:processLimit]); err != nil {
			return 0, 0, err
		}
		return processLimit, 0, nil
	}
	outputLen := int64(processLimit) + int64(consumed-processLimit) + int64(count)*(int64(len(new))-int64(len(old)))
	if outputLen >= 0 && outputLen <= plainReplaceMaxBufferedOutputBytes {
		out := make([]byte, 0, int(outputLen))
		writePos := 0
		searchPos := 0
		for searchPos < processLimit {
			idx := bytes.Index(window[searchPos:], old)
			if idx < 0 {
				break
			}
			matchStart := searchPos + idx
			if matchStart >= processLimit {
				break
			}
			out = append(out, window[writePos:matchStart]...)
			out = append(out, new...)
			writePos = matchStart + len(old)
			searchPos = writePos
		}
		if writePos < processLimit {
			out = append(out, window[writePos:processLimit]...)
		}
		if _, err := dst.Write(out); err != nil {
			return 0, 0, err
		}
		return consumed, count, nil
	}
	return writeReplacedPrefixStreaming(dst, window, processLimit, old, old, new)
}

func countPlainFastPath(window []byte, processLimit int, old []byte) (consumed int, count int) {
	writePos := 0
	searchPos := 0
	for searchPos < processLimit {
		idx := bytes.Index(window[searchPos:], old)
		if idx < 0 {
			break
		}
		matchStart := searchPos + idx
		if matchStart >= processLimit {
			break
		}
		count++
		writePos = matchStart + len(old)
		searchPos = writePos
	}
	if writePos < processLimit {
		writePos = processLimit
	}
	return writePos, count
}

func writeReplacedPrefixStreaming(dst io.Writer, window []byte, processLimit int, needle []byte, old []byte, new []byte) (consumed int, count int, err error) {
	writePos := 0
	searchPos := 0
	for searchPos < processLimit {
		idx := bytes.Index(window[searchPos:], needle)
		if idx < 0 {
			break
		}

		matchStart := searchPos + idx
		if matchStart >= processLimit {
			break
		}
		if _, err := dst.Write(window[writePos:matchStart]); err != nil {
			return consumed, count, err
		}
		if _, err := dst.Write(new); err != nil {
			return consumed, count, err
		}

		count++
		writePos = matchStart + len(old)
		searchPos = writePos
	}

	if writePos < processLimit {
		if _, err := dst.Write(window[writePos:processLimit]); err != nil {
			return consumed, count, err
		}
		writePos = processLimit
	}

	return writePos, count, nil
}

func indexPlain(window []byte, needle []byte, caseInsensitive bool) int {
	if caseInsensitive {
		return asciifold.IndexFolded(window, needle)
	}
	return bytes.Index(window, needle)
}
