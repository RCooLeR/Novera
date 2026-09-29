package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/fileio"
)

var (
	ErrInMemoryBufferTooLarge = errors.New("file exceeds in-memory editor budget")
	ErrInMemoryBufferBinary   = errors.New("binary-looking file cannot use in-memory editor")
)

// InMemoryBuffer is a full decoded text buffer for files that fit the
// configured editable-window budget. Huge files must continue using FileDocument
// streaming and viewport reads instead of this type.
type InMemoryBuffer struct {
	Path          string
	OriginalSize  int64
	WindowStart   int64
	WindowEnd     int64
	EditableLimit int64
	Encoding      string
	LineEnding    string
	FileType      string
	Text          string
}

type WriteCopyOptions struct {
	Overwrite bool
}

type InMemoryLoadStage string

const (
	InMemoryLoadStageReading  InMemoryLoadStage = "reading"
	InMemoryLoadStageDecoding InMemoryLoadStage = "decoding"
)

type InMemoryLoadProgress struct {
	Stage         InMemoryLoadStage
	Start         int64
	End           int64
	Bytes         int64
	EditableLimit int64
}

type InMemoryLoadOptions struct {
	Progress func(InMemoryLoadProgress)
}

// LoadInMemoryBuffer decodes the whole file only after proving it is inside the
// caller-provided memory budget. This is the boundary that keeps normal editor
// behavior separate from Novera's large-file streaming path.
func LoadInMemoryBuffer(doc *FileDocument, editableLimit int64) (*InMemoryBuffer, error) {
	return LoadInMemoryBufferContext(context.Background(), doc, editableLimit)
}

// LoadInMemoryBufferContext is the cancellation-aware form of
// LoadInMemoryBuffer. It checks the context before expensive stages and again
// before publishing decoded text to callers.
func LoadInMemoryBufferContext(ctx context.Context, doc *FileDocument, editableLimit int64) (*InMemoryBuffer, error) {
	return LoadInMemoryBufferContextWithOptions(ctx, doc, editableLimit, InMemoryLoadOptions{})
}

func LoadInMemoryBufferContextWithOptions(ctx context.Context, doc *FileDocument, editableLimit int64, opts InMemoryLoadOptions) (*InMemoryBuffer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("nil document")
	}
	if editableLimit <= 0 {
		return nil, errors.New("editable limit must be positive")
	}
	if doc.Size() > editableLimit {
		return nil, fmt.Errorf("%w: size %d > limit %d", ErrInMemoryBufferTooLarge, doc.Size(), editableLimit)
	}
	meta := doc.Metadata()
	if meta.Binary {
		return nil, fmt.Errorf("%w: confidence %.2f", ErrInMemoryBufferBinary, meta.BinaryConfidence)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reportInMemoryLoadProgress(opts, InMemoryLoadProgress{
		Stage:         InMemoryLoadStageReading,
		Start:         0,
		End:           doc.Size(),
		Bytes:         doc.Size(),
		EditableLimit: editableLimit,
	})
	data, err := doc.ReadRangeWithLimit(0, doc.Size(), editableLimit)
	if err != nil {
		return nil, err
	}
	return newInMemoryBufferContext(ctx, doc, 0, doc.Size(), editableLimit, data, opts)
}

// LoadInMemoryWindow decodes a bounded byte range for normal-editor behavior.
// Unlike LoadInMemoryBuffer, this may be used for huge files because it proves
// only the requested window, not the whole file, fits the caller-provided budget.
func LoadInMemoryWindow(doc *FileDocument, start, end int64, editableLimit int64) (*InMemoryBuffer, error) {
	return LoadInMemoryWindowContext(context.Background(), doc, start, end, editableLimit)
}

// LoadInMemoryWindowContext is the cancellation-aware form of
// LoadInMemoryWindow. It preserves the same budget and binary guards while
// letting UI callers suppress stale huge-file slice loads before decode/apply.
func LoadInMemoryWindowContext(ctx context.Context, doc *FileDocument, start, end int64, editableLimit int64) (*InMemoryBuffer, error) {
	return LoadInMemoryWindowContextWithOptions(ctx, doc, start, end, editableLimit, InMemoryLoadOptions{})
}

func LoadInMemoryWindowContextWithOptions(ctx context.Context, doc *FileDocument, start, end int64, editableLimit int64, opts InMemoryLoadOptions) (*InMemoryBuffer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, errors.New("nil document")
	}
	if editableLimit <= 0 {
		return nil, errors.New("editable limit must be positive")
	}
	if start < 0 {
		start = 0
	}
	if end > doc.Size() {
		end = doc.Size()
	}
	if end < start {
		end = start
	}
	if end-start > editableLimit {
		return nil, fmt.Errorf("%w: window size %d > limit %d", ErrInMemoryBufferTooLarge, end-start, editableLimit)
	}
	meta := doc.Metadata()
	if meta.Binary {
		return nil, fmt.Errorf("%w: confidence %.2f", ErrInMemoryBufferBinary, meta.BinaryConfidence)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reportInMemoryLoadProgress(opts, InMemoryLoadProgress{
		Stage:         InMemoryLoadStageReading,
		Start:         start,
		End:           end,
		Bytes:         end - start,
		EditableLimit: editableLimit,
	})
	data, err := doc.ReadRangeWithLimit(start, end, editableLimit)
	if err != nil {
		return nil, err
	}
	return newInMemoryBufferContext(ctx, doc, start, end, editableLimit, data, opts)
}

func newInMemoryBuffer(doc *FileDocument, start, end, editableLimit int64, data []byte) (*InMemoryBuffer, error) {
	return newInMemoryBufferContext(context.Background(), doc, start, end, editableLimit, data, InMemoryLoadOptions{})
}

func newInMemoryBufferContext(ctx context.Context, doc *FileDocument, start, end, editableLimit int64, data []byte, opts InMemoryLoadOptions) (*InMemoryBuffer, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	meta := doc.Metadata()
	reportInMemoryLoadProgress(opts, InMemoryLoadProgress{
		Stage:         InMemoryLoadStageDecoding,
		Start:         start,
		End:           end,
		Bytes:         int64(len(data)),
		EditableLimit: editableLimit,
	})
	text, err := encodingx.DecodeBytes(meta.Encoding, data)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &InMemoryBuffer{
		Path:          doc.Path(),
		OriginalSize:  doc.Size(),
		WindowStart:   start,
		WindowEnd:     end,
		EditableLimit: editableLimit,
		Encoding:      meta.Encoding,
		LineEnding:    meta.LineEnding,
		FileType:      meta.FileType,
		Text:          text,
	}, nil
}

func reportInMemoryLoadProgress(opts InMemoryLoadOptions, progress InMemoryLoadProgress) {
	if opts.Progress != nil {
		opts.Progress(progress)
	}
}

func (b *InMemoryBuffer) CoversWholeFile() bool {
	return b != nil && b.WindowStart == 0 && b.WindowEnd == b.OriginalSize
}

// EncodedBytes returns the buffer text encoded with the file's detected source
// encoding so save-copy output remains compatible with the opened file.
func (b *InMemoryBuffer) EncodedBytes(text string) ([]byte, error) {
	if b == nil {
		return nil, errors.New("nil in-memory buffer")
	}
	return encodingx.EncodeString(b.Encoding, text)
}

// EncodedReplacement returns the source byte range and encoded replacement
// bytes represented by the current buffer text. Callers can stage this range
// through the manual-edit pipeline instead of rewriting the source in place.
func (b *InMemoryBuffer) EncodedReplacement(text string) (int64, int64, []byte, error) {
	if b == nil {
		return 0, 0, nil, errors.New("nil in-memory buffer")
	}
	encoded, err := b.EncodedBytes(text)
	if err != nil {
		return 0, 0, nil, err
	}
	if int64(len(encoded)) > b.EditableLimit {
		return 0, 0, nil, fmt.Errorf("%w: encoded replacement size %d > limit %d", ErrInMemoryBufferTooLarge, len(encoded), b.EditableLimit)
	}
	return b.WindowStart, b.WindowEnd, encoded, nil
}

// WriteCopy writes edited buffer text to a user-selected output path. It refuses
// to write over the source path; replacing originals must go through Novera's
// manifest/backup/finalize workflows.
func (b *InMemoryBuffer) WriteCopy(outputPath string, text string) (int64, error) {
	return b.WriteCopyWithOptions(outputPath, text, WriteCopyOptions{})
}

// WriteCopyContext is the cancellation-aware form of WriteCopy. Cancellation is
// checked before encoding and before the atomic publish starts; once the publish
// begins, the fileio layer is allowed to finish or fail atomically.
func (b *InMemoryBuffer) WriteCopyContext(ctx context.Context, outputPath string, text string) (int64, error) {
	return b.WriteCopyContextWithOptions(ctx, outputPath, text, WriteCopyOptions{})
}

// WriteCopyWithOptions writes edited buffer text through Novera's safe-output
// path: encode, write an exclusive temp file, fsync/close it, and publish it
// with a final rename. The default policy refuses to overwrite existing files.
func (b *InMemoryBuffer) WriteCopyWithOptions(outputPath string, text string, opts WriteCopyOptions) (int64, error) {
	return b.WriteCopyContextWithOptions(context.Background(), outputPath, text, opts)
}

// WriteCopyContextWithOptions writes a full in-memory buffer copy with
// cancellation checkpoints around the potentially expensive encode and publish
// boundary while preserving the existing atomic-output guarantees.
func (b *InMemoryBuffer) WriteCopyContextWithOptions(ctx context.Context, outputPath string, text string, opts WriteCopyOptions) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if b == nil {
		return 0, errors.New("nil in-memory buffer")
	}
	if !b.CoversWholeFile() {
		return 0, errors.New("window buffers cannot be saved as whole-file copies; stage the bounded replacement instead")
	}
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		return 0, errors.New("output path is required")
	}
	same, err := fileio.SamePath(outputPath, b.Path)
	if err != nil {
		return 0, err
	}
	if same {
		return 0, errors.New("in-memory editor writes a copy; choose an output path different from the source file")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	encoded, err := b.EncodedBytes(text)
	if err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(b.Path); err == nil {
		mode = info.Mode().Perm()
	}
	summary, err := fileio.WriteFileAtomic(outputPath, encoded, fileio.AtomicWriteOptions{
		Mode:      mode,
		Overwrite: opts.Overwrite,
	})
	if err != nil {
		return 0, err
	}
	return summary.BytesWritten, nil
}
