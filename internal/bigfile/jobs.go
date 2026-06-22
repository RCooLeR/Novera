package bigfile

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// jobManager tracks the one in-flight long transform so the UI can show a
// progress toast and cancel it. Transforms run synchronously on their own Wails
// RPC goroutine; a concurrent CancelJob RPC flips the shared context, and the
// streaming transforms (which all honor ctx) abort and clean up their partial
// output. Progress/start/end are pushed to the frontend as application events.
type jobManager struct {
	mu      sync.Mutex
	seq     int64
	id      string
	title   string
	cancel  context.CancelFunc
}

func (s *FileService) jobs() *jobManager {
	s.jobOnce.Do(func() { s.jobMgr = &jobManager{} })
	return s.jobMgr
}

func emitEvent(name string, data any) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.EmitEvent(&application.CustomEvent{Name: name, Data: data})
}

// withJob runs fn under a cancellable context registered as the active job,
// emitting start/progress/end events. progress(records, note) may be called by
// fn to report incremental work.
func (s *FileService) withJob(title string, fn func(ctx context.Context, progress func(records int64, note string)) (TransformResult, error)) (TransformResult, error) {
	jm := s.jobs()
	ctx, cancel := context.WithCancel(context.Background())

	jm.mu.Lock()
	if jm.id != "" {
		// Only one in-flight transform is tracked/cancellable at a time; a second
		// would overwrite the first's cancel handle and make it uncancellable.
		jm.mu.Unlock()
		cancel()
		return TransformResult{}, fmt.Errorf("another transform is already running (%s)", jm.title)
	}
	jm.seq++
	id := fmt.Sprintf("job%d", jm.seq)
	jm.id = id
	jm.title = title
	jm.cancel = cancel
	jm.mu.Unlock()

	emitEvent("bigfile:job-start", map[string]any{"id": id, "title": title})
	progress := func(records int64, note string) {
		emitEvent("bigfile:job-progress", map[string]any{"id": id, "records": records, "note": note})
	}

	res, err := fn(ctx, progress)

	jm.mu.Lock()
	if jm.id == id {
		jm.id, jm.title, jm.cancel = "", "", nil
	}
	jm.mu.Unlock()
	cancel()

	emitEvent("bigfile:job-end", map[string]any{"id": id})
	if errors.Is(err, context.Canceled) {
		return TransformResult{}, errors.New("cancelled")
	}
	return res, err
}

// CancelJob cancels the active long transform, if any. The transform aborts and
// removes its partial output.
func (s *FileService) CancelJob() {
	jm := s.jobs()
	jm.mu.Lock()
	cancel := jm.cancel
	jm.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
