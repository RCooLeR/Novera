// Package secret is a small at-rest credential store kept SEPARATE from
// settings and chat history. Values are encrypted on disk on every platform: on
// Windows with DPAPI, elsewhere with AES-256-GCM under a per-user master key
// held in the OS keyring (macOS Keychain / Linux Secret Service). The 0600 file
// holds only ciphertext. The value getter is Go-internal only: the bound
// frontend service exposes set/has/delete/list but never reads a secret back,
// so credentials can't leak into the UI or transcripts.
package secret

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Store is the on-disk encrypted secret map (ref -> ciphertext).
type Store struct {
	mu   sync.Mutex
	path string
	data map[string]string
}

// New opens (or initialises) the secret store under the OS config dir.
func New() *Store {
	s := &Store{path: filepath.Join(configDir(), "Novera", "secrets.json"), data: map[string]string{}}
	s.load()
	return s
}

// configDir resolves a stable, absolute per-user config directory. If neither
// the OS config dir nor the home dir is available it falls back to a temp dir
// (logged) rather than returning "" — which would yield a CWD-relative path
// that silently moves with the process working directory.
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
	b, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		// Don't silently discard then overwrite — preserve the corrupt file so
		// stored credentials can be recovered.
		_ = os.Rename(s.path, s.path+".corrupt")
		s.data = map[string]string{}
		return
	}
	if s.data == nil {
		s.data = map[string]string{}
	}
}

func (s *Store) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// Get returns the decrypted secret for ref. Go-internal only — never exposed to
// the frontend.
func (s *Store) Get(ref string) (string, bool) {
	s.mu.Lock()
	enc, ok := s.data[ref]
	s.mu.Unlock()
	if !ok {
		return "", false
	}
	plain, err := decryptString(enc)
	if err != nil {
		// A value IS stored but can't be decrypted (e.g. DPAPI failure, or the
		// store was copied from another machine/user). Don't silently mask it as
		// "not found" — log the real cause so it's diagnosable.
		log.Printf("secret: stored value for %q could not be decrypted: %v", ref, err)
		return "", false
	}
	return plain, true
}

// Set encrypts and stores value under ref.
func (s *Store) Set(ref, value string) error {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return errors.New("secret ref is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// An empty value clears the secret, so Has() never reports a blank as set.
	if value == "" {
		delete(s.data, ref)
		return s.persistLocked()
	}
	enc, err := encryptString(value)
	if err != nil {
		return err
	}
	s.data[ref] = enc
	return s.persistLocked()
}

// Has reports whether a secret is stored for ref.
func (s *Store) Has(ref string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data[ref]
	return ok
}

// Delete removes the secret for ref.
func (s *Store) Delete(ref string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, ref)
	return s.persistLocked()
}

// List returns the stored refs (never the values).
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
