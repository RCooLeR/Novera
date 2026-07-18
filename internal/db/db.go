// Package db is the Wails service for database connections and read-only
// querying. Connection profiles persist as non-secret JSON; passwords live ONLY
// in the encrypted secret store (never in the profile file or returned to the
// UI). Every user query is normalised by sqlguard (single read-only SELECT) AND
// executed inside a read-only transaction — defense in depth. SQLite, Postgres,
// and MySQL are supported.
package db

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	mysqldriver "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"novera/internal/persistfile"
	"novera/internal/sqlguard"
)

const (
	defaultRowLimit          = 1000
	maxRowLimit              = 5000
	queryTimeout             = 30 * time.Second
	connectTimeout           = 10 * time.Second
	maxProfilesSize          = 4 << 20
	maxProfiles              = 10_000
	maxProfileField          = 32 << 10
	maxDBPassword            = 1 << 20
	maxQueryColumns          = 256
	maxQueryCellBytes        = 1 << 20
	maxQueryResultBytes      = int64(8 << 20)
	maxQueryColumnNameBytes  = 4 << 10
	maxMetadataObjects       = 10_000
	maxMetadataColumns       = 4_096
	maxMetadataFieldBytes    = 4 << 10
	maxMetadataBytes         = int64(4 << 20)
	defaultTablePreviewLimit = 100
)

const (
	queryTruncatedRows  = "row_limit"
	queryTruncatedBytes = "byte_limit"
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
	SSLMode   string `json:"sslMode"`            // transport mode for postgres + mysql; empty = secure-by-default (see resolveTLS)
	SecretRef string `json:"secretRef"`          // output-only backend-owned profile/scope handle; caller input is ignored
	Password  string `json:"password,omitempty"` // input only — never persisted
}

// TestResult reports a connectivity check.
type TestResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// ProfileCredentialStatus is a fail-closed, non-secret description of whether
// the credential referenced by a profile can actually be used. The renderer
// must not infer this from SecretRef alone: a retained ref may be quarantined
// legacy evidence, missing, or unreadable while the encrypted store is ill.
type ProfileCredentialStatus struct {
	ProfileID string `json:"profileId"`
	SecretRef string `json:"secretRef"` // non-secret correlation handle already present on Profile
	Status    string `json:"status"`    // none | verified | quarantined | unavailable
}

// SaveProfileResult preserves the backend-issued identity even when the
// profile replacement was published but directory finalization failed. A
// non-empty FinalizationWarning means the visible state is forward-committed,
// but durability is uncertain and further mutations are blocked.
type SaveProfileResult struct {
	Profile             Profile `json:"profile"`
	FinalizationWarning string  `json:"finalizationWarning"`
}

// DeleteProfileResult distinguishes a forward-committed deletion from an
// ordinary pre-publication failure without exposing credential material.
type DeleteProfileResult struct {
	Deleted             bool   `json:"deleted"`
	FinalizationWarning string `json:"finalizationWarning"`
}

// Table is a schema object listed for a connection.
type Table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
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
	// TruncationReason is "row_limit" or "byte_limit" when Truncated is true.
	// Column-count and per-cell violations are errors instead of partial success.
	TruncationReason string `json:"truncationReason"`
	ElapsedMs        int64  `json:"elapsedMs"`
}

// Service is the bound Wails db service.
type Service struct {
	mu       sync.Mutex
	path     string
	profiles []Profile
	secrets  SecretStore
	loadErr  string // non-empty if the profiles file was corrupt; surfaced to the UI
	// writeAtomic is an injectable persistence seam for deterministic tests of
	// the narrow post-publication/finalization failure window.
	writeAtomic func(path string, data []byte, perm os.FileMode) error
}

// SecretStore is the narrow store surface needed for profile-owned database
// credentials. The bound renderer never receives this object.
type SecretStore interface {
	Get(ref string) (string, bool)
	Set(ref, value string) error
	Delete(ref string) error
}

type secretStoreHealthReporter interface {
	Health() error
}

type checkedSecretStore interface {
	GetChecked(ref string) (value string, found bool, err error)
}

type checkedManySecretStore interface {
	GetManyChecked(refs []string) (map[string]string, error)
}

type replacingSecretStore interface {
	Replace(oldRef, newRef, value string) error
}

type secretPresenceStore interface {
	Has(ref string) bool
}

func secretStoreHealth(store SecretStore) error {
	if reporter, ok := store.(secretStoreHealthReporter); ok {
		return reporter.Health()
	}
	return nil
}

func secretStoreGetChecked(store SecretStore, ref string) (string, bool, error) {
	if checked, ok := store.(checkedSecretStore); ok {
		return checked.GetChecked(ref)
	}
	value, found := store.Get(ref)
	return value, found, nil
}

// New constructs the db service, loading saved profiles.
func New(sec SecretStore) *Service {
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

func legacyDBCredentialRef(id string) string {
	return "db.cred." + strings.TrimSpace(id)
}

// ownedDBCredentialRef returns only a credential reference that this exact
// profile is allowed to own. The legacy form is purpose-bound by profile ID;
// arbitrary caller-selected or cross-subsystem references are never accepted.
func ownedDBCredentialRef(p Profile) string {
	ref := strings.TrimSpace(p.SecretRef)
	if ref == "" || dbCredentialRef(p) == "" {
		return ""
	}
	if ref == dbCredentialRef(p) {
		return ref
	}
	if strings.TrimSpace(p.ID) != "" && ref == legacyDBCredentialRef(p.ID) {
		return ref
	}
	return ""
}

// dbCredentialRef binds a password to one backend-issued profile ID and its
// complete connection/security scope. Editing the host, user, database, kind,
// port, or TLS mode therefore cannot silently carry the old password to the new
// destination; the user must provide it again.
func dbCredentialRef(p Profile) string {
	id := strings.TrimSpace(p.ID)
	kind := strings.ToLower(strings.TrimSpace(p.Kind))
	if id == "" || kind == "" || kind == "sqlite" {
		return ""
	}
	host := strings.ToLower(strings.TrimSpace(firstNonEmpty(p.Host, "localhost")))
	port := p.Port
	if port == 0 {
		if kind == "postgres" {
			port = 5432
		} else if kind == "mysql" {
			port = 3306
		}
	}
	scope := strings.Join([]string{
		id,
		kind,
		host,
		fmt.Sprintf("%d", port),
		strings.TrimSpace(p.Database),
		strings.TrimSpace(p.User),
		strings.ToLower(strings.TrimSpace(p.SSLMode)),
	}, "\n")
	sum := sha256.Sum256([]byte(scope))
	return fmt.Sprintf("db.cred.v1.%x", sum[:])
}

// sanitizeLoadedCredentialRefsLocked accepts only the current scoped handle or
// the exact per-profile handle emitted by old releases. An arbitrary ref (for
// example an LLM key or a different profile's key) is detached, never copied.
func (s *Service) sanitizeLoadedCredentialRefsLocked() (changed bool, deleteAfter []string) {
	var (
		healthErr     error
		checkedValues map[string]string
		checkedMany   bool
	)
	if bulk, ok := s.secrets.(checkedManySecretStore); ok {
		refs := make([]string, 0, len(s.profiles))
		seen := make(map[string]struct{}, len(s.profiles))
		for _, profile := range s.profiles {
			ref := strings.TrimSpace(profile.SecretRef)
			if ref == "" {
				continue
			}
			expected := dbCredentialRef(profile)
			legacy := legacyDBCredentialRef(profile.ID)
			if (ref == expected || ref == legacy) && expected != "" {
				candidates := []string{ref}
				if ref == legacy {
					// Include the exact derived ref in the same authenticated
					// snapshot so startup can recognize a published explicit
					// replacement whose profile metadata commit was interrupted.
					candidates = append(candidates, expected)
				}
				for _, candidate := range candidates {
					if _, duplicate := seen[candidate]; !duplicate {
						seen[candidate] = struct{}{}
						refs = append(refs, candidate)
					}
				}
			}
		}
		checkedValues, healthErr = bulk.GetManyChecked(refs)
		checkedMany = true
	} else {
		healthErr = secretStoreHealth(s.secrets)
	}
	if healthErr != nil {
		log.Printf("db: preserving owned credential references while secret storage is unavailable: %v", healthErr)
	}
	for i := range s.profiles {
		p := &s.profiles[i]
		ref := strings.TrimSpace(p.SecretRef)
		if ref == "" {
			continue
		}
		expected := dbCredentialRef(*p)
		if expected == "" || s.secrets == nil {
			p.SecretRef = ""
			changed = true
			continue
		}
		if ref == expected {
			if healthErr != nil {
				continue
			}
			if checkedMany {
				if _, found := checkedValues[ref]; !found {
					p.SecretRef = ""
					changed = true
				}
				continue
			}
			if presence, ok := s.secrets.(secretPresenceStore); ok {
				if !presence.Has(ref) {
					p.SecretRef = ""
					changed = true
				}
				continue
			}
			_, found, readErr := secretStoreGetChecked(s.secrets, ref)
			if readErr != nil {
				log.Printf("db: preserving scoped credential reference for profile %q after checked-read failure: %v", p.ID, readErr)
				continue
			}
			if !found {
				p.SecretRef = ""
				changed = true
			}
			continue
		}
		legacy := legacyDBCredentialRef(p.ID)
		if ref != legacy {
			p.SecretRef = ""
			changed = true
			continue
		}
		if healthErr != nil {
			continue
		}
		var (
			legacyValue string
			legacyFound bool
			scopedValue string
			scopedFound bool
			readErr     error
		)
		if checkedMany {
			legacyValue, legacyFound = checkedValues[legacy]
			scopedValue, scopedFound = checkedValues[expected]
		} else {
			legacyValue, legacyFound, readErr = secretStoreGetChecked(s.secrets, legacy)
			if readErr != nil {
				log.Printf("db: deferring legacy credential inspection for profile %q after checked-read failure: %v", p.ID, readErr)
				continue
			}
			if !legacyFound || legacyValue == "" {
				scopedValue, scopedFound, readErr = secretStoreGetChecked(s.secrets, expected)
				if readErr != nil {
					log.Printf("db: deferring interrupted legacy replacement recovery for profile %q after checked-read failure: %v", p.ID, readErr)
					continue
				}
			}
		}
		if legacyFound && legacyValue != "" {
			// The historical ref authenticates only the profile ID, not its host,
			// database, user, or TLS scope. Automatically copying it would trust
			// editable profile metadata and could redirect the credential. Preserve
			// it as quarantined recovery evidence; point-of-use refuses it until the
			// user explicitly re-enters the password for the current destination.
			log.Printf("db: legacy credential for profile %q is preserved but quarantined until its password is re-entered", p.ID)
			continue
		}
		if scopedFound && scopedValue != "" {
			p.SecretRef = expected
			changed = true
			log.Printf("db: recovered credential ownership for profile %q after an interrupted explicit legacy replacement", p.ID)
			continue
		}
		p.SecretRef = ""
		changed = true
	}
	return changed, deleteAfter
}

func (s *Service) load() {
	// A load failure is deliberately latched for this Service lifetime. Retrying
	// in-place would let later mutations overwrite the only copy of evidence
	// after a transient or operator-visible storage problem.
	if s.loadErr != "" {
		return
	}
	b, err := persistfile.Read(s.path, maxProfilesSize)
	if err != nil {
		if !os.IsNotExist(err) {
			s.latchLoadError(err)
		}
		return
	}
	loaded, err := decodeProfiles(b)
	if err != nil {
		s.latchLoadError(err)
		return
	}
	s.profiles = loaded
	originalProfiles := append([]Profile(nil), s.profiles...)
	changed, deleteAfter := s.sanitizeLoadedCredentialRefsLocked()
	if changed {
		if err := s.persistLocked(); err != nil {
			if !persistfile.IsPublished(err) {
				s.profiles = originalProfiles
			}
			s.latchPersistenceError(fmt.Errorf("persist sanitized credential ownership: %w", err))
			log.Printf("db: could not persist sanitized credential ownership: %v", err)
			return
		}
	}
	for _, ref := range deleteAfter {
		if err := s.secrets.Delete(ref); err != nil {
			log.Printf("db: could not delete migrated legacy credential %q: %v", ref, err)
		}
	}
}

// decodeProfiles enforces the object-count limit while decoding, rather than
// unmarshalling an attacker-sized array and checking its length afterward.
// That makes the 4 MiB acquisition limit useful against allocation
// amplification from inputs such as millions of empty objects.
func decodeProfiles(b []byte) ([]Profile, error) {
	if !utf8.Valid(b) {
		return nil, errors.New("database profiles JSON is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	opening, ok := tok.(json.Delim)
	if !ok || opening != '[' {
		return nil, errors.New("database profiles must be a JSON array")
	}
	profiles := make([]Profile, 0)
	ids := make(map[string]struct{})
	for dec.More() {
		if len(profiles) >= maxProfiles {
			return nil, fmt.Errorf("profile count exceeds the safety limit of %d", maxProfiles)
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if err := rejectDuplicateJSONKeys(raw); err != nil {
			return nil, fmt.Errorf("profile %d: %w", len(profiles), err)
		}
		var p Profile
		profileDecoder := json.NewDecoder(bytes.NewReader(raw))
		profileDecoder.DisallowUnknownFields()
		if err := profileDecoder.Decode(&p); err != nil {
			return nil, fmt.Errorf("profile %d: %w", len(profiles), err)
		}
		if err := validateProfileStrings(p); err != nil {
			return nil, fmt.Errorf("profile %d: %w", len(profiles), err)
		}
		id := strings.TrimSpace(p.ID)
		if id == "" {
			return nil, fmt.Errorf("profile %d has no id", len(profiles))
		}
		if _, exists := ids[id]; exists {
			return nil, fmt.Errorf("profile id %q is duplicated", id)
		}
		ids[id] = struct{}{}
		if strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("profile %q has no name", id)
		}
		if !validKind(strings.ToLower(strings.TrimSpace(p.Kind))) {
			return nil, fmt.Errorf("profile %q has unsupported kind %q", id, p.Kind)
		}
		if p.ID != id || p.Kind != strings.ToLower(strings.TrimSpace(p.Kind)) || p.SecretRef != strings.TrimSpace(p.SecretRef) {
			return nil, fmt.Errorf("profile %q contains non-canonical id, kind, or credential reference whitespace/casing", id)
		}
		if p.Password != "" {
			return nil, fmt.Errorf("profile %q contains a plaintext password; remove it from the profile file and re-enter it through Novera", id)
		}
		profiles = append(profiles, p)
	}
	closing, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if delim, ok := closing.(json.Delim); !ok || delim != ']' {
		return nil, errors.New("database profiles JSON array is not terminated")
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("unexpected content after database profiles array")
		}
		return nil, err
	}
	return profiles, nil
}

func validateProfileStrings(p Profile) error {
	values := []struct {
		label string
		value string
		limit int
	}{
		{"id", p.ID, maxProfileField},
		{"name", p.Name, maxProfileField},
		{"kind", p.Kind, maxProfileField},
		{"file", p.File, maxProfileField},
		{"host", p.Host, maxProfileField},
		{"database", p.Database, maxProfileField},
		{"user", p.User, maxProfileField},
		{"TLS mode", p.SSLMode, maxProfileField},
		{"credential reference", p.SecretRef, maxProfileField},
		{"password", p.Password, maxDBPassword},
	}
	for _, item := range values {
		if !utf8.ValidString(item.value) {
			return fmt.Errorf("%s is not valid UTF-8", item.label)
		}
		if len(item.value) > item.limit {
			return fmt.Errorf("%s exceeds the %d-byte safety limit", item.label, item.limit)
		}
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := validateUniqueJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateUniqueJSONValue(dec *json.Decoder) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]string)
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			canonical := strings.ToLower(key)
			if prior, exists := seen[canonical]; exists {
				return fmt.Errorf("duplicate JSON object key %q (conflicts with %q)", key, prior)
			}
			seen[canonical] = key
			if err := validateUniqueJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("malformed JSON object")
		}
	case '[':
		for dec.More() {
			if err := validateUniqueJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("malformed JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func (s *Service) latchLoadError(cause error) {
	s.profiles = []Profile{}
	s.loadErr = fmt.Sprintf("Your saved database connections could not be safely loaded (%v). The original file was left unchanged, and connection profile changes are blocked until it is repaired and Novera is restarted.", cause)
	log.Printf("db: profiles file %s could not be safely loaded; left unchanged and blocked mutations: %v", s.path, cause)
}

func (s *Service) latchPersistenceError(cause error) {
	s.loadErr = fmt.Sprintf("Your saved database connections could not be safely finalized (%v). Connection profile changes are blocked until the file is inspected and Novera is restarted.", cause)
	log.Printf("db: profiles file %s could not be safely finalized; blocked mutations: %v", s.path, cause)
}

// LoadError returns a user-facing message when saved profiles could not be
// safely loaded or finalized. Mutations stay blocked for this Service lifetime
// so the on-disk state cannot be overwritten after an uncertain load outcome.
func (s *Service) LoadError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadErr
}

func (s *Service) persistLocked() error {
	if s.loadErr != "" {
		return errors.New(s.loadErr)
	}
	if len(s.profiles) > maxProfiles {
		return fmt.Errorf("profile count %d exceeds the safety limit of %d", len(s.profiles), maxProfiles)
	}
	b, err := json.MarshalIndent(s.profiles, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > maxProfilesSize {
		return fmt.Errorf("database profiles would exceed the %d-byte safety limit", maxProfilesSize)
	}
	writeAtomic := s.writeAtomic
	if writeAtomic == nil {
		writeAtomic = persistfile.WriteAtomic
	}
	return writeAtomic(s.path, b, 0o600)
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

// CredentialStatuses returns only backend-verified availability states. A
// SecretRef is metadata, not proof that decryptable credential material is
// present. Legacy refs remain deliberately quarantined even when readable.
func (s *Service) CredentialStatuses() []ProfileCredentialStatus {
	s.mu.Lock()
	defer s.mu.Unlock()

	statuses := make([]ProfileCredentialStatus, len(s.profiles))
	refs := make([]string, 0, len(s.profiles))
	seen := make(map[string]struct{}, len(s.profiles))
	for i, profile := range s.profiles {
		ref := strings.TrimSpace(profile.SecretRef)
		statuses[i] = ProfileCredentialStatus{ProfileID: profile.ID, SecretRef: ref, Status: "none"}
		if ref == "" {
			continue
		}
		statuses[i].Status = "unavailable"
		if ownedDBCredentialRef(profile) == "" {
			continue
		}
		if _, duplicate := seen[ref]; !duplicate {
			seen[ref] = struct{}{}
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 || s.secrets == nil {
		return statuses
	}

	var (
		checkedValues map[string]string
		bulkChecked   bool
		storeErr      error
	)
	if bulk, ok := s.secrets.(checkedManySecretStore); ok {
		checkedValues, storeErr = bulk.GetManyChecked(refs)
		bulkChecked = true
	} else {
		storeErr = secretStoreHealth(s.secrets)
	}
	if storeErr != nil {
		return statuses
	}

	for i, profile := range s.profiles {
		ref := strings.TrimSpace(profile.SecretRef)
		if ref == "" || ownedDBCredentialRef(profile) == "" {
			continue
		}
		var (
			value string
			found bool
			err   error
		)
		if bulkChecked {
			value, found = checkedValues[ref]
		} else {
			value, found, err = secretStoreGetChecked(s.secrets, ref)
		}
		if err != nil || !found || value == "" {
			continue
		}
		if ref == dbCredentialRef(profile) {
			statuses[i].Status = "verified"
		} else if ref == legacyDBCredentialRef(profile.ID) {
			statuses[i].Status = "quarantined"
		}
	}
	return statuses
}

// SaveProfileReconciled converts only the post-publication finalization error
// into a structured success-with-warning. This lets the renderer retain the
// backend-issued ID and reload the visible forward-committed state without a
// duplicate create retry. Ordinary failures remain errors.
func (s *Service) SaveProfileReconciled(p Profile) (SaveProfileResult, error) {
	saved, err := s.SaveProfile(p)
	if err == nil {
		return SaveProfileResult{Profile: saved}, nil
	}
	warning := s.LoadError()
	// A secret-store WriteAtomic can also report PublishedError before the
	// profile file is touched. Only SaveProfile's forward-committed profile path
	// returns the backend ID and latches the DB finalization diagnostic.
	if !persistfile.IsPublished(err) || saved.ID == "" || warning == "" {
		return SaveProfileResult{}, err
	}
	return SaveProfileResult{Profile: saved, FinalizationWarning: warning}, nil
}

// SaveProfile creates or updates a profile. Profile IDs and credential refs are
// backend owned: a caller may update an existing ID or create with an empty ID,
// but can never attach a supplied SecretRef. A non-empty Password is stored
// under the ref derived from this exact profile connection scope.
func (s *Service) SaveProfile(p Profile) (Profile, error) {
	if err := validateProfileStrings(p); err != nil {
		return Profile{}, err
	}
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
	if s.loadErr != "" {
		return Profile{}, errors.New(s.loadErr)
	}
	existingIndex := -1
	if p.ID == "" {
		if len(s.profiles) >= maxProfiles {
			return Profile{}, fmt.Errorf("cannot save more than %d database connection profiles", maxProfiles)
		}
		id, err := newID()
		if err != nil {
			return Profile{}, err
		}
		p.ID = id
	} else {
		for i := range s.profiles {
			if s.profiles[i].ID == p.ID {
				existingIndex = i
				break
			}
		}
		if existingIndex < 0 {
			return Profile{}, errors.New("connection not found; create a profile with an empty id")
		}
	}

	var old Profile
	if existingIndex >= 0 {
		old = s.profiles[existingIndex]
	}
	oldOwnedRef := ownedDBCredentialRef(old)
	oldRef := old.SecretRef
	if oldRef != dbCredentialRef(old) {
		oldRef = ""
	}
	newRef := dbCredentialRef(p)
	password := p.Password
	p.SecretRef = "" // input is never authoritative
	p.Password = ""
	legacyOwned := oldOwnedRef != "" && oldOwnedRef == legacyDBCredentialRef(old.ID) && oldRef == ""
	if existingIndex >= 0 && oldRef != "" && oldRef != newRef && newRef != "" && password == "" {
		return Profile{}, errors.New("connection security details changed; re-enter the password to bind it to the new destination")
	}
	if legacyOwned && password != "" && newRef != dbCredentialRef(old) {
		// If the secret replacement published but profile persistence failed,
		// startup can derive a recovery ref only from the still-persisted legacy
		// profile. Keep that recovery deterministic by requiring legacy re-entry
		// to retain the credential scope. Once rebound, a second ordinary save can
		// safely change scope with the existing compensation path.
		return Profile{}, errors.New("rebind the legacy credential without changing host, port, database, user, kind, or TLS mode; then save destination changes separately")
	}
	if oldOwnedRef != "" && s.secrets == nil {
		return Profile{}, errors.New("credential storage is unavailable; the connection profile and owned credential were left unchanged")
	}
	// Checked read/Set/Replace validate the whole store on credential writes.
	// Retain a standalone health gate only for metadata-only edits that preserve
	// an existing owned ref, avoiding duplicate full-store decryption per save.
	if s.secrets != nil && password == "" && oldOwnedRef != "" {
		if err := secretStoreHealth(s.secrets); err != nil {
			return Profile{}, fmt.Errorf("credential storage is unavailable; the connection profile and owned credential were left unchanged: %w", err)
		}
	}
	if oldOwnedRef != "" && oldRef == "" && password == "" {
		// This can occur when startup preserved an exact legacy ref while the
		// secret backend was unhealthy and that backend later recovers. Silently
		// treating the legacy handle as absent would persist a detached profile and
		// then delete its only credential. Require an explicit re-entry (which binds
		// a new scoped ref). Startup never copies a legacy credential into an
		// editable destination scope automatically.
		return Profile{}, errors.New("the connection credential still uses a legacy reference; re-enter the password before saving")
	}

	secretChanged := false
	replacedLegacy := false
	previousValue, hadPrevious := "", false
	if password != "" {
		if newRef == "" {
			return Profile{}, errors.New("database passwords are supported only for network profiles")
		}
		if s.secrets == nil {
			return Profile{}, errors.New("credential storage is unavailable")
		}
		replacedLegacy = legacyOwned
		if replacedLegacy {
			replacer, ok := s.secrets.(replacingSecretStore)
			if !ok {
				return Profile{}, errors.New("credential storage cannot atomically replace the quarantined legacy database credential")
			}
			if err := replacer.Replace(oldOwnedRef, newRef, password); err != nil {
				return Profile{}, fmt.Errorf("replace quarantined legacy database credential: %w", err)
			}
		} else {
			var readErr error
			previousValue, hadPrevious, readErr = secretStoreGetChecked(s.secrets, newRef)
			if readErr != nil {
				return Profile{}, fmt.Errorf("credential storage is unavailable; the connection profile was left unchanged: %w", readErr)
			}
			if err := s.secrets.Set(newRef, password); err != nil {
				return Profile{}, err
			}
		}
		secretChanged = true
		p.SecretRef = newRef
	} else if existingIndex >= 0 && oldRef != "" && oldRef == newRef {
		// Preserve an existing password only while every security-relevant profile
		// field still resolves to its exact current scoped handle.
		p.SecretRef = oldRef
	}

	originalProfiles := append([]Profile(nil), s.profiles...)
	if existingIndex >= 0 {
		s.profiles[existingIndex] = p
	} else {
		s.profiles = append(s.profiles, p)
	}
	if err := s.persistLocked(); err != nil {
		if persistfile.IsPublished(err) {
			// The new profile bytes are already visible. Keep memory and the newly
			// written credential aligned with them; rolling either back could leave
			// the published profile pointing at a deleted secret.
			s.latchPersistenceError(fmt.Errorf("save connection profile %q: %w", p.ID, err))
			return p, err
		}
		s.profiles = originalProfiles
		// A published legacy replacement remains forward-committed. Startup can
		// idempotently attach newRef when it sees the old metadata with oldRef
		// absent and the exact derived ref present. Reversing here could destroy a
		// pre-existing destination credential that explicit re-entry replaced.
		if secretChanged && !replacedLegacy {
			if hadPrevious {
				if restoreErr := s.secrets.Set(newRef, previousValue); restoreErr != nil {
					log.Printf("db: failed to restore credential after profile persistence failure: %v", restoreErr)
				}
			} else if restoreErr := s.secrets.Delete(newRef); restoreErr != nil {
				log.Printf("db: failed to remove orphaned credential after profile persistence failure: %v", restoreErr)
			}
		}
		return Profile{}, err
	}
	if oldOwnedRef != "" && oldOwnedRef != p.SecretRef && s.secrets != nil && !replacedLegacy {
		if err := s.secrets.Delete(oldOwnedRef); err != nil {
			log.Printf("db: failed to delete detached credential %q: %v", oldOwnedRef, err)
		}
	}
	return p, nil
}

// DeleteProfileReconciled reports a published deletion as a structured
// forward commit with a durable warning. Pre-publication failures remain
// ordinary errors and do not remove renderer state.
func (s *Service) DeleteProfileReconciled(id string) (DeleteProfileResult, error) {
	err := s.DeleteProfile(id)
	if err == nil {
		return DeleteProfileResult{Deleted: true}, nil
	}
	s.mu.Lock()
	warning := s.loadErr
	deleted := true
	for _, profile := range s.profiles {
		if profile.ID == id {
			deleted = false
			break
		}
	}
	s.mu.Unlock()
	// Health may relay a prior secret-store PublishedError while the profile is
	// still present. The DB latch is set only after its own replacement commits.
	if !persistfile.IsPublished(err) || warning == "" || !deleted {
		return DeleteProfileResult{}, err
	}
	return DeleteProfileResult{Deleted: true, FinalizationWarning: warning}, nil
}

// DeleteProfile removes a profile and its stored credential.
func (s *Service) DeleteProfile(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != "" {
		return errors.New(s.loadErr)
	}
	var ref string
	found := false
	originalProfiles := append([]Profile(nil), s.profiles...)
	// Build a separate slice. Reusing s.profiles[:0] would overwrite the live
	// backing array before the secret-health gate below and could therefore
	// mutate in-memory evidence even when deletion is rejected.
	kept := make([]Profile, 0, len(s.profiles))
	for _, p := range s.profiles {
		if p.ID == id {
			found = true
			ref = ownedDBCredentialRef(p)
			continue
		}
		kept = append(kept, p)
	}
	if !found {
		return errors.New("connection not found")
	}
	if ref != "" && s.secrets == nil {
		return errors.New("credential storage is unavailable; the connection profile and owned credential were left unchanged")
	}
	if ref != "" {
		if err := secretStoreHealth(s.secrets); err != nil {
			return fmt.Errorf("credential storage is unavailable; the connection profile and owned credential were left unchanged: %w", err)
		}
	}
	s.profiles = kept
	// Persist the removal FIRST; only drop the credential once the profile is
	// durably gone, so a persist failure can't leave a profile pointing at a
	// secret we already deleted.
	if err := s.persistLocked(); err != nil {
		if persistfile.IsPublished(err) {
			// The deletion is visible but not confirmed durable. Keep the in-memory
			// deletion and retain the credential as recoverable orphan evidence in
			// case a crash exposes the previous directory entry.
			s.latchPersistenceError(fmt.Errorf("delete connection profile %q: %w", id, err))
			return err
		}
		s.profiles = originalProfiles
		return err
	}
	if ref != "" && s.secrets != nil {
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
	query := tableListSQL(p.Kind)
	if query == "" {
		return nil, fmt.Errorf("unsupported kind %q", p.Kind)
	}
	rows, err := conn.QueryContext(ctx, query)
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer rows.Close()
	out := []Table{}
	var metadataBytes int64
	for rows.Next() {
		if len(out) >= maxMetadataObjects {
			return nil, fmt.Errorf("database metadata exceeds the %d-object limit", maxMetadataObjects)
		}
		schema := boundedTextScanner{limit: maxMetadataFieldBytes, label: "schema name"}
		name := boundedTextScanner{limit: maxMetadataFieldBytes, label: "table name"}
		typ := boundedTextScanner{limit: maxMetadataFieldBytes, label: "table type"}
		if err := rows.Scan(&schema, &name, &typ); err != nil {
			return nil, fmt.Errorf("scan database metadata: %w", err)
		}
		if err := validateTableIdentity(p.Kind, schema.value, name.value); err != nil {
			return nil, err
		}
		if err := consumeMetadataBudget(&metadataBytes, schema.value, name.value, typ.value); err != nil {
			return nil, err
		}
		out = append(out, Table{Schema: schema.value, Name: name.value, Type: normalizeTableType(typ.value)})
	}
	if err := rows.Err(); err != nil {
		return nil, cleanDBErr(err)
	}
	return out, nil
}

// ListColumns returns the columns of a table for the schema browser. The query
// is service-issued (not user SQL), and schema/table identity is always passed
// as bound data rather than interpolated into SQL.
func (s *Service) ListColumns(id, schema, table string) ([]Column, error) {
	p, ok := s.profile(id)
	if !ok {
		return nil, errors.New("connection not found")
	}
	if err := validateTableIdentity(p.Kind, schema, table); err != nil {
		return nil, err
	}
	conn, err := s.open(p)
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	out := []Column{}
	var metadataBytes int64

	if p.Kind == "sqlite" {
		// SQLite's table-valued PRAGMA accepts the table and schema as bound
		// arguments. This preserves valid names containing whitespace, quotes,
		// punctuation, Unicode, or a leading digit without SQL construction.
		rows, err := conn.QueryContext(ctx,
			`SELECT name, type, "notnull" FROM pragma_table_info(?, ?) ORDER BY cid`,
			table, schema,
		)
		if err != nil {
			return nil, cleanDBErr(err)
		}
		defer rows.Close()
		for rows.Next() {
			if len(out) >= maxMetadataColumns {
				return nil, fmt.Errorf("table metadata exceeds the %d-column limit", maxMetadataColumns)
			}
			name := boundedTextScanner{limit: maxMetadataFieldBytes, label: "column name"}
			ctype := boundedTextScanner{limit: maxMetadataFieldBytes, label: "column type"}
			var notnull int
			if err := rows.Scan(&name, &ctype, &notnull); err != nil {
				return nil, fmt.Errorf("scan column metadata: %w", err)
			}
			if name.null || name.value == "" {
				return nil, errors.New("database returned an empty column name")
			}
			if err := consumeMetadataBudget(&metadataBytes, name.value, ctype.value); err != nil {
				return nil, err
			}
			out = append(out, Column{Name: name.value, Type: ctype.value, Nullable: notnull == 0})
		}
		if err := rows.Err(); err != nil {
			return nil, cleanDBErr(err)
		}
		return out, nil
	}

	var query string
	var args []any
	switch p.Kind {
	case "postgres":
		query = `SELECT column_name, data_type, is_nullable FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2
			ORDER BY ordinal_position`
		args = []any{schema, table}
	case "mysql":
		query = `SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = ? AND table_name = ? ORDER BY ordinal_position`
		args = []any{schema, table}
	default:
		return nil, fmt.Errorf("unsupported kind %q", p.Kind)
	}
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, cleanDBErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		if len(out) >= maxMetadataColumns {
			return nil, fmt.Errorf("table metadata exceeds the %d-column limit", maxMetadataColumns)
		}
		name := boundedTextScanner{limit: maxMetadataFieldBytes, label: "column name"}
		dtype := boundedTextScanner{limit: maxMetadataFieldBytes, label: "column type"}
		nullable := boundedTextScanner{limit: maxMetadataFieldBytes, label: "column nullability"}
		if err := rows.Scan(&name, &dtype, &nullable); err != nil {
			return nil, fmt.Errorf("scan column metadata: %w", err)
		}
		if name.null || name.value == "" {
			return nil, errors.New("database returned an empty column name")
		}
		if err := consumeMetadataBudget(&metadataBytes, name.value, dtype.value, nullable.value); err != nil {
			return nil, err
		}
		out = append(out, Column{Name: name.value, Type: dtype.value, Nullable: strings.EqualFold(nullable.value, "YES")})
	}
	if err := rows.Err(); err != nil {
		return nil, cleanDBErr(err)
	}
	return out, nil
}

// BuildTableQuery constructs the schema-browser preview query inside the
// backend, where the connection dialect is known. The renderer supplies
// identity as structured data and never interpolates identifiers into SQL.
func (s *Service) BuildTableQuery(id, schema, table string, limit int) (string, error) {
	p, ok := s.profile(id)
	if !ok {
		return "", errors.New("connection not found")
	}
	if err := validateTableIdentity(p.Kind, schema, table); err != nil {
		return "", err
	}
	if limit <= 0 {
		limit = defaultTablePreviewLimit
	}
	if limit > maxRowLimit {
		limit = maxRowLimit
	}
	quotedSchema, err := quoteDBIdentifier(p.Kind, schema)
	if err != nil {
		return "", err
	}
	quotedTable, err := quoteDBIdentifier(p.Kind, table)
	if err != nil {
		return "", err
	}
	return "SELECT * FROM " + quotedSchema + "." + quotedTable + " LIMIT " + strconv.Itoa(limit), nil
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
	resultBytes, err := queryHeaderBudget(cols)
	if err != nil {
		return QueryResult{}, err
	}
	result := QueryResult{Columns: cols, Rows: [][]string{}, Nulls: [][]bool{}}
	for rows.Next() {
		if len(result.Rows) >= limit {
			result.Truncated = true
			result.TruncationReason = queryTruncatedRows
			break
		}

		// Reserve structural JSON overhead before scanning values. Every scanner
		// shares the remaining row budget, so an oversized row stops during Scan
		// instead of materialising all of its cells and checking afterward.
		fixedRowBytes := int64(32 + len(cols)*8)
		if fixedRowBytes > maxQueryResultBytes-resultBytes {
			result.Truncated = true
			result.TruncationReason = queryTruncatedBytes
			break
		}
		var rowStringBytes int64
		rowStringLimit := maxQueryResultBytes - resultBytes - fixedRowBytes
		cells := make([]boundedTextScanner, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			cells[i] = boundedTextScanner{
				limit:       maxQueryCellBytes,
				label:       fmt.Sprintf("query cell in column %q", cols[i]),
				nullText:    "NULL",
				budgetUsed:  &rowStringBytes,
				budgetLimit: rowStringLimit,
			}
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			if errors.Is(err, errQueryResultByteLimit) {
				result.Truncated = true
				result.TruncationReason = queryTruncatedBytes
				break
			}
			return QueryResult{}, fmt.Errorf("scan query row %d: %w", len(result.Rows)+1, err)
		}
		row := make([]string, len(cols))
		nullRow := make([]bool, len(cols))
		for i, c := range cells {
			nullRow[i] = c.null
			row[i] = c.value
		}
		result.Rows = append(result.Rows, row)
		result.Nulls = append(result.Nulls, nullRow)
		resultBytes += fixedRowBytes + rowStringBytes
	}
	if err := rows.Err(); err != nil {
		return QueryResult{}, err
	}
	result.RowCount = len(result.Rows)
	result.ElapsedMs = time.Since(start).Milliseconds()
	return result, nil
}

// --- internals ---

var errQueryResultByteLimit = errors.New("query result byte limit reached")

// boundedTextScanner rejects a single oversized value before copying driver
// byte slices into result-owned strings. For query rows, all scanners share a
// conservative serialized-output budget so Scan stops at the first cell that
// would exceed the result envelope.
type boundedTextScanner struct {
	value       string
	null        bool
	limit       int
	label       string
	nullText    string
	budgetUsed  *int64
	budgetLimit int64
}

func (s *boundedTextScanner) Scan(src any) error {
	s.value = ""
	s.null = src == nil

	var text string
	switch value := src.(type) {
	case nil:
		text = s.nullText
	case string:
		if len(value) > s.limit {
			return fmt.Errorf("%s exceeds the %d-byte limit", s.label, s.limit)
		}
		text = value
	case []byte:
		if len(value) > s.limit {
			return fmt.Errorf("%s exceeds the %d-byte limit", s.label, s.limit)
		}
		// Check the shared budget before allocating the string copy.
		if err := s.consumeBudgetForLength(len(value)); err != nil {
			return err
		}
		text = string(value)
		s.value = text
		return nil
	case time.Time:
		text = value.Format(time.RFC3339)
	case int64:
		text = strconv.FormatInt(value, 10)
	case float64:
		text = strconv.FormatFloat(value, 'g', -1, 64)
	case bool:
		text = strconv.FormatBool(value)
	default:
		return fmt.Errorf("%s has unsupported database type %T", s.label, src)
	}
	if len(text) > s.limit {
		return fmt.Errorf("%s exceeds the %d-byte limit", s.label, s.limit)
	}
	if err := s.consumeBudgetForLength(len(text)); err != nil {
		return err
	}
	s.value = text
	return nil
}

func (s *boundedTextScanner) consumeBudgetForLength(length int) error {
	if s.budgetUsed == nil {
		return nil
	}
	cost := conservativeJSONStringBytesForLength(length) + 1
	if *s.budgetUsed > s.budgetLimit || cost > s.budgetLimit-*s.budgetUsed {
		return errQueryResultByteLimit
	}
	*s.budgetUsed += cost
	return nil
}

// JSON may escape every input byte as a six-byte \uXXXX sequence. This upper
// bound also covers quotes around the string and invalid UTF-8 replacement.
func conservativeJSONStringBytesForLength(length int) int64 {
	return int64(length)*6 + 2
}

func queryHeaderBudget(columns []string) (int64, error) {
	if len(columns) > maxQueryColumns {
		return 0, fmt.Errorf("query returned %d columns; limit is %d", len(columns), maxQueryColumns)
	}
	used := int64(512) // fixed QueryResult object fields and array punctuation
	for index, name := range columns {
		if len(name) > maxQueryColumnNameBytes {
			return 0, fmt.Errorf("query column %d name exceeds the %d-byte limit", index+1, maxQueryColumnNameBytes)
		}
		if !utf8.ValidString(name) {
			return 0, fmt.Errorf("query column %d name is not valid UTF-8", index+1)
		}
		cost := conservativeJSONStringBytesForLength(len(name)) + 8
		if cost > maxQueryResultBytes-used {
			return 0, fmt.Errorf("query column metadata exceeds the %d-byte result limit", maxQueryResultBytes)
		}
		used += cost
	}
	return used, nil
}

func consumeMetadataBudget(used *int64, values ...string) error {
	for _, value := range values {
		cost := conservativeJSONStringBytesForLength(len(value)) + 32
		if *used > maxMetadataBytes || cost > maxMetadataBytes-*used {
			return fmt.Errorf("database metadata exceeds the %d-byte limit", maxMetadataBytes)
		}
		*used += cost
	}
	return nil
}

func validateMetadataIdentifier(label, value string) error {
	if value == "" {
		return fmt.Errorf("%s is empty", label)
	}
	if len(value) > maxMetadataFieldBytes {
		return fmt.Errorf("%s exceeds the %d-byte limit", label, maxMetadataFieldBytes)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", label)
	}
	if strings.IndexByte(value, 0) >= 0 {
		return fmt.Errorf("%s contains a NUL byte", label)
	}
	return nil
}

func validateTableIdentity(kind, schema, table string) error {
	if !validKind(kind) {
		return fmt.Errorf("unsupported kind %q", kind)
	}
	if err := validateMetadataIdentifier("schema name", schema); err != nil {
		return err
	}
	if err := validateMetadataIdentifier("table name", table); err != nil {
		return err
	}
	if kind == "sqlite" && schema != "main" {
		return errors.New("SQLite schema must be main")
	}
	return nil
}

func quoteDBIdentifier(kind, identifier string) (string, error) {
	switch kind {
	case "sqlite", "postgres":
		return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`, nil
	case "mysql":
		return "`" + strings.ReplaceAll(identifier, "`", "``") + "`", nil
	default:
		return "", fmt.Errorf("unsupported kind %q", kind)
	}
}

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
	// Re-derive ownership at the point of use. Even a malformed in-memory or
	// hand-edited profile cannot make this service resolve another subsystem's
	// secret or carry a password to a changed connection scope.
	expected := dbCredentialRef(p)
	ref := strings.TrimSpace(p.SecretRef)
	if expected != "" && ref == expected {
		if s.secrets == nil {
			return nil, errors.New("database credential storage is unavailable")
		}
		var found bool
		var err error
		if checked, ok := s.secrets.(checkedSecretStore); ok {
			pass, found, err = checked.GetChecked(ref)
		} else {
			if err := secretStoreHealth(s.secrets); err != nil {
				return nil, fmt.Errorf("database credential storage is unavailable: %w", err)
			}
			pass, found = s.secrets.Get(ref)
		}
		if err != nil {
			return nil, fmt.Errorf("database credential could not be read: %w", err)
		}
		if !found || pass == "" {
			return nil, errors.New("database credential is missing or empty; connection was not attempted")
		}
	} else if expected != "" && strings.TrimSpace(p.ID) != "" && ref == legacyDBCredentialRef(p.ID) {
		// Legacy references are recognized as profile-owned for preservation and
		// deletion, but explicit password re-entry must bind a scoped ref before use.
		return nil, errors.New("database credential uses a quarantined legacy reference; re-enter the password before connecting")
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
		// Secure-by-default: a network host requires hostname-verified TLS, while
		// loopback keeps the lenient `prefer` (most local servers have no TLS
		// configured). An explicit sslmode always wins, but insecure network modes
		// are logged so an opt-out is never silent.
		sslmode := strings.ToLower(strings.TrimSpace(p.SSLMode))
		if sslmode == "" {
			if isLoopbackHost(host) {
				sslmode = "prefer"
			} else {
				sslmode = "verify-full"
			}
		}
		if !isLoopbackHost(host) {
			switch sslmode {
			case "disable", "allow":
				log.Printf("db: profile %q connects to network host %s with sslmode=%s — credentials may be sent UNENCRYPTED (explicit opt-in)", p.Name, host, sslmode)
			case "prefer":
				log.Printf("db: profile %q connects to network host %s with sslmode=prefer — TLS is unverified and may fall back to UNENCRYPTED transport (explicit opt-in)", p.Name, host)
			case "require":
				log.Printf("db: profile %q connects to network host %s with sslmode=require — transport is encrypted but the server identity is NOT VERIFIED (explicit opt-in)", p.Name, host)
			case "verify-ca":
				log.Printf("db: profile %q connects to network host %s with sslmode=verify-ca — the certificate chain is checked but the hostname is NOT VERIFIED (explicit opt-in)", p.Name, host)
			}
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
		// network host verifies TLS by default; loopback stays plaintext-friendly;
		// an explicit choice always wins. Insecure network opt-outs are logged.
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
			if !isLoopbackHost(host) {
				log.Printf("db: profile %q connects to network host %s with unverified TLS (explicit opt-in)", p.Name, host)
			}
		default: // empty / unknown -> secure-by-default
			if !isLoopbackHost(host) {
				cfg.TLSConfig = "true" // encrypt and verify the server certificate
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
		return "SELECT 'main' AS table_schema, name, type FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' ORDER BY name"
	case "postgres":
		return "SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE table_schema NOT IN ('pg_catalog','information_schema') ORDER BY table_schema, table_name"
	case "mysql":
		return "SELECT table_schema, table_name, table_type FROM information_schema.tables WHERE table_schema = DATABASE() ORDER BY table_schema, table_name"
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
