package bigfile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"novera/internal/bigfile/document"
)

const (
	sourceFingerprintChunkBytes    int64 = 1024 * 1024
	maxSourceFingerprintChunkBytes int64 = 16 * 1024 * 1024
	maxSourceFingerprintChunks           = 256 * 1024
	maxSourceFingerprintProgress   int64 = 1024
)

var ErrOutputSourceVerificationLimit = errors.New("big-file source exceeds the exact verification limit")

type documentSourceExpectation struct {
	path         string
	info         fs.FileInfo
	state        document.FileState
	size         int64
	digest       [sha256.Size]byte
	chunkSize    int64
	chunkDigests [][sha256.Size]byte
}

// captureDocumentSourceExpectation records the exact retained descriptor,
// current pathname identity, stable size/mtime, and a full content digest. It
// validates identity both before and after hashing so a transform is never
// authorized by a pathname-only preflight.
func captureDocumentSourceExpectation(ctx context.Context, doc *document.FileDocument, path string, progress func(int64, int64)) (*documentSourceExpectation, error) {
	if doc == nil {
		return nil, errors.New("source document is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	before, state, err := inspectDocumentSourceIdentity(doc, path, nil)
	if err != nil {
		return nil, err
	}
	digest, chunkSize, chunkDigests, err := fingerprintReaderAtWithChunks(ctx, doc, before.Size(), progress)
	if err != nil {
		return nil, fmt.Errorf("fingerprint source: %w", err)
	}
	after, finalState, err := inspectDocumentSourceIdentity(doc, path, before)
	if err != nil {
		return nil, err
	}
	if !sameFileState(before, after) || !state.Equal(finalState) {
		return nil, ErrOutputSourceChanged
	}
	return &documentSourceExpectation{
		path:         path,
		info:         after,
		state:        finalState,
		size:         after.Size(),
		digest:       digest,
		chunkSize:    chunkSize,
		chunkDigests: chunkDigests,
	}, nil
}

func (expected *documentSourceExpectation) validateContext(ctx context.Context, doc *document.FileDocument, progress func(int64, int64)) error {
	if expected == nil || doc == nil {
		return errors.New("source expectation and document are required")
	}
	before, state, err := inspectDocumentSourceIdentity(doc, expected.path, expected.info)
	if err != nil {
		return err
	}
	if !sameFileState(expected.info, before) || !expected.state.Equal(state) {
		return ErrOutputSourceChanged
	}
	digest, err := fingerprintReaderAt(ctx, doc, expected.size, progress)
	if err != nil {
		return fmt.Errorf("revalidate source: %w", err)
	}
	if digest != expected.digest {
		return ErrOutputSourceChanged
	}
	after, finalState, err := inspectDocumentSourceIdentity(doc, expected.path, expected.info)
	if err != nil {
		return err
	}
	if !sameFileState(expected.info, after) || !expected.state.Equal(finalState) {
		return ErrOutputSourceChanged
	}
	return ctx.Err()
}

func inspectDocumentSourceIdentity(doc *document.FileDocument, path string, expected fs.FileInfo) (fs.FileInfo, document.FileState, error) {
	if doc == nil || path == "" {
		return nil, document.FileState{}, errors.New("source document and path are required")
	}
	opened, err := doc.OpenedFileInfo()
	if err != nil {
		return nil, document.FileState{}, fmt.Errorf("%w: inspect retained descriptor: %v", ErrOutputSourceChanged, err)
	}
	current, err := os.Stat(path)
	if err != nil {
		return nil, document.FileState{}, fmt.Errorf("%w: inspect source path: %v", ErrOutputSourceChanged, err)
	}
	if !opened.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(opened, current) {
		return nil, document.FileState{}, ErrOutputSourceChanged
	}
	if expected != nil && !os.SameFile(expected, opened) {
		return nil, document.FileState{}, ErrOutputSourceChanged
	}
	original := doc.OriginalFileState()
	state := document.FileState{Size: current.Size(), ModTime: current.ModTime()}
	if opened.Size() != current.Size() || !opened.ModTime().Equal(current.ModTime()) || !state.Equal(original) {
		return nil, document.FileState{}, ErrOutputSourceChanged
	}
	return opened, state, nil
}

func sameFileState(first, second fs.FileInfo) bool {
	return first != nil && second != nil &&
		os.SameFile(first, second) &&
		first.Size() == second.Size() &&
		first.ModTime().Equal(second.ModTime())
}

func fingerprintReaderAt(ctx context.Context, reader io.ReaderAt, size int64, progress func(int64, int64)) ([sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if reader == nil {
		return result, errors.New("fingerprint reader is required")
	}
	if size < 0 {
		return result, errors.New("negative fingerprint size")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reporter := newSourceFingerprintProgressReporter(size, progress)
	if size == 0 {
		reporter.report(0)
	}
	digest := sha256.New()
	buffer := make([]byte, int(sourceFingerprintChunkBytes))
	var offset int64
	for offset < size {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		want := int64(len(buffer))
		if remaining := size - offset; want > remaining {
			want = remaining
		}
		n, readErr := reader.ReadAt(buffer[:int(want)], offset)
		if n > 0 {
			_, _ = digest.Write(buffer[:n])
			offset += int64(n)
			reporter.report(offset)
		}
		if n != int(want) {
			if readErr == nil {
				readErr = io.ErrUnexpectedEOF
			}
			return result, fmt.Errorf("read at %d returned %d of %d bytes: %w", offset-int64(n), n, want, readErr)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return result, readErr
		}
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	copy(result[:], digest.Sum(nil))
	return result, nil
}

func fingerprintReaderAtWithChunks(ctx context.Context, reader io.ReaderAt, size int64, progress func(int64, int64)) ([sha256.Size]byte, int64, [][sha256.Size]byte, error) {
	var result [sha256.Size]byte
	if reader == nil {
		return result, 0, nil, errors.New("fingerprint reader is required")
	}
	if size < 0 {
		return result, 0, nil, errors.New("negative fingerprint size")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	chunkSize, chunkCount, err := boundedSourceFingerprintLayout(size)
	if err != nil {
		return result, 0, nil, err
	}
	reporter := newSourceFingerprintProgressReporter(size, progress)
	if size == 0 {
		reporter.report(0)
	}
	chunks := make([][sha256.Size]byte, 0, chunkCount)
	digest := sha256.New()
	buffer := make([]byte, int(chunkSize))
	for offset := int64(0); offset < size; {
		if err := ctx.Err(); err != nil {
			return result, 0, nil, err
		}
		want := chunkSize
		if remaining := size - offset; want > remaining {
			want = remaining
		}
		n, readErr := reader.ReadAt(buffer[:int(want)], offset)
		if n > 0 {
			_, _ = digest.Write(buffer[:n])
		}
		if n != int(want) {
			if readErr == nil {
				readErr = io.ErrUnexpectedEOF
			}
			return result, 0, nil, fmt.Errorf("read at %d returned %d of %d bytes: %w", offset, n, want, readErr)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return result, 0, nil, readErr
		}
		chunks = append(chunks, sha256.Sum256(buffer[:n]))
		offset += int64(n)
		reporter.report(offset)
	}
	if err := ctx.Err(); err != nil {
		return result, 0, nil, err
	}
	copy(result[:], digest.Sum(nil))
	return result, chunkSize, chunks, nil
}

func boundedSourceFingerprintLayout(size int64) (chunkSize int64, chunkCount int, err error) {
	if size < 0 {
		return 0, 0, errors.New("negative fingerprint size")
	}
	maximumSize := int64(maxSourceFingerprintChunks) * maxSourceFingerprintChunkBytes
	if size > maximumSize {
		return 0, 0, fmt.Errorf("%w: %d bytes (maximum %d)", ErrOutputSourceVerificationLimit, size, maximumSize)
	}
	chunkSize = sourceFingerprintChunkBytes
	if size > int64(maxSourceFingerprintChunks)*chunkSize {
		minimum := 1 + (size-1)/int64(maxSourceFingerprintChunks)
		chunkSize = (1 + (minimum-1)/sourceFingerprintChunkBytes) * sourceFingerprintChunkBytes
	}
	if chunkSize > maxSourceFingerprintChunkBytes {
		return 0, 0, fmt.Errorf("%w: computed chunk size %d", ErrOutputSourceVerificationLimit, chunkSize)
	}
	if size == 0 {
		return chunkSize, 0, nil
	}
	count := 1 + (size-1)/chunkSize
	if count <= 0 || count > maxSourceFingerprintChunks {
		return 0, 0, fmt.Errorf("%w: invalid chunk count %d", ErrOutputSourceVerificationLimit, count)
	}
	return chunkSize, int(count), nil
}

type sourceFingerprintProgressReporter struct {
	callback      func(completed, total int64)
	total         int64
	step          int64
	lastBucket    int64
	lastCompleted int64
	reportedEmpty bool
}

func newSourceFingerprintProgressReporter(total int64, callback func(int64, int64)) sourceFingerprintProgressReporter {
	reporter := sourceFingerprintProgressReporter{
		callback:      callback,
		total:         total,
		lastCompleted: -1,
	}
	if total > 0 {
		reporter.step = 1 + (total-1)/maxSourceFingerprintProgress
	}
	return reporter
}

func (r *sourceFingerprintProgressReporter) report(completed int64) {
	if r == nil || r.callback == nil {
		return
	}
	if r.total <= 0 {
		if !r.reportedEmpty {
			r.reportedEmpty = true
			r.lastCompleted = 0
			r.callback(0, 0)
		}
		return
	}
	if completed < 0 {
		completed = 0
	}
	if completed > r.total {
		completed = r.total
	}
	bucket := completed / r.step
	if completed < r.total && bucket <= r.lastBucket {
		return
	}
	if completed == r.lastCompleted {
		return
	}
	r.lastBucket = bucket
	r.lastCompleted = completed
	r.callback(completed, r.total)
}
