package agent

import (
	"fmt"
	"strings"
	"sync"
)

// boundedCommandOutput accepts all writes so os/exec can drain both pipes, but
// retains only a fixed prefix. It is safe for concurrent stdout/stderr copies.
type boundedCommandOutput struct {
	mu      sync.Mutex
	limit   int
	bytes   []byte
	total   int64
	omitted int64
}

func newBoundedCommandOutput(limit int) *boundedCommandOutput {
	if limit <= 0 {
		limit = 1
	}
	return &boundedCommandOutput{limit: limit, bytes: make([]byte, 0, limit)}
}

func (w *boundedCommandOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.total += int64(len(p))
	remaining := w.limit - len(w.bytes)
	if remaining > 0 {
		keep := len(p)
		if keep > remaining {
			keep = remaining
		}
		w.bytes = append(w.bytes, p[:keep]...)
	}
	w.omitted = w.total - int64(len(w.bytes))
	return len(p), nil
}

func (w *boundedCommandOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := strings.ToValidUTF8(string(w.bytes), "\uFFFD")
	if w.omitted > 0 {
		text = strings.TrimRight(text, "\r\n") + fmt.Sprintf("\n(output truncated; %d bytes omitted)", w.omitted)
	}
	return text
}
