package document

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/lineindex"
	"novera/internal/bigfile/newlines"
	"novera/internal/bigfile/regularfile"
)

const openSampleSize = 1024 * 1024
const priorityIndexWindowSize = 4 * 1024 * 1024

// A cache hit requires a full-source digest before its sparse anchors can be
// trusted. Keep that work bounded during OpenFile; larger sources defer both
// the digest and cache hydration until StartIndexing is requested explicitly.
const synchronousIndexCacheMaxSourceSize = 16 << 20
const defaultReadRangeMaxBytes = 64 * 1024 * 1024
const exactScanChunkSize = 1024 * 1024

var errDocumentClosed = errors.New("document is closed")
var ErrReadRangeTooLarge = errors.New("document read range exceeds maximum bounded read")
var ErrSourceChanged = errors.New("opened source changed during the operation")
var ErrVisiblePageLimit = errors.New("visible page request exceeds the bounded viewport limits")

const (
	maxVisiblePageLines   = 10_000
	maxVisiblePageBytes   = 16 * 1024 * 1024
	maxVisibleLineBytes   = 1 * 1024 * 1024
	maxLongLineProbeBytes = 16 * 1024 * 1024
)

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
	f, err := regularfile.Open(path)
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
		if indexCacheCandidateExists(path) {
			sampleHash, err = computeIndexSourceHashContext(ctx, f, st.Size())
			if err != nil {
				return closeOnError(err)
			}
			if err := ctx.Err(); err != nil {
				return closeOnError(err)
			}
			if cached, ok := loadIndexCacheSnapshot(path, st.Size(), st.ModTime(), sampleHash, idx.EveryLines); ok {
				if err := idx.RestoreSnapshotOwned(cached); err == nil {
					report(OpenProgress{Stage: OpenStageIndexCacheLoaded, Size: st.Size(), SampleBytes: len(sample), CacheLoaded: true})
				}
			}
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

func (d *FileDocument) hydrateIndexCache(ctx context.Context) bool {
	if d == nil || d.idx == nil || d.idx.Done() {
		return false
	}
	if !indexCacheCandidateExists(d.path) {
		return false
	}
	sampleHash, err := d.ensureIndexSampleHash(ctx)
	if err != nil {
		return false
	}
	cached, ok := loadIndexCacheSnapshot(d.path, d.size, d.mtime, sampleHash, d.idx.EveryLines)
	if !ok {
		return false
	}
	if err := d.ValidateUnchanged(); err != nil {
		return false
	}
	return d.idx.RestoreSnapshotOwned(cached) == nil
}

func (d *FileDocument) ensureIndexSampleHash(ctx context.Context) (string, error) {
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
	digest, err := computeIndexSourceHashContext(ctx, f, size)
	if err != nil {
		return "", err
	}
	d.indexSampleHash = digest
	return d.indexSampleHash, nil
}

func (d *FileDocument) saveIndexCache() {
	if d == nil {
		return
	}
	sampleHash, err := d.ensureIndexSampleHash(context.Background())
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
	if d.hydrateIndexCache(ctx) {
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
	scanner := newlines.New(d.meta.Encoding)
	currentLine := startLine
	readOffset := startOffset
	emitThroughOffset := func(br newlines.Break) bool {
		if br.End <= offset {
			currentLine++
		}
		return true
	}

	for readOffset < offset {
		want := len(buf)
		if remaining := offset - readOffset; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := d.ReadAt(buf[:want], readOffset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return 0, false, readErr
		}
		if n != want {
			return 0, false, io.ErrUnexpectedEOF
		}
		scanner.Scan(buf[:n], readOffset, emitThroughOffset)
		readOffset += int64(n)
		if errors.Is(readErr, io.EOF) || n == 0 {
			break
		}
	}
	if readOffset >= d.size {
		scanner.Finish(emitThroughOffset)
	} else if readOffset == offset {
		lookaheadEnd := offset + 4
		if lookaheadEnd < offset || lookaheadEnd > d.size {
			lookaheadEnd = d.size
		}
		if lookaheadEnd > offset {
			lookahead := make([]byte, lookaheadEnd-offset)
			n, readErr := d.ReadAt(lookahead, offset)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return 0, false, readErr
			}
			if n != len(lookahead) {
				return 0, false, io.ErrUnexpectedEOF
			}
			scanner.Scan(lookahead, offset, emitThroughOffset)
		}
	}

	d.rememberExactOffsetLine(offset, currentLine)
	return currentLine, true, nil
}

// ExactOffsetToLineWithin returns the exact line only when the complete scan
// from the nearest sparse anchor, including fixed newline lookahead, fits
// within maxScanBytes. A false ok result performs no source scan.
func (d *FileDocument) ExactOffsetToLineWithin(offset int64, maxScanBytes int64) (line int64, ok bool, err error) {
	if maxScanBytes < 0 {
		return 0, false, errors.New("exact offset scan byte budget must not be negative")
	}
	if !d.idx.Done() {
		return 0, false, nil
	}
	offset = d.ClampOffset(offset)
	entry, found := d.idx.FloorOffsetEntry(offset)
	if !found {
		return 0, false, nil
	}
	if offset <= entry.Offset {
		return entry.Line, true, nil
	}
	scanBytes := offset - entry.Offset
	lookaheadBytes := int64(0)
	if offset < d.size {
		lookaheadEnd := offset + 4
		if lookaheadEnd < offset || lookaheadEnd > d.size {
			lookaheadEnd = d.size
		}
		lookaheadBytes = lookaheadEnd - offset
	}
	if scanBytes > maxScanBytes || lookaheadBytes > maxScanBytes-scanBytes {
		return 0, false, nil
	}
	return d.ExactOffsetToLine(offset)
}

func (d *FileDocument) ApproxLineToOffset(line int64) (offset int64, ok bool) {
	entry, ok := d.idx.ApproxLineToOffset(line)
	if !ok {
		return 0, false
	}
	return entry.Offset, true
}

// LineStartLookupStatus describes why a line-to-offset lookup did or did not
// produce the requested exact line start.
type LineStartLookupStatus uint8

const (
	// LineStartLookupPending means the sparse index was not complete. Result may
	// still contain the nearest currently known index position.
	LineStartLookupPending LineStartLookupStatus = iota
	// LineStartLookupExact means Result.Line and Result.Offset are the requested
	// exact line start.
	LineStartLookupExact
	// LineStartLookupAbsent means a completed index proves the requested line is
	// beyond the final logical line.
	LineStartLookupAbsent
	// LineStartLookupLimited means the requested line may exist, but the exact
	// scan stopped at its byte budget or cancellation boundary. Result may contain
	// the nearest line start that was proven while scanning from the sparse index.
	LineStartLookupLimited
)

// LineStartLookupResult preserves a usable offset zero by separating position
// availability from Offset. For an exact result, Line is the requested line.
// For a pending or limited result, Line is the actual known line at Offset.
type LineStartLookupResult struct {
	Status      LineStartLookupStatus
	Line        int64
	Offset      int64
	HasPosition bool
}

// ExactLineToOffset returns a line start offset after the full sparse index is
// ready. It is the compatibility API for callers that explicitly permit an
// unlimited exact scan; latency-sensitive RPCs must use LookupLineStart.
func (d *FileDocument) ExactLineToOffset(line int64) (offset int64, ok bool, err error) {
	if line <= 0 || !d.idx.Done() {
		return 0, false, nil
	}
	result, err := d.lookupLineStart(context.Background(), line, -1, d.ReadAt)
	if err != nil {
		return 0, false, err
	}
	if result.Status != LineStartLookupExact {
		return 0, false, nil
	}
	return result.Offset, true, nil
}

// LookupLineStart resolves a positive 1-based line under a hard source-read
// budget. It distinguishes an exact result, a line proven absent by a complete
// index, an incomplete index, and a budget/cancellation fallback. A canceled
// scan returns its honest fallback result together with ctx.Err().
func (d *FileDocument) LookupLineStart(ctx context.Context, line int64, maxScanBytes int64) (LineStartLookupResult, error) {
	if maxScanBytes < 0 {
		return LineStartLookupResult{}, errors.New("exact line scan byte budget must not be negative")
	}
	return d.lookupLineStart(ctx, line, maxScanBytes, d.ReadAtValidated)
}

// lookupLineStart uses maxScanBytes < 0 only for ExactLineToOffset's
// compatibility path. readAt is injected so focused tests can prove the hard
// byte ceiling and cancellation behavior without production hooks.
func (d *FileDocument) lookupLineStart(ctx context.Context, line int64, maxScanBytes int64, readAt func([]byte, int64) (int, error)) (LineStartLookupResult, error) {
	if line <= 0 {
		return LineStartLookupResult{}, errors.New("line number must be positive")
	}
	if maxScanBytes < -1 {
		return LineStartLookupResult{}, errors.New("exact line scan byte budget must not be less than -1")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if line == 1 {
		return LineStartLookupResult{Status: LineStartLookupExact, Line: 1, Offset: 0, HasPosition: true}, nil
	}

	progress := d.idx.Progress()
	entry, hasEntry := d.idx.ApproxLineToOffset(line)
	pending := LineStartLookupResult{Status: LineStartLookupPending}
	if hasEntry {
		pending.Line = entry.Line
		pending.Offset = entry.Offset
		pending.HasPosition = true
	}
	if !progress.Done {
		return pending, nil
	}

	// Progress.Lines is the completed count of logical newline breaks. Therefore
	// the last valid 1-based line is Lines+1, including an empty line at EOF after
	// a trailing newline. This proves a missing request without source I/O.
	if line-1 > progress.Lines {
		return LineStartLookupResult{Status: LineStartLookupAbsent}, nil
	}
	if !hasEntry {
		return LineStartLookupResult{}, errors.New("completed line index has no line-start anchor")
	}
	if entry.Line == line {
		d.rememberExactLineStart(line, entry.Offset)
		return LineStartLookupResult{Status: LineStartLookupExact, Line: line, Offset: entry.Offset, HasPosition: true}, nil
	}

	startLine := entry.Line
	startOffset := entry.Offset
	if memo, ok := d.exactLineStartMemoFor(line, entry); ok {
		startLine = memo.Line
		startOffset = memo.Offset
	}
	if startLine == line {
		return LineStartLookupResult{Status: LineStartLookupExact, Line: line, Offset: startOffset, HasPosition: true}, nil
	}
	if startOffset < 0 || startOffset > d.size {
		return LineStartLookupResult{}, errors.New("completed line index has an out-of-range line-start anchor")
	}

	fallback := LineStartLookupResult{
		Status:      LineStartLookupLimited,
		Line:        startLine,
		Offset:      startOffset,
		HasPosition: true,
	}
	if err := ctx.Err(); err != nil {
		return fallback, err
	}
	if maxScanBytes == 0 {
		return fallback, nil
	}

	scanEnd := d.size
	if maxScanBytes >= 0 && maxScanBytes < d.size-startOffset {
		scanEnd = startOffset + maxScanBytes
	}

	bufPtr := takeExactScanBuffer()
	defer releaseExactScanBuffer(bufPtr)
	buf := *bufPtr
	scanner := newlines.New(d.meta.Encoding)
	currentLine := startLine
	readOffset := startOffset
	var foundOffset int64
	emit := func(br newlines.Break) bool {
		currentLine++
		fallback.Line = currentLine
		fallback.Offset = br.End
		if currentLine == line {
			foundOffset = br.End
			return false
		}
		return true
	}

	for readOffset < scanEnd {
		if err := ctx.Err(); err != nil {
			return fallback, err
		}
		want := len(buf)
		if remaining := scanEnd - readOffset; remaining < int64(want) {
			want = int(remaining)
		}
		n, readErr := readAt(buf[:want], readOffset)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return LineStartLookupResult{}, readErr
		}
		if n != want {
			return LineStartLookupResult{}, io.ErrUnexpectedEOF
		}
		if err := ctx.Err(); err != nil {
			return fallback, err
		}
		if !scanner.Scan(buf[:n], readOffset, emit) {
			d.rememberExactLineStart(line, foundOffset)
			return LineStartLookupResult{Status: LineStartLookupExact, Line: line, Offset: foundOffset, HasPosition: true}, nil
		}
		readOffset += int64(n)
		if errors.Is(readErr, io.EOF) || n == 0 {
			break
		}
	}

	if readOffset < d.size {
		return fallback, nil
	}
	if err := ctx.Err(); err != nil {
		return fallback, err
	}
	if !scanner.Finish(emit) {
		d.rememberExactLineStart(line, foundOffset)
		return LineStartLookupResult{Status: LineStartLookupExact, Line: line, Offset: foundOffset, HasPosition: true}, nil
	}

	// A complete index said this line exists, but a full exact scan did not find
	// it. Treat that as a source/index consistency failure, never as absence.
	return LineStartLookupResult{}, fmt.Errorf("%w: completed line index expected line %d before EOF", ErrSourceChanged, line)
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
	scanner := newlines.New(d.meta.Encoding)
	readOffset := startOffset
	var foundOffset int64
	found := false
	emit := func(br newlines.Break) bool {
		currentLine++
		if currentLine == line {
			foundOffset = br.End
			found = true
			return false
		}
		return true
	}

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
		if n != want {
			return 0, false, io.ErrUnexpectedEOF
		}
		scanner.Scan(buf[:n], readOffset, emit)
		if found {
			return foundOffset, true, nil
		}
		readOffset += int64(n)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if !found {
		scanner.Finish(emit)
	}
	if found {
		return foundOffset, true, nil
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

// ReadAtValidated performs one positional read bracketed by retained-handle
// and pathname identity checks. It is intended for user-visible scans whose
// result must not describe a source that visibly changed during the read.
func (d *FileDocument) ReadAtValidated(p []byte, off int64) (int, error) {
	if err := d.beginRead(); err != nil {
		return 0, err
	}
	defer d.endRead()
	if off < 0 {
		return 0, errors.New("negative read offset")
	}
	if err := d.validateHandleStateLocked(); err != nil {
		return 0, err
	}
	n, readErr := d.file.ReadAt(p, off)
	if stateErr := d.validateHandleStateLocked(); stateErr != nil {
		return 0, stateErr
	}
	return n, readErr
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
	if start > d.size {
		start = d.size
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
	if err := d.validateHandleStateLocked(); err != nil {
		if d.cache != nil {
			d.cache.clear()
		}
		return nil, err
	}

	if d.cache == nil {
		buf := make([]byte, end-start)
		n, err := d.file.ReadAt(buf, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if n != len(buf) {
			return nil, io.ErrUnexpectedEOF
		}
		if err := d.validateHandleStateLocked(); err != nil {
			return nil, err
		}
		return buf, nil
	}
	buf, err := d.cache.readRange(start, end, d.readChunkLocked)
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) != end-start {
		return nil, io.ErrUnexpectedEOF
	}
	if err := d.validateHandleStateLocked(); err != nil {
		d.cache.clear()
		return nil, err
	}
	return buf, nil
}

func (d *FileDocument) validateHandleStateLocked() error {
	openedInfo, err := d.file.Stat()
	if err != nil {
		return err
	}
	if openedInfo.Size() != d.size || !openedInfo.ModTime().Equal(d.mtime) {
		return fmt.Errorf(
			"%w: opened size=%d mtime=%s, current size=%d mtime=%s",
			ErrSourceChanged,
			d.size,
			d.mtime.UTC().Format(time.RFC3339Nano),
			openedInfo.Size(),
			openedInfo.ModTime().UTC().Format(time.RFC3339Nano),
		)
	}
	pathInfo, err := os.Stat(d.path)
	if err != nil {
		return fmt.Errorf("%w: inspect current source path: %v", ErrSourceChanged, err)
	}
	if !pathInfo.Mode().IsRegular() || !os.SameFile(openedInfo, pathInfo) {
		return fmt.Errorf("%w: current source path no longer names the opened regular file", ErrSourceChanged)
	}
	if pathInfo.Size() != d.size || !pathInfo.ModTime().Equal(d.mtime) {
		return fmt.Errorf(
			"%w: path size=%d mtime=%s, opened size=%d mtime=%s",
			ErrSourceChanged,
			pathInfo.Size(),
			pathInfo.ModTime().UTC().Format(time.RFC3339Nano),
			d.size,
			d.mtime.UTC().Format(time.RFC3339Nano),
		)
	}
	return nil
}

// ValidateUnchanged checks that the retained descriptor and its current path
// still identify the regular file generation captured at open.
func (d *FileDocument) ValidateUnchanged() error {
	if err := d.beginRead(); err != nil {
		return err
	}
	defer d.endRead()
	return d.validateHandleStateLocked()
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
	if n != len(buf) {
		return nil, io.ErrUnexpectedEOF
	}
	return buf, nil
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
	if count > maxVisiblePageLines {
		return page, fmt.Errorf("%w: line count %d > %d", ErrVisiblePageLimit, count, maxVisiblePageLines)
	}
	if opts.MaxBytes > maxVisiblePageBytes {
		return page, fmt.Errorf("%w: page bytes %d > %d", ErrVisiblePageLimit, opts.MaxBytes, maxVisiblePageBytes)
	}
	if opts.MaxLineBytes > maxVisibleLineBytes {
		return page, fmt.Errorf("%w: line bytes %d > %d", ErrVisiblePageLimit, opts.MaxLineBytes, maxVisibleLineBytes)
	}
	if opts.LongLineLimitBytes > maxLongLineProbeBytes {
		return page, fmt.Errorf("%w: long-line probe %d > %d", ErrVisiblePageLimit, opts.LongLineLimitBytes, maxLongLineProbeBytes)
	}
	if opts.HorizontalByteOffset < 0 {
		return page, fmt.Errorf("%w: horizontal byte offset must be non-negative", ErrVisiblePageLimit)
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
	if opts.FirstLineNumber > math.MaxInt64-int64(count) {
		return page, fmt.Errorf("%w: first line number overflows requested page", ErrVisiblePageLimit)
	}

	end := d.size
	if int64(opts.MaxBytes) <= d.size-offset {
		end = offset + int64(opts.MaxBytes)
	}
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
		br, hasBreak, breakErr := d.firstLineBreakWithLookahead(data[lineStart:], offset+int64(lineStart), offset+int64(len(data)))
		if breakErr != nil {
			return page, breakErr
		}
		if hasBreak {
			lineEnd = int(br.Start - offset)
			hasNewline = true
			newlineWidth = int(br.End - br.Start)
		}

		contentEnd := lineEnd
		contentEnd = trimLineEndingPrefix(data, lineStart, contentEnd, d.meta.Encoding)

		displayStart := lineStart
		if opts.HorizontalByteOffset < contentEnd-lineStart {
			displayStart += opts.HorizontalByteOffset
		} else if opts.HorizontalByteOffset > 0 {
			displayStart = contentEnd
		}
		displayStart = alignVisibleDisplayStart(data, lineStart, displayStart, contentEnd, offset, d.meta.Encoding)
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
				displayEnd = alignVisibleDisplayEnd(data, displayStart, displayEnd, offset, d.meta.Encoding)
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
				displayEnd = alignVisibleDisplayEnd(data, displayStart, displayEnd, offset, d.meta.Encoding)
				if displayEnd == displayStart {
					return page, fmt.Errorf("%w: line byte budget %d cannot hold one complete character", ErrVisiblePageLimit, opts.MaxLineBytes)
				}
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

// firstLineBreakWithLookahead resolves a CR or UTF-16 code unit that begins in
// the bounded data and completes immediately after it. The fixed four-byte
// lookahead affects framing only and is never appended to the visible payload.
func (d *FileDocument) firstLineBreakWithLookahead(data []byte, absoluteStart int64, dataEnd int64) (newlines.Break, bool, error) {
	scanner := newlines.New(d.meta.Encoding)
	var found newlines.Break
	ok := false
	emit := func(br newlines.Break) bool {
		found = br
		ok = true
		return false
	}
	completed := scanner.Scan(data, absoluteStart, emit)
	if !completed || ok {
		return found, ok, nil
	}
	if dataEnd >= d.size {
		scanner.Finish(emit)
		return found, ok, nil
	}
	lookaheadEnd := dataEnd + 4
	if lookaheadEnd < dataEnd || lookaheadEnd > d.size {
		lookaheadEnd = d.size
	}
	lookahead, err := d.ReadRangeWithLimit(dataEnd, lookaheadEnd, 4)
	if err != nil {
		return newlines.Break{}, false, err
	}
	scanner.Scan(lookahead, dataEnd, func(br newlines.Break) bool {
		if br.Start < dataEnd {
			return emit(br)
		}
		return false
	})
	return found, ok, nil
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

	start := offset - 4
	if start < 0 {
		start = 0
	}
	if (d.meta.Encoding == "UTF-16LE" || d.meta.Encoding == "UTF-16BE") && start&1 != 0 {
		start--
	}
	end := offset + 4
	if end < offset || end > d.size {
		end = d.size
	}
	data, err := d.ReadRangeWithLimit(start, end, 8)
	if err != nil {
		return false, err
	}
	scanner := newlines.New(d.meta.Encoding)
	isStart := false
	emit := func(br newlines.Break) bool {
		if br.End == offset {
			isStart = true
			return false
		}
		return true
	}
	completed := scanner.Scan(data, start, emit)
	if completed && end == d.size {
		scanner.Finish(emit)
	}
	return !isStart, nil
}

func (d *FileDocument) findLineStartWithinLimit(offset int64, limit int64) (int64, bool, error) {
	if offset <= 0 {
		return 0, false, nil
	}

	if limit < 0 {
		return offset, true, nil
	}
	start := int64(0)
	if limit < offset {
		start = offset - limit
	}
	if start > 4 {
		start -= 4
	} else {
		start = 0
	}
	if (d.meta.Encoding == "UTF-16LE" || d.meta.Encoding == "UTF-16BE") && start&1 != 0 {
		start--
	}
	end := offset + 4
	if end < offset || end > d.size {
		end = d.size
	}
	data, err := d.ReadRangeWithLimit(start, end, end-start)
	if err != nil {
		return 0, false, err
	}
	scanner := newlines.New(d.meta.Encoding)
	var last newlines.Break
	found := false
	emit := func(br newlines.Break) bool {
		if br.End <= offset {
			last = br
			found = true
		}
		return true
	}
	scanner.Scan(data, start, emit)
	if end == d.size {
		scanner.Finish(emit)
	}
	if found {
		if offset-last.End > limit {
			return last.End, true, nil
		}
		return last.End, false, nil
	}
	if start > 0 {
		return start, true, nil
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

	end := offset + limit
	if end < offset || end > d.size {
		end = d.size
	}
	if end < d.size {
		probeEnd := end + 4
		if probeEnd < end || probeEnd > d.size {
			probeEnd = d.size
		}
		end = probeEnd
	}
	data, err := d.ReadRangeWithLimit(offset, end, end-offset)
	if err != nil {
		return 0, false, err
	}
	scanner := newlines.New(d.meta.Encoding)
	var first newlines.Break
	found := false
	emit := func(br newlines.Break) bool {
		first = br
		found = true
		return false
	}
	completed := scanner.Scan(data, offset, emit)
	if completed && end == d.size {
		scanner.Finish(emit)
	}
	if found {
		if first.Start-offset > limit {
			return first.Start, true, nil
		}
		return first.Start, false, nil
	}
	if end < d.size {
		return end, true, nil
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

func alignVisibleDisplayEnd(data []byte, displayStart, proposedEnd int, absoluteBase int64, encodingName string) int {
	if proposedEnd <= displayStart || proposedEnd >= len(data) {
		return proposedEnd
	}
	switch encodingName {
	case "UTF-8", "":
		for proposedEnd > displayStart && proposedEnd < len(data) && !utf8.RuneStart(data[proposedEnd]) {
			proposedEnd--
		}
	case "UTF-16LE", "UTF-16BE":
		if (absoluteBase+int64(proposedEnd))&1 != 0 {
			proposedEnd--
		}
		if proposedEnd >= displayStart+2 && proposedEnd+2 <= len(data) {
			before := decodeUTF16Unit(data[proposedEnd-2:proposedEnd], encodingName)
			after := decodeUTF16Unit(data[proposedEnd:proposedEnd+2], encodingName)
			if before >= 0xD800 && before <= 0xDBFF && after >= 0xDC00 && after <= 0xDFFF {
				proposedEnd -= 2
			}
		}
	}
	return proposedEnd
}

func alignVisibleDisplayStart(data []byte, lineStart, proposedStart, contentEnd int, absoluteBase int64, encodingName string) int {
	if proposedStart <= lineStart || proposedStart >= contentEnd {
		return proposedStart
	}
	switch encodingName {
	case "UTF-8", "":
		for proposedStart < contentEnd && !utf8.RuneStart(data[proposedStart]) {
			proposedStart++
		}
	case "UTF-16LE", "UTF-16BE":
		if (absoluteBase+int64(proposedStart))&1 != 0 {
			proposedStart++
		}
		if proposedStart >= lineStart+2 && proposedStart+2 <= contentEnd {
			before := decodeUTF16Unit(data[proposedStart-2:proposedStart], encodingName)
			at := decodeUTF16Unit(data[proposedStart:proposedStart+2], encodingName)
			if before >= 0xD800 && before <= 0xDBFF && at >= 0xDC00 && at <= 0xDFFF {
				proposedStart += 2
			}
		}
	}
	if proposedStart > contentEnd {
		return contentEnd
	}
	return proposedStart
}

func decodeUTF16Unit(data []byte, encodingName string) uint16 {
	if len(data) < 2 {
		return 0
	}
	if encodingName == "UTF-16BE" {
		return uint16(data[0])<<8 | uint16(data[1])
	}
	return uint16(data[1])<<8 | uint16(data[0])
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
		br, found := newlines.First(d.meta.Encoding, data, start, end >= d.size)
		if !found {
			return
		}
		consumed := br.End - start
		lineOffset = br.End
		data = data[consumed:]
		lineNumber++
	}

	if len(data) == 0 {
		return
	}
	if (lineNumber-1)%d.idx.EveryLines == 0 {
		d.idx.AddPriorityEntry(lineindex.Entry{Line: lineNumber, Offset: lineOffset})
	}

	currentLine := lineNumber
	scanner := newlines.New(d.meta.Encoding)
	scanner.Scan(data, lineOffset, func(br newlines.Break) bool {
		currentLine++
		if (currentLine-1)%d.idx.EveryLines == 0 {
			d.idx.AddPriorityEntry(lineindex.Entry{Line: currentLine, Offset: br.End})
		}
		return true
	})
	if end >= d.size {
		scanner.Finish(func(br newlines.Break) bool {
			currentLine++
			if (currentLine-1)%d.idx.EveryLines == 0 {
				d.idx.AddPriorityEntry(lineindex.Entry{Line: currentLine, Offset: br.End})
			}
			return true
		})
	}

	d.priorityMu.Lock()
	d.priorityWindowLo = start
	d.priorityWindowHi = end
	d.priorityMu.Unlock()
}

func (d *FileDocument) startPriorityIndexWorker() {
	d.priorityOnce.Do(func() {
		d.priorityWG.Go(func() {
			d.runPriorityIndexWorker()
		})
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
	scanner := newlines.New(d.meta.Encoding)
	scanner.Scan(data, entry.Offset, func(newlines.Break) bool {
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
