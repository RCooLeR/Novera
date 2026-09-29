package bigfile

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"novera/internal/bigfile/document"
)

func openSearchRequestFixture(t *testing.T, content string) (*FileService, FileMeta, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "search-request.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.ServiceShutdown(); err != nil {
			t.Errorf("ServiceShutdown: %v", err)
		}
	})
	return service, meta, path
}

func TestSearchRequestIDsValidateBeforeServiceAccess(t *testing.T) {
	var service *FileService
	invalid := strings.Repeat("x", 1<<20)
	if _, err := service.FindNextRequest(invalid, "needle", 0, false, true, false); !errors.Is(err, ErrInvalidSearchRequestID) {
		t.Fatalf("FindNextRequest error = %v, want ErrInvalidSearchRequestID", err)
	}
	if _, err := service.SearchAllRequest(invalid, "needle", false, true, false, 10); !errors.Is(err, ErrInvalidSearchRequestID) {
		t.Fatalf("SearchAllRequest error = %v, want ErrInvalidSearchRequestID", err)
	}
	if _, err := service.CancelSearch(invalid); !errors.Is(err, ErrInvalidSearchRequestID) {
		t.Fatalf("CancelSearch error = %v, want ErrInvalidSearchRequestID", err)
	}
	if _, err := service.BeginSearchRequest("not-a-file-id"); !errors.Is(err, ErrInvalidFileID) {
		t.Fatalf("BeginSearchRequest error = %v, want ErrInvalidFileID", err)
	}
}

func TestExportedSearchSurfaceRequiresStableRequestOwnership(t *testing.T) {
	serviceType := reflect.TypeOf((*FileService)(nil))
	allowed := map[string]struct{}{
		"BeginSearchRequest": {},
		"CancelSearch":       {},
		"FindNextRequest":    {},
		"FindPrevRequest":    {},
		"SearchAllRequest":   {},
	}
	for i := 0; i < serviceType.NumMethod(); i++ {
		name := serviceType.Method(i).Name
		if !strings.Contains(name, "Search") && !strings.HasPrefix(name, "Find") {
			continue
		}
		if _, ok := allowed[name]; !ok {
			t.Fatalf("exported search method %s bypasses request ownership", name)
		}
		delete(allowed, name)
	}
	if len(allowed) != 0 {
		t.Fatalf("missing request-owned search methods: %v", allowed)
	}
}

func TestSearchRequestCancelIsExactAndIDsAreNeverReused(t *testing.T) {
	service, meta, _ := openSearchRequestFixture(t, "needle\nother\n")
	first, err := service.BeginSearchRequest(meta.FileID)
	if err != nil || first != "search1" {
		t.Fatalf("first request = %q, %v", first, err)
	}
	if canceled, err := service.CancelSearch(first); err != nil || !canceled {
		t.Fatalf("cancel first = %v, %v", canceled, err)
	}
	if canceled, err := service.CancelSearch(first); err != nil || canceled {
		t.Fatalf("repeat cancel first = %v, %v", canceled, err)
	}

	second, err := service.BeginSearchRequest(meta.FileID)
	if err != nil || second != "search2" {
		t.Fatalf("second request = %q, %v", second, err)
	}
	if canceled, err := service.CancelSearch(first); err != nil || canceled {
		t.Fatalf("stale cancel = %v, %v", canceled, err)
	}
	result, err := service.SearchAllRequest(second, "needle", false, true, false, 10)
	if err != nil || len(result.Hits) != 1 {
		t.Fatalf("second request result = %+v, %v", result, err)
	}
}

func TestSearchRequestCapacityAndSequenceAreBounded(t *testing.T) {
	service, meta, _ := openSearchRequestFixture(t, "needle\n")
	ids := make([]string, 0, maxSearchRequestsPerFile)
	for i := 0; i < maxSearchRequestsPerFile; i++ {
		id, err := service.BeginSearchRequest(meta.FileID)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if id, err := service.BeginSearchRequest(meta.FileID); id != "" || !errors.Is(err, ErrSearchRequestLimit) {
		t.Fatalf("over-cap request = %q, %v", id, err)
	}
	for _, id := range ids {
		if canceled, err := service.CancelSearch(id); err != nil || !canceled {
			t.Fatalf("cancel %q = %v, %v", id, canceled, err)
		}
	}

	service.searchRequests.mu.Lock()
	service.searchRequests.seq = math.MaxInt64 - 1
	service.searchRequests.mu.Unlock()
	last, err := service.BeginSearchRequest(meta.FileID)
	if err != nil || last != "search9223372036854775807" {
		t.Fatalf("last request = %q, %v", last, err)
	}
	if canceled, err := service.CancelSearch(last); err != nil || !canceled {
		t.Fatalf("cancel last = %v, %v", canceled, err)
	}
	if id, err := service.BeginSearchRequest(meta.FileID); id != "" || !errors.Is(err, ErrSearchRequestSequenceExhausted) {
		t.Fatalf("exhausted request = %q, %v", id, err)
	}
}

func TestRunningSearchCancellationAndSingleClaim(t *testing.T) {
	service, meta, _ := openSearchRequestFixture(t, "needle\nother\n")
	requestID, err := service.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	previousHook := searchRequestClaimedHook
	searchRequestClaimedHook = func(id string) {
		if id == requestID {
			close(entered)
			<-release
		}
	}
	t.Cleanup(func() { searchRequestClaimedHook = previousHook })

	done := make(chan error, 1)
	go func() {
		_, searchErr := service.FindNextRequest(requestID, "needle", 0, false, true, false)
		done <- searchErr
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("search did not reach claimed state")
	}
	if _, err := service.FindPrevRequest(requestID, "needle", meta.Size, false, true, false); !errors.Is(err, ErrSearchRequestAlreadyStarted) {
		t.Fatalf("duplicate execution error = %v", err)
	}
	if canceled, err := service.CancelSearch(requestID); err != nil || !canceled {
		t.Fatalf("cancel running = %v, %v", canceled, err)
	}
	if canceled, err := service.CancelSearch(requestID); err != nil || canceled {
		t.Fatalf("duplicate running cancel = %v, %v", canceled, err)
	}
	close(release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled search error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled search did not return")
	}
}

func TestSearchRequestsRejectInvalidOffsetsAndResultLimits(t *testing.T) {
	service, meta, _ := openSearchRequestFixture(t, "needle\n")
	requestID, err := service.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindNextRequest(requestID, "needle", -1, false, true, false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("negative search offset error = %v", err)
	}

	requestID, err = service.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindNextRequest(requestID, "needle", meta.Size+1, false, true, false); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("oversize search offset error = %v", err)
	}

	for _, limit := range []int{0, -1, maxSearchAllHits + 1} {
		requestID, err = service.BeginSearchRequest(meta.FileID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.SearchAllRequest(requestID, "needle", false, true, false, limit); err == nil || !strings.Contains(err.Error(), "result limit") {
			t.Fatalf("SearchAllRequest limit %d error = %v", limit, err)
		}
	}
}

func TestSearchRequestRejectsChangedSourceAndOldGeneration(t *testing.T) {
	service, meta, path := openSearchRequestFixture(t, "needle\n")
	requestID, err := service.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.FindNextRequest(requestID, "needle", 0, false, true, false); !errors.Is(err, document.ErrSourceChanged) {
		t.Fatalf("changed-source search error = %v, want ErrSourceChanged", err)
	}

	if _, err := service.RefreshFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	current, ok := service.reg.Get(meta.FileID)
	if !ok {
		t.Fatal("refreshed file is missing")
	}
	currentGeneration := current.Generation
	current.Release()
	if currentGeneration < 2 {
		t.Fatalf("refreshed generation = %d", currentGeneration)
	}
	if _, err := service.findRegisteredContext(context.Background(), meta.FileID, currentGeneration-1, "changed", 0, false, false, true, false); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatalf("old-generation search error = %v", err)
	}
}

func TestSearchReservationsAreCanceledByRefreshCloseAndShutdown(t *testing.T) {
	service, meta, _ := openSearchRequestFixture(t, "needle\n")
	refreshID, err := service.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	if canceled, err := service.CancelSearch(refreshID); err != nil || canceled {
		t.Fatalf("cancel after refresh = %v, %v", canceled, err)
	}

	closeID, err := service.BeginSearchRequest(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CloseFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	if canceled, err := service.CancelSearch(closeID); err != nil || canceled {
		t.Fatalf("cancel after close = %v, %v", canceled, err)
	}

	secondService, secondMeta, _ := openSearchRequestFixture(t, "needle\n")
	shutdownID, err := secondService.BeginSearchRequest(secondMeta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondService.ServiceShutdown(); err != nil {
		t.Fatal(err)
	}
	if canceled, err := secondService.CancelSearch(shutdownID); err != nil || canceled {
		t.Fatalf("cancel after shutdown = %v, %v", canceled, err)
	}
	if _, err := secondService.BeginSearchRequest(secondMeta.FileID); !errors.Is(err, ErrServiceStopped) {
		t.Fatalf("begin after shutdown error = %v, want ErrServiceStopped", err)
	}
}

func TestSearchRequestCannotPublishAfterLifecycleCancellationPass(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func(*FileService, string) error
	}{
		{name: "close", run: func(service *FileService, fileID string) error {
			return service.CloseFile(fileID)
		}},
		{name: "refresh", run: func(service *FileService, fileID string) error {
			_, err := service.RefreshFile(fileID)
			return err
		}},
	} {
		t.Run(operation.name, func(t *testing.T) {
			service, meta, _ := openSearchRequestFixture(t, "needle\n")
			beforePublication := make(chan struct{})
			allowPublication := make(chan struct{})
			cancellationPassed := make(chan struct{})

			previousBefore := searchRequestBeforePublicationHook
			previousCanceled := searchRequestsCanceledForFileHook
			searchRequestBeforePublicationHook = func(fileID string) {
				if fileID == meta.FileID {
					close(beforePublication)
					<-allowPublication
				}
			}
			searchRequestsCanceledForFileHook = func(fileID string) {
				if fileID == meta.FileID {
					close(cancellationPassed)
				}
			}
			t.Cleanup(func() {
				searchRequestBeforePublicationHook = previousBefore
				searchRequestsCanceledForFileHook = previousCanceled
			})

			type beginResult struct {
				id  string
				err error
			}
			beginDone := make(chan beginResult, 1)
			go func() {
				id, err := service.BeginSearchRequest(meta.FileID)
				beginDone <- beginResult{id: id, err: err}
			}()
			select {
			case <-beforePublication:
			case <-time.After(2 * time.Second):
				t.Fatal("search request did not reach publication seam")
			}

			lifecycleDone := make(chan error, 1)
			go func() {
				lifecycleDone <- operation.run(service, meta.FileID)
			}()
			select {
			case <-cancellationPassed:
			case <-time.After(2 * time.Second):
				t.Fatal("lifecycle cancellation pass did not run")
			}
			close(allowPublication)

			select {
			case result := <-beginDone:
				if result.id != "" || result.err == nil {
					t.Fatalf("late request publication = %q, %v; want rejection", result.id, result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("late request publication did not return")
			}
			select {
			case err := <-lifecycleDone:
				if err != nil {
					t.Fatalf("%s: %v", operation.name, err)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s did not finish", operation.name)
			}

			service.searchRequests.mu.Lock()
			retained := len(service.searchRequests.requests)
			service.searchRequests.mu.Unlock()
			if retained != 0 {
				t.Fatalf("late request retained after %s: %d", operation.name, retained)
			}
		})
	}
}
