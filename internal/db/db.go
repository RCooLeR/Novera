// Package db is the Wails service for database connections and read-only
// querying. Connection profiles persist as non-secret JSON; passwords live ONLY
// in the encrypted secret store (never in the profile file or returned to the
// UI). Every user query is normalised by sqlguard (single read-only SELECT) AND
// executed inside a read-only transaction — defense in depth. SQLite, Postgres,
// and MySQL are supported.
package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"novera/internal/secret"
	"novera/internal/sqlguard"
)

const (
	defaultRowLimit = 1000
	maxRowLimit     = 5000
	queryTimeout    = 30 * time.Second
	connectTimeout  = 10 * time.Second
)

// Profile is a saved connection. Password is input-only (json omitempty) and is
// never persisted or returned — it is moved into the secret store on save.
type Profile struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"` // sqlite | postgres | mysql
	File      string `json:"file"` // sqlite
	Host      string `json:"host"`
	Port      int    `json:"port"`
	Database  string `json:"database"`
	User      string `json:"user"`
	SSLMode   string `json:"sslMode"` // transport mode for postgres + mysql; empty = secure-by-default (see resolveTLS)
	SecretRef string `json:"secretRef"`
	Password  string `json:"password,omitempty"` // input only — never persisted
}

// TestResult reports a connectivity check.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// Table is a schema object listed for a connection.
type Table struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Column describes one column of a table.
type Column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

// QueryResult is a read-only query's columns + stringified rows. Nulls is a mask
// parallel to Rows marking which cells were SQL NULL, so the UI can distinguish a
// real NULL from the literal text "NULL" out-of-band (not by string compare).
type QueryResult struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Nulls     [][]bool   `json:"nulls"`
	RowCount  int        `json:"rowCount"`
	Truncated bool       `json:"truncated"`
	ElapsedMs int64      `json:"elapsedMs"`
}

// Service is the bound Wails db service.
type Service struct {
	mu       sync.Mutex
	path     string
	profiles []Profile
	secrets  *secret.Store
	loadErr  string // non-empty if the profiles file was corrupt; surfaced to the UI
}

// New constructs the db service, loading saved profiles.
func New(sec *secret.Store) *Service {
	s := &Service{path: filepath.Join(configDir(), "Novera", "db-profiles.json"), secrets: sec, profiles: []Profile{}}
	s.load()
	return s
}

// configDir resolves a stable, absolute per-user config directory, falling back
// to a temp dir (logged) rather than returning "" — which would yield a
// CWD-relative path that silently moves with the process working directory.
func configDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return dir
	}
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		return dir
	}
	dir := os.TempDir()
	log.Printf("db: no config/home dir available, falling back to %s", dir)
	return dir
}

func (s *Service) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.profiles); err != nil {
		// Preserve the corrupt file instead of clobbering it on next save, AND
		// record/log the failure so the user isn't left wondering why their saved
		// connections silently vanished.
		backup := s.path + ".corrupt"
		_ = os.Rename(s.path, backup)
		s.loadErr = "Your saved database connections could not be read and were set aside (backed up to " + backup + "). They've been reset."
		log.Printf("db: profiles file %s is corrupt (%v); backed up to %s", s.path, err, backup)
		s.profiles = []Profile{}
		return
	}
	if s.profiles == nil {
		s.profiles = []Profile{}
	}
}

// LoadError returns a user-facing message if the saved profiles file was corrupt
// on load (and was backed up), or "" otherwise. Bound to the UI so the silent
// "my connections disappeared" case becomes visible.
func (s *Service) LoadError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

func (s *Service) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.profiles, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ListProfiles returns saved profiles (never the password).
func (s *Service) ListProfiles() []Profile {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Profile, len(s.profiles))
	copy(out, s.profiles)
	for i := range out {
		out[i].Password = ""
	}
	return out
}

// SaveProfile creates or updates a profile. A non-empty Password is stored
// (encrypted) in the secret store and cleared from the persisted profile.
func (s *Service) SaveProfile(p Profile) (Profile, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.Kind = strings.ToLower(strings.TrimSpace(p.Kind))
	if p.Name == "" {
		return Profile{}, errors.New("a connection name is required")
	}
	if !validKind(p.Kind) {
		return Profile{}, fmt.Errorf("unsupported database kind %q", p.Kind)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.ID == "" {
		id, err := newID()
		if err != nil {
			return Profile{}, err
		}
		p.ID = id
	}
	if pw := p.Password; pw != "" {
		ref := "db.cred." + p.ID
		if err := s.secrets.Set(ref, pw); err != nil {
			return Profile{}, err
		}
		p.SecretRef = ref
	}
	p.Password = ""
	replaced := false
	for i := range s.profiles {
		if s.profiles[i].ID == p.ID {
			s.profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		s.profiles = append(s.profiles, p)
	}
	if err := s.persistLocked(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// DeleteProfile removes a profile and its stored credential.
func (s *Service) DeleteProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ref string
	kept := s.profiles[:0]
	for _, p := range s.profiles {
		if p.ID == id {
			ref = p.SecretRef
			continue
		}
		kept = append(kept, p)
	}
	s.profiles = kept
	// Persist the removal FIRST; only drop the credential once the profile is
	// durably gone, so a persist failure can't leave a profile pointing at a
	// secret we already deleted.
	if err := s.persistLocked(); err != nil {
		return err
	}
	if ref != "" {
		if derr := s.secrets.Delete(ref); derr != nil {
			log.Printf("db: failed to delete credential %q after removing profile: %v", ref, derr)
		}
	}
	return nil
}

// TestProfile opens the connection and pings it.
func (s *Service) TestProfile(id string) (TestResult, error) {
	p, ok := s.profile(id)
	if !ok {
		return TestResult{}, errors.New("connection not found")
	}
	conn, err := s.open(p)
	if err != nil {
		return TestResult{OK: false, Message: cleanDBErr(err).Error()}, nil
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		return TestResult{OK: false, Message: cleanDBErr(err).Error()}, nil
	}
	return TestResult{OK: true, Message: "Connection OK"}, nil
}

// ListTables returns base tables and views for the connection.
func (s *Service) ListTables(id string) ([]Table, error) {
	p, ok := s.profile(id)
	if !ok {
		return nil, errors.New("connection not found")
	}
	conn, err := s.open(p)
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	rows, err := conn.QueryContext(ctx, tableListSQL(p.Kind))
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer rows.Close()
	out := []Table{}
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		out = append(out, Table{Name: name, Type: normalizeTableType(typ)})
	}
	return out, rows.Err()
}

// ListColumns returns the columns of a table for the schema browser. The query
// is service-issued (not user SQL); sqlite identifiers are validated, network
// engines use bound parameters.
func (s *Service) ListColumns(id, table string) ([]Column, error) {
	p, ok := s.profile(id)
	if !ok {
		return nil, errors.New("connection not found")
	}
	conn, err := s.open(p)
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out := []Column{}

	if p.Kind == "sqlite" {
		if !safeIdent(table) {
			return nil, errors.New("invalid table name")
		}
		rows, err := conn.QueryContext(ctx, `PRAGMA table_info("`+table+`")`)
		if err != nil {
			return nil, cleanDBErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			var cid, notnull, pk int
			var name, ctype string
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				return nil, err
			}
			out = append(out, Column{Name: name, Type: ctype, Nullable: notnull == 0})
		}
		return out, rows.Err()
	}

	var query string
	var arg any
	switch p.Kind {
	case "postgres":
		// Scope to the schema that `SELECT * FROM <table>` would actually resolve
		// to (first match on the search_path), so a table name reused across
		// schemas doesn't merge column sets from every schema that has it.
		query = `SELECT column_name, data_type, is_nullable FROM information_schema.columns
			WHERE table_name = $1 AND table_schema = (
				SELECT table_schema FROM information_schema.columns
				WHERE table_name = $1 AND table_schema = ANY(current_schemas(true))
				ORDER BY array_position(current_schemas(true), table_schema)
				LIMIT 1
			)
			ORDER BY ordinal_position`
		arg = table
	case "mysql":
		query = `SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`
		arg = table
	default:
		return nil, fmt.Errorf("unsupported kind %q", p.Kind)
	}
	rows, err := conn.QueryContext(ctx, query, arg)
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name, dtype, nullable string
		if err := rows.Scan(&name, &dtype, &nullable); err != nil {
			return nil, err
		}
		out = append(out, Column{Name: name, Type: dtype, Nullable: strings.EqualFold(nullable, "YES")})
	}
	return out, rows.Err()
}

func safeIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// Query runs a guarded, read-only SELECT and returns columns + rows.
func (s *Service) Query(id, query string, limit int) (QueryResult, error) {
	p, ok := s.profile(id)
	if !ok {
		return QueryResult{}, errors.New("connection not found")
	}
	safe, err := sqlguard.NormalizeReadOnly(query, sqlguard.Options{Kind: p.Kind, AllowWith: true})
	if err != nil {
		return QueryResult{}, err
	}
	if limit <= 0 {
		limit = defaultRowLimit
	}
	if limit > maxRowLimit {
		limit = maxRowLimit
	}

	conn, err := s.open(p)
	if err != nil {
		return QueryResult{}, cleanDBErr(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()

	start := time.Now()
	rows, cleanup, err := s.runReadOnly(ctx, conn, p.Kind, safe)
	if err != nil {
		return QueryResult{}, err
	}
	defer cleanup()
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Columns: cols, Rows: [][]string{}, Nulls: [][]bool{}}
	for rows.Next() {
		if len(result.Rows) >= limit {
			result.Truncated = true
			break
		}
		cells := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return QueryResult{}, err
		}
		row := make([]string, len(cols))
		nullRow := make([]bool, len(cols))
		for i, c := range cells {
			nullRow[i] = c == nil
			row[i] = cellString(c)
		}
		result.Rows = append(result.Rows, row)
		result.Nulls = append(result.Nulls, nullRow)
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	result.ElapsedMs = time.Since(start).Milliseconds()
	return result, nil
}

// --- internals ---

func (s *Service) profile(id string) (Profile, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.profiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func (s *Service) open(p Profile) (*sql.DB, error) {
	pass := ""
	if p.SecretRef != "" && s.secrets != nil {
		pass, _ = s.secrets.Get(p.SecretRef)
	}
	driver, dsn, err := dsnFor(p, pass)
	if err != nil {
		return nil, err
	}
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(2)
	conn.SetConnMaxLifetime(time.Minute)
	return conn, nil
}

// runReadOnly executes query and returns the rows plus a cleanup func the
// caller must defer. SQLite (modernc) doesn't honour read-only tx options, so
// it is pinned to a single connection with PRAGMA query_only=ON (defense in
// depth alongside sqlguard); network engines run inside a real read-only
// transaction that is rolled back by cleanup as soon as the read completes —
// tied to the read's scope, not the ctx lifetime.
func (s *Service) runReadOnly(ctx context.Context, conn *sql.DB, kind, query string) (*sql.Rows, func(), error) {
	if kind == "sqlite" {
		// Grab one dedicated connection so the PRAGMA and the query are
		// guaranteed to run on the same session (a pooled DB could otherwise
		// split them across connections).
		c, err := conn.Conn(ctx)
		if err != nil {
			return nil, nil, cleanDBErr(err)
		}
		if _, err := c.ExecContext(ctx, "PRAGMA query_only = ON"); err != nil {
			_ = c.Close()
			return nil, nil, cleanDBErr(err)
		}
		rows, err := c.QueryContext(ctx, query)
		if err != nil {
			_ = c.Close()
			return nil, nil, cleanDBErr(err)
		}
		return rows, func() { _ = c.Close() }, nil
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		// Fail closed: never silently fall back to a non-transactional read.
		return nil, nil, fmt.Errorf("begin read-only transaction: %w", cleanDBErr(err))
	}
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		_ = tx.Rollback()
		return nil, nil, cleanDBErr(err)
	}
	return rows, func() { _ = tx.Rollback() }, nil
}

// credInDSN matches a "user:password@" credential pair in either a URL DSN
// (postgres://user:pass@host) or a Go-MySQL DSN (user:pass@tcp(host)/db) so the
// password can be redacted before any driver error reaches the UI.
var credInDSN = regexp.MustCompile(`(^|[/@])([^:/@\s]+):[^:@/\s]+@`)

// cleanDBErr redacts embedded credentials from a driver error.
func cleanDBErr(err error) error {
	if err == nil {
		return nil
	}
	msg := credInDSN.ReplaceAllString(err.Error(), "${1}${2}:***@")
	return errors.New(msg)
}

func dsnFor(p Profile, pass string) (driver, dsn string, err error) {
	switch p.Kind {
	case "sqlite":
		// The file path is user-chosen (via the profile editor's file picker) and
		// intentionally not workspace-contained — a local-first app legitimately
		// opens DBs anywhere. We do require it be absolute + cleaned so it can't
		// resolve unpredictably against the process CWD; the agent's db_query is
		// separately approval-gated against unattended reads.
		file := filepath.Clean(strings.TrimSpace(p.File))
		if file == "" || file == "." {
			return "", "", errors.New("a SQLite file path is required")
		}
		if !filepath.IsAbs(file) {
			return "", "", errors.New("the SQLite file path must be absolute")
		}
		return "sqlite", file, nil
	case "postgres":
		host := firstNonEmpty(p.Host, "localhost")
		port := p.Port
		if port == 0 {
			port = 5432
		}
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(p.User, pass),
			Host:   net.JoinHostPort(host, fmt.Sprint(port)),
			Path:   "/" + p.Database,
		}
		// Secure-by-default: a network host requires TLS so credentials are never
		// sent in cleartext, while loopback keeps the lenient `prefer` (most local
		// servers have no TLS configured). An explicit sslmode always wins; a
		// network host left on a plaintext mode is logged for audit honesty.
		sslmode := strings.ToLower(strings.TrimSpace(p.SSLMode))
		if sslmode == "" {
			if isLoopbackHost(host) {
				sslmode = "prefer"
			} else {
				sslmode = "require"
			}
		}
		if !isLoopbackHost(host) && (sslmode == "disable" || sslmode == "allow") {
			log.Printf("db: profile %q connects to network host %s with sslmode=%s — credentials may be sent UNENCRYPTED (explicit opt-in)", p.Name, host, sslmode)
		}
		q := url.Values{}
		q.Set("sslmode", sslmode)
		u.RawQuery = q.Encode()
		return "pgx", u.String(), nil
	case "mysql":
		host := firstNonEmpty(p.Host, "localhost")
		port := p.Port
		if port == 0 {
			port = 3306
		}
		// Build via the driver's typed config so credentials/db-name are escaped
		// and can't inject extra DSN params.
		cfg := mysqldriver.NewConfig()
		cfg.User = p.User
		cfg.Passwd = pass
		cfg.Net = "tcp"
		cfg.Addr = net.JoinHostPort(host, fmt.Sprint(port))
		cfg.DBName = p.Database
		cfg.ParseTime = true
		cfg.Timeout = connectTimeout
		cfg.MultiStatements = false
		cfg.AllowAllFiles = false
		// Secure-by-default transport, mirroring the Postgres policy. The SSLMode
		// field is reused (Postgres vocabulary) so one UI control covers both: a
		// network host encrypts in transit by default; loopback stays plaintext-
		// friendly; an explicit choice always wins. A network host opted into
		// plaintext is logged for audit honesty.
		switch strings.ToLower(strings.TrimSpace(p.SSLMode)) {
		case "disable", "allow", "off", "false":
			// plaintext (explicit opt-in) — leave cfg.TLSConfig unset
			if !isLoopbackHost(host) {
				log.Printf("db: profile %q connects to network host %s WITHOUT TLS (explicit opt-in) — credentials sent unencrypted", p.Name, host)
			}
		case "verify-ca", "verify-full", "true":
			cfg.TLSConfig = "true" // encrypt + verify the server certificate
		case "require", "skip-verify":
			cfg.TLSConfig = "skip-verify" // encrypt, no strict cert verification
		default: // empty / unknown -> secure-by-default
			if !isLoopbackHost(host) {
				cfg.TLSConfig = "skip-verify" // encrypt credentials over the network
			}
		}
		return "mysql", cfg.FormatDSN(), nil
	default:
		return "", "", fmt.Errorf("unsupported kind %q", p.Kind)
	}
}

func tableListSQL(kind string) string {
	switch kind {
	case "sqlite":
		return "SELECT name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name"
	case "postgres":
		return "SELECT table_name, table_type FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_name"
	case "mysql":
		return "SELECT table_name, table_type FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_name"
	default:
		return ""
	}
}

func normalizeTableType(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if strings.Contains(t, "view") {
		return "view"
	}
	return "table"
}

func cellString(v any) string {
	switch typed := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(typed)
	case string:
		return typed
	case time.Time:
		return typed.Format(time.RFC3339)
	default:
		return fmt.Sprint(typed)
	}
}

// validKind reports whether kind is one the app supports, using sqlguard's
// SupportedKinds as the single source of truth so the query guard and the
// connection layer can never disagree about which engines exist.
func validKind(kind string) bool {
	for _, k := range sqlguard.SupportedKinds {
		if kind == k {
			return true
		}
	}
	return false
}

func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// isLoopbackHost reports whether host refers to the local machine, where
// plaintext DB transport is acceptable by default (local dev). Anything else is
// a network host whose credentials must be encrypted in transit by default.
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if h == "" || h == "localhost" {
		return true
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]") // strip IPv6 brackets
	if i := strings.IndexByte(h, '%'); i >= 0 {
		h = h[:i] // strip IPv6 zone id
	}
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
