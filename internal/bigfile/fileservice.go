package bigfile

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/regexutil"
	"novera/internal/bigfile/search"
	"novera/internal/bigfile/session"
)

// searchTimeout bounds a single find call so a no-match search on a huge file
// cannot run forever. The UI shows "searching…" while it runs.
const searchTimeout = 60 * time.Second

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
	// windowScanCap bounds the bytes scanned per window so a region of huge lines
	// (few lines per MiB) stays fast and bounded.
	windowScanCap int64 = 32 << 20
)

// FileService exposes streaming, windowed access to very large files. It is
// bound to the frontend by Wails; the whole-file content never crosses the
// bridge — only bounded, line-aligned windows do.
type FileService struct {
	reg *session.Registry

	sqlMu      sync.Mutex
	sqlSummary map[string]cachedSQLSummary // cached by file id and immutable document generation

	jobOnce sync.Once
	jobMgr  *jobManager
}

// NewFileService constructs the service with an empty session registry.
func NewFileService() *FileService {
	return &FileService{
		reg:        session.New(),
		sqlSummary: make(map[string]cachedSQLSummary),
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
	err := s.reg.Close(fileID)
	s.invalidateSQLSummary(fileID)
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
	f, err := s.reg.ReopenIfClean(fileID)
	if err != nil {
		return FileMeta{}, err
	}
	defer f.Release()
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

// FindNext finds the first match at/after fromByte (single streaming pass).
func (s *FileService) FindNext(fileID, query string, fromByte int64, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	return s.find(fileID, query, fromByte, false, regex, caseSensitive, wholeWord)
}

// FindPrev finds the first match before beforeByte (streaming backward pass).
func (s *FileService) FindPrev(fileID, query string, beforeByte int64, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	return s.find(fileID, query, beforeByte, true, regex, caseSensitive, wholeWord)
}

func (s *FileService) find(fileID, query string, start int64, backward, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SearchHit{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if strings.TrimSpace(query) == "" {
		return SearchHit{}, nil
	}
	// The search engine scans the file's raw bytes, so the query must be encoded
	// into the document's encoding first — otherwise a UTF-8 query never matches a
	// UTF-16 / Windows-125x dump and the user gets a false "no matches".
	enc := f.Doc.Metadata().Encoding
	isUTF8 := enc == "" || strings.EqualFold(enc, "UTF-8")
	var pattern []byte
	if regex {
		if !isUTF8 {
			return SearchHit{Unsupported: true, Message: "Regex search isn't supported on " + enc + " files. Use plain text search, or convert the file to UTF-8."}, nil
		}
		pattern = []byte(query)
	} else if isUTF8 {
		pattern = []byte(query)
	} else {
		encoded, encErr := encodingx.EncodeString(enc, query)
		if encErr != nil {
			return SearchHit{Unsupported: true, Message: "This term can't be searched in a " + enc + " file."}, nil
		}
		pattern = encoded
	}

	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()

	var results []search.Result
	var err error
	if regex {
		results, err = search.CollectRegexp(ctx, f.Doc, pattern, search.RegexOptions{
			StartOffset:     start,
			MaxHits:         1,
			Backward:        backward,
			CaseInsensitive: !caseSensitive,
		}, 0)
	} else {
		results, err = search.CollectPlain(ctx, f.Doc, pattern, search.PlainOptions{
			StartOffset:     start,
			MaxHits:         1,
			Backward:        backward,
			CaseInsensitive: !caseSensitive,
			WholeWord:       wholeWord,
		}, 0)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return SearchHit{TimedOut: true}, nil
		}
		return SearchHit{}, err
	}
	if len(results) == 0 {
		return SearchHit{Found: false}, nil
	}
	m := results[0]
	line, _ := f.Doc.ApproxOffsetToLine(m.Offset)
	return SearchHit{Found: true, Offset: m.Offset, Length: m.Length, Line: line}, nil
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

// SearchAll collects up to maxHits matches across the whole file with previews,
// for a results panel. The query is encoded into the file's encoding first.
func (s *FileService) SearchAll(fileID, query string, regex, caseSensitive, wholeWord bool, maxHits int) (SearchAllResult, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SearchAllResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if strings.TrimSpace(query) == "" {
		return SearchAllResult{}, nil
	}
	maxHits = clampRequestInt(maxHits, defaultSearchAllHits, maxSearchAllHits)
	enc := f.Doc.Metadata().Encoding
	isUTF8 := enc == "" || strings.EqualFold(enc, "UTF-8")
	var pattern []byte
	if regex {
		if !isUTF8 {
			return SearchAllResult{Unsupported: true, Message: "Regex search isn't supported on " + enc + " files."}, nil
		}
		pattern = []byte(query)
	} else if isUTF8 {
		pattern = []byte(query)
	} else {
		encoded, encErr := encodingx.EncodeString(enc, query)
		if encErr != nil {
			return SearchAllResult{Unsupported: true, Message: "This term can't be searched in a " + enc + " file."}, nil
		}
		pattern = encoded
	}

	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	defer cancel()
	var results []search.Result
	var err error
	if regex {
		results, err = search.CollectRegexp(ctx, f.Doc, pattern, search.RegexOptions{MaxHits: maxHits, CaseInsensitive: !caseSensitive}, 160)
	} else {
		results, err = search.CollectPlain(ctx, f.Doc, pattern, search.PlainOptions{MaxHits: maxHits, CaseInsensitive: !caseSensitive, WholeWord: wholeWord}, 160)
	}
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return SearchAllResult{TimedOut: true}, nil
		}
		return SearchAllResult{}, err
	}
	hits := make([]SearchAllHit, 0, len(results))
	for _, m := range results {
		line, _ := f.Doc.ApproxOffsetToLine(m.Offset)
		preview := strings.TrimRight(strings.ReplaceAll(m.Preview, "\n", " "), " \t\r")
		hits = append(hits, SearchAllHit{Offset: m.Offset, Length: m.Length, Line: line, Preview: preview})
	}
	return SearchAllResult{Hits: hits, Truncated: len(hits) >= maxHits}, nil
}

// HarvestMatchesViaDialog runs a regex over the whole file and writes every
// match, one per line, to a chosen file — e.g. extract every email or id.
func (s *FileService) HarvestMatchesViaDialog(fileID, pattern string, caseInsensitive bool) (TransformResult, error) {
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
	re, err := regexutil.Compile([]byte(pattern), caseInsensitive)
	if err != nil {
		return TransformResult{}, err
	}
	dst, err := saveDialog("Save extracted matches as", "matches.txt")
	if err != nil || strings.TrimSpace(dst) == "" {
		return TransformResult{}, err
	}
	return s.withJob("Harvest regex matches", func(jobCtx context.Context, progress func(int64, string)) (TransformResult, error) {
		ctx, cancel := context.WithTimeout(jobCtx, searchTimeout)
		defer cancel()
		var count int64
		_, err = writeSafeOutput(f.Doc, f.Path, dst, func(out io.Writer) error {
			bw := bufio.NewWriterSize(out, 1<<20)
			if err := search.FindRegexp(ctx, f.Doc, re, search.RegexOptions{CaseInsensitive: caseInsensitive}, func(m search.Match) error {
				b, rerr := f.Doc.ReadRange(m.Offset, m.Offset+int64(m.Length))
				if rerr != nil {
					return rerr
				}
				if _, werr := bw.Write(bytes.ReplaceAll(b, []byte("\n"), []byte(" "))); werr != nil {
					return werr
				}
				count++
				progress(count, "matches written")
				return bw.WriteByte('\n')
			}); err != nil {
				return err
			}
			return bw.Flush()
		})
		if err != nil {
			return TransformResult{}, err
		}
		return TransformResult{OutputPath: dst, RecordsWritten: count, Note: fmt.Sprintf("%d matches", count)}, nil
	})
}

// ResolveLine maps a 1-based line number to a byte offset (exact if the index
// is built, otherwise approximate). Returns 0 when the line can't be resolved.
func (s *FileService) ResolveLine(fileID string, line int64) (int64, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return 0, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if line < 1 {
		line = 1
	}
	if off, ok, err := f.Doc.ExactLineToOffset(line); err == nil && ok {
		return off, nil
	}
	if off, ok := f.Doc.ApproxLineToOffset(line); ok {
		return off, nil
	}
	return 0, nil
}

// GetWindow returns a window beginning at startByte (clamped, aligned by the
// document's own line handling).
func (s *FileService) GetWindow(fileID string, startByte int64, maxBytes int) (Window, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	return s.windowFrom(f, startByte, maxBytes)
}

// GetNextWindow continues forward from a previous window's NextByte.
func (s *FileService) GetNextWindow(fileID string, fromByte int64, maxBytes int) (Window, error) {
	return s.GetWindow(fileID, fromByte, maxBytes)
}

// GetPrevWindow returns the window immediately preceding currentStart, aligned
// to a line boundary so it reads cleanly.
func (s *FileService) GetPrevWindow(fileID string, currentStart int64, maxBytes int) (Window, error) {
	f, ok := s.reg.Get(fileID)
	if !ok {
		return Window{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	maxBytes = clampRequestInt(maxBytes, defaultWindowBytes, maxWindowRequestBytes)
	if currentStart <= 0 {
		return s.windowFrom(f, 0, maxBytes)
	}
	target := currentStart - int64(maxBytes)
	if target < 0 {
		target = 0
	}
	if target > 0 {
		// Align up to the next line boundary so the window starts cleanly.
		raw, err := f.Doc.ReadRange(target, currentStart)
		if err != nil {
			return Window{}, err
		}
		if i := bytes.IndexByte(raw, '\n'); i >= 0 {
			target += int64(i) + 1
		}
	}
	span := int(currentStart - target)
	if span <= 0 {
		span = maxBytes
	}
	return s.windowFrom(f, target, span)
}

func (s *FileService) windowFrom(f *session.File, startByte int64, maxBytes int) (Window, error) {
	maxBytes = clampRequestInt(maxBytes, defaultWindowBytes, maxWindowRequestBytes)
	size := f.Doc.Size()
	startByte = f.Doc.ClampOffset(startByte)

	firstLine, ok := f.Doc.ApproxOffsetToLine(startByte)
	approx := true
	switch {
	case startByte == 0:
		firstLine, approx = 1, false
	case !ok || firstLine <= 0:
		firstLine = 1 // unknown until indexed; numbers shown are relative
	}

	_ = maxBytes // window size is governed by windowLineTarget / windowScanCap

	// Scan for line boundaries and decode only the first chunk of each line,
	// collapsing huge mysqldump INSERT lines to one compact row. This stays fast
	// even across a region of multi-MB lines (it never decodes the whole window),
	// so the window has enough rows to scroll and the next load is cheap.
	scanEnd := boundedReadEnd(startByte, size, int(windowScanCap))

	var b strings.Builder
	offsets := make([]int64, 0, 256)
	numbers := make([]int64, 0, 256)
	lineNo := firstLine
	lineStart := startByte
	head := make([]byte, 0, rowDisplayBytes)
	headFull := false

	emitRow := func(truncated bool) {
		if len(offsets) > 0 {
			b.WriteByte('\n')
		}
		text := string(head)
		if r := []rune(text); len(r) > maxRowDisplayRunes {
			text = string(r[:maxRowDisplayRunes])
			truncated = true
		}
		b.WriteString(text)
		if truncated {
			b.WriteString(" ⋯")
		}
		offsets = append(offsets, lineStart)
		numbers = append(numbers, lineNo)
		lineNo++
		head = head[:0]
		headFull = false
	}

	takeHead := func(seg []byte) {
		if headFull {
			return // already captured enough of this line; the rest is dropped
		}
		room := rowDisplayBytes - len(head)
		if len(seg) > room {
			head = append(head, seg[:room]...)
			headFull = true
		} else {
			head = append(head, seg...)
		}
	}

	const chunk = 1 << 20
	pos := startByte
	for pos < scanEnd && len(offsets) < windowLineTarget {
		readEnd := boundedReadEnd(pos, scanEnd, chunk)
		buf, err := f.Doc.ReadRange(pos, readEnd)
		if err != nil {
			return Window{}, err
		}
		i := 0
		for i < len(buf) && len(offsets) < windowLineTarget {
			nl := bytes.IndexByte(buf[i:], '\n')
			if nl < 0 {
				takeHead(buf[i:])
				i = len(buf)
				break
			}
			takeHead(buf[i : i+nl])
			emitRow(headFull)
			lineStart = pos + int64(i+nl) + 1
			i += nl + 1
		}
		pos = readEnd
	}

	// Trailing content with no newline: the file's last line (at EOF) or a line
	// longer than the scan cap. Emit it so the window always makes progress.
	nextByte := lineStart
	if lineStart < size && len(offsets) < windowLineTarget {
		switch {
		case pos >= size: // true EOF, last line has no newline
			emitRow(headFull)
			nextByte = size
		case len(offsets) == 0: // a single line longer than the scan cap
			emitRow(true)
			nextByte = scanEnd
		}
	}
	if nextByte > size {
		nextByte = size
	}

	return Window{
		FileID:      f.ID,
		StartByte:   startByte,
		NextByte:    nextByte,
		Text:        b.String(),
		LineOffsets: offsets,
		LineNumbers: numbers,
		AtBOF:       startByte == 0,
		AtEOF:       nextByte >= size,
		Approx:      approx,
	}, nil
}
