package document

import "io"

// ReaderAtSize is the minimal interface needed by search/index operations.
type ReaderAtSize interface {
	io.ReaderAt
	Size() int64
}

// VisualLine is a line or line-slice ready for rendering.
type VisualLine struct {
	LineNumber             int64
	Offset                 int64
	DisplayOffset          int64
	DisplayEndOffset       int64
	DisplayRuneByteOffsets []int
	Text                   string
	Truncated              bool
	HorizontalByteOffset   int
	HasLeftHidden          bool
	HasRightHidden         bool
	ExceedsRenderLimit     bool
	RenderLimitBytes       int
}

// VisiblePage is a bounded viewport read suitable for virtual rendering.
type VisiblePage struct {
	StartOffset int64
	NextOffset  int64
	Lines       []VisualLine
}

// VisibleLineOptions bounds a viewport read.
type VisibleLineOptions struct {
	MaxBytes             int
	MaxLineBytes         int
	LongLineLimitBytes   int
	FirstLineNumber      int64
	HorizontalByteOffset int
}

// Metadata describes detected file properties.
type Metadata struct {
	Path               string
	Size               int64
	Encoding           string
	EncodingConfidence float64
	LineEnding         string
	FileType           string
	Binary             bool
	BinaryConfidence   float64
}
