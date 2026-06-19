package main

import "novera/internal/secret"

// SecretService is the frontend-facing surface of the secret store. It
// intentionally exposes NO value getter — the UI can store, check, delete, and
// list credential refs, but can never read a secret back. Only Go services
// (e.g. llm) read values, via the underlying store.
type SecretService struct {
	store *secret.Store
}

// SetKey stores (encrypted) value under ref.
func (s *SecretService) SetKey(ref, value string) error { return s.store.Set(ref, value) }

// HasKey reports whether a credential is stored for ref.
func (s *SecretService) HasKey(ref string) bool { return s.store.Has(ref) }

// DeleteKey removes the credential for ref.
func (s *SecretService) DeleteKey(ref string) error { return s.store.Delete(ref) }

// ListKeys returns the stored refs (never the values).
func (s *SecretService) ListKeys() []string { return s.store.List() }
