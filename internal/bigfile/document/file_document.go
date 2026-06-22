package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/lineindex"
)

const openSampleSize = 1024 * 1024
const priorityIndexWindowSize = 4 * 1024 * 1024
const synchronousIndexCacheMaxSourceSize = 1 << 30
const defaultReadRangeMaxBytes = 64 * 1024 * 1024
const exactScanChunkSize = 1024 * 1024

var errDocumentClosed = errors.New("document is closed")
var ErrReadRangeTooLarge = errors.New("document read range exceeds maximum bounded read")

var exactScanBufferPool = sync.Pool{
	New: func() any {
		buf := make([]byte, exactScanChunkSize)
		return &buf
	},
}

type OpenStage string

const (
	OpenStageOpening            OpenStage = "opening"
	OpenStageStat               OpenStage = "stat"
	OpenStageMetadataSample     OpenStage = "metadata-sample"
	OpenStageIndexCacheCheck    OpenStage = "index-cache-check"
	OpenStageIndexCacheLoaded   OpenStage = "index-cache-loaded"
	OpenStageIndexCacheDeferred OpenStage = "index-cache-deferred"
	OpenStageMetadataDetected   OpenStage = "metadata-detected"
	OpenStageReady              OpenStage = "ready"
)

type OpenOptions struct {
	Progress func(OpenProgress)
}

type OpenProgress struct {
	Stage       OpenStage
	Path        string
	Size        int64
	SampleBytes int
	CacheLoaded bool
}

// FileDocument exposes a huge file through bounded reads.
type FileDocument struct {
	path  string
	file  *os.File
	size  int64
	mtime time.Time
	meta  Metadata
	idx   *lineindex.Index
	cache *chunkCache

	indexSampleHashMu sync.Mutex
	indexSampleHash   string

	priorityMu       sync.Mutex
	priorityWindowLo int64
	priorityWindowHi int64
	priorityRequests chan int64
	priorityDone     chan struct{}
	priorityOnce     sync.Once
	priorityWG       sync.WaitGroup

	lifecycleMu sync.RWMutex
	closed      bool

	exactMemoMu        sync.Mutex
	exactOffsetMemo    exactOffsetLineMemo
	exactLineStartMemo exactLineStartMemo
}

type exactOffsetLineMemo struct {
	Valid  bool
	Offset int64
	Line   int64
}

type exactLineStartMemo struct {
	Valid  bool
	Line   int64
	Offset int64
}

// OpenFile opens a file without reading it fully.
func OpenFile(path string) (*FileDocument, error) {
	return OpenFileWithOptions(path, OpenOptions{})
}

// OpenFileContext opens a file without reading it fully and stops at bounded
// open-stage boundaries when the caller cancels the operation.
func OpenFileContext(ctx context.Context, path string) (*FileDocument, error) {
	return OpenFileContextWithOptions(ctx, path, OpenOptions{})
}

// OpenFileWithOptions opens a file without reading it fully and reports bounded
// metadata/cache stages for UI status and future async orchestration.
func OpenFileWithOptions(path string, options OpenOptions) (*FileDocument, error) {
	return OpenFileContextWithOptions(context.Background(), path, options)
}

// OpenFileContextWithOptions opens a file without reading it fully and reports
// bounded metadata/cache stages for UI status and async orchestration.
func OpenFileContextWithOptions(ctx context.Context, path string, options OpenOptions) (*FileDocument, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	report := func(progress OpenProgress) {
		if options.Progress != nil {
			if progress.Path == "" {
				progress.Path = path
			}
			options.Progress(progress)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report(OpenProgress{Stage: OpenStageOpening})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	f = applyReadHint(f, path) // widen OS read-ahead for sequential full-file passes
	closeOnError := func(err error) (*FileDocument, error) {
		_ = f.Close()
		return nil, err
	}

	report(OpenProgress{Stage: OpenStageStat})
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}
	st, err := f.Stat()
	if err != nil {
		return closeOnError(err)
	}
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}

	report(OpenProgress{Stage: OpenStageMetadataSample, Size: st.Size()})
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}
	sample, err := readSample(f, st.Size(), openSampleSize)
	if err != nil {
		return closeOnError(err)
	}
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}

	sampleHash := ""
	idx := lineindex.New(lineindex.EveryLinesForSize(st.Size()))
	if shouldLoadIndexCacheSynchronously(st.Size()) {
		report(OpenProgress{Stage: OpenStageIndexCacheCheck, Size: st.Size(), SampleBytes: len(sample)})
		if err := ctx.Err(); err != nil {
			return closeOnError(err)
		}
		sampleHash = computeIndexSampleHash(f, st.Size(), sample)
		if err := ctx.Err(); err != nil {
			return closeOnError(err)
		}
		if cached, ok := loadIndexCache(path, st.Size(), st.ModTime(), sampleHash); ok {
			idx = cached
			report(OpenProgress{Stage: OpenStageIndexCacheLoaded, Size: st.Size(), SampleBytes: len(sample), CacheLoaded: true})
		}
	} else {
		report(OpenProgress{Stage: OpenStageIndexCacheDeferred, Size: st.Size(), SampleBytes: len(sample)})
	}
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}

	report(OpenProgress{Stage: OpenStageMetadataDetected, Size: st.Size(), SampleBytes: len(sample)})
	if err := ctx.Err(); err != nil {
		return closeOnError(err)
	}
	doc := &FileDocument{
		path:             path,
		file:             f,
		size:             st.Size(),
		mtime:            st.ModTime(),
		meta:             detectMetadata(path, st.Size(), sample),
		idx:              idx,
		indexSampleHash:  sampleHash,
		priorityRequests: make(chan int64, 1),
		priorityDone:     make(chan struct{}),
		cache: newChunkCache(
			defaultChunkCacheChunkSize,
			currentCacheMaxBytes(),
		),
	}

	report(OpenProgress{Stage: OpenStageReady, Size: st.Size(), SampleBytes: len(sample), CacheLoaded: idx.Done()})
	return doc, nil
}

func shouldLoadIndexCacheSynchronously(sourceSize int64) bool {
	return sourceSize <= synchronousIndexCacheMaxSourceSize
}

func (d *FileDocument) hydrateIndexCache() bool {
	if d == nil || d.idx == nil || d.idx.Done() {
		return false
	}
	sampleHash, err := d.ensureIndexSampleHash()
	if err != nil {
		return false
	}
	if cached, ok := loadIndexCache(d.path, d.size, d.mtime, sampleHash); ok {
		d.idx = cached
		return true
	}
	return false
}

func (d *FileDocument) ensureIndexSampleHash() (string, error) {
	if d == nil {
		return "", errDocumentClosed
	}
	d.indexSampleHashMu.Lock()
	defer d.indexSampleHashMu.Unlock()
	if d.indexSampleHash != "" {
		return d.indexSampleHash, nil
	}
	d.lifecycleMu.RLock()
	f := d.file
	closed := d.closed || f == nil
	size := d.size
	d.lifecycleMu.RUnlock()
	if closed {
		return "", errDocumentClosed
	}
	sample, err := readSample(f, size, openSampleSize)
	if err != nil {
		return "", err
	}
	d.indexSampleHash = computeIndexSampleHash(f, size, sample)
	return d.indexSampleHash, nil
}

func (d *FileDocument) saveIndexCache() {
	if d == nil {
		return
	}
	sampleHash, err := d.ensureIndexSampleHash()
	if err != nil {
		return
	}
	_ = saveIndexCache(d.path, d.size, d.mtime, sampleHash, d.idx)
}

func (d *FileDocument) Path() string {
	return d.path
}

func (d *FileDocument) Size() int64 {
	return d.size
}

func (d *FileDocument) Metadata() Metadata {
	return d.meta
}

func (d *FileDocument) IndexProgress() lineindex.Progress {
	return d.idx.Progress()
}

func (d *FileDocument) StartIndexing(ctx context.Context) error {
	if d.idx.Done() {
		return nil
	}
	if d.hydrateIndexCache() {
		return nil
	}
	if d.size == 0 {
		d.idx.MarkDone(0, 0)
		d.saveIndexCache()
		return nil
	}
	if err := d.idx.BuildWithEncoding(ctx, io.NewSectionReader(d, 0, d.size), d.meta.Encoding); err != nil {
		return err
	}
	d.saveIndexCache()
	return nil
}

// RequestPriorityIndex seeds approximate sparse anchors near the active viewport.
func (d *FileDocument) RequestPriorityIndex(offset int64) {
	if d == nil || d.isClosed() || d.idx.Done() || d.size == 0 {
		return
	}
	offset = d.ClampOffset(offset)
	d.priorityMu.Lock()
	if offset >= d.priorityWindowLo && offset < d.priorityWindowHi {
		d.priorityMu.Unlock()
		return
	}
	d.priorityMu.Unlock()
	d.startPriorityIndexWorker()
	coalescePriorityIndexRequest(d.priorityRequests, d.priorityDone, offset)
}

func (d *FileDocument) ApproxOffsetToLine(offset int64) (line int64, ok bool) {
	entry, ok := d.idx.ApproxOffsetToLine(offset)
	if !ok {
		return 0, false
	}
	return entry.Line, true
}

// ExactOffsetToLine returns the exact line containing offset after indexing completes.
func (d *FileDocument) ExactOffsetToLine(offset int64) (line int64, ok bool, err error) {
	if !d.idx.Done() {
		return 0, false, nil
	}
	offset = d.ClampOffset(offset)
	entry, ok := d.idx.FloorOffsetEntry(offset)
	if !ok {
		return 0, false, nil
	}
	if offset <= entry.Offset {
		return entry.Line, true, nil
	}
	startLine := entry.Line
	startOffset := entry.Offset
	if memo, ok := d.exactOffsetMemoFor(offset, entry); ok {
		startLine = memo.Line
		startOffset = memo.Offset
	}

	bufPtr := takeExactScanBuffer()
	defer releaseExactScanBuffer(bufPtr)
	buf := *bufPtr
	carry := make([]byte, 0, lineBreakOverlap(d.meta.Encoding))
	currentLine := startLine
	readOffset := startOffset

	for readOffset < offset {
		want := len(buf)
		if remaining := offset - readOffset; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := d.ReadAt(buf[:want], readOffset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return 0, false, readErr
		}
		carry = scanLineBreakOffsets(d.meta.Encoding, carry, buf[:n], readOffset, func(_ int64) bool {
			currentLine++
			return true
		})
		readOffset += int64(n)
		if errors.Is(readErr, io.EOF) || n == 0 {
			break
		}
	}

	d.rememberExactOffsetLine(offset, currentLine)
	return currentLine, true, nil
}

func (d *FileDocument) ApproxLineToOffset(line int64) (offset int64, ok bool) {
	entry, ok := d.idx.ApproxLineToOffset(line)
	if !ok {
		return 0, false
	}
	return entry.Offset, true
}

// ExactLineToOffset returns a line start offset after the full sparse index is ready.
func (d *FileDocument) ExactLineToOffset(line int64) (offset int64, ok bool, err error) {
	if line <= 0 || !d.idx.Done() {
		return 0, false, nil
	}
	entry, ok := d.idx.ApproxLineToOffset(line)
	if !ok {
		return 0, false, nil
	}
	if entry.Line == line {
		d.rememberExactLineStart(line, entry.Offset)
		return entry.Offset, true, nil
	}
	startLine := entry.Line
	startOffset := entry.Offset
	if memo, ok := d.exactLineStartMemoFor(line, entry); ok {
		startLine = memo.Line
		startOffset = memo.Offset
	}

	bufPtr := takeExactScanBuffer()
	defer releaseExactScanBuffer(bufPtr)
	buf := *bufPtr
	carry := make([]byte, 0, lineBreakOverlap(d.meta.Encoding))
	currentLine := startLine
	readOffset := startOffset

	for readOffset < d.size {
		want := len(buf)
		if remaining := d.size - readOffset; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := d.ReadAt(buf[:want], readOffset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return 0, false, readErr
		}
		var foundOffset int64
		found := false
		carry = scanLineBreakOffsets(d.meta.Encoding, carry, buf[:n], readOffset, func(nextOffset int64) bool {
			currentLine++
			if currentLine == line {
				foundOffset = nextOffset
				found = true
				return false
			}
			return true
		})
		if found {
			d.rememberExactLineStart(line, foundOffset)
			return foundOffset, true, nil
		}
		readOffset += int64(n)
		if errors.Is(readErr, io.EOF) || n == 0 {
			break
		}
	}

	return 0, false, nil
}

func (d *FileDocument) exactOffsetMemoFor(offset int64, floor lineindex.Entry) (exactOffsetLineMemo, bool) {
	d.exactMemoMu.Lock()
	defer d.exactMemoMu.Unlock()
	memo := d.exactOffsetMemo
	if !memo.Valid {
		return memo, false
	}
	if memo.Offset < floor.Offset || memo.Offset > offset || memo.Line < floor.Line {
		return memo, false
	}
	return memo, true
}

func (d *FileDocument) rememberExactOffsetLine(offset int64, line int64) {
	d.exactMemoMu.Lock()
	defer d.exactMemoMu.Unlock()
	d.exactOffsetMemo = exactOffsetLineMemo{
		Valid:  true,
		Offset: offset,
		Line:   line,
	}
}

func (d *FileDocument) exactLineStartMemoFor(line int64, floor lineindex.Entry) (exactLineStartMemo, bool) {
	d.exactMemoMu.Lock()
	defer d.exactMemoMu.Unlock()
	memo := d.exactLineStartMemo
	if !memo.Valid {
		return memo, false
	}
	if memo.Line < floor.Line || memo.Line > line || memo.Offset < floor.Offset {
		return memo, false
	}
	return memo, true
}

func (d *FileDocument) rememberExactLineStart(line int64, offset int64) {
	d.exactMemoMu.Lock()
	defer d.exactMemoMu.Unlock()
	d.exactLineStartMemo = exactLineStartMemo{
		Valid:  true,
		Line:   line,
		Offset: offset,
	}
}

func takeExactScanBuffer() *[]byte {
	return exactScanBufferPool.Get().(*[]byte)
}

func releaseExactScanBuffer(buf *[]byte) {
	if buf == nil || cap(*buf) < exactScanChunkSize {
		return
	}
	*buf = (*buf)[:exactScanChunkSize]
	exactScanBufferPool.Put(buf)
}

// LineStartOffset returns the byte offset for the requested 1-based line number.
// It can scan forward from the nearest known sparse anchor even if full indexing is not complete.
func (d *FileDocument) LineStartOffset(line int64) (offset int64, ok bool, err error) {
	if line <= 0 {
		return 0, false, nil
	}
	if line == 1 {
		return 0, true, nil
	}

	startOffset := int64(0)
	currentLine := int64(1)
	if entry, found := d.idx.ApproxLineToOffset(line); found {
		startOffset = entry.Offset
		currentLine = entry.Line
		if currentLine == line {
			return startOffset, true, nil
		}
	}

	bufPtr := takeExactScanBuffer()
	defer releaseExactScanBuffer(bufPtr)
	buf := *bufPtr
	carry := make([]byte, 0, lineBreakOverlap(d.meta.Encoding))
	readOffset := startOffset

	for readOffset < d.size {
		want := len(buf)
		if remaining := d.size - readOffset; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := d.ReadAt(buf[:want], readOffset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return 0, false, readErr
		}
		if n == 0 {
			break
		}
		var foundOffset int64
		found := false
		carry = scanLineBreakOffsets(d.meta.Encoding, carry, buf[:n], readOffset, func(nextStart int64) bool {
			currentLine++
			if currentLine == line {
				foundOffset = nextStart
				found = true
				return false
			}
			return true
		})
		if found {
			return foundOffset, true, nil
		}
		readOffset += int64(n)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	return 0, false, nil
}

// LineRangeOffsets returns a half-open byte range spanning startLine through endLine, inclusive.
func (d *FileDocument) LineRangeOffsets(startLine int64, endLine int64) (startOffset int64, endOffset int64, err error) {
	if startLine <= 0 || endLine <= 0 {
		return 0, 0, errors.New("line numbers must be positive")
	}
	if endLine < startLine {
		return 0, 0, errors.New("end line must be greater than or equal to start line")
	}

	startOffset, ok, err := d.LineStartOffset(startLine)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		return 0, 0, errors.New("start line is beyond end of file")
	}
	if endLine == startLine && startOffset >= d.size {
		return 0, 0, errors.New("start line is beyond end of file")
	}

	nextLineOffset, ok, err := d.LineStartOffset(endLine + 1)
	if err != nil {
		return 0, 0, err
	}
	if ok {
		return startOffset, nextLineOffset, nil
	}
	return startOffset, d.size, nil
}

func (d *FileDocument) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("negative offset")
	}
	if err := d.beginRead(); err != nil {
		return 0, err
	}
	defer d.endRead()
	return d.file.ReadAt(p, off)
}

// ReadRange reads [start, end) with bounds checks.
func (d *FileDocument) ReadRange(start, end int64) ([]byte, error) {
	return d.ReadRangeWithLimit(start, end, defaultReadRangeMaxBytes)
}

// ReadRangeWithLimit reads [start, end) after the caller proves the requested
// byte window is within a feature-specific memory budget.
func (d *FileDocument) ReadRangeWithLimit(start, end int64, maxBytes int64) ([]byte, error) {
	if err := d.beginRead(); err != nil {
		return nil, err
	}
	defer d.endRead()

	if start < 0 {
		start = 0
	}
	if end > d.size {
		end = d.size
	}
	if end < start {
		end = start
	}
	if maxBytes <= 0 {
		maxBytes = defaultReadRangeMaxBytes
	}
	if end-start > maxBytes {
		return nil, fmt.Errorf("%w: range %d > limit %d", ErrReadRangeTooLarge, end-start, maxBytes)
	}

	if d.cache == nil {
		buf := make([]byte, end-start)
		n, err := d.file.ReadAt(buf, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		return buf[:n], nil
	}
	return d.cache.readRange(start, end, d.readChunkLocked)
}

func (d *FileDocument) readChunkLocked(start int64, size int) ([]byte, error) {
	if start < 0 {
		start = 0
	}
	if start >= d.size {
		return nil, nil
	}
	end := start + int64(size)
	if end > d.size {
		end = d.size
	}
	if end < start {
		end = start
	}

	buf := make([]byte, end-start)
	n, err := d.file.ReadAt(buf, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}

// VisibleLinesFromOffset returns displayable logical line slices from a byte offset.
// It reads a bounded byte window and truncates individual lines that exceed the
// configured visual line budget.
func (d *FileDocument) VisibleLinesFromOffset(offset int64, count int, opts VisibleLineOptions) ([]VisualLine, error) {
	page, err := d.VisiblePageFromOffset(offset, count, opts)
	if err != nil {
		return nil, err
	}
	return page.Lines, nil
}

// VisiblePageFromOffset returns a bounded page of visual lines and the next byte offset.
func (d *FileDocument) VisiblePageFromOffset(offset int64, count int, opts VisibleLineOptions) (VisiblePage, error) {
	page := VisiblePage{StartOffset: d.ClampOffset(offset), NextOffset: d.ClampOffset(offset)}
	if count <= 0 {
		return page, nil
	}
	offset = page.StartOffset
	if offset >= d.size {
		return page, nil
	}
	if opts.MaxLineBytes <= 0 {
		opts.MaxLineBytes = 4096
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = 64 * 1024
	}
	if opts.MaxBytes < opts.MaxLineBytes {
		opts.MaxBytes = opts.MaxLineBytes
	}
	if opts.FirstLineNumber <= 0 {
		opts.FirstLineNumber = 1
	}
	if opts.HorizontalByteOffset < 0 {
		opts.HorizontalByteOffset = 0
	}

	end := offset + int64(opts.MaxBytes)
	data, err := d.ReadRange(offset, end)
	if err != nil {
		return page, err
	}
	startsInsideLine, err := d.offsetStartsInsideLine(offset)
	if err != nil {
		return page, err
	}

	lines := make([]VisualLine, 0, count)
	lineNumber := opts.FirstLineNumber
	pos := 0

	for len(lines) < count && pos < len(data) {
		lineStart := pos
		lineEnd := len(data)
		hasNewline := false
		newlineWidth := 0
		if rel, width := findLineBreak(data[lineStart:], d.meta.Encoding); rel >= 0 {
			lineEnd = lineStart + rel
			hasNewline = true
			newlineWidth = width
		}

		contentEnd := lineEnd
		contentEnd = trimLineEndingPrefix(data, lineStart, contentEnd, d.meta.Encoding)

		displayStart := lineStart + opts.HorizontalByteOffset
		if opts.HorizontalByteOffset > 0 {
			hasLeftHidden := contentEnd > lineStart || (lineStart == 0 && startsInsideLine)
			hasRightHidden := false
			truncated := false
			if displayStart > contentEnd {
				displayStart = contentEnd
				truncated = true
			}

			displayEnd := contentEnd
			if displayEnd-displayStart > opts.MaxLineBytes {
				displayEnd = displayStart + opts.MaxLineBytes
				truncated = true
				hasRightHidden = true
			}
			if !hasNewline && offset+int64(len(data)) < d.size {
				truncated = true
				hasRightHidden = true
			}
			if displayStart == contentEnd && !hasNewline && offset+int64(len(data)) < d.size {
				hasRightHidden = true
			}

			exceedsRenderLimit := false
			if opts.LongLineLimitBytes > 0 && (truncated || hasLeftHidden || hasRightHidden) {
				exceedsRenderLimit, err = d.lineExceedsByteLimit(offset+int64(lineStart), opts.LongLineLimitBytes)
				if err != nil {
					return page, err
				}
			}

			displayText, displayByteOffsets := decodeVisibleTextWithOffsets(d.meta.Encoding, data[displayStart:displayEnd], offset+int64(displayStart) == 0)
			lines = append(lines, VisualLine{
				LineNumber:             lineNumber,
				Offset:                 offset + int64(displayStart),
				DisplayOffset:          offset + int64(displayStart),
				DisplayEndOffset:       offset + int64(displayEnd),
				DisplayRuneByteOffsets: displayByteOffsets,
				Text:                   displayText,
				Truncated:              truncated,
				HorizontalByteOffset:   opts.HorizontalByteOffset,
				HasLeftHidden:          hasLeftHidden,
				HasRightHidden:         hasRightHidden,
				ExceedsRenderLimit:     exceedsRenderLimit,
				RenderLimitBytes:       opts.LongLineLimitBytes,
			})

			if !hasNewline {
				pos = len(data)
				break
			}
			pos = lineEnd + newlineWidth
			lineNumber++
			continue
		}

		lineContinuesBeyondRead := !hasNewline && offset+int64(len(data)) < d.size
		lineExceedsVisibleWidth := contentEnd-lineStart > opts.MaxLineBytes || lineContinuesBeyondRead
		exceedsRenderLimit := false
		if opts.LongLineLimitBytes > 0 && lineExceedsVisibleWidth {
			exceedsRenderLimit, err = d.lineExceedsByteLimit(offset+int64(lineStart), opts.LongLineLimitBytes)
			if err != nil {
				return page, err
			}
		}

		stopPage := false
		for {
			displayEnd := contentEnd
			if displayEnd-displayStart > opts.MaxLineBytes {
				displayEnd = displayStart + opts.MaxLineBytes
			}
			hasRightHidden := displayEnd < contentEnd || lineContinuesBeyondRead
			hasLeftHidden := displayStart > lineStart || (displayStart == lineStart && lineStart == 0 && startsInsideLine)
			displayText, displayByteOffsets := decodeVisibleTextWithOffsets(d.meta.Encoding, data[displayStart:displayEnd], offset+int64(displayStart) == 0)
			lines = append(lines, VisualLine{
				LineNumber:             lineNumber,
				Offset:                 offset + int64(displayStart),
				DisplayOffset:          offset + int64(displayStart),
				DisplayEndOffset:       offset + int64(displayEnd),
				DisplayRuneByteOffsets: displayByteOffsets,
				Text:                   displayText,
				Truncated:              hasRightHidden,
				HorizontalByteOffset:   opts.HorizontalByteOffset,
				HasLeftHidden:          hasLeftHidden,
				HasRightHidden:         hasRightHidden,
				ExceedsRenderLimit:     exceedsRenderLimit,
				RenderLimitBytes:       opts.LongLineLimitBytes,
			})

			if len(lines) >= count {
				if displayEnd >= contentEnd && hasNewline {
					pos = lineEnd + newlineWidth
				} else {
					pos = displayEnd
				}
				stopPage = true
				break
			}
			if displayEnd >= contentEnd {
				break
			}
			displayStart = displayEnd
		}
		if stopPage {
			break
		}
		if !hasNewline {
			pos = len(data)
			break
		}
		pos = lineEnd + newlineWidth
		lineNumber++
	}

	page.Lines = lines
	page.NextOffset = d.ClampOffset(offset + int64(pos))
	if page.NextOffset == page.StartOffset && page.NextOffset < d.size {
		page.NextOffset = d.ClampOffset(page.StartOffset + int64(len(data)))
	}

	return page, nil
}

func (d *FileDocument) lineExceedsByteLimit(offset int64, limit int) (bool, error) {
	if d == nil || limit <= 0 {
		return false, nil
	}
	offset = d.ClampOffset(offset)

	lineStart, exceedsLeft, err := d.findLineStartWithinLimit(offset, int64(limit))
	if err != nil {
		return false, err
	}
	if exceedsLeft {
		return true, nil
	}

	remaining := int64(limit) - abs64(offset-lineStart)
	lineEnd, exceedsRight, err := d.findLineEndWithinLimit(offset, remaining)
	if err != nil {
		return false, err
	}
	if exceedsRight {
		return true, nil
	}

	return lineEnd-lineStart > int64(limit), nil
}

func (d *FileDocument) offsetStartsInsideLine(offset int64) (bool, error) {
	if d == nil || offset <= 0 {
		return false, nil
	}
	offset = d.ClampOffset(offset)
	if offset <= 0 {
		return false, nil
	}

	switch d.meta.Encoding {
	case "UTF-16LE":
		data, err := d.ReadRange(offset-2, offset)
		if err != nil {
			return false, err
		}
		if len(data) < 2 {
			return false, nil
		}
		return !bytes.Equal(data, []byte{0x0A, 0x00}) && !bytes.Equal(data, []byte{0x0D, 0x00}), nil
	case "UTF-16BE":
		data, err := d.ReadRange(offset-2, offset)
		if err != nil {
			return false, err
		}
		if len(data) < 2 {
			return false, nil
		}
		return !bytes.Equal(data, []byte{0x00, 0x0A}) && !bytes.Equal(data, []byte{0x00, 0x0D}), nil
	default:
		data, err := d.ReadRange(offset-1, offset)
		if err != nil {
			return false, err
		}
		if len(data) == 0 {
			return false, nil
		}
		return data[0] != '\n' && data[0] != '\r', nil
	}
}

func (d *FileDocument) findLineStartWithinLimit(offset int64, limit int64) (int64, bool, error) {
	if offset <= 0 {
		return 0, false, nil
	}

	const chunkSize int64 = 64 * 1024
	scanned := int64(0)
	pos := offset
	for pos > 0 {
		if scanned > limit {
			return 0, true, nil
		}
		start := pos - chunkSize
		if start < 0 {
			start = 0
		}
		data, err := d.ReadRange(start, pos)
		if err != nil {
			return 0, false, err
		}
		if idx, width := findLastLineBreak(data, d.meta.Encoding); idx >= 0 {
			return start + int64(idx+width), false, nil
		}
		scanned += pos - start
		if start == 0 {
			return 0, false, nil
		}
		pos = start
	}

	return 0, false, nil
}

func (d *FileDocument) findLineEndWithinLimit(offset int64, limit int64) (int64, bool, error) {
	if offset >= d.size {
		return d.size, false, nil
	}
	if limit < 0 {
		return offset, true, nil
	}

	const chunkSize int64 = 64 * 1024
	scanned := int64(0)
	pos := offset
	for pos < d.size {
		if scanned > limit {
			return pos, true, nil
		}
		end := pos + chunkSize
		if end > d.size {
			end = d.size
		}
		data, err := d.ReadRange(pos, end)
		if err != nil {
			return 0, false, err
		}
		if idx, _ := findLineBreak(data, d.meta.Encoding); idx >= 0 {
			return pos + int64(idx), false, nil
		}
		scanned += end - pos
		if end == d.size {
			return d.size, false, nil
		}
		pos = end
	}

	return d.size, false, nil
}

// ClampOffset normalizes an offset into the document byte range.
func (d *FileDocument) ClampOffset(offset int64) int64 {
	if offset < 0 {
		return 0
	}
	if offset > d.size {
		return d.size
	}
	return offset
}

func (d *FileDocument) Close() error {
	d.lifecycleMu.Lock()
	if d.closed || d.file == nil {
		d.lifecycleMu.Unlock()
		return nil
	}
	d.closed = true
	close(d.priorityDone)
	d.lifecycleMu.Unlock()

	d.priorityWG.Wait()
	drainPriorityIndexRequests(d.priorityRequests)

	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.file == nil {
		return nil
	}
	err := d.file.Close()
	d.file = nil
	return err
}

func (d *FileDocument) beginRead() error {
	if d == nil {
		return errDocumentClosed
	}
	d.lifecycleMu.RLock()
	if d.closed || d.file == nil {
		d.lifecycleMu.RUnlock()
		return errDocumentClosed
	}
	return nil
}

func (d *FileDocument) endRead() {
	d.lifecycleMu.RUnlock()
}

func (d *FileDocument) isClosed() bool {
	if d == nil {
		return true
	}
	d.lifecycleMu.RLock()
	defer d.lifecycleMu.RUnlock()
	return d.closed || d.file == nil
}

func findLineBreak(data []byte, encodingName string) (index int, width int) {
	switch encodingName {
	case "UTF-16LE":
		lf := bytes.Index(data, []byte{0x0A, 0x00})
		cr := bytes.Index(data, []byte{0x0D, 0x00})
		if lf >= 0 && (cr < 0 || lf <= cr+2) {
			return lf, 2
		}
		if cr >= 0 {
			return cr, 2
		}
	case "UTF-16BE":
		lf := bytes.Index(data, []byte{0x00, 0x0A})
		cr := bytes.Index(data, []byte{0x00, 0x0D})
		if lf >= 0 && (cr < 0 || lf <= cr+2) {
			return lf, 2
		}
		if cr >= 0 {
			return cr, 2
		}
	default:
		lf := bytes.IndexByte(data, '\n')
		cr := bytes.IndexByte(data, '\r')
		if lf >= 0 && (cr < 0 || lf <= cr+1) {
			return lf, 1
		}
		if cr >= 0 {
			return cr, 1
		}
	}
	return -1, 0
}

func lineBreakOverlap(encodingName string) int {
	switch encodingName {
	case "UTF-16LE", "UTF-16BE":
		return 1
	default:
		return 0
	}
}

func scanLineBreakOffsets(encodingName string, carry []byte, data []byte, baseOffset int64, emit func(nextOffset int64) bool) []byte {
	overlap := lineBreakOverlap(encodingName)
	if len(data) == 0 {
		if overlap > len(carry) {
			overlap = len(carry)
		}
		return append(carry[:0], carry[len(carry)-overlap:]...)
	}

	scanData := data
	scanBase := baseOffset
	if overlap > 0 && len(carry) > 0 {
		seam := [2]byte{carry[len(carry)-1], data[0]}
		if idx, width := findLineBreak(seam[:], encodingName); idx == 0 {
			if !emit(baseOffset - int64(len(carry)) + int64(width)) {
				return retainLineBreakCarry(carry, data, overlap)
			}
		}
		scanData = data[1:]
		scanBase = baseOffset + 1
	}

	searchFrom := 0
	for searchFrom < len(scanData) {
		idx, width := findLineBreak(scanData[searchFrom:], encodingName)
		if idx < 0 {
			break
		}
		rel := searchFrom + idx
		if !emit(scanBase + int64(rel+width)) {
			break
		}
		searchFrom = rel + width
	}
	return retainLineBreakCarry(carry, data, overlap)
}

func retainLineBreakCarry(carry []byte, data []byte, overlap int) []byte {
	if overlap <= 0 {
		return carry[:0]
	}
	if overlap > len(data) {
		overlap = len(data)
	}
	return append(carry[:0], data[len(data)-overlap:]...)
}

func findLastLineBreak(data []byte, encodingName string) (index int, width int) {
	switch encodingName {
	case "UTF-16LE":
		lf := bytes.LastIndex(data, []byte{0x0A, 0x00})
		cr := bytes.LastIndex(data, []byte{0x0D, 0x00})
		if lf >= cr && lf >= 0 {
			return lf, 2
		}
		if cr >= 0 {
			return cr, 2
		}
	case "UTF-16BE":
		lf := bytes.LastIndex(data, []byte{0x00, 0x0A})
		cr := bytes.LastIndex(data, []byte{0x00, 0x0D})
		if lf >= cr && lf >= 0 {
			return lf, 2
		}
		if cr >= 0 {
			return cr, 2
		}
	default:
		lf := bytes.LastIndexByte(data, '\n')
		cr := bytes.LastIndexByte(data, '\r')
		if lf >= cr && lf >= 0 {
			return lf, 1
		}
		if cr >= 0 {
			return cr, 1
		}
	}
	return -1, 0
}

func trimLineEndingPrefix(data []byte, lineStart int, contentEnd int, encodingName string) int {
	switch encodingName {
	case "UTF-16LE":
		if contentEnd >= lineStart+2 && bytes.Equal(data[contentEnd-2:contentEnd], []byte{0x0D, 0x00}) {
			return contentEnd - 2
		}
	case "UTF-16BE":
		if contentEnd >= lineStart+2 && bytes.Equal(data[contentEnd-2:contentEnd], []byte{0x00, 0x0D}) {
			return contentEnd - 2
		}
	default:
		if contentEnd > lineStart && data[contentEnd-1] == '\r' {
			return contentEnd - 1
		}
	}
	return contentEnd
}

func decodeVisibleText(encodingName string, data []byte, atFileStart bool) string {
	text, _ := decodeVisibleTextWithOffsets(encodingName, data, atFileStart)
	return text
}

func decodeVisibleTextWithOffsets(encodingName string, data []byte, atFileStart bool) (string, []int) {
	bomLen := 0
	if atFileStart {
		switch encodingName {
		case "UTF-8":
			trimmed := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
			bomLen = len(data) - len(trimmed)
			data = trimmed
		case "UTF-16LE":
			trimmed := bytes.TrimPrefix(data, []byte{0xFF, 0xFE})
			bomLen = len(data) - len(trimmed)
			data = trimmed
		case "UTF-16BE":
			trimmed := bytes.TrimPrefix(data, []byte{0xFE, 0xFF})
			bomLen = len(data) - len(trimmed)
			data = trimmed
		}
	}
	switch encodingName {
	case "UTF-8":
		return decodeUTF8WithOffsets(data, bomLen)
	case "UTF-16LE":
		return decodeUTF16WithOffsets(data, bomLen, true)
	case "UTF-16BE":
		return decodeUTF16WithOffsets(data, bomLen, false)
	default:
		return decodeSingleByteWithOffsets(encodingName, data, bomLen)
	}
}

func decodeUTF8WithOffsets(data []byte, bomLen int) (string, []int) {
	if len(data) == 0 {
		return "", []int{bomLen}
	}
	var b strings.Builder
	offsets := make([]int, 0, len(data)+1)
	offsets = append(offsets, bomLen)
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			offsets = append(offsets, bomLen+i)
			continue
		}
		b.WriteRune(r)
		i += size
		offsets = append(offsets, bomLen+i)
	}
	return b.String(), offsets
}

func decodeUTF16WithOffsets(data []byte, bomLen int, littleEndian bool) (string, []int) {
	if len(data) == 0 {
		return "", []int{bomLen}
	}
	var b strings.Builder
	offsets := []int{bomLen}
	for i := 0; i+1 < len(data); {
		u := uint16(data[i]) | uint16(data[i+1])<<8
		if !littleEndian {
			u = uint16(data[i+1]) | uint16(data[i])<<8
		}
		width := 2
		r := rune(u)
		if utf16.IsSurrogate(r) {
			if i+3 < len(data) {
				v := uint16(data[i+2]) | uint16(data[i+3])<<8
				if !littleEndian {
					v = uint16(data[i+3]) | uint16(data[i+2])<<8
				}
				if decoded := utf16.DecodeRune(r, rune(v)); decoded != utf8.RuneError {
					r = decoded
					width = 4
				} else {
					r = utf8.RuneError
				}
			} else {
				r = utf8.RuneError
			}
		}
		b.WriteRune(r)
		i += width
		offsets = append(offsets, bomLen+i)
	}
	if len(data)%2 == 1 {
		b.WriteRune(utf8.RuneError)
		offsets = append(offsets, bomLen+len(data))
	}
	return b.String(), offsets
}

func decodeSingleByteWithOffsets(encodingName string, data []byte, bomLen int) (string, []int) {
	text := encodingx.DecodeBytesBestEffort(encodingName, data)
	offsets := make([]int, 0, len(data)+1)
	offsets = append(offsets, bomLen)
	for i := 1; i <= len(data); i++ {
		offsets = append(offsets, bomLen+i)
	}
	return text, offsets
}

func (d *FileDocument) seedPriorityIndex(offset int64) {
	d.priorityMu.Lock()
	if d.idx.Done() {
		d.priorityMu.Unlock()
		return
	}

	start := offset - priorityIndexWindowSize/2
	if start < 0 {
		start = 0
	}
	end := start + priorityIndexWindowSize
	if end > d.size {
		end = d.size
	}
	if start >= end {
		d.priorityMu.Unlock()
		return
	}
	if offset >= d.priorityWindowLo && offset < d.priorityWindowHi {
		d.priorityMu.Unlock()
		return
	}
	d.priorityMu.Unlock()

	data, err := d.ReadRange(start, end)
	if err != nil || len(data) == 0 {
		return
	}

	lineNumber, ok := d.prioritySeedLine(start)
	if !ok {
		return
	}

	lineOffset := start
	if start > 0 {
		rel, width := findLineBreak(data, d.meta.Encoding)
		if rel < 0 {
			return
		}
		lineOffset += int64(rel) + int64(width)
		data = data[rel+width:]
		lineNumber++
	}

	if len(data) == 0 {
		return
	}
	if (lineNumber-1)%d.idx.EveryLines == 0 {
		d.idx.AddPriorityEntry(lineindex.Entry{Line: lineNumber, Offset: lineOffset})
	}

	currentLine := lineNumber
	scanLineBreakOffsets(d.meta.Encoding, nil, data, lineOffset, func(nextOffset int64) bool {
		currentLine++
		if (currentLine-1)%d.idx.EveryLines == 0 {
			d.idx.AddPriorityEntry(lineindex.Entry{Line: currentLine, Offset: nextOffset})
		}
		return true
	})

	d.priorityMu.Lock()
	d.priorityWindowLo = start
	d.priorityWindowHi = end
	d.priorityMu.Unlock()
}

func (d *FileDocument) startPriorityIndexWorker() {
	d.priorityOnce.Do(func() {
		d.priorityWG.Add(1)
		go func() {
			defer d.priorityWG.Done()
			d.runPriorityIndexWorker()
		}()
	})
}

func (d *FileDocument) runPriorityIndexWorker() {
	for {
		select {
		case <-d.priorityDone:
			return
		case offset := <-d.priorityRequests:
			var ok bool
			if offset, ok = latestPriorityIndexRequest(d.priorityRequests, d.priorityDone, offset); !ok {
				return
			}
			d.seedPriorityIndex(offset)
		}
	}
}

func coalescePriorityIndexRequest(requests chan int64, done <-chan struct{}, offset int64) {
	select {
	case <-done:
		return
	default:
	}

	select {
	case requests <- offset:
		return
	default:
	}

	select {
	case <-requests:
	default:
	}

	select {
	case requests <- offset:
	case <-done:
	default:
	}
}

func drainPriorityIndexRequests(requests chan int64) {
	for {
		select {
		case <-requests:
		default:
			return
		}
	}
}

func latestPriorityIndexRequest(requests <-chan int64, done <-chan struct{}, offset int64) (int64, bool) {
	for {
		select {
		case <-done:
			return offset, false
		case newer := <-requests:
			offset = newer
		default:
			return offset, true
		}
	}
}

func (d *FileDocument) prioritySeedLine(offset int64) (int64, bool) {
	if offset == 0 {
		return 1, true
	}
	entry, ok := d.idx.FloorOffsetEntry(offset)
	if !ok || abs64(offset-entry.Offset) > priorityIndexWindowSize {
		return 0, false
	}
	if entry.Offset == offset {
		return entry.Line, true
	}
	data, err := d.ReadRange(entry.Offset, offset)
	if err != nil {
		return 0, false
	}
	line := entry.Line
	scanLineBreakOffsets(d.meta.Encoding, nil, data, entry.Offset, func(_ int64) bool {
		line++
		return true
	})
	return line, true
}

func abs64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func readSample(f *os.File, size int64, max int64) ([]byte, error) {
	if size <= 0 {
		return nil, nil
	}
	if size < max {
		max = size
	}
	buf := make([]byte, max)
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return buf[:n], nil
}
