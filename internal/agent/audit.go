package agent

import (
	"bufio"
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// AuditEntry is one recorded agent tool action. The log is the durable,
// after-the-fact record of what the agent did and which mutations the user
// approved or denied — the approval/audit surface the UI reads back.
type AuditEntry struct {
	Time     string `json:"time"` // RFC3339
	RunID    string `json:"runId"`
	CallID   string `json:"callId"`
	Tool     string `json:"tool"`
	Summary  string `json:"summary"`  // short, secret-free description of the args
	Decision string `json:"decision"` // auto | approved | denied
	Status   string `json:"status"`   // ok | error | denied
	Detail   string `json:"detail"`   // clipped result/error
}

// auditLog appends agent actions to a JSONL file under the user config dir. A
// JSONL append is crash-safe (a torn final line is skipped on read) and cheap.
type auditLog struct {
	mu   sync.Mutex
	path string
}

func newAuditLog() *auditLog {
	return &auditLog{path: filepath.Join(auditConfigDir(), "Novera", "agent-audit.jsonl")}
}

// record appends an entry. Failures are logged, never fatal — auditing must not
// break an agent run.
func (a *auditLog) record(e AuditEntry) {
	if a == nil {
		return
	}
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(a.path), 0o755); err != nil {
		log.Printf("agent audit: mkdir: %v", err)
		return
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("agent audit: open: %v", err)
		return
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		log.Printf("agent audit: write: %v", err)
	}
}

// list returns up to limit most-recent entries (newest first). limit<=0 returns
// all retained entries.
func (a *auditLog) list(limit int) []AuditEntry {
	out := []AuditEntry{}
	if a == nil {
		return out
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	f, err := os.Open(a.path)
	if err != nil {
		return out // no log yet
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			out = append(out, e)
		}
	}
	// Newest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	if limit > 0 && limit < len(out) {
		out = out[:limit]
	}
	return out
}

// auditSummary extracts a short, secret-free identifier from a tool's args. It
// deliberately excludes free-form content fields (e.g. write_file "content")
// so file bodies / pasted secrets never land in the plaintext audit file.
func auditSummary(args map[string]any) string {
	for _, k := range []string{"url", "path", "command", "sql", "query", "table", "outPath", "outDir", "connectionId"} {
		if v := strings.TrimSpace(getStr(args, k)); v != "" {
			if k == "url" {
				return auditURLSummary(v)
			}
			return clip(v, 200)
		}
	}
	return ""
}

func auditURLSummary(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && u.Host != "" {
		u.User = nil
		u.RawQuery = ""
		u.Fragment = ""
		return clip(u.String(), 200)
	}
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	return clip(strings.TrimSpace(raw), 200)
}

func auditConfigDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return dir
	}
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		return dir
	}
	return os.TempDir()
}
