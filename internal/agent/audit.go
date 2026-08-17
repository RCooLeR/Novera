package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novera/internal/persistfile"
)

// AuditEntry is one recorded agent tool action. The log is the durable,
// after-the-fact record of what the agent did and which mutations the user
// approved or denied — the approval/audit surface the UI reads back.
type AuditEntry struct {
	FormatVersion int    `json:"formatVersion,omitempty"`
	Time          string `json:"time"` // RFC3339
	RunID         string `json:"runId"`
	CallID        string `json:"callId"`
	Tool          string `json:"tool"`
	Summary       string `json:"summary"`                // bounded target/size metadata; never raw free-form payloads
	Decision      string `json:"decision"`               // auto | approved | denied
	Status        string `json:"status"`                 // ok | error | denied
	Detail        string `json:"detail"`                 // bounded outcome metadata; never raw result/error text
	IntentDigest  string `json:"intentDigest,omitempty"` // exact reviewed operation, blank for automatic actions
}

// auditAction is the write-side representation of an audit entry. It
// deliberately has no Summary or Detail string: record derives both fields
// from structured metadata so a future caller cannot accidentally persist a
// tool result, error, command, SQL statement, or search query.
type auditAction struct {
	Time         string
	RunID        string
	CallID       string
	Tool         string
	Args         map[string]any
	Decision     string
	Status       string
	IntentDigest string
	ResultBytes  int
}

const auditFormatVersion = 2

const (
	maxAuditFileBytes  = 8 << 20
	retainAuditBytes   = 4 << 20
	maxAuditEntryBytes = 1 << 20
)

// auditLog appends agent actions to a JSONL file under the user config dir. A
// JSONL append is crash-safe (a torn final line is skipped on read) and cheap.
type auditLog struct {
	mu       sync.Mutex
	path     string
	prepared bool
}

func newAuditLog() *auditLog {
	a := &auditLog{path: filepath.Join(auditConfigDir(), "Novera", "agent-audit.jsonl")}
	a.mu.Lock()
	if err := a.sanitizeAndCompactLocked(false, retainAuditBytes); err != nil {
		log.Printf("agent audit: privacy migration: %v", err)
	} else {
		a.prepared = true
	}
	a.mu.Unlock()
	return a
}

// record appends an entry. Failures are logged, never fatal — auditing must not
// break an agent run. All payload-derived text is reduced to safe metadata
// before the JSON representation is created.
func (a *auditLog) record(action auditAction) {
	if a == nil {
		return
	}
	e := AuditEntry{
		FormatVersion: auditFormatVersion,
		Time:          action.Time,
		RunID:         action.RunID,
		CallID:        action.CallID,
		Tool:          action.Tool,
		Summary:       auditSummary(action.Args),
		Decision:      action.Decision,
		Status:        action.Status,
		Detail:        auditDetail(action.Status, action.ResultBytes),
		IntentDigest:  action.IntentDigest,
	}
	if e.Time == "" {
		e.Time = time.Now().Format(time.RFC3339)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	b = append(b, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.prepared {
		if err := a.sanitizeAndCompactLocked(false, retainAuditBytes); err != nil {
			log.Printf("agent audit: privacy migration: %v", err)
			return
		}
		a.prepared = true
	}
	dir := filepath.Dir(a.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("agent audit: mkdir: %v", err)
		return
	}
	dirInfo, err := os.Lstat(dir)
	if err != nil || dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		log.Printf("agent audit: unsafe directory %q", dir)
		return
	}
	info, err := os.Lstat(a.path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			log.Printf("agent audit: unsafe file %q", a.path)
			return
		}
		if info.Size()+int64(len(b)) > maxAuditFileBytes {
			if err := a.sanitizeAndCompactLocked(true, retainAuditBytes); err != nil {
				log.Printf("agent audit: compact: %v", err)
				return
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		log.Printf("agent audit: inspect: %v", err)
		return
	}
	f, err := os.OpenFile(a.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		log.Printf("agent audit: open: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.Write(b); err != nil {
		log.Printf("agent audit: write: %v", err)
	}
}

// sanitizeAndCompactLocked rewrites the application-owned audit journal using
// only the current structured schema. Legacy free-form fields, unknown JSON
// fields, malformed/torn lines, and entries outside the bounded newest window
// are removed from the physical file, not merely hidden by the read API.
func (a *auditLog) sanitizeAndCompactLocked(force bool, retainBytes int) error {
	if a == nil || strings.TrimSpace(a.path) == "" {
		return nil
	}
	if retainBytes <= 0 || retainBytes > maxAuditFileBytes {
		return fmt.Errorf("invalid retention budget %d", retainBytes)
	}
	dir := filepath.Dir(a.path)
	dirInfo, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if dirInfo.Mode()&os.ModeSymlink != 0 || !dirInfo.IsDir() {
		return fmt.Errorf("%w: audit directory %q is linked or not a directory", persistfile.ErrUnsafePath, dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure audit directory permissions: %w", err)
	}
	pathInfo, err := os.Lstat(a.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return fmt.Errorf("%w: audit path %q is linked or not regular", persistfile.ErrUnsafePath, a.path)
	}
	f, err := os.Open(a.path)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !os.SameFile(pathInfo, info) {
		return fmt.Errorf("%w: audit path %q changed while opening", persistfile.ErrUnsafePath, a.path)
	}
	if err := os.Chmod(a.path, 0o600); err != nil {
		return fmt.Errorf("secure audit permissions: %w", err)
	}
	rewrite := force || info.Size() > int64(retainBytes)
	lines := make([][]byte, 0, 256)
	total := 0
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxAuditEntryBytes)
	for scanner.Scan() {
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			rewrite = true
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			rewrite = true
			continue
		}
		if entry.FormatVersion < auditFormatVersion {
			entry.FormatVersion = auditFormatVersion
			entry.Summary = "legacy audit payload omitted"
			entry.Detail = "legacy audit payload omitted"
			rewrite = true
		}
		safe, err := json.Marshal(entry)
		if err != nil {
			rewrite = true
			continue
		}
		if !bytes.Equal(raw, safe) {
			// Re-marshalling strips any unknown legacy fields as well.
			rewrite = true
		}
		safe = append(safe, '\n')
		if len(safe) > retainBytes {
			rewrite = true
			continue
		}
		lines = append(lines, safe)
		total += len(safe)
		for total > retainBytes && len(lines) > 0 {
			total -= len(lines[0])
			lines[0] = nil
			lines = lines[1:]
			rewrite = true
		}
	}
	if err := scanner.Err(); err != nil {
		// An oversized/corrupt legacy line must not keep the old plaintext file
		// in place. Publish the successfully sanitized newest window instead.
		rewrite = true
		log.Printf("agent audit: dropped unreadable legacy tail: %v", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	closed = true
	if !rewrite {
		return nil
	}
	out := make([]byte, 0, total)
	for _, line := range lines {
		out = append(out, line...)
	}
	return persistfile.WriteAtomic(a.path, out, 0o600)
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
	if !a.prepared {
		if err := a.sanitizeAndCompactLocked(false, retainAuditBytes); err != nil {
			log.Printf("agent audit: privacy migration: %v", err)
			return out
		}
		a.prepared = true
	}
	raw, err := persistfile.Read(a.path, maxAuditFileBytes)
	if err != nil {
		return out // no log yet
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if json.Unmarshal([]byte(line), &e) == nil {
			if e.FormatVersion < auditFormatVersion {
				// Older releases persisted caller-provided free-form summaries and
				// tool output excerpts. Never surface those legacy payloads through
				// the UI after upgrading; users can separately decide whether to
				// retain or delete the underlying audit file.
				e.Summary = "legacy audit payload omitted"
				e.Detail = "legacy audit payload omitted"
			}
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

// auditSummary extracts a short, secret-free description from a tool's args.
// Target paths and identifiers remain useful for the audit viewer. Free-form
// payloads are represented only by their byte length, so commands, SQL, search
// queries, file bodies, headers, and request bodies never land in the plaintext
// audit file.
func auditSummary(args map[string]any) string {
	parts := make([]string, 0, 2)
	for _, k := range []string{"url", "path", "table", "outPath", "outDir", "connectionId"} {
		if v := strings.TrimSpace(getStr(args, k)); v != "" {
			if k == "url" {
				parts = append(parts, auditURLSummary(v))
			} else {
				parts = append(parts, clipAuditMetadata(v, 200))
			}
			break
		}
	}
	for _, field := range []struct {
		key   string
		label string
	}{
		{key: "command", label: "command"},
		{key: "sql", label: "SQL"},
		{key: "query", label: "query"},
	} {
		if value := strings.TrimSpace(getStr(args, field.key)); value != "" {
			parts = append(parts, fmt.Sprintf("%s omitted (%d bytes)", field.label, len(value)))
			break
		}
	}
	return clipAuditMetadata(strings.Join(parts, "; "), 240)
}

func auditURLSummary(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err == nil && u.Host != "" {
		u.User = nil
		u.Path = ""
		u.RawPath = ""
		u.RawQuery = ""
		u.Fragment = ""
		return clipAuditMetadata(u.String(), 200)
	}
	return "invalid URL omitted"
}

func auditDetail(status string, resultBytes int) string {
	if resultBytes < 0 {
		resultBytes = 0
	}
	switch status {
	case "ok":
		return fmt.Sprintf("completed; result text omitted (%d bytes)", resultBytes)
	case "error":
		return fmt.Sprintf("failed; result and diagnostic text omitted (%d bytes)", resultBytes)
	case "denied":
		return "not run; denied before dispatch"
	case "canceled":
		return "canceled; result and diagnostic text omitted"
	case "completed_after_cancel":
		return fmt.Sprintf("completed after cancellation; result text omitted (%d bytes)", resultBytes)
	case "error_after_cancel":
		return fmt.Sprintf("failed after cancellation; result and diagnostic text omitted (%d bytes)", resultBytes)
	default:
		return "outcome recorded; result and diagnostic text omitted"
	}
}

func clipAuditMetadata(value string, maxRunes int) string {
	value = strings.TrimSpace(value)
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes]) + "…"
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
