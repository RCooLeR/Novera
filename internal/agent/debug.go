package agent

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	agentDebugResponsesEnv          = "NOVERA_AGENT_DEBUG_RESPONSES"
	maxAgentDebugPreviewBytes       = 64 * 1024
	maxAgentDebugLogBytes     int64 = 4 * 1024 * 1024
)

// AgentDebugEntry is an opt-in provider trace for debugging Agent mode. It
// intentionally avoids request bodies, auth headers, model content, and tool
// arguments. ResponsePreview contains only a redacted structural JSON preview.
type AgentDebugEntry struct {
	Time              string   `json:"time"`
	RunID             string   `json:"runId"`
	Event             string   `json:"event"`
	Model             string   `json:"model"`
	Endpoint          string   `json:"endpoint"`
	StatusCode        int      `json:"statusCode"`
	DurationMs        int64    `json:"durationMs"`
	MessageCount      int      `json:"messageCount"`
	Tools             []string `json:"tools,omitempty"`
	ToolsDisabled     bool     `json:"toolsDisabled"`
	RequestBytes      int      `json:"requestBytes"`
	ResponseBytes     int      `json:"responseBytes"`
	ResponseTruncated bool     `json:"responseTruncated"`
	ResponseRedacted  bool     `json:"responseRedacted"`
	ResponsePreview   string   `json:"responsePreview,omitempty"`
	Error             string   `json:"error,omitempty"`
}

type agentDebugLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
}

func newAgentDebugLog() *agentDebugLog {
	return newAgentDebugLogAt(filepath.Join(auditConfigDir(), "Novera", "agent-debug.jsonl"))
}

func newAgentDebugLogAt(path string) *agentDebugLog {
	// Provider diagnostics are session-scoped. Removing previous files at
	// startup also clears raw-response logs created by older Novera versions.
	if err := removeAgentDebugLogs(path); err != nil {
		log.Printf("agent debug: clear previous logs: %v", err)
		return nil // fail closed instead of appending beside potentially raw data
	}
	if !agentDebugResponsesEnabled(os.Getenv(agentDebugResponsesEnv)) {
		return nil
	}
	return &agentDebugLog{path: path, maxBytes: maxAgentDebugLogBytes}
}

func removeAgentDebugLogs(path string) error {
	for _, candidate := range []string{path, path + ".1"} {
		if err := os.Remove(candidate); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func agentDebugResponsesEnabled(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (d *agentDebugLog) record(e AgentDebugEntry) {
	if d == nil {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	if int64(len(b)) > d.maxBytes {
		log.Printf("agent debug: entry is larger than the log budget; dropping it")
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(d.path), 0o700); err != nil {
		log.Printf("agent debug: mkdir: %v", err)
		return
	}
	if err := d.rotateIfNeeded(int64(len(b))); err != nil {
		log.Printf("agent debug: rotate: %v", err)
		return
	}
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("agent debug: open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		log.Printf("agent debug: write: %v", err)
	}
}

// rotateIfNeeded bounds retention to the current file and one backup, each no
// larger than maxBytes. Legacy files from the old unbounded logger are removed
// instead of being retained as an oversized backup.
func (d *agentDebugLog) rotateIfNeeded(nextBytes int64) error {
	info, err := os.Stat(d.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	backup := d.path + ".1"
	if info.Size() > d.maxBytes {
		if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
			return err
		}
		return os.Remove(d.path)
	}
	if info.Size()+nextBytes <= d.maxBytes {
		return nil
	}
	if err := os.Remove(backup); err != nil && !os.IsNotExist(err) {
		// Keep the existing bounded backup and truncate the current log; this
		// still preserves the two-file total-size invariant.
		return os.Truncate(d.path, 0)
	}
	if err := os.Rename(d.path, backup); err != nil {
		return os.Truncate(d.path, 0)
	}
	return nil
}

func debugResponsePreview(raw []byte) (preview string, truncated bool, redacted bool) {
	if len(raw) == 0 {
		return "", false, false
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Sprintf("[non-JSON provider response redacted: %d bytes]", len(raw)), false, true
	}
	if safe, ok := redactDebugScalar(payload); ok {
		payload = safe
	} else {
		redactDebugJSON(payload)
	}
	safe, err := json.Marshal(payload)
	if err != nil {
		return fmt.Sprintf("[provider response redacted: %d bytes]", len(raw)), false, true
	}
	if len(safe) > maxAgentDebugPreviewBytes {
		return fmt.Sprintf("[redacted provider response structure omitted: %d-byte preview exceeds 64 KiB]", len(safe)), true, true
	}
	return string(safe), false, true
}

func redactDebugJSON(value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if !safeDebugField(key) {
				delete(value, key)
				continue
			}
			if safe, ok := redactDebugScalar(child); ok {
				value[key] = safe
				continue
			}
			redactDebugJSON(child)
		}
	case []any:
		for i, child := range value {
			if safe, ok := redactDebugScalar(child); ok {
				value[i] = safe
				continue
			}
			redactDebugJSON(child)
		}
	}
}

func safeDebugField(field string) bool {
	switch strings.ToLower(field) {
	case "id", "object", "created", "model", "system_fingerprint",
		"choices", "index", "message", "delta", "role", "content",
		"tool_calls", "function", "type", "name", "arguments", "finish_reason",
		"usage", "prompt_tokens", "completion_tokens", "total_tokens",
		"error", "code", "param":
		return true
	default:
		return false
	}
}

func redactDebugScalar(value any) (any, bool) {
	switch value := value.(type) {
	case string:
		return fmt.Sprintf("[REDACTED %d-byte string]", len([]byte(value))), true
	case float64:
		return "[REDACTED number]", true
	case bool:
		return "[REDACTED boolean]", true
	case nil:
		return nil, true
	default:
		return nil, false
	}
}
