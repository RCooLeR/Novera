package bigfile

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"

	"novera/internal/bigfile/fileio"
)

var (
	ErrJobAlreadyRunning    = errors.New("another big-file job is already running")
	ErrNoActiveJob          = errors.New("no big-file job is active")
	ErrJobIDMismatch        = errors.New("big-file job id does not match the active job")
	ErrJobAlreadyCommitted  = errors.New("big-file job has already committed its result")
	ErrJobCancelled         = errors.New("big-file job was cancelled")
	ErrJobSequenceExhausted = errors.New("big-file job id sequence is exhausted")
	ErrInvalidJobID         = errors.New("invalid big-file job id")
	ErrJobsStopped          = errors.New("big-file jobs are shutting down")
	ErrFileJobsBlocked      = errors.New("big-file jobs are blocked for a file lifecycle transition")
)

const (
	jobKindTransform          = "transform"
	jobKindSQLAnalysis        = "sql-analysis"
	jobKindSourceVerification = "source-verification"

	// Event counters cross JSON into JavaScript and must remain exactly
	// representable.
	maxJobProgressValue = int64(1<<53 - 1)
	maxJobNoteBytes     = 160
)

type jobSpec struct {
	Title   string
	Kind    string
	FileID  string
	FileIDs []string
	Total   int64
}

type activeJob struct {
	id       string
	sequence int64
	spec     jobSpec
	ctx      context.Context
	cancel   context.CancelFunc

	completed       int64
	total           int64
	note            string
	cancelRequested bool
	committed       bool
	done            chan struct{}
	doneOnce        sync.Once
}

// jobManager owns one authoritative service-wide job slot. Events and cancel
// requests carry the immutable job ID so a delayed action for job A can never
// mutate or cancel successor B.
type jobManager struct {
	mu       sync.Mutex
	eventMu  sync.Mutex
	seq      int64
	active   *activeJob
	emit     func(string, any)
	blocked  map[string]int
	stopping bool
	stopDone chan struct{}

	// afterOutputPublication is a deterministic test seam at the otherwise
	// tiny interval between publication and recording the sticky committed
	// state. It runs while mu is held, so production cancellation semantics
	// remain unchanged.
	afterOutputPublication func(string, error)
}

func (s *FileService) jobs() *jobManager {
	s.jobOnce.Do(func() {
		s.jobMgr = &jobManager{emit: emitEvent}
	})
	return s.jobMgr
}

func emitEvent(name string, data any) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.EmitEvent(&application.CustomEvent{Name: name, Data: data})
}

func clampJobProgress(value int64) int64 {
	switch {
	case value < 0:
		return 0
	case value > maxJobProgressValue:
		return maxJobProgressValue
	default:
		return value
	}
}

func boundedJobNote(note string) string {
	if len(note) > maxJobNoteBytes {
		note = note[:maxJobNoteBytes]
	}
	return strings.ToValidUTF8(note, "")
}

func normalizeJobSpec(spec jobSpec) jobSpec {
	spec.Title = strings.TrimSpace(spec.Title)
	if spec.Title == "" {
		spec.Title = "Working"
	}
	spec.Kind = strings.TrimSpace(spec.Kind)
	if spec.Kind == "" {
		spec.Kind = jobKindTransform
	}
	spec.FileID, spec.FileIDs = normalizeJobFileIDs(spec.FileID, spec.FileIDs)
	spec.Total = clampJobProgress(spec.Total)
	return spec
}

// normalizeJobFileIDs returns one immutable, sorted ownership set. FileID
// remains the event/UI primary when supplied; otherwise the lexically first
// owner becomes primary. Sorting makes blocked-admission errors and tests
// deterministic regardless of caller argument order.
func normalizeJobFileIDs(primary string, fileIDs []string) (string, []string) {
	primary = strings.TrimSpace(primary)
	unique := make(map[string]struct{}, len(fileIDs)+1)
	if primary != "" {
		unique[primary] = struct{}{}
	}
	for _, fileID := range fileIDs {
		fileID = strings.TrimSpace(fileID)
		if fileID != "" {
			unique[fileID] = struct{}{}
		}
	}
	normalized := make([]string, 0, len(unique))
	for fileID := range unique {
		normalized = append(normalized, fileID)
	}
	sort.Strings(normalized)
	if primary == "" && len(normalized) > 0 {
		primary = normalized[0]
	}
	return primary, normalized
}

func (spec jobSpec) ownsFile(fileID string) bool {
	if fileID == "" {
		return false
	}
	if spec.FileID == fileID {
		return true
	}
	index := sort.SearchStrings(spec.FileIDs, fileID)
	return index < len(spec.FileIDs) && spec.FileIDs[index] == fileID
}

func (manager *jobManager) begin(spec jobSpec) (*activeJob, error) {
	spec = normalizeJobSpec(spec)
	ctx, cancel := context.WithCancel(context.Background())

	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.stopping {
		cancel()
		return nil, ErrJobsStopped
	}
	for _, fileID := range spec.FileIDs {
		if manager.blocked[fileID] > 0 {
			cancel()
			return nil, fmt.Errorf("%w for %q", ErrFileJobsBlocked, fileID)
		}
	}
	if manager.active != nil {
		cancel()
		return nil, fmt.Errorf("%w (%s)", ErrJobAlreadyRunning, manager.active.spec.Title)
	}
	if manager.seq >= maxJobProgressValue {
		cancel()
		return nil, ErrJobSequenceExhausted
	}
	manager.seq++
	job := &activeJob{
		id:       fmt.Sprintf("job%d", manager.seq),
		sequence: manager.seq,
		spec:     spec,
		ctx:      ctx,
		cancel:   cancel,
		total:    spec.Total,
		done:     make(chan struct{}),
	}
	manager.active = job
	return job, nil
}

func emitJobEventSafely(emit func(string, any), name string, data any) {
	if emit == nil {
		return
	}
	defer func() { _ = recover() }()
	emit(name, data)
}

func jobEventData(job *activeJob) map[string]any {
	return map[string]any{
		"id":        job.id,
		"sequence":  job.sequence,
		"title":     job.spec.Title,
		"kind":      job.spec.Kind,
		"fileId":    job.spec.FileID,
		"completed": job.completed,
		"total":     job.total,
		// Keep the old name during the event schema transition.
		"records": job.completed,
		"note":    job.note,
	}
}

func (manager *jobManager) startEvent(job *activeJob) {
	manager.eventMu.Lock()
	defer manager.eventMu.Unlock()
	manager.mu.Lock()
	emit := manager.emit
	payload := jobEventData(job)
	manager.mu.Unlock()
	emitJobEventSafely(emit, "bigfile:job-start", payload)
}

func (manager *jobManager) report(jobID string, completed, total int64, note string) {
	completed = clampJobProgress(completed)
	total = clampJobProgress(total)
	note = boundedJobNote(note)

	// Progress and terminal delivery share eventMu, preventing a helper
	// goroutine from emitting progress after the end event.
	manager.eventMu.Lock()
	defer manager.eventMu.Unlock()
	manager.mu.Lock()
	job := manager.active
	if job == nil || job.id != jobID || job.cancelRequested || job.ctx.Err() != nil {
		manager.mu.Unlock()
		return
	}
	if total > 0 {
		if job.total == 0 || total > job.total {
			job.total = total
		} else {
			total = job.total
		}
	}
	if completed < job.completed {
		completed = job.completed
	}
	if job.total > 0 && completed > job.total {
		completed = job.total
	}
	if completed == job.completed && note == job.note && total == 0 {
		manager.mu.Unlock()
		return
	}
	job.completed = completed
	job.note = note
	payload := jobEventData(job)
	emit := manager.emit
	manager.mu.Unlock()
	emitJobEventSafely(emit, "bigfile:job-progress", payload)
}

func (manager *jobManager) finish(job *activeJob, status string) {
	defer job.doneOnce.Do(func() { close(job.done) })
	manager.eventMu.Lock()
	defer manager.eventMu.Unlock()
	job.cancel()

	manager.mu.Lock()
	if manager.active == job {
		manager.active = nil
	}
	if manager.stopping && manager.active == nil && manager.stopDone != nil {
		select {
		case <-manager.stopDone:
		default:
			close(manager.stopDone)
		}
	}
	payload := jobEventData(job)
	payload["status"] = status
	emit := manager.emit
	manager.mu.Unlock()
	emitJobEventSafely(emit, "bigfile:job-end", payload)
}

// blockFile closes job admission for one file before lifecycle cancellation.
// It returns an idempotent unblock function for reversible refresh attempts.
func (manager *jobManager) blockFile(fileID string) func() {
	manager.mu.Lock()
	if manager.blocked == nil {
		manager.blocked = make(map[string]int)
	}
	manager.blocked[fileID]++
	manager.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			manager.mu.Lock()
			if manager.blocked[fileID] <= 1 {
				delete(manager.blocked, fileID)
			} else {
				manager.blocked[fileID]--
			}
			manager.mu.Unlock()
		})
	}
}

// stop raises an irreversible admission barrier, cancels the current owner,
// and returns a channel closed when that job has finished its cleanup.
func (manager *jobManager) stop() <-chan struct{} {
	manager.mu.Lock()
	if manager.stopDone == nil {
		manager.stopDone = make(chan struct{})
	}
	done := manager.stopDone
	if manager.stopping {
		manager.mu.Unlock()
		return done
	}
	manager.stopping = true
	job := manager.active
	if job == nil {
		close(done)
		manager.mu.Unlock()
		return done
	}
	shouldCancel := !job.committed
	if shouldCancel {
		job.cancelRequested = true
	}
	cancel := job.cancel
	manager.mu.Unlock()
	if shouldCancel {
		cancel()
	}
	return done
}

type serviceJobPublicationBoundary func(func() (bool, error)) error

type serviceJobPublicationContextKey struct{}
type serviceJobIdentityContextKey struct{}

func serviceJobID(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	id, ok := ctx.Value(serviceJobIdentityContextKey{}).(string)
	return id, ok && id != ""
}

// commit serializes an in-memory job result with cancellation. It is used for
// exact edit preparation: cancellation first prevents session installation;
// installation first makes a later cancel stale.
func (manager *jobManager) commit(jobID string, install func() bool) bool {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.active
	if job == nil || job.id != jobID || job.cancelRequested || job.ctx.Err() != nil {
		return false
	}
	if install == nil || !install() {
		return false
	}
	job.committed = true
	return true
}

// publishOutput serializes the irreversible publication point with every
// cancellation path. Cancellation that wins the lock prevents publication;
// publication that wins makes a later cancel stale. The bool returned by
// publish records whether the complete final output became visible even when a
// later durability step returned an error.
func (manager *jobManager) publishOutput(jobID string, publish func() (bool, error)) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.active
	if job == nil || job.id != jobID || job.cancelRequested || job.ctx.Err() != nil {
		return errors.Join(ErrJobCancelled, context.Canceled)
	}
	published, err := publish()
	if manager.afterOutputPublication != nil {
		manager.afterOutputPublication(jobID, err)
	}
	if published {
		job.committed = true
	}
	return err
}

// publishAtomicOutput owns AtomicOutput's final-name publication point. A
// successful publication, or PublicationError evidence that an artifact may
// already be visible, makes cancellation stale for the remainder of the job.
func (manager *jobManager) publishAtomicOutput(jobID string, publish func() error) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.active
	if job == nil || job.id != jobID || job.cancelRequested || job.ctx.Err() != nil {
		return errors.Join(ErrJobCancelled, context.Canceled)
	}
	err := publish()
	if manager.afterOutputPublication != nil {
		manager.afterOutputPublication(jobID, err)
	}
	var publication *fileio.PublicationError
	if err == nil || errors.As(err, &publication) {
		job.committed = true
	}
	return err
}

func publishServiceJobOutput(ctx context.Context, publish func() (bool, error)) error {
	if publish == nil {
		return errors.New("output publication callback is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if boundary, ok := ctx.Value(serviceJobPublicationContextKey{}).(serviceJobPublicationBoundary); ok && boundary != nil {
		return boundary(publish)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := publish()
	return err
}

func (manager *jobManager) cancelID(jobID string) error {
	manager.mu.Lock()
	job := manager.active
	if job == nil {
		manager.mu.Unlock()
		return ErrNoActiveJob
	}
	if job.id != jobID {
		manager.mu.Unlock()
		return fmt.Errorf("%w: got %q", ErrJobIDMismatch, jobID)
	}
	if job.committed {
		manager.mu.Unlock()
		return ErrJobAlreadyCommitted
	}
	job.cancelRequested = true
	cancel := job.cancel
	manager.mu.Unlock()
	cancel()
	return nil
}

func (manager *jobManager) cancelFile(fileID string) bool {
	manager.mu.Lock()
	job := manager.active
	if job == nil || !job.spec.ownsFile(fileID) {
		manager.mu.Unlock()
		return false
	}
	if job.committed {
		manager.mu.Unlock()
		return false
	}
	job.cancelRequested = true
	cancel := job.cancel
	manager.mu.Unlock()
	cancel()
	return true
}

func jobStatus(err error, panicked bool) string {
	switch {
	case panicked:
		return "panicked"
	case errors.Is(err, context.Canceled), errors.Is(err, ErrJobCancelled):
		return "cancelled"
	case err != nil:
		return "failed"
	default:
		return "completed"
	}
}

func (service *FileService) runServiceJob[T any](spec jobSpec, callback func(context.Context, func(int64, int64, string)) (T, error)) (result T, retErr error) {
	if service == nil || callback == nil {
		return result, errors.New("big-file service and job callback are required")
	}
	job, err := service.jobs().begin(spec)
	if err != nil {
		return result, err
	}
	ctx := context.WithValue(job.ctx, serviceJobIdentityContextKey{}, job.id)
	ctx = context.WithValue(ctx, serviceJobPublicationContextKey{}, serviceJobPublicationBoundary(func(publish func() (bool, error)) error {
		return service.jobs().publishOutput(job.id, publish)
	}))
	ctx = fileio.WithPublicationBoundary(ctx, func(publish func() error) error {
		return service.jobs().publishAtomicOutput(job.id, publish)
	})
	panicked := false
	defer func() {
		if recovered := recover(); recovered != nil {
			panicked = true
			var zero T
			result = zero
			retErr = fmt.Errorf("big-file job %q panicked: %v", job.spec.Title, recovered)
		}
		if errors.Is(retErr, context.Canceled) {
			var zero T
			result = zero
			retErr = errors.Join(ErrJobCancelled, context.Canceled)
		}
		service.jobs().finish(job, jobStatus(retErr, panicked))
	}()

	service.jobs().startEvent(job)
	return callback(ctx, func(completed, total int64, note string) {
		service.jobs().report(job.id, completed, total, note)
	})
}

func (s *FileService) withFileJob(fileID, title string, callback func(context.Context, func(int64, string)) (TransformResult, error)) (TransformResult, error) {
	return s.withFileJobResult(fileID, title, jobKindTransform, callback)
}

func (service *FileService) withJobResult[T any](title string, callback func(context.Context, func(int64, string)) (T, error)) (T, error) {
	return service.withOwnedJobResult(jobSpec{Title: title, Kind: jobKindTransform}, callback)
}

func (service *FileService) withFileJobResult[T any](fileID, title, kind string, callback func(context.Context, func(int64, string)) (T, error)) (T, error) {
	return service.withOwnedJobResult(jobSpec{Title: title, Kind: kind, FileID: fileID}, callback)
}

func (service *FileService) withOwnedJobResult[T any](spec jobSpec, callback func(context.Context, func(int64, string)) (T, error)) (T, error) {
	return service.runServiceJob(spec, func(ctx context.Context, progress func(int64, int64, string)) (T, error) {
		return callback(ctx, func(completed int64, note string) {
			progress(completed, spec.Total, note)
		})
	})
}

func validateJobID(jobID string) error {
	if len(jobID) < 4 || len(jobID) > 32 || !strings.HasPrefix(jobID, "job") {
		return ErrInvalidJobID
	}
	number := jobID[3:]
	if number == "" || number[0] == '0' {
		return ErrInvalidJobID
	}
	for _, char := range number {
		if char < '0' || char > '9' {
			return ErrInvalidJobID
		}
	}
	value, err := strconv.ParseInt(number, 10, 64)
	if err != nil || value <= 0 || value > maxJobProgressValue {
		return ErrInvalidJobID
	}
	return nil
}

// CancelJob cancels exactly the active job represented by jobID. A stale ID is
// rejected and cannot cancel a newer operation.
func (s *FileService) CancelJob(jobID string) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	return s.jobs().cancelID(jobID)
}
