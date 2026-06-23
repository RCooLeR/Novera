package agent

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxAgentDebugRawBodyBytes = 5 * 1024 * 1024

// AgentDebugEntry is a temporary raw provider trace for debugging Agent mode.
// It intentionally avoids request bodies and auth headers; RawResponse is the
// provider's response body, capped only to keep the JSONL file bounded.
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
	RawResponse       string   `json:"rawResponse"`
	Error             string   `json:"error,omitempty"`
}

type agentDebugLog struct {
	mu   sync.Mutex
	path string
}

func newAgentDebugLog() *agentDebugLog {
	return &agentDebugLog{path: filepath.Join(auditConfigDir(), "Novera", "agent-debug.jsonl")}
}

func (d *agentDebugLog) record(e AgentDebugEntry) {
	if d == nil {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
		log.Printf("agent debug: mkdir: %v", err)
		return
	}
	f, err := os.OpenFile(d.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("agent debug: open: %v", err)
		return
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		log.Printf("agent debug: write: %v", err)
	}
}

func debugRawBody(raw []byte) (string, bool) {
	if len(raw) <= maxAgentDebugRawBodyBytes {
		return string(raw), false
	}
	return string(raw[:maxAgentDebugRawBodyBytes]), true
}
