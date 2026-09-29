package bigfile

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/search"
)

var (
	ErrInvalidFileID                  = errors.New("invalid file id")
	ErrInvalidSearchRequestID         = errors.New("invalid search request id")
	ErrSearchRequestNotFound          = errors.New("search request is not active")
	ErrSearchRequestAlreadyStarted    = errors.New("search request was already started")
	ErrSearchRequestSequenceExhausted = errors.New("search request sequence is exhausted")
	ErrSearchRequestLimit             = errors.New("too many active search requests")
	ErrSearchRequestsStopped          = errors.New("search requests are stopped")
)

const (
	searchRequestIDPrefix       = "search"
	maxSearchRequestsTotal      = 16
	maxSearchRequestsPerFile    = 4
	maxGeneratedNumericIDDigits = 19
)

type searchRequestRegistry struct {
	mu       sync.Mutex
	requests map[string]*interactiveSearchRequest
	seq      uint64
	stopped  bool
}

type interactiveSearchRequest struct {
	id         string
	fileID     string
	generation uint64
	ctx        context.Context
	cancel     context.CancelFunc
	running    bool
	canceled   bool
	finishOnce sync.Once
}

// searchRequestClaimedHook is a deterministic test seam after single-owner
// claim and before source acquisition. Production leaves it as a no-op.
var searchRequestClaimedHook = func(string) {}

// These lifecycle seams make the two-RPC publication race deterministic in
// tests. Production leaves both as no-ops.
var searchRequestBeforePublicationHook = func(string) {}
var searchRequestsCanceledForFileHook = func(string) {}

func (request *interactiveSearchRequest) finalize() {
	if request == nil {
		return
	}
	request.finishOnce.Do(request.cancel)
}

func validateSearchFileID(fileID string) error {
	return validateGeneratedNumericID(fileID, "f", ErrInvalidFileID)
}

func validateSearchRequestID(requestID string) error {
	return validateGeneratedNumericID(requestID, searchRequestIDPrefix, ErrInvalidSearchRequestID)
}

func validateGeneratedNumericID(value, prefix string, sentinel error) error {
	if !strings.HasPrefix(value, prefix) {
		return sentinel
	}
	suffix := value[len(prefix):]
	if len(suffix) == 0 || len(suffix) > maxGeneratedNumericIDDigits || suffix[0] == '0' {
		return sentinel
	}
	for i := 0; i < len(suffix); i++ {
		if suffix[i] < '0' || suffix[i] > '9' {
			return sentinel
		}
	}
	parsed, err := strconv.ParseUint(suffix, 10, 63)
	if err != nil || parsed == 0 || parsed > math.MaxInt64 {
		return sentinel
	}
	return nil
}

// BeginSearchRequest reserves one bounded, server-owned cancellation identity.
// The reservation expires after searchTimeout even if the frontend abandons it.
// Call exactly one *Request method or CancelSearch for each returned ID.
func (s *FileService) BeginSearchRequest(fileID string) (string, error) {
	if err := validateSearchFileID(fileID); err != nil {
		return "", err
	}
	if err := s.ensureServiceRunning(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), searchTimeout)
	f, ok := s.reg.Get(fileID)
	if !ok {
		cancel()
		return "", fmt.Errorf("unknown file id %q", fileID)
	}
	// Keep this lease until publication. Close/refresh first establish their
	// registry transition barrier, then cancel published requests before they
	// wait for this lease, so registration cannot slip past lifecycle cleanup.
	defer f.Release()
	if err := f.Doc.ValidateUnchanged(); err != nil {
		cancel()
		return "", err
	}
	if err := validateJavaScriptSafeInteger("file size", f.Doc.Size()); err != nil {
		cancel()
		return "", err
	}
	searchRequestBeforePublicationHook(fileID)

	registry := &s.searchRequests
	registry.mu.Lock()
	if registry.stopped {
		registry.mu.Unlock()
		cancel()
		return "", ErrSearchRequestsStopped
	}
	if err := ctx.Err(); err != nil {
		registry.mu.Unlock()
		cancel()
		return "", err
	}
	// Synchronize publication with CloseFile/RefreshFile. Their registry
	// barrier is established before they acquire this same request mutex for
	// cancellation. A pre-existing barrier fails this non-blocking check; a
	// later barrier necessarily observes and cancels the published request.
	if !s.reg.IsCurrentGeneration(fileID, f.Generation) {
		registry.mu.Unlock()
		cancel()
		return "", fmt.Errorf("file %q changed generation before search request publication", fileID)
	}
	if registry.requests == nil {
		registry.requests = make(map[string]*interactiveSearchRequest)
	}
	if len(registry.requests) >= maxSearchRequestsTotal {
		registry.mu.Unlock()
		cancel()
		return "", ErrSearchRequestLimit
	}
	perFile := 0
	for _, request := range registry.requests {
		if request.fileID == fileID {
			perFile++
		}
	}
	if perFile >= maxSearchRequestsPerFile {
		registry.mu.Unlock()
		cancel()
		return "", ErrSearchRequestLimit
	}
	if registry.seq >= math.MaxInt64 {
		registry.mu.Unlock()
		cancel()
		return "", ErrSearchRequestSequenceExhausted
	}
	registry.seq++
	requestID := fmt.Sprintf("%s%d", searchRequestIDPrefix, registry.seq)
	request := &interactiveSearchRequest{
		id:         requestID,
		fileID:     fileID,
		generation: f.Generation,
		ctx:        ctx,
		cancel:     cancel,
	}
	registry.requests[requestID] = request
	registry.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.finishUnclaimedSearchRequest(request)
	}()
	return requestID, nil
}

func (s *FileService) claimSearchRequest(requestID string) (*interactiveSearchRequest, error) {
	if err := validateSearchRequestID(requestID); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrSearchRequestNotFound
	}
	registry := &s.searchRequests
	registry.mu.Lock()
	request := registry.requests[requestID]
	if request == nil {
		registry.mu.Unlock()
		return nil, ErrSearchRequestNotFound
	}
	if request.running {
		registry.mu.Unlock()
		return nil, ErrSearchRequestAlreadyStarted
	}
	if err := request.ctx.Err(); err != nil {
		delete(registry.requests, requestID)
		registry.mu.Unlock()
		request.finalize()
		return nil, err
	}
	request.running = true
	registry.mu.Unlock()
	return request, nil
}

func (s *FileService) completeSearchRequest(request *interactiveSearchRequest) {
	if request == nil {
		return
	}
	registry := &s.searchRequests
	registry.mu.Lock()
	if registry.requests[request.id] == request {
		delete(registry.requests, request.id)
	}
	registry.mu.Unlock()
	request.finalize()
}

func (s *FileService) finishUnclaimedSearchRequest(request *interactiveSearchRequest) {
	if s == nil || request == nil {
		return
	}
	registry := &s.searchRequests
	registry.mu.Lock()
	owned := registry.requests[request.id] == request
	finish := owned && !request.running
	if finish {
		request.canceled = true
		delete(registry.requests, request.id)
	}
	registry.mu.Unlock()
	if finish {
		request.finalize()
	}
}

// CancelSearch cancels only the exact active ID. IDs are monotonically
// generated and never reused, so a delayed cancellation cannot hit newer work.
func (s *FileService) CancelSearch(requestID string) (bool, error) {
	if err := validateSearchRequestID(requestID); err != nil {
		return false, err
	}
	if s == nil {
		return false, nil
	}
	registry := &s.searchRequests
	registry.mu.Lock()
	request := registry.requests[requestID]
	if request == nil || request.canceled {
		registry.mu.Unlock()
		return false, nil
	}
	request.canceled = true
	if !request.running {
		delete(registry.requests, requestID)
	}
	registry.mu.Unlock()
	request.finalize()
	return true, nil
}

// cancelSearchRequestsForFile is called after the registry's per-file
// transition barrier rejects new leases and before lifecycle code waits for
// active leases to drain.
func (s *FileService) cancelSearchRequestsForFile(fileID string) {
	if s == nil {
		return
	}
	registry := &s.searchRequests
	registry.mu.Lock()
	requests := make([]*interactiveSearchRequest, 0)
	for requestID, request := range registry.requests {
		if request.fileID != fileID {
			continue
		}
		delete(registry.requests, requestID)
		request.canceled = true
		requests = append(requests, request)
	}
	registry.mu.Unlock()
	for _, request := range requests {
		request.finalize()
	}
	searchRequestsCanceledForFileHook(fileID)
}

// cancelAllSearchRequests permanently closes request admission and cancels all
// active/reserved searches at service shutdown.
func (s *FileService) cancelAllSearchRequests() {
	if s == nil {
		return
	}
	registry := &s.searchRequests
	registry.mu.Lock()
	registry.stopped = true
	requests := make([]*interactiveSearchRequest, 0, len(registry.requests))
	for requestID, request := range registry.requests {
		delete(registry.requests, requestID)
		request.canceled = true
		requests = append(requests, request)
	}
	registry.mu.Unlock()
	for _, request := range requests {
		request.finalize()
	}
}

func (s *FileService) FindNextRequest(requestID, query string, fromByte int64, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	return s.findSearchRequest(requestID, query, fromByte, false, regex, caseSensitive, wholeWord)
}

func (s *FileService) FindPrevRequest(requestID, query string, beforeByte int64, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	return s.findSearchRequest(requestID, query, beforeByte, true, regex, caseSensitive, wholeWord)
}

func (s *FileService) findSearchRequest(requestID, query string, start int64, backward, regex, caseSensitive, wholeWord bool) (SearchHit, error) {
	request, err := s.claimSearchRequest(requestID)
	if err != nil {
		return SearchHit{}, err
	}
	defer s.completeSearchRequest(request)
	searchRequestClaimedHook(requestID)
	if err := validateServiceSearchQuery(query, regex); err != nil {
		return SearchHit{}, err
	}
	result, err := s.findRegisteredContext(request.ctx, request.fileID, request.generation, query, start, backward, regex, caseSensitive, wholeWord)
	if errors.Is(request.ctx.Err(), context.DeadlineExceeded) {
		return SearchHit{TimedOut: true}, nil
	}
	if ctxErr := request.ctx.Err(); ctxErr != nil {
		return SearchHit{}, ctxErr
	}
	return result, err
}

func (s *FileService) SearchAllRequest(requestID, query string, regex, caseSensitive, wholeWord bool, maxHits int) (SearchAllResult, error) {
	request, err := s.claimSearchRequest(requestID)
	if err != nil {
		return SearchAllResult{}, err
	}
	defer s.completeSearchRequest(request)
	searchRequestClaimedHook(requestID)
	if err := validateServiceSearchQuery(query, regex); err != nil {
		return SearchAllResult{}, err
	}
	if maxHits < 1 || maxHits > maxSearchAllHits {
		return SearchAllResult{}, fmt.Errorf("search result limit must be between 1 and %d", maxSearchAllHits)
	}
	result, err := s.searchAllRegisteredContext(request.ctx, request.fileID, request.generation, query, regex, caseSensitive, wholeWord, maxHits)
	if errors.Is(request.ctx.Err(), context.DeadlineExceeded) {
		return SearchAllResult{TimedOut: true}, nil
	}
	if ctxErr := request.ctx.Err(); ctxErr != nil {
		return SearchAllResult{}, ctxErr
	}
	return result, err
}

func (s *FileService) findRegisteredContext(
	ctx context.Context,
	fileID string,
	expectedGeneration uint64,
	query string,
	start int64,
	backward, regex, caseSensitive, wholeWord bool,
) (SearchHit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SearchHit{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SearchHit{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if expectedGeneration != 0 && f.Generation != expectedGeneration {
		return SearchHit{}, documentGenerationMismatchError(expectedGeneration, f.Generation)
	}
	if err := validateSearchSourceBefore(ctx, f.Doc); err != nil {
		return SearchHit{}, err
	}
	size := f.Doc.Size()
	if err := validateJavaScriptSafeInteger("file size", size); err != nil {
		return SearchHit{}, err
	}
	if start < 0 || start > size {
		return SearchHit{}, fmt.Errorf("search start %d is outside 0..%d", start, size)
	}
	if strings.TrimSpace(query) == "" {
		return SearchHit{}, nil
	}
	pattern, unsupported := prepareSearchPattern(f.Doc.Metadata().Encoding, query, regex, caseSensitive, wholeWord)
	if unsupported != "" {
		return SearchHit{Unsupported: true, Message: unsupported}, nil
	}

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
			ByteAlignment:   plainSearchByteAlignment(f.Doc.Metadata().Encoding),
		}, 0)
	}
	if err != nil {
		return SearchHit{}, err
	}
	if err := validateSearchSourceAfter(ctx, f.Doc); err != nil {
		return SearchHit{}, err
	}
	if len(results) == 0 {
		return SearchHit{Found: false}, nil
	}
	match := results[0]
	if err := validateJavaScriptSafeInteger("search match offset", match.Offset); err != nil {
		return SearchHit{}, err
	}
	line, _ := f.Doc.ApproxOffsetToLine(match.Offset)
	if err := validateJavaScriptSafeInteger("search match line", line); err != nil {
		return SearchHit{}, err
	}
	return SearchHit{Found: true, Offset: match.Offset, Length: match.Length, Line: line}, nil
}

func (s *FileService) searchAllRegisteredContext(
	ctx context.Context,
	fileID string,
	expectedGeneration uint64,
	query string,
	regex, caseSensitive, wholeWord bool,
	maxHits int,
) (SearchAllResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return SearchAllResult{}, err
	}
	f, ok := s.reg.Get(fileID)
	if !ok {
		return SearchAllResult{}, fmt.Errorf("unknown file id %q", fileID)
	}
	defer f.Release()
	if expectedGeneration != 0 && f.Generation != expectedGeneration {
		return SearchAllResult{}, documentGenerationMismatchError(expectedGeneration, f.Generation)
	}
	if err := validateSearchSourceBefore(ctx, f.Doc); err != nil {
		return SearchAllResult{}, err
	}
	if err := validateJavaScriptSafeInteger("file size", f.Doc.Size()); err != nil {
		return SearchAllResult{}, err
	}
	if maxHits < 1 || maxHits > maxSearchAllHits {
		return SearchAllResult{}, fmt.Errorf("search result limit must be between 1 and %d", maxSearchAllHits)
	}
	if strings.TrimSpace(query) == "" {
		return SearchAllResult{}, nil
	}
	pattern, unsupported := prepareSearchPattern(f.Doc.Metadata().Encoding, query, regex, caseSensitive, wholeWord)
	if unsupported != "" {
		return SearchAllResult{Unsupported: true, Message: unsupported}, nil
	}

	var results []search.Result
	var err error
	if regex {
		results, err = search.CollectRegexp(ctx, f.Doc, pattern, search.RegexOptions{
			MaxHits:         maxHits,
			CaseInsensitive: !caseSensitive,
		}, 160)
	} else {
		results, err = search.CollectPlain(ctx, f.Doc, pattern, search.PlainOptions{
			MaxHits:         maxHits,
			CaseInsensitive: !caseSensitive,
			WholeWord:       wholeWord,
			ByteAlignment:   plainSearchByteAlignment(f.Doc.Metadata().Encoding),
		}, 160)
	}
	if err != nil {
		return SearchAllResult{}, err
	}
	if err := validateSearchSourceAfter(ctx, f.Doc); err != nil {
		return SearchAllResult{}, err
	}
	hits := make([]SearchAllHit, 0, len(results))
	for _, match := range results {
		if err := validateJavaScriptSafeInteger("search match offset", match.Offset); err != nil {
			return SearchAllResult{}, err
		}
		line, _ := f.Doc.ApproxOffsetToLine(match.Offset)
		if err := validateJavaScriptSafeInteger("search match line", line); err != nil {
			return SearchAllResult{}, err
		}
		preview := strings.TrimRight(strings.ReplaceAll(match.Preview, "\n", " "), " \t\r")
		hits = append(hits, SearchAllHit{
			Offset:  match.Offset,
			Length:  match.Length,
			Line:    line,
			Preview: preview,
		})
	}
	return SearchAllResult{Hits: hits, Truncated: len(hits) >= maxHits}, nil
}

func validateSearchSourceBefore(ctx context.Context, doc interface{ ValidateUnchanged() error }) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return doc.ValidateUnchanged()
}

func validateSearchSourceAfter(ctx context.Context, doc interface{ ValidateUnchanged() error }) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := doc.ValidateUnchanged(); err != nil {
		return err
	}
	return ctx.Err()
}

func documentGenerationMismatchError(expected, current uint64) error {
	return fmt.Errorf("search source generation changed from %d to %d", expected, current)
}

func prepareSearchPattern(encoding, query string, regex, caseSensitive, wholeWord bool) ([]byte, string) {
	isUTF8 := encoding == "" || strings.EqualFold(encoding, "UTF-8")
	if regex {
		if !isUTF8 {
			return nil, "Regex search isn't supported on " + encoding + " files. Use plain text search, or convert the file to UTF-8."
		}
		return []byte(query), ""
	}
	if reason := plainSearchUnsupportedReason(encoding, query, caseSensitive, wholeWord); reason != "" {
		return nil, reason
	}
	if isUTF8 {
		return []byte(query), ""
	}
	encoded, err := encodingx.EncodeString(encoding, query)
	if err != nil {
		return nil, "This term can't be searched in a " + encoding + " file."
	}
	return encoded, ""
}
