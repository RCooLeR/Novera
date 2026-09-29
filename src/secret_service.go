package main

import (
	"errors"

	"novera/internal/settings"
)

// SecretService is a compatibility facade for the existing generated frontend
// binding. It intentionally exposes no generic store access: every operation is
// constrained to the current backend-owned LLM credential scope.
type SecretService struct {
	settings *settings.Service
}

const legacyLLMKeySelector = "llm.apikey"

// acceptsLLMSelector keeps the old binding signature compatible while turning
// ref into a non-authoritative selector. The actual storage handle is always
// derived and owned by the settings service for the current provider/origin.
func (s *SecretService) acceptsLLMSelector(ref string) bool {
	return ref == legacyLLMKeySelector
}

// SetKey stores only the current LLM credential. Arbitrary caller-selected
// references (including database credential refs) are rejected.
func (s *SecretService) SetKey(ref, value string) error {
	if s.settings == nil || !s.acceptsLLMSelector(ref) {
		return errors.New("credential reference is not owned by the LLM provider configuration")
	}
	return s.settings.SetLLMAPIKey(value)
}

// HasKey reports only the current LLM credential state.
func (s *SecretService) HasKey(ref string) bool {
	return s.settings != nil && s.acceptsLLMSelector(ref) && s.settings.HasLLMAPIKey()
}

// DeleteKey removes only the current LLM credential.
func (s *SecretService) DeleteKey(ref string) error {
	if s.settings == nil || !s.acceptsLLMSelector(ref) {
		return errors.New("credential reference is not owned by the LLM provider configuration")
	}
	return s.settings.DeleteLLMAPIKey()
}

// ListKeys returns at most the current LLM-owned ref. Database and other
// subsystem refs are never disclosed across the renderer boundary.
func (s *SecretService) ListKeys() []string {
	if s.settings == nil || !s.settings.HasLLMAPIKey() {
		return []string{}
	}
	return []string{legacyLLMKeySelector}
}
