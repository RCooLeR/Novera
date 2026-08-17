package bigfile

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync"

	"novera/internal/bigfile/document"
)

// verifiedDocumentReader exposes only bytes belonging to a captured source
// expectation. A source block is read and hashed before any of its bytes are
// returned, so a concurrent rewrite cannot contaminate an artifact and then be
// hidden by restoring the file before final validation.
type verifiedDocumentReader struct {
	ctx      context.Context
	expected *documentSourceExpectation
	doc      *document.FileDocument

	readAt func([]byte, int64) (int, error)
	sum256 func([]byte) [sha256.Size]byte

	mu          sync.Mutex
	buffer      []byte
	cachedChunk int64
	cacheValid  bool
}

func newVerifiedDocumentReader(ctx context.Context, expected *documentSourceExpectation, doc *document.FileDocument) (*verifiedDocumentReader, error) {
	if expected == nil || doc == nil {
		return nil, errors.New("source expectation and document are required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expected.chunkSize <= 0 {
		return nil, errors.New("source expectation has no block verification layout")
	}
	wantChunks := 0
	if expected.size > 0 {
		wantChunks = int(1 + (expected.size-1)/expected.chunkSize)
	}
	if wantChunks != len(expected.chunkDigests) {
		return nil, errors.New("source expectation block map is incomplete")
	}
	if _, _, err := inspectDocumentSourceIdentity(doc, expected.path, expected.info); err != nil {
		return nil, err
	}
	return &verifiedDocumentReader{
		ctx:      ctx,
		expected: expected,
		doc:      doc,
		readAt:   doc.ReadAt,
		sum256:   sha256.Sum256,
	}, nil
}

func (r *verifiedDocumentReader) Size() int64 {
	if r == nil || r.expected == nil {
		return 0
	}
	return r.expected.size
}

func (r *verifiedDocumentReader) ReadAt(dst []byte, offset int64) (int, error) {
	if r == nil || r.expected == nil || r.doc == nil {
		return 0, errors.New("verified source reader is required")
	}
	if offset < 0 {
		return 0, errors.New("negative source read offset")
	}
	if len(dst) == 0 {
		return 0, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	size := r.Size()
	if offset >= size {
		return 0, io.EOF
	}
	want := int64(len(dst))
	truncated := false
	if available := size - offset; want > available {
		want = available
		truncated = true
	}
	if int64(cap(r.buffer)) < r.expected.chunkSize {
		r.buffer = make([]byte, int(r.expected.chunkSize))
	}
	r.buffer = r.buffer[:int(r.expected.chunkSize)]

	written := 0
	end := offset + want
	for current := offset; current < end; {
		if err := r.ctx.Err(); err != nil {
			return written, err
		}
		chunkIndex := current / r.expected.chunkSize
		chunkStart := chunkIndex * r.expected.chunkSize
		chunkEnd := chunkStart + r.expected.chunkSize
		if chunkEnd > size {
			chunkEnd = size
		}
		chunkLength := chunkEnd - chunkStart
		buffer := r.buffer[:int(chunkLength)]
		if !r.cacheValid || r.cachedChunk != chunkIndex {
			r.cacheValid = false
			readAt := r.readAt
			if readAt == nil {
				readAt = r.doc.ReadAt
			}
			n, readErr := readAt(buffer, chunkStart)
			if n != len(buffer) {
				if readErr == nil {
					readErr = io.ErrUnexpectedEOF
				}
				return written, fmt.Errorf("%w: verified block %d read %d of %d bytes: %v", ErrOutputSourceChanged, chunkIndex, n, len(buffer), readErr)
			}
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return written, fmt.Errorf("%w: verified block %d read failed: %v", ErrOutputSourceChanged, chunkIndex, readErr)
			}
			sum256 := r.sum256
			if sum256 == nil {
				sum256 = sha256.Sum256
			}
			if got := sum256(buffer); got != r.expected.chunkDigests[chunkIndex] {
				return written, fmt.Errorf("%w: source block %d differs from the captured generation", ErrOutputSourceChanged, chunkIndex)
			}
			r.cachedChunk = chunkIndex
			r.cacheValid = true
		}

		copyStart := current - chunkStart
		copyEnd := chunkLength
		if end < chunkEnd {
			copyEnd = end - chunkStart
		}
		copied := copy(dst[written:], buffer[int(copyStart):int(copyEnd)])
		written += copied
		current += int64(copied)
		if copied == 0 {
			return written, io.ErrNoProgress
		}
	}
	if err := r.ctx.Err(); err != nil {
		return written, err
	}
	if truncated {
		return written, io.EOF
	}
	return written, nil
}
