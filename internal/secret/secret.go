// Package secret is a small at-rest credential store kept SEPARATE from
// settings and chat history. Values are encrypted on disk on every platform:
// Windows uses DPAPI; other platforms use AES-256-GCM under a per-user master
// key held in the OS keyring. The 0600 file holds only ciphertext. Generic ref
// operations are Go-internal; the renderer receives purpose-bound facades.
package secret

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	maxSecretStoreBytes      = 8 << 20
	maxSecretEntries         = 4096
	maxSecretLookupRefs      = 32_768
	maxSecretLookupRefBytes  = 16 << 20
	maxSecretRefBytes        = 1024
	maxSecretCiphertextBytes = 1 << 20
	// Both supported backends emit standard-base64 ciphertext. AES-GCM needs a
	// 12-byte nonce and 16-byte authentication tag even for an empty plaintext;
	// DPAPI blobs are larger. Anything shorter cannot be a Novera ciphertext.
	minSecretCiphertextRawBytes = 28
	maxSecretPlaintextBytes     = (maxSecretCiphertextBytes / 4 * 3) - 128

	ciphertextRecordPrefix = "novera-secret:"
	ciphertextV2Prefix     = ciphertextRecordPrefix + "v2:"
	ciphertextV3Prefix     = ciphertextRecordPrefix + "v3:"
	ciphertextV2AADDomain  = "Novera secret ciphertext v2\x00"
	ciphertextV3AADDomain  = "Novera secret ciphertext v3\x00"
)

var (
	errSecretStoreTooLarge      = errors.New("encrypted secret store exceeds its size limit")
	ErrStoreUnsafePath          = errors.New("encrypted secret store path is unsafe")
	ErrCiphertextAuthentication = errors.New("encrypted secret ciphertext authentication failed")
)

type ciphertextFormat uint8

const (
	ciphertextLegacy ciphertextFormat = iota + 1
	ciphertextV2
	ciphertextV3
)

type ciphertextRecord struct {
	format ciphertextFormat
	keyID  string
	raw    []byte
}

type ciphertextDecryptor func(ciphertextRecord, string) (string, error)

// storeHooks is a deliberately narrow, package-private fault/crypto seam. The
// zero value selects the real platform implementation. Tests use it to verify
// corruption and commit behavior without depending on DPAPI or a live keyring.
type storeHooks struct {
	decryptSession func(bool) (ciphertextDecryptor, error)
	encrypt        func(string, string, bool) (string, error)
	replace        func(string, string) error
	syncDir        func(string) error
	staged         func(string) error
}

// Store is the on-disk encrypted secret map (ref -> ciphertext).
type Store struct {
	mu        sync.Mutex
	path      string
	data      map[string]string
	healthErr error
	hooks     storeHooks
}

var ErrStoreCorrupt = errors.New("encrypted secret store is corrupt")

// StoreCorruptionError is retained for the lifetime of a Store. The source
// file stays at Path byte-for-byte and every mutation fails until an operator
// deliberately repairs/restores it and restarts the service.
type StoreCorruptionError struct {
	Path  string
	Cause error
}

func (e *StoreCorruptionError) Error() string {
	return fmt.Sprintf("encrypted secret store %q is corrupt (%v); it was left unchanged and all secret mutations are blocked; restore or inspect this file deliberately before retrying", e.Path, e.Cause)
}

func (e *StoreCorruptionError) Unwrap() error { return e.Cause }
func (e *StoreCorruptionError) Is(target error) bool {
	return target == ErrStoreCorrupt
}

// New opens (or initialises) the secret store under the OS config dir.
func New() *Store {
	s := &Store{path: filepath.Join(configDir(), "Novera", "secrets.json"), data: map[string]string{}}
	s.load()
	return s
}

// configDir resolves a stable, absolute per-user config directory. If neither
// the OS config dir nor home is available it uses a logged temp fallback rather
// than a CWD-relative path that silently moves with the process.
func configDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return dir
	}
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		return dir
	}
	dir := os.TempDir()
	log.Printf("secret: no config/home dir available, falling back to %s", dir)
	return dir
}

func (s *Store) load() {
	s.data = map[string]string{}
	s.healthErr = nil
	b, err := readSecretStoreFile(s.path)
	if err != nil {
		if errors.Is(err, errSecretStoreTooLarge) {
			s.healthErr = &StoreCorruptionError{Path: s.path, Cause: err}
		} else if !os.IsNotExist(err) {
			s.healthErr = fmt.Errorf("read encrypted secret store %q: %w", s.path, err)
		}
		return
	}
	loaded, err := parseSecretStore(b)
	if err != nil {
		s.healthErr = &StoreCorruptionError{Path: s.path, Cause: err}
		return
	}
	s.data = loaded
}

func readSecretStoreFile(path string) ([]byte, error) {
	if err := inspectSecretStorePath(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxSecretStoreBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSecretStoreBytes {
		return nil, fmt.Errorf("%w (%d-byte limit)", errSecretStoreTooLarge, maxSecretStoreBytes)
	}
	return b, nil
}

// parseSecretStore retains compatibility with the unversioned ref->ciphertext
// object written by every released Novera version. It tokenizes the object
// before constructing the map so duplicate keys cannot be silently resolved by
// encoding/json's last-value-wins behavior.
func parseSecretStore(data []byte) (map[string]string, error) {
	if len(data) > maxSecretStoreBytes {
		return nil, fmt.Errorf("%w (%d-byte limit)", errSecretStoreTooLarge, maxSecretStoreBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("secret store is not valid UTF-8 JSON")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	first, err := dec.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("secret store is empty")
		}
		return nil, fmt.Errorf("decode top-level secret store: %w", err)
	}
	opening, ok := first.(json.Delim)
	if !ok || opening != '{' {
		return nil, errors.New("top-level JSON value must be an object")
	}

	loaded := make(map[string]string)
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("decode secret ref: %w", err)
		}
		ref, ok := keyToken.(string)
		if !ok {
			return nil, errors.New("secret store object contains a non-string key")
		}
		if len(ref) == 0 || strings.TrimSpace(ref) == "" {
			return nil, errors.New("secret store contains an empty ref")
		}
		if len(ref) > maxSecretRefBytes {
			return nil, fmt.Errorf("secret ref exceeds the %d-byte limit", maxSecretRefBytes)
		}
		if _, duplicate := loaded[ref]; duplicate {
			return nil, fmt.Errorf("secret store contains duplicate ref %q", ref)
		}
		if len(loaded) >= maxSecretEntries {
			return nil, fmt.Errorf("secret store exceeds the %d-entry limit", maxSecretEntries)
		}

		var ciphertext string
		if err := dec.Decode(&ciphertext); err != nil {
			return nil, fmt.Errorf("decode ciphertext for ref %q: %w", ref, err)
		}
		if err := validateCiphertextShape(ciphertext); err != nil {
			return nil, fmt.Errorf("ciphertext for ref %q is invalid: %w", ref, err)
		}
		loaded[ref] = ciphertext
	}
	closing, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("decode closing secret store object: %w", err)
	}
	if delim, ok := closing.(json.Delim); !ok || delim != '}' {
		return nil, errors.New("secret store object is not properly terminated")
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple top-level JSON values")
		}
		return nil, fmt.Errorf("decode trailing secret store data: %w", err)
	}
	return loaded, nil
}

func validateCiphertextShape(ciphertext string) error {
	_, err := decodeCiphertextRecord(ciphertext)
	return err
}

func decodeCiphertextRecord(ciphertext string) (ciphertextRecord, error) {
	if ciphertext == "" {
		return ciphertextRecord{}, errors.New("ciphertext is empty")
	}
	if len(ciphertext) > maxSecretCiphertextBytes {
		return ciphertextRecord{}, fmt.Errorf("ciphertext exceeds the %d-byte limit", maxSecretCiphertextBytes)
	}
	format := ciphertextLegacy
	keyID := ""
	payload := ciphertext
	if strings.HasPrefix(ciphertext, ciphertextRecordPrefix) {
		switch {
		case strings.HasPrefix(ciphertext, ciphertextV2Prefix):
			format = ciphertextV2
			payload = strings.TrimPrefix(ciphertext, ciphertextV2Prefix)
		case strings.HasPrefix(ciphertext, ciphertextV3Prefix):
			format = ciphertextV3
			rest := strings.TrimPrefix(ciphertext, ciphertextV3Prefix)
			separator := strings.IndexByte(rest, ':')
			if separator < 0 {
				return ciphertextRecord{}, errors.New("v3 ciphertext has no key identifier separator")
			}
			keyID = rest[:separator]
			payload = rest[separator+1:]
			if len(keyID) != masterKeyIDHexBytes {
				return ciphertextRecord{}, fmt.Errorf("v3 ciphertext key identifier must be %d hexadecimal bytes", masterKeyIDHexBytes)
			}
			decodedID, err := hex.DecodeString(keyID)
			if err != nil || hex.EncodeToString(decodedID) != keyID {
				return ciphertextRecord{}, errors.New("v3 ciphertext key identifier is not canonical lowercase hexadecimal")
			}
		default:
			return ciphertextRecord{}, errors.New("ciphertext uses an unsupported record version")
		}
	}
	if payload == "" {
		return ciphertextRecord{}, errors.New("ciphertext payload is empty")
	}
	if strings.ContainsAny(payload, " \t\r\n") {
		return ciphertextRecord{}, errors.New("ciphertext base64 contains whitespace")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(payload)
	if err != nil {
		return ciphertextRecord{}, fmt.Errorf("ciphertext is not canonical standard base64: %w", err)
	}
	if base64.StdEncoding.EncodeToString(raw) != payload {
		return ciphertextRecord{}, errors.New("ciphertext base64 is not canonical")
	}
	if len(raw) < minSecretCiphertextRawBytes {
		return ciphertextRecord{}, fmt.Errorf("ciphertext is truncated (%d decoded bytes; minimum is %d)", len(raw), minSecretCiphertextRawBytes)
	}
	return ciphertextRecord{format: format, keyID: keyID, raw: raw}, nil
}

func encodeCiphertextRecord(raw []byte, format ciphertextFormat, keyIDs ...string) string {
	payload := base64.StdEncoding.EncodeToString(raw)
	switch format {
	case ciphertextV2:
		return ciphertextV2Prefix + payload
	case ciphertextV3:
		keyID := ""
		if len(keyIDs) != 0 {
			keyID = keyIDs[0]
		}
		return ciphertextV3Prefix + keyID + ":" + payload
	default:
		return payload
	}
}

func secretCiphertextAAD(format ciphertextFormat, keyID, ref string) []byte {
	domain := ciphertextV2AADDomain
	capacity := len(domain) + len(ref)
	if format == ciphertextV3 {
		domain = ciphertextV3AADDomain
		capacity += len(keyID) + 1
	}
	aad := make([]byte, 0, capacity)
	aad = append(aad, domain...)
	if format == ciphertextV3 {
		aad = append(aad, keyID...)
		aad = append(aad, 0)
	}
	aad = append(aad, ref...)
	return aad
}

func validateSecretData(data map[string]string) error {
	if data == nil {
		return errors.New("secret store map is nil")
	}
	if len(data) > maxSecretEntries {
		return fmt.Errorf("secret store exceeds the %d-entry limit", maxSecretEntries)
	}
	for ref, ciphertext := range data {
		if ref == "" || strings.TrimSpace(ref) == "" {
			return errors.New("secret store contains an empty ref")
		}
		if !utf8.ValidString(ref) {
			return errors.New("secret store contains a ref that is not valid UTF-8")
		}
		if len(ref) > maxSecretRefBytes {
			return fmt.Errorf("secret ref exceeds the %d-byte limit", maxSecretRefBytes)
		}
		if !utf8.ValidString(ciphertext) {
			return fmt.Errorf("ciphertext for ref %q is not valid UTF-8", ref)
		}
		if err := validateCiphertextShape(ciphertext); err != nil {
			return fmt.Errorf("ciphertext for ref %q is invalid: %w", ref, err)
		}
	}
	return nil
}

// persistLocked stages and verifies a complete candidate before publishing it.
// published is true once the atomic replacement occurred, including the rare
// case where the subsequent directory sync reports an error. Callers use that
// bit to keep memory consistent with the file visible to this process.
func (s *Store) persistLocked(data map[string]string) (published bool, err error) {
	if s.healthErr != nil {
		return false, s.healthErr
	}
	if err := validateSecretData(data); err != nil {
		return false, fmt.Errorf("refuse to persist invalid secret store: %w", err)
	}
	if err := inspectSecretStorePath(s.path); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return false, err
	}
	if err := inspectSecretStorePath(s.path); err != nil {
		return false, err
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return false, err
	}
	encoded, err := parseSecretStore(b)
	if err != nil {
		return false, fmt.Errorf("refuse to persist invalid secret store: %w", err)
	}
	if !maps.Equal(encoded, data) {
		return false, errors.New("refuse to persist secret store: JSON round-trip changed candidate semantics")
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".secrets-*.tmp")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	closed := false
	defer func() {
		if !closed {
			_ = f.Close()
		}
		if !published {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return false, fmt.Errorf("set staged secret-store permissions: %w", err)
	}
	n, err := f.Write(b)
	if err != nil {
		return false, fmt.Errorf("write staged secret store: %w", err)
	}
	if n != len(b) {
		return false, fmt.Errorf("write staged secret store: short write (%d of %d bytes)", n, len(b))
	}
	if err := f.Sync(); err != nil {
		return false, fmt.Errorf("sync staged secret store: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close staged secret store: %w", err)
	}
	closed = true
	if s.hooks.staged != nil {
		if err := s.hooks.staged(tmp); err != nil {
			return false, fmt.Errorf("inspect staged secret store: %w", err)
		}
	}
	verify, err := readSecretStoreFile(tmp)
	if err != nil {
		return false, fmt.Errorf("verify staged secret store: %w", err)
	}
	if !bytes.Equal(verify, b) {
		return false, errors.New("verify staged secret store: bytes differ from encoded candidate")
	}
	verifiedData, err := parseSecretStore(verify)
	if err != nil {
		return false, fmt.Errorf("verify staged secret-store schema: %w", err)
	}
	if !maps.Equal(verifiedData, data) {
		return false, errors.New("verify staged secret store: decoded map differs from candidate")
	}
	if err := inspectSecretStorePath(s.path); err != nil {
		return false, err
	}
	replace := s.hooks.replace
	if replace == nil {
		replace = replaceSecretFile
	}
	if err := replace(tmp, s.path); err != nil {
		return false, fmt.Errorf("publish staged secret store: %w", err)
	}
	published = true
	syncDir := s.hooks.syncDir
	if syncDir == nil {
		syncDir = syncSecretDirectory
	}
	if err := syncDir(s.path); err != nil {
		return true, fmt.Errorf("sync secret-store directory: %w", err)
	}
	return true, nil
}

func syncSecretDirectory(path string) error {
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil && runtime.GOOS != "windows" {
		return err
	}
	return nil
}

func cloneData(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for ref, value := range in {
		out[ref] = value
	}
	return out
}

func (s *Store) newDecryptor(hasEncryptedSecrets bool) (ciphertextDecryptor, error) {
	if s.hooks.decryptSession != nil {
		return s.hooks.decryptSession(hasEncryptedSecrets)
	}
	return newCiphertextDecryptor(hasEncryptedSecrets)
}

func (s *Store) encrypt(plaintext, ref string, allowKeyCreation bool) (string, error) {
	if s.hooks.encrypt != nil {
		return s.hooks.encrypt(plaintext, ref, allowKeyCreation)
	}
	return encryptString(plaintext, ref, allowKeyCreation)
}

func (s *Store) latchCorruptionLocked(cause error) error {
	if s.healthErr == nil {
		s.healthErr = &StoreCorruptionError{Path: s.path, Cause: cause}
	}
	return s.healthErr
}

// validateDataLocked proves that every value in data is structurally valid and
// authentic under one backend/key snapshot. Authentication failures are only
// latched when latchAuthentication is true (the durable current-store path);
// candidate verification and transient backend failures remain retryable.
func (s *Store) validateDataManyLocked(data map[string]string, wanted map[string]struct{}, latchAuthentication bool) (map[string]string, error) {
	refs := make([]string, 0, len(data))
	records := make(map[string]ciphertextRecord, len(data))
	for ref, ciphertext := range data {
		record, err := decodeCiphertextRecord(ciphertext)
		if err != nil {
			wrapped := fmt.Errorf("ciphertext for ref %q is invalid: %w", ref, err)
			if latchAuthentication {
				return nil, s.latchCorruptionLocked(wrapped)
			}
			return nil, wrapped
		}
		records[ref] = record
		refs = append(refs, ref)
	}
	decrypt, err := s.newDecryptor(len(refs) != 0)
	if err != nil {
		return nil, err
	}
	sort.Strings(refs)
	requested := make(map[string]string)
	for _, ref := range refs {
		plaintext, err := decrypt(records[ref], ref)
		if err != nil {
			if latchAuthentication && errors.Is(err, ErrCiphertextAuthentication) {
				return nil, s.latchCorruptionLocked(fmt.Errorf("ciphertext authentication/decryption failed for ref %q: %w", ref, err))
			}
			return nil, fmt.Errorf("decrypt ciphertext for ref %q: %w", ref, err)
		}
		if _, keep := wanted[ref]; keep {
			requested[ref] = plaintext
		}
	}
	return requested, nil
}

func (s *Store) validateDataLocked(data map[string]string, wanted *string, latchAuthentication bool) (string, bool, error) {
	var requestedRefs map[string]struct{}
	if wanted != nil {
		requestedRefs = map[string]struct{}{*wanted: {}}
	}
	requested, err := s.validateDataManyLocked(data, requestedRefs, latchAuthentication)
	if err != nil || wanted == nil {
		return "", false, err
	}
	plaintext, found := requested[*wanted]
	return plaintext, found, nil
}

// validateLocked validates the currently loaded durable state and therefore
// latches definitive ciphertext corruption for the Store's lifetime.
func (s *Store) validateLocked(wanted *string) (string, bool, error) {
	if s.healthErr != nil {
		return "", false, s.healthErr
	}
	return s.validateDataLocked(s.data, wanted, true)
}

// Health validates the store schema, every ciphertext, and, where applicable,
// the durable OS-keyring master key. It never creates, replaces, rotates, or
// deletes material. Ciphertext failures are latched for this Store's lifetime,
// preserving the source bytes and blocking every subsequent mutation.
func (s *Store) Health() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, err := s.validateLocked(nil)
	return err
}

// GetChecked returns the decrypted value, whether ref exists, and any store or
// decryption failure. It validates every ciphertext so "not found" can never
// mask unrelated store corruption.
func (s *Store) GetChecked(ref string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.validateLocked(&ref)
}

// GetManyChecked validates the whole store once under one backend/key snapshot
// and returns plaintext only for requested refs that exist. It is Go-internal
// and is not renderer-bound. An absent map entry means the ref was not stored;
// an error means no returned plaintexts are trustworthy.
func (s *Store) GetManyChecked(refs []string) (map[string]string, error) {
	wanted := make(map[string]struct{}, len(refs))
	totalBytes := 0
	for _, ref := range refs {
		if len(ref) > maxSecretRefBytes {
			return nil, fmt.Errorf("requested secret ref exceeds the %d-byte limit", maxSecretRefBytes)
		}
		if !utf8.ValidString(ref) {
			return nil, errors.New("requested secret ref must be valid UTF-8")
		}
		if _, duplicate := wanted[ref]; duplicate {
			continue
		}
		if len(wanted) >= maxSecretLookupRefs {
			return nil, fmt.Errorf("requested unique secret refs exceed the %d-entry lookup limit", maxSecretLookupRefs)
		}
		if len(ref) > maxSecretLookupRefBytes-totalBytes {
			return nil, fmt.Errorf("requested secret refs exceed the %d-byte aggregate lookup limit", maxSecretLookupRefBytes)
		}
		totalBytes += len(ref)
		wanted[ref] = struct{}{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.healthErr != nil {
		return nil, s.healthErr
	}
	return s.validateDataManyLocked(s.data, wanted, true)
}

// Get returns the decrypted secret for ref. Go-internal only; never exposed to
// the frontend.
func (s *Store) Get(ref string) (string, bool) {
	plain, ok, err := s.GetChecked(ref)
	if err != nil {
		log.Printf("secret: cannot read %q: %v", ref, err)
		return "", false
	}
	return plain, ok
}

// Set encrypts and stores value under ref.
func (s *Store) Set(ref, value string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("secret ref is required")
	}
	if len(ref) > maxSecretRefBytes {
		return fmt.Errorf("secret ref exceeds the %d-byte limit", maxSecretRefBytes)
	}
	if !utf8.ValidString(ref) {
		return errors.New("secret ref must be valid UTF-8")
	}
	if len(value) > maxSecretPlaintextBytes {
		return fmt.Errorf("secret value exceeds the %d-byte limit", maxSecretPlaintextBytes)
	}
	if !utf8.ValidString(value) {
		return errors.New("secret value must be valid UTF-8")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.validateLocked(nil); err != nil {
		return err
	}
	next := cloneData(s.data)
	// An empty value clears the secret, so Has never reports a blank as set.
	if value == "" {
		if _, exists := s.data[ref]; !exists {
			return nil
		}
		delete(next, ref)
		published, err := s.persistLocked(next)
		if published {
			s.data = next
		}
		return err
	}
	if _, exists := s.data[ref]; !exists && len(s.data) >= maxSecretEntries {
		return fmt.Errorf("secret store already contains the maximum of %d entries", maxSecretEntries)
	}
	// A key may be created only for a genuinely empty ciphertext map. Once any
	// ciphertext exists, a missing durable key is a recovery condition.
	enc, err := s.encrypt(value, ref, len(s.data) == 0)
	if err != nil {
		return err
	}
	if err := validateCiphertextShape(enc); err != nil {
		return fmt.Errorf("encryption backend returned invalid ciphertext: %w", err)
	}
	next[ref] = enc
	// Re-open one backend/key snapshot after encryption and authenticate the
	// entire candidate before publishing it. This catches a keyring change
	// between initial validation and encryption, prevents mixed-key stores, and
	// proves a fresh key was durably readable before its first ciphertext lands.
	roundTrip, found, err := s.validateDataLocked(next, &ref, false)
	if err != nil {
		return fmt.Errorf("verify encrypted secret-store candidate: %w", err)
	}
	if !found || roundTrip != value {
		return errors.New("verify encrypted secret-store candidate: encrypted value did not round-trip")
	}
	published, err := s.persistLocked(next)
	if published {
		s.data = next
	}
	return err
}

// Replace atomically removes oldRef and stores value under newRef. It is a
// Go-internal primitive for explicit credential replacement: callers can move
// a quarantined legacy credential to its derived owner-scoped ref without a
// transient extra entry, even when the store is at capacity. The old entry is
// required so this operation cannot silently degrade into an unowned write.
func (s *Store) Replace(oldRef, newRef, value string) error {
	oldRef = strings.TrimSpace(oldRef)
	newRef = strings.TrimSpace(newRef)
	if oldRef == "" {
		return errors.New("old secret ref is required")
	}
	if newRef == "" {
		return errors.New("new secret ref is required")
	}
	if oldRef == newRef {
		return errors.New("old and new secret refs must differ")
	}
	if len(oldRef) > maxSecretRefBytes || len(newRef) > maxSecretRefBytes {
		return fmt.Errorf("secret ref exceeds the %d-byte limit", maxSecretRefBytes)
	}
	if !utf8.ValidString(oldRef) || !utf8.ValidString(newRef) {
		return errors.New("secret ref must be valid UTF-8")
	}
	if value == "" {
		return errors.New("replacement secret value is required")
	}
	if len(value) > maxSecretPlaintextBytes {
		return fmt.Errorf("secret value exceeds the %d-byte limit", maxSecretPlaintextBytes)
	}
	if !utf8.ValidString(value) {
		return errors.New("secret value must be valid UTF-8")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.validateLocked(nil); err != nil {
		return err
	}
	if _, exists := s.data[oldRef]; !exists {
		return fmt.Errorf("old secret ref %q does not exist", oldRef)
	}

	next := cloneData(s.data)
	// Delete before the capacity check so an old->new replacement is
	// count-neutral at a full store. A pre-existing destination is explicitly
	// overwritten by the newly entered plaintext and reduces the count by one.
	delete(next, oldRef)
	if _, exists := next[newRef]; !exists && len(next) >= maxSecretEntries {
		return fmt.Errorf("secret store already contains the maximum of %d entries", maxSecretEntries)
	}
	// An existing old entry means durable ciphertext already exists, so key
	// creation is never safe here; a missing key is a recovery condition.
	enc, err := s.encrypt(value, newRef, false)
	if err != nil {
		return err
	}
	if err := validateCiphertextShape(enc); err != nil {
		return fmt.Errorf("encryption backend returned invalid ciphertext: %w", err)
	}
	next[newRef] = enc

	// Authenticate the entire candidate under one backend/key snapshot before
	// publication, including the new ref binding and every untouched entry.
	roundTrip, found, err := s.validateDataLocked(next, &newRef, false)
	if err != nil {
		return fmt.Errorf("verify encrypted secret-store replacement candidate: %w", err)
	}
	if !found || roundTrip != value {
		return errors.New("verify encrypted secret-store replacement candidate: encrypted value did not round-trip")
	}
	published, err := s.persistLocked(next)
	if published {
		s.data = next
	}
	return err
}

// Has reports whether ciphertext is stored for ref. It intentionally answers
// the storage question without attempting decryption.
func (s *Store) Has(ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[ref]
	return ok
}

// Delete removes the secret for ref. A corrupt/unavailable encryption backend
// blocks deletion so ordinary caller cleanup cannot destroy recovery evidence.
func (s *Store) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, _, err := s.validateLocked(nil); err != nil {
		return err
	}
	if _, exists := s.data[ref]; !exists {
		return nil
	}
	next := cloneData(s.data)
	delete(next, ref)
	published, err := s.persistLocked(next)
	if published {
		s.data = next
	}
	return err
}

// List returns stored refs (never values). This remains Go-internal.
func (s *Store) List() []string {
	s.mu.Lock()
	refs := make([]string, 0, len(s.data))
	for k := range s.data {
		refs = append(refs, k)
	}
	s.mu.Unlock()
	sort.Strings(refs)
	return refs
}
