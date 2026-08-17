package manualedit

import (
	"context"
	"errors"
	"fmt"
	"io"

	"novera/internal/bigfile/document"
)

var (
	ErrSessionSourceUnbound     = errors.New("manual-edit session is not bound to a source generation")
	ErrSessionGenerationChanged = errors.New("manual-edit source generation changed")
	ErrSessionSourceChanged     = errors.New("manual-edit source identity or content changed")
)

// BoundSource is the exact-generation boundary supplied by FileService. Its
// implementation owns the full digest and bounded per-block digest map
// captured before the first edit is installed.
type BoundSource interface {
	Size() int64
	VerifiedReader(context.Context) (document.ReaderAtSize, error)
	ValidateContext(context.Context, func(completed, total int64)) error
}

type sourceBinding struct {
	path       string
	generation uint64
	source     BoundSource
}

// NewSourceBoundSession creates a staging session tied to one immutable
// registry generation and one exact source fingerprint.
func NewSourceBoundSession(size int64, path string, generation uint64, source BoundSource, limits Limits) (*Session, error) {
	if generation == 0 {
		return nil, ErrSessionGenerationChanged
	}
	if path == "" {
		return nil, fmt.Errorf("%w: source path is required", ErrSessionSourceChanged)
	}
	if source == nil || source.Size() != size {
		return nil, fmt.Errorf("%w: source size does not match the staged generation", ErrSessionSourceChanged)
	}
	session, err := NewSessionWithLimits(size, limits)
	if err != nil {
		return nil, err
	}
	session.binding = &sourceBinding{path: path, generation: generation, source: source}
	return session, nil
}

// IsSourceBound reports whether this session was prepared against an exact
// source expectation rather than only a byte count.
func (s *Session) IsSourceBound() bool {
	return s != nil && s.binding != nil && s.binding.source != nil
}

// MatchesSourceGeneration checks the immutable registry identity stored when
// staging was prepared.
func (s *Session) MatchesSourceGeneration(path string, generation uint64) bool {
	return s != nil && s.binding != nil &&
		generation != 0 && generation == s.binding.generation &&
		path == s.binding.path
}

// ValidateSourceContext performs an exact full-source validation against the
// expectation captured before the first edit. The scan is memory-bounded and
// cancellable; progress callback frequency is bounded by the source layer.
func (s *Session) ValidateSourceContext(ctx context.Context, progress func(completed, total int64)) error {
	if s == nil || s.binding == nil || s.binding.source == nil {
		return ErrSessionSourceUnbound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.binding.source.ValidateContext(ctx, progress); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrSessionSourceChanged, err)
	}
	return nil
}

func (s *Session) verifiedSourceReader(ctx context.Context) (document.ReaderAtSize, error) {
	if s == nil || s.binding == nil || s.binding.source == nil {
		return nil, ErrSessionSourceUnbound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reader, err := s.binding.source.VerifiedReader(ctx)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrSessionSourceChanged, err)
	}
	if reader == nil || reader.Size() != s.originalSize {
		return nil, ErrSessionSourceChanged
	}
	return reader, nil
}

// ReadRangeContext returns a bounded range from the edited view. Every source
// block consumed by the range is authenticated before any bytes are returned.
func (s *Session) ReadRangeContext(ctx context.Context, start, end, maxBytes int64) ([]byte, error) {
	if s == nil || s.table == nil {
		return nil, errors.New("session is required")
	}
	if err := validateBoundedSourceRange(start, end, s.Size(), maxBytes); err != nil {
		return nil, err
	}
	reader, err := s.verifiedSourceReader(ctx)
	if err != nil {
		return nil, err
	}
	result, err := s.table.ReadRange(reader, start, end)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrSessionSourceChanged, err)
	}
	return result, nil
}

// ReadSourceRangeContext returns authenticated original bytes for diff
// previews whose coordinates are anchored to the prepared generation.
func (s *Session) ReadSourceRangeContext(ctx context.Context, start, end, maxBytes int64) ([]byte, error) {
	if s == nil {
		return nil, errors.New("session is required")
	}
	if err := validateBoundedSourceRange(start, end, s.originalSize, maxBytes); err != nil {
		return nil, err
	}
	reader, err := s.verifiedSourceReader(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]byte, int(end-start))
	n, readErr := reader.ReadAt(result, start)
	if n != len(result) {
		if readErr == nil {
			readErr = io.ErrUnexpectedEOF
		}
		return nil, fmt.Errorf("%w: %v", ErrSessionSourceChanged, readErr)
	}
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, fmt.Errorf("%w: %v", ErrSessionSourceChanged, readErr)
	}
	return result, nil
}

// WriteBoundTo streams the staged view using only source blocks authenticated
// against the prepare-time expectation. Callers must still invoke
// ValidateSourceContext at their atomic publication boundary, because a fully
// replaced source range is intentionally not read by the piece table.
func (s *Session) WriteBoundTo(ctx context.Context, dst io.Writer, progress func(Progress)) (int64, error) {
	reader, err := s.verifiedSourceReader(ctx)
	if err != nil {
		return 0, err
	}
	return s.WriteTo(ctx, reader, dst, progress)
}

func validateBoundedSourceRange(start, end, size, maxBytes int64) error {
	if start < 0 || end < start || end > size {
		return fmt.Errorf("invalid read range [%d,%d) for size %d", start, end, size)
	}
	if maxBytes <= 0 || end-start > maxBytes || end-start > int64(maxInt()) {
		return fmt.Errorf("read range %d exceeds bounded limit %d", end-start, maxBytes)
	}
	return nil
}
