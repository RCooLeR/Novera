package bigfile

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/document"
	"novera/internal/bigfile/regexutil"
	"novera/internal/bigfile/search"
	"novera/internal/bigfile/session"
)

// searchTimeout bounds a single find call so a no-match search on a huge file
// cannot run forever. The UI shows "searching…" while it runs.
const searchTimeout = 60 * time.Second
const harvestRegexMatchWindow = 1 << 20

// defaultWindowBytes is the byte budget for one editor window. The editor never
// holds more than a few of these regardless of file size, so a 400 GB file and
// a 4 KB file cost the same in memory.
const defaultWindowBytes = 1 << 20 // 1 MiB

const (
	// maxRowDisplayRunes collapses a long line to this many runes (+ ⋯) so a huge
	// line (mysqldump extended INSERT) shows as one compact row, not a giant
	// CodeMirror line that breaks the viewport.
	maxRowDisplayRunes = 500
	// rowDisplayBytes is how many bytes of each line are read for display before
	// trimming to maxRowDisplayRunes (enough for 500 runes of up to 4 bytes each).
	rowDisplayBytes = 2400
	// windowLineTarget is the number of logical lines a window aims to include.
	windowLineTarget = 2000
)

// FileService exposes streaming, windowed access to very large files. It is
// bound to the frontend by Wails; the whole-file content never crosses the
// bridge — only bounded, line-aligned windows do.
type FileService struct {
	reg *session.Registry

	serviceMu       sync.Mutex
	serviceStopping bool
	serviceStopDone chan struct{}
	serviceStopErr  error

	editMu        sync.Mutex
	preparedEdits map[string]struct{}
	editWorkMu    sync.Mutex

	sqlMu          sync.Mutex
	sqlSummary     map[string]cachedSQLSummary // cached by file id and immutable document generation
	sqlUseSeq      uint64
	csvCursors     csvGridCursorRegistry
	searchRequests searchRequestRegistry

	jobOnce sync.Once
	jobMgr  *jobManager
}

// NewFileService constructs the service with an empty session registry.
func NewFileService() *FileService {
	return &FileService{
		reg:           session.New(),
		preparedEdits: make(map[string]struct{}),
		sqlSummary:    make(map[string]cachedSQLSummary),
	}
}

// FileMeta describes a freshly opened file.
type FileMeta struct {
	FileID   string `json:"fileId"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Encoding string `json:"encoding"`
	Detected string `json:"detected"`
	Binary   bool   `json:"binary"`
	Editable bool   `json:"editable"` // UTF-8/ASCII + LF: in-window editing allowed
}

// Window is a bounded, line-aligned slice of the file decoded to UTF-8, plus
// the per-line global coordinates the editor gutter needs.
type Window struct {
	FileID      string  `json:"fileId"`
	StartByte   int64   `json:"startByte"`   // global byte offset of the first line
	NextByte    int64   `json:"nextByte"`    // feed to GetNextWindow to continue
	Text        string  `json:"text"`        // lines joined with "\n"
	LineOffsets []int64 `json:"lineOffsets"` // global byte offset per line
	LineNumbers []int64 `json:"lineNumbers"` // global line number per line (approx until indexed)
	AtBOF       bool    `json:"atBof"`
	AtEOF       bool    `json:"atEof"`
	Approx      bool    `json:"approx"` // line numbers are approximate (index not ready)
}

// OpenViaDialog shows a native open-file dialog and opens the chosen file. A
// cancelled dialog returns an empty FileMeta (FileID == "") with a nil error.
func (s *FileService) OpenViaDialog() (FileMeta, error) {
	if err := s.ensureServiceRunning(); err != nil {
		return FileMeta{}, err
	}
	path, err := application.Get().Dialog.OpenFile().
		CanChooseFiles(true).
		SetTitle("Open file in Novera").
		AddFilter("All files (*.*)", "*.*").
		AddFilter("Data & dumps (*.sql, *.csv, *.tsv, *.log, *.txt, *.json)", "*.sql;*.csv;*.tsv;*.log;*.txt;*.json").
		PromptForSingleSelection()
	if err != nil {
		return FileMeta{}, err
	}
	if strings.TrimSpace(path) == "" {
		return FileMeta{}, nil // cancelled
	}
	return s.OpenFile(path)
}

// OpenFile opens path, starts background indexing, and returns its metadata.
// Opening a file is intentionally non-mutating: adjacent recovery artifacts
// are left untouched for an explicit, user-confirmed recovery workflow.
func (s *FileService) OpenFile(path string) (FileMeta, error) {
	if err := s.ensureServiceRunning(); err != nil {
		return FileMeta{}, err
	}
	f, err := s.reg.Open(path)
	if err != nil {
		return FileMeta{}, err
	}
	defer f.Release()
	m := f.Doc.Metadata()
	return FileMeta{
		FileID:   f.ID,
		Path:     m.Path,
		Size:     m.Size,
		Encoding: m.Encoding,
		Detected: m.FileType,
		Binary:   m.Binary,
		Editable: !m.Binary && editableEncoding(m.Encoding, m.LineEnding),
	}, nil
}

// CloseFile releases a file's resources.
func (s *FileService) CloseFile(fileID string) error {
	if err := s.ensureServiceRunning(); err != nil {
		return err
	}
	handle, err := s.reg.BeginClose(fileID)
	if err != nil {
		return err
	}
	unblockJobs := s.jobs().blockFile(fileID)
	defer unblockJobs()
	s.cancelSearchRequestsForFile(fileID)
	s.jobs().cancelFile(fileID)
	err = handle.Finish()
	s.releasePreparedEdit(fileID)
	s.invalidateSQLSummary(fileID)
	s.csvCursors.invalidateFile(fileID)
	return err
}

// FileSize re-stats the file on disk and returns its current size. Cheap — used
// to poll a growing file (tail/follow) without reopening it.
func (s *FileService) FileSize(fileID string) (int64, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return 0, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	st, err := f.Doc.CurrentFileState()
	if err != nil {
		return 0, err
	}
	return st.Size, nil
}

// RefreshFile reloads the file from disk under the same id (fresh size + line
// index), for following a growing file or picking up external changes. It
// fails while edits are staged; discarding them requires the explicit
// DiscardEdits operation.
func (s *FileService) RefreshFile(fileID string) (FileMeta, error) {
	if err := s.ensureServiceRunning(); err != nil {
		return FileMeta{}, err
	}
	transition, err := s.reg.BeginTransition(fileID)
	if err != nil {
		return FileMeta{}, err
	}
	unblockJobs := s.jobs().blockFile(fileID)
	defer unblockJobs()
	s.cancelSearchRequestsForFile(fileID)
	s.jobs().cancelFile(fileID)
	// Revoke issued continuation offsets before the registry begins installing a
	// new generation. A refresh failure merely forces the old grid to restart
	// from byte zero; a successful refresh cannot inherit an old cursor.
	s.csvCursors.invalidateFile(fileID)
	f, err := transition.Reopen(true)
	if err != nil {
		return FileMeta{}, err
	}
	defer f.Release()
	s.releasePreparedEdit(fileID)
	s.invalidateSQLSummary(fileID)
	m := f.Doc.Metadata()
	return FileMeta{
		FileID:   f.ID,
		Path:     m.Path,
		Size:     m.Size,
		Encoding: m.Encoding,
		Detected: m.FileType,
		Binary:   m.Binary,
		Editable: !m.Binary && editableEncoding(m.Encoding, m.LineEnding),
	}, nil
}

// SearchHit is one match (or a not-found / timed-out / unsupported result).
type SearchHit struct {
	Found    bool  `json:"found"`
	Offset   int64 `json:"offset"`
	Length   int   `json:"length"`
	Line     int64 `json:"line"`     // approximate until the index is built
	TimedOut bool  `json:"timedOut"` // search hit the time budget before finishing
	// Unsupported is set when the query can't be searched in the file's encoding
	// (e.g. regex over a UTF-16/Windows-125x file, or a term with characters not
	// representable in that encoding) — so the UI can say so instead of "no matches".
	Unsupported bool   `json:"unsupported"`
	Message     string `json:"message"`
}

// SearchAllHit is one match in a whole-file search, with a preview line.
type SearchAllHit struct {
	Offset  int64  `json:"offset"`
	Length  int    `json:"length"`
	Line    int64  `json:"line"`
	Preview string `json:"preview"`
}

// SearchAllResult holds up to MaxHits matches plus a truncation flag.
type SearchAllResult struct {
	Hits        []SearchAllHit `json:"hits"`
	Truncated   bool           `json:"truncated"`
	TimedOut    bool           `json:"timedOut"`
	Unsupported bool           `json:"unsupported"`
	Message     string         `json:"message"`
}

// HarvestMatchesViaDialog runs a regex over the whole file and writes every
// match, one per line, to a chosen file — e.g. extract every email or id.
var harvestSaveDialog = saveDialog

func (s *FileService) HarvestMatchesViaDialog(fileID, pattern string, caseInsensitive bool) (TransformResult, error) {
	if err := validateServiceSearchQuery(pattern, true); err != nil {
		return TransformResult{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return TransformResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if strings.TrimSpace(pattern) == "" {
		return TransformResult{}, errors.New("enter a regex pattern")
	}
	enc := f.Doc.Metadata().Encoding
	if !(enc == "" || strings.EqualFold(enc, "UTF-8")) {
		return TransformResult{}, fmt.Errorf("regex harvest needs a UTF-8 file (this file is %s)", enc)
	}
	re, _, err := regexutil.CompileBounded([]byte(pattern), caseInsensitive, harvestRegexMatchWindow)
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := harvestSaveDialog("Save extracted matches as", "matches.txt")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withFileJob(fileID, "Harvest regex matches", func(ctx context.Context, progress func(int64, string)) (TransformResult, error) {
		return s.harvestMatchesToPath(ctx, f, re, caseInsensitive, dst, progress)
	})
}

func (s *FileService) harvestMatchesToPath(ctx context.Context, f *session.File, re *regexp.Regexp, caseInsensitive bool, dst string, progress func(int64, string)) (TransformResult, error) {
	if f == nil || f.Doc == nil {
		return TransformResult{}, errors.New("source file is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := regexutil.ValidateCompiledBounded(re, harvestRegexMatchWindow); err != nil {
		return TransformResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return TransformResult{}, err
	}
	expected, err := captureDocumentSourceExpectation(ctx, f.Doc, f.Path, nil)
	if err != nil {
		return TransformResult{}, err
	}
	verified, err := newVerifiedDocumentReader(ctx, expected, f.Doc)
	if err != nil {
		return TransformResult{}, err
	}

	var count int64
	_, err = writeSafeOutputValidatedContext(ctx, f.Doc, f.Path, dst, func(out io.Writer) error {
		bw := bufio.NewWriterSize(out, 1<<20)
		if err := search.FindRegexp(ctx, verified, re, search.RegexOptions{
			CaseInsensitive: caseInsensitive,
			MaxMatchWindow:  harvestRegexMatchWindow,
		}, func(m search.Match) error {
			if m.Length < 0 || m.Length > harvestRegexMatchWindow {
				return fmt.Errorf("regex match length %d is outside 0..%d", m.Length, harvestRegexMatchWindow)
			}
			match := make([]byte, m.Length)
			if len(match) > 0 {
				n, readErr := verified.ReadAt(match, m.Offset)
				if readErr != nil && !errors.Is(readErr, io.EOF) {
					return readErr
				}
				if n != len(match) {
					return io.ErrUnexpectedEOF
				}
			}
			if _, writeErr := bw.Write(flattenHarvestMatchNewlines(match)); writeErr != nil {
				return writeErr
			}
			count++
			if progress != nil && count%1000 == 0 {
				progress(count, "matches written")
			}
			return bw.WriteByte('\n')
		}); err != nil {
			return err
		}
		return bw.Flush()
	}, func(validateCtx context.Context) error {
		return expected.validateContext(validateCtx, f.Doc, nil)
	})
	if err != nil {
		return TransformResult{}, err
	}
	return TransformResult{OutputPath: dst, RecordsWritten: count, Note: fmt.Sprintf("%d matches", count)}, nil
}

func flattenHarvestMatchNewlines(match []byte) []byte {
	write := 0
	for read := 0; read < len(match); read++ {
		switch match[read] {
		case '\r':
			match[write] = ' '
			write++
			if read+1 < len(match) && match[read+1] == '\n' {
				read++
			}
		case '\n':
			match[write] = ' '
			write++
		default:
			match[write] = match[read]
			write++
		}
	}
	return match[:write]
}

const (
	resolveLineExactScanBytes int64 = 8 << 20
	resolveLineTimeout              = 5 * time.Second
)

// LineResolution distinguishes a real offset zero, an approximate known line
// start, a line proven absent, an incomplete index, and a bounded-scan fallback.
// When Exact is false and Found is true, ResolvedLine is the actual line at
// Offset; it must not be mistaken for proof that the requested line was found.
type LineResolution struct {
	Offset        int64 `json:"offset"`
	ResolvedLine  int64 `json:"resolvedLine"`
	Exact         bool  `json:"exact"`
	Found         bool  `json:"found"`
	IndexComplete bool  `json:"indexComplete"`
	Limited       bool  `json:"limited"`
}

// ResolveLine maps a positive 1-based line number under a hard 8 MiB exact-scan
// budget and a timeout. Source read failures remain errors; a timeout fallback
// is returned explicitly and never reported as proof that the line is absent.
func (s *FileService) ResolveLine(fileID string, line int64) (LineResolution, error) {
	if line < 1 {
		return LineResolution{}, errors.New("line number must be positive")
	}
	ctx, cancel := context.WithTimeout(context.Background(), resolveLineTimeout)
	defer cancel()
	resolution, err := s.resolveLineContext(ctx, fileID, line)
	if err != nil &&
		errors.Is(ctx.Err(), context.DeadlineExceeded) &&
		(errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		resolution.Limited = true
		return resolution, nil
	}
	return resolution, err
}

// resolveLineContext is the cancellable core used by focused tests. The
// current session registry has generation leases but no foreground-operation
// cancellation registry, so Close/Refresh wait for this bounded lease.
func (s *FileService) resolveLineContext(ctx context.Context, fileID string, line int64) (LineResolution, error) {
	if line < 1 {
		return LineResolution{}, errors.New("line number must be positive")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return LineResolution{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return LineResolution{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return LineResolution{}, err
	}

	lookup, lookupErr := f.Doc.LookupLineStart(ctx, line, resolveLineExactScanBytes)
	resolution := lineResolutionFromLookup(lookup, f.Doc.IndexProgress().Done)
	if lookupErr != nil {
		if stateErr := f.Doc.ValidateUnchanged(); stateErr != nil {
			return LineResolution{}, stateErr
		}
		return resolution, lookupErr
	}
	if err := f.Doc.ValidateUnchanged(); err != nil {
		return LineResolution{}, err
	}
	return resolution, nil
}

func lineResolutionFromLookup(lookup document.LineStartLookupResult, indexComplete bool) LineResolution {
	resolution := LineResolution{
		Offset:        lookup.Offset,
		ResolvedLine:  lookup.Line,
		Found:         lookup.HasPosition,
		IndexComplete: indexComplete,
	}
	switch lookup.Status {
	case document.LineStartLookupExact:
		resolution.Exact = true
		resolution.Found = true
	case document.LineStartLookupAbsent:
		return LineResolution{IndexComplete: true}
	case document.LineStartLookupPending:
		resolution.IndexComplete = false
	case document.LineStartLookupLimited:
		resolution.IndexComplete = true
		resolution.Limited = true
	}
	return resolution
}

// GetWindow returns a bounded, decoded window beginning at the line that
// contains startByte.
func (s *FileService) GetWindow(fileID string, startByte int64, maxBytes int) (Window, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return Window{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	return boundedWindowForFile(f, startByte, maxBytes, true, -1)
}

// GetNextWindow continues forward from a previous window's NextByte.
func (s *FileService) GetNextWindow(fileID string, fromByte int64, maxBytes int) (Window, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return Window{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	return boundedWindowForFile(f, fromByte, maxBytes, false, -1)
}

// GetPrevWindow returns the bounded decoded window immediately preceding
// currentStart. Its NextByte is exactly currentStart.
func (s *FileService) GetPrevWindow(fileID string, currentStart int64, maxBytes int) (Window, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return Window{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	return previousBoundedWindowForFile(f, currentStart, maxBytes)
}
