// Package settings persists user/application preferences as JSON under the OS
// config directory (e.g. %AppData%/Novera on Windows). Secrets are NOT stored
// here — credential material lives in the dedicated secrets store so it never
// lands in plaintext config (a defect in the previous generation).
package settings

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"novera/internal/persistfile"
)

const (
	maxRecent             = 12
	maxSettingsBytes      = 1 << 20 // settings are small; bound hostile/corrupt input before JSON allocation
	maxWorkspacePathBytes = 32 << 10
	maxLLMBaseURLBytes    = 16 << 10
	maxLLMModelBytes      = 4 << 10
	maxSettingsLabelBytes = 1 << 10
)

const (
	DefaultLLMProvider = "ollama"
	DefaultLLMBaseURL  = "http://localhost:11434/v1"
	DefaultLLMModel    = "gemma4:12b-it-q8_0"
	legacyBadLLMModel  = "gemma4:12b-it-q_8_0"
)

// DefaultRequestTimeoutSec is the fallback per-request LLM timeout. It is
// generous on purpose: a local model's first call also pays model-load time,
// and agent completions are non-streaming (the whole answer must be generated
// before the request returns), so a large model on modest hardware can
// legitimately need many minutes.
const (
	DefaultRequestTimeoutSec = 1800
	MinRequestTimeoutSec     = 30
	MaxRequestTimeoutSec     = 21600 // six hours; matches the settings UI ceiling
)

const (
	// legacyLLMAPIKeyRef is the only unscoped credential reference emitted by
	// older Novera releases. It is recognized only as quarantined ownership for
	// explicit replacement or interrupted-replacement recovery; arbitrary refs
	// are deliberately never copied into LLM scope.
	legacyLLMAPIKeyRef = "llm.apikey"
	llmAPIKeyPrefix    = "llm.apikey.v1."
)

const (
	DefaultAgentMaxToolOutputChars = 6000
	DefaultAgentStepBatch          = 50
	DefaultAgentMaxTotalSteps      = 1000
	DefaultAgentHistoryWindow      = 8
	DefaultAgentCommandTimeoutSec  = 60
)

const (
	DefaultUIFontSize     = 13
	MinUIFontSize         = 10
	MaxUIFontSize         = 20
	DefaultEditorFontSize = 13
	MinEditorFontSize     = 9
	MaxEditorFontSize     = 28
	DefaultEditorTabSize  = 4
	MinEditorTabSize      = 1
	MaxEditorTabSize      = 8
)

// Editor holds editor-pane preferences mirrored into Monaco on the frontend.
type Editor struct {
	FontSize     int    `json:"fontSize"`
	TabSize      int    `json:"tabSize"`
	WordWrap     bool   `json:"wordWrap"`
	Minimap      bool   `json:"minimap"`
	Theme        string `json:"theme"` // monaco theme id
	FormatOnSave bool   `json:"formatOnSave"`
}

// Normalized keeps persisted editor numbers inside the same operational ranges
// exposed by the renderer. Missing zero values use defaults; hostile or manually
// edited outliers are clamped before they can reach Monaco.
func (e Editor) Normalized() Editor {
	if e.FontSize == 0 {
		e.FontSize = DefaultEditorFontSize
	}
	if e.FontSize < MinEditorFontSize {
		e.FontSize = MinEditorFontSize
	}
	if e.FontSize > MaxEditorFontSize {
		e.FontSize = MaxEditorFontSize
	}
	if e.TabSize == 0 {
		e.TabSize = DefaultEditorTabSize
	}
	if e.TabSize < MinEditorTabSize {
		e.TabSize = MinEditorTabSize
	}
	if e.TabSize > MaxEditorTabSize {
		e.TabSize = MaxEditorTabSize
	}
	return e
}

func normalizedUIFontSize(size int) int {
	if size == 0 {
		size = DefaultUIFontSize
	}
	if size < MinUIFontSize {
		return MinUIFontSize
	}
	if size > MaxUIFontSize {
		return MaxUIFontSize
	}
	return size
}

// LLM holds non-secret provider configuration. The API key itself is NEVER
// stored here — only a reference (APIKeyRef) into the dedicated secret store.
type LLM struct {
	Provider  string `json:"provider"` // "ollama" | "openai" | "custom"
	BaseURL   string `json:"baseURL"`
	Model     string `json:"model"`
	APIKeyRef string `json:"apiKeyRef"` // output-only backend-owned provider/origin handle; caller input is ignored
	// RequestTimeoutSec bounds a single LLM request (one agent completion, or
	// the whole Ask-mode stream). User-tunable because local-model latency
	// varies wildly with hardware. 0 means "use the default" — see RequestTimeout.
	RequestTimeoutSec int `json:"requestTimeoutSec"`
}

// Normalized fills missing LLM fields. The default model is local-Ollama only:
// custom/OpenAI profiles should stay explicit instead of inheriting a local tag.
func (l LLM) Normalized() LLM {
	if strings.TrimSpace(l.Provider) == "" {
		l.Provider = DefaultLLMProvider
	}
	if strings.TrimSpace(l.BaseURL) == "" && strings.EqualFold(l.Provider, DefaultLLMProvider) {
		l.BaseURL = DefaultLLMBaseURL
	}
	if strings.TrimSpace(l.Model) == "" && strings.EqualFold(l.Provider, DefaultLLMProvider) {
		l.Model = DefaultLLMModel
	}
	if strings.EqualFold(l.Provider, DefaultLLMProvider) && strings.TrimSpace(l.Model) == legacyBadLLMModel {
		l.Model = DefaultLLMModel
	}
	if l.RequestTimeoutSec == 0 || l.RequestTimeoutSec < MinRequestTimeoutSec || l.RequestTimeoutSec > MaxRequestTimeoutSec {
		l.RequestTimeoutSec = DefaultRequestTimeoutSec
	}
	return l
}

func (l LLM) validate() error {
	sec := l.RequestTimeoutSec
	if sec == 0 {
		return nil
	}
	if sec < MinRequestTimeoutSec || sec > MaxRequestTimeoutSec {
		return fmt.Errorf("request timeout must be 0 (default) or between %d and %d seconds", MinRequestTimeoutSec, MaxRequestTimeoutSec)
	}
	return nil
}

// Agent holds runtime controls for Agent mode. These are intentionally exposed:
// local LLMs vary wildly by model and hardware, so output/window/step budgets
// need to be adjustable without recompiling.
type Agent struct {
	MaxToolOutputChars  int `json:"maxToolOutputChars"`
	StepBatch           int `json:"stepBatch"`
	MaxTotalSteps       int `json:"maxTotalSteps"`
	HistoryWindowGroups int `json:"historyWindowGroups"`
	CommandTimeoutSec   int `json:"commandTimeoutSec"`
}

// RequestTimeout resolves the per-request LLM timeout through Normalized, which
// guarantees the seconds-to-duration conversion cannot overflow and remains
// within the documented operational range even for hostile persisted input.
func (l LLM) RequestTimeout() time.Duration {
	normalized := l.Normalized()
	return time.Duration(normalized.RequestTimeoutSec) * time.Second
}

// Normalized fills missing Agent fields and clamps user-edited values into
// operationally safe ranges.
func (a Agent) Normalized() Agent {
	if a.MaxToolOutputChars <= 0 {
		a.MaxToolOutputChars = DefaultAgentMaxToolOutputChars
	}
	if a.MaxToolOutputChars < 1000 {
		a.MaxToolOutputChars = 1000
	}
	if a.MaxToolOutputChars > 50000 {
		a.MaxToolOutputChars = 50000
	}
	if a.StepBatch <= 0 {
		a.StepBatch = DefaultAgentStepBatch
	}
	if a.StepBatch < 1 {
		a.StepBatch = 1
	}
	if a.StepBatch > 500 {
		a.StepBatch = 500
	}
	if a.MaxTotalSteps <= 0 {
		a.MaxTotalSteps = DefaultAgentMaxTotalSteps
	}
	if a.MaxTotalSteps < a.StepBatch {
		a.MaxTotalSteps = a.StepBatch
	}
	if a.MaxTotalSteps > 10000 {
		a.MaxTotalSteps = 10000
	}
	if a.HistoryWindowGroups <= 0 {
		a.HistoryWindowGroups = DefaultAgentHistoryWindow
	}
	if a.HistoryWindowGroups < 1 {
		a.HistoryWindowGroups = 1
	}
	if a.HistoryWindowGroups > 50 {
		a.HistoryWindowGroups = 50
	}
	if a.CommandTimeoutSec <= 0 {
		a.CommandTimeoutSec = DefaultAgentCommandTimeoutSec
	}
	if a.CommandTimeoutSec < 5 {
		a.CommandTimeoutSec = 5
	}
	if a.CommandTimeoutSec > 3600 {
		a.CommandTimeoutSec = 3600
	}
	return a
}

func (a Agent) CommandTimeout() time.Duration {
	n := a.Normalized()
	return time.Duration(n.CommandTimeoutSec) * time.Second
}

// Settings is the full persisted preference set.
type Settings struct {
	Theme            string   `json:"theme"` // app theme: "dark" | "light"
	LastWorkspace    string   `json:"lastWorkspace"`
	RecentWorkspaces []string `json:"recentWorkspaces"`
	UIFontSize       int      `json:"uiFontSize"` // base font size for the app chrome (px)
	Editor           Editor   `json:"editor"`
	LLM              LLM      `json:"llm"`
	Agent            Agent    `json:"agent"`
}

// LLMAPIKeyStatus is a non-secret, backend-verified credential state for UI
// rendering. Presence of an opaque APIKeyRef alone is never treated as proof
// that a credential is usable.
type LLMAPIKeyStatus struct {
	State   string `json:"state"`   // missing | verified | quarantined | unavailable
	Message string `json:"message"` // actionable diagnostic for non-verified states
}

func defaults() Settings {
	return Settings{
		Theme:            "dark",
		RecentWorkspaces: []string{},
		UIFontSize:       DefaultUIFontSize,
		Editor: Editor{
			FontSize: DefaultEditorFontSize,
			TabSize:  DefaultEditorTabSize,
			WordWrap: false,
			Minimap:  true,
			Theme:    "novera-dark",
		},
		LLM: LLM{
			Provider:          DefaultLLMProvider,
			BaseURL:           DefaultLLMBaseURL,
			Model:             DefaultLLMModel,
			RequestTimeoutSec: DefaultRequestTimeoutSec,
		},
		Agent: Agent{
			MaxToolOutputChars:  DefaultAgentMaxToolOutputChars,
			StepBatch:           DefaultAgentStepBatch,
			MaxTotalSteps:       DefaultAgentMaxTotalSteps,
			HistoryWindowGroups: DefaultAgentHistoryWindow,
			CommandTimeoutSec:   DefaultAgentCommandTimeoutSec,
		},
	}
}

// Service is the bound Wails service for settings.
type Service struct {
	mu      sync.Mutex
	path    string
	secrets SecretStore
	loadErr error // latched until restart so corrupt evidence cannot be overwritten by a later mutation
}

// SecretStore is the narrow credential-store surface needed to own the LLM
// credential lifecycle. Keeping ownership here means the renderer can update a
// provider configuration, but cannot choose which existing secret it uses.
type SecretStore interface {
	Get(ref string) (string, bool)
	Set(ref, value string) error
	Has(ref string) bool
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

func secretStoreGetManyChecked(store SecretStore, refs []string) (map[string]string, error) {
	if checked, ok := store.(checkedManySecretStore); ok {
		return checked.GetManyChecked(refs)
	}
	values := make(map[string]string, len(refs))
	for _, ref := range refs {
		value, found, err := secretStoreGetChecked(store, ref)
		if err != nil {
			return nil, err
		}
		if found {
			values[ref] = value
		}
	}
	return values, nil
}

// New constructs the settings service, resolving the on-disk config path.
func New(secrets SecretStore) *Service {
	return &Service{path: filepath.Join(configDir(), "Novera", "settings.json"), secrets: secrets}
}

// configDir resolves a stable, absolute per-user config directory. If neither
// the OS config dir nor the home dir is available it falls back to a temp dir
// (logged) rather than returning "" — which would produce a CWD-relative path
// whose location silently changes with the process working directory.
func configDir() string {
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return dir
	}
	if dir, err := os.UserHomeDir(); err == nil && dir != "" {
		return dir
	}
	dir := os.TempDir()
	log.Printf("settings: no config/home dir available, falling back to %s", dir)
	return dir
}

// sanitizeBaseURL strips any embedded userinfo (user:pass@) from a base URL so
// credentials accidentally pasted into the URL never land in plaintext config;
// credentials belong in the dedicated secret store. Unparseable input is left
// untouched (request-time validation rejects it).
func sanitizeBaseURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if u.User != nil {
		u.User = nil
		return u.String()
	}
	return raw
}

// llmCredentialRef returns the backend-owned credential handle for the exact
// provider and network origin. A credential therefore stops being usable when
// either its provider or destination origin changes.
func llmCredentialRef(in LLM) (string, error) {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	if provider == "" {
		return "", errors.New("an LLM provider is required")
	}
	raw := sanitizeBaseURL(in.BaseURL)
	u, err := url.Parse(raw)
	if err != nil || u == nil {
		return "", errors.New("the LLM base URL is invalid")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return "", errors.New("the LLM base URL must use http or https")
	}
	host := strings.ToLower(strings.TrimSpace(u.Host))
	if host == "" || u.Hostname() == "" {
		return "", errors.New("the LLM base URL must include a host")
	}
	// The key is intentionally scoped to the origin, rather than a path. The LLM
	// client appends several API paths under the same origin and never follows a
	// cross-host redirect.
	scope := provider + "\n" + scheme + "://" + host
	sum := sha256.Sum256([]byte(scope))
	return fmt.Sprintf("%s%x", llmAPIKeyPrefix, sum[:]), nil
}

// ExpectedLLMAPIKeyRef returns the backend-owned credential handle for this
// exact provider origin. Secret consumers must compare a persisted APIKeyRef
// with this value again at point of use: a preserved legacy ref represents
// migration/recovery evidence, not authority to send that credential.
func ExpectedLLMAPIKeyRef(in LLM) (string, error) {
	return llmCredentialRef(in)
}

func isLLMAPIKeyRef(ref string) bool {
	return ref == legacyLLMAPIKeyRef || strings.HasPrefix(ref, llmAPIKeyPrefix)
}

// loadLocked reads and normalizes settings and sanitizes the persisted LLM
// credential handle. Legacy plaintext is never copied automatically.
func (s *Service) loadLocked() (out Settings, changed bool, deleteAfter string) {
	out = defaults()
	if s.loadErr != nil {
		return out, false, ""
	}
	data, err := persistfile.Read(s.path, maxSettingsBytes)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, false, ""
		}
		s.latchLoadError(err)
		return out, false, ""
	}
	if err := decodeSettings(data, &out); err != nil {
		// Leave the primary byte-for-byte in place. Renaming first and then falling
		// back to defaults allowed a failed rename to be followed by a destructive
		// overwrite. The latched error blocks every mutation until restart.
		s.latchLoadError(err)
		return defaults(), false, ""
	}
	if out.RecentWorkspaces == nil {
		out.RecentWorkspaces = []string{}
		changed = true
	}
	if len(out.RecentWorkspaces) > maxRecent {
		out.RecentWorkspaces = append([]string(nil), out.RecentWorkspaces[:maxRecent]...)
		changed = true
	}
	originalLLM := out.LLM
	originalAgent := out.Agent
	originalEditor := out.Editor
	originalUIFontSize := out.UIFontSize
	out.LLM = out.LLM.Normalized()
	out.Agent = out.Agent.Normalized()
	out.Editor = out.Editor.Normalized()
	out.UIFontSize = normalizedUIFontSize(out.UIFontSize)
	out.LLM.BaseURL = sanitizeBaseURL(out.LLM.BaseURL)
	if out.LLM != originalLLM || out.Agent != originalAgent || out.Editor != originalEditor || out.UIFontSize != originalUIFontSize {
		changed = true
	}

	ref := strings.TrimSpace(out.LLM.APIKeyRef)
	if ref == "" {
		return out, changed, ""
	}
	var healthErr error
	if _, checked := s.secrets.(checkedSecretStore); !checked {
		healthErr = secretStoreHealth(s.secrets)
	}
	expected, scopeErr := llmCredentialRef(out.LLM)
	if scopeErr != nil {
		out.LLM.APIKeyRef = ""
		return out, true, ""
	}
	if ref == expected {
		if healthErr != nil {
			log.Printf("settings: preserving LLM credential ownership while secret storage is unavailable: %v", healthErr)
			return out, changed, ""
		}
		// A dangling handle is not useful and should not make the UI claim that a
		// credential exists. With no store (unit-only construction), retain the
		// structurally valid handle.
		if s.secrets != nil {
			_, found, readErr := secretStoreGetChecked(s.secrets, ref)
			if readErr != nil {
				log.Printf("settings: preserving LLM credential ownership after checked-read failure: %v", readErr)
				return out, changed, ""
			}
			if !found {
				out.LLM.APIKeyRef = ""
				return out, true, ""
			}
		}
		return out, changed, ""
	}
	if ref != legacyLLMAPIKeyRef {
		// Never reinterpret an arbitrary existing reference as an LLM credential.
		// This is the critical confused-deputy boundary (for example, a DB ref).
		out.LLM.APIKeyRef = ""
		return out, true, ""
	}
	if s.secrets == nil {
		// Preserve the exact historical ownership record. It is not accepted at
		// point of use and can be rebound only by explicit API-key re-entry.
		return out, changed, ""
	}
	if healthErr != nil {
		log.Printf("settings: deferring legacy LLM credential inspection while secret storage is unavailable: %v", healthErr)
		return out, changed, ""
	}
	// Read both possible refs from one authenticated store snapshot when the
	// implementation supports it. If the explicit cross-store replacement was
	// published but the settings metadata was not, old is absent and the exact
	// derived ref is the only safe, deterministic recovery target.
	values, readErr := secretStoreGetManyChecked(s.secrets, []string{legacyLLMAPIKeyRef, expected})
	if readErr != nil {
		log.Printf("settings: deferring legacy LLM credential inspection after checked-read failure: %v", readErr)
		return out, changed, ""
	}
	if value := values[legacyLLMAPIKeyRef]; value != "" {
		// The legacy ciphertext authenticates neither provider nor origin. Never
		// automatically bind it to scope metadata that may have been edited on
		// disk; keep it quarantined and require explicit re-entry.
		log.Printf("settings: legacy LLM credential is preserved but quarantined until the API key is re-entered for the current provider origin")
		return out, changed, ""
	}
	if value := values[expected]; value != "" {
		out.LLM.APIKeyRef = expected
		log.Printf("settings: recovered LLM credential ownership after an interrupted explicit legacy replacement")
		return out, true, ""
	}
	out.LLM.APIKeyRef = ""
	return out, true, ""
}

func decodeSettings(data []byte, out *Settings) error {
	if !utf8.Valid(data) {
		return errors.New("settings JSON is not valid UTF-8")
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("settings must be a JSON object")
	}
	if err := rejectDuplicateSettingsKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("unexpected content after settings object")
		}
		return err
	}
	return validateSettingsStrings(*out)
}

func validateSettingsStrings(in Settings) error {
	values := []struct {
		label string
		value string
		limit int
	}{
		{"theme", in.Theme, maxSettingsLabelBytes},
		{"last workspace", in.LastWorkspace, maxWorkspacePathBytes},
		{"editor theme", in.Editor.Theme, maxSettingsLabelBytes},
		{"LLM provider", in.LLM.Provider, maxSettingsLabelBytes},
		{"LLM base URL", in.LLM.BaseURL, maxLLMBaseURLBytes},
		{"LLM model", in.LLM.Model, maxLLMModelBytes},
		{"LLM credential reference", in.LLM.APIKeyRef, maxSettingsLabelBytes},
	}
	for i, recent := range in.RecentWorkspaces {
		values = append(values, struct {
			label string
			value string
			limit int
		}{fmt.Sprintf("recent workspace %d", i), recent, maxWorkspacePathBytes})
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

func rejectDuplicateSettingsKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := validateUniqueSettingsJSONValue(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple settings JSON values")
		}
		return err
	}
	return nil
}

func validateUniqueSettingsJSONValue(dec *json.Decoder) error {
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
				return errors.New("settings JSON object key is not a string")
			}
			canonical := strings.ToLower(key)
			if prior, exists := seen[canonical]; exists {
				return fmt.Errorf("duplicate settings JSON object key %q (conflicts with %q)", key, prior)
			}
			seen[canonical] = key
			if err := validateUniqueSettingsJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim('}') {
			return errors.New("malformed settings JSON object")
		}
	case '[':
		for dec.More() {
			if err := validateUniqueSettingsJSONValue(dec); err != nil {
				return err
			}
		}
		end, err := dec.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("malformed settings JSON array")
		}
	default:
		return errors.New("unexpected settings JSON delimiter")
	}
	return nil
}

func (s *Service) latchLoadError(cause error) {
	if s.loadErr != nil {
		return
	}
	s.loadErr = fmt.Errorf("settings file %q could not be safely loaded (%v); it was left unchanged and settings mutations are blocked until the file is repaired and Novera is restarted", s.path, cause)
	log.Printf("settings: %v", s.loadErr)
}

// Load returns the persisted settings, falling back to defaults for any missing
// fields or if no file exists yet. Legacy credentials remain quarantined; the
// only legacy metadata repair recognizes an already-completed explicit
// replacement whose settings-file publication was interrupted.
func (s *Service) Load() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, changed, deleteAfter := s.loadLocked()
	if changed {
		if err := s.saveLocked(out); err != nil {
			log.Printf("settings: could not persist sanitized LLM credential ownership: %v", err)
			return out
		}
	}
	if deleteAfter != "" && s.secrets != nil {
		if err := s.secrets.Delete(deleteAfter); err != nil {
			log.Printf("settings: could not delete migrated legacy LLM credential %q: %v", deleteAfter, err)
		}
	}
	return out
}

// LoadError returns the latched, actionable persistence diagnostic, if any.
// Load itself remains a best-effort read for startup compatibility, while this
// separate bound method lets the renderer distinguish defaults from a corrupt
// or unsafe primary file.
func (s *Service) LoadError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr == nil {
		return ""
	}
	return s.loadErr.Error()
}

// Save persists the full settings object atomically.
func (s *Service) Save(in Settings) error {
	if err := in.LLM.validate(); err != nil {
		return err
	}
	if len(in.RecentWorkspaces) > maxRecent {
		return fmt.Errorf("recent workspace count exceeds the limit of %d", maxRecent)
	}
	if err := validateSettingsStrings(in); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, currentChanged, deleteAfter := s.loadLocked()
	if s.loadErr != nil {
		return s.loadErr
	}
	if currentChanged {
		if err := s.saveLocked(current); err != nil {
			return fmt.Errorf("persist sanitized settings before update: %w", err)
		}
		if deleteAfter != "" && s.secrets != nil {
			if err := s.secrets.Delete(deleteAfter); err != nil {
				log.Printf("settings: could not delete migrated legacy LLM credential %q: %v", deleteAfter, err)
			}
		}
	}

	// APIKeyRef is output-only. Preserve the backend-owned current handle when
	// the submitted provider/origin still maps to exactly the same scope. This
	// includes the one quarantined legacy handle: unrelated whole-object saves
	// must not destroy the ownership evidence that explicit key re-entry needs.
	// Caller-selected refs and endpoint changes are still cleared.
	in.LLM = in.LLM.Normalized()
	in.LLM.BaseURL = sanitizeBaseURL(in.LLM.BaseURL)
	in.LLM.APIKeyRef = ""
	newRef, newErr := llmCredentialRef(in.LLM)
	currentRef, currentErr := llmCredentialRef(current.LLM)
	if newErr == nil && currentErr == nil && newRef == currentRef {
		switch current.LLM.APIKeyRef {
		case currentRef, legacyLLMAPIKeyRef:
			in.LLM.APIKeyRef = current.LLM.APIKeyRef
		}
	}
	if oldRef := current.LLM.APIKeyRef; oldRef != "" && oldRef != in.LLM.APIKeyRef && isLLMAPIKeyRef(oldRef) && s.secrets != nil {
		if err := secretStoreHealth(s.secrets); err != nil {
			return fmt.Errorf("credential storage is unavailable; LLM credential ownership was preserved: %w", err)
		}
	}
	if err := s.saveLocked(in); err != nil {
		return err
	}
	if oldRef := current.LLM.APIKeyRef; oldRef != "" && oldRef != in.LLM.APIKeyRef && isLLMAPIKeyRef(oldRef) && s.secrets != nil {
		if err := s.secrets.Delete(oldRef); err != nil {
			log.Printf("settings: could not delete detached LLM credential %q: %v", oldRef, err)
		}
	}
	return nil
}

func (s *Service) saveLocked(in Settings) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	if in.RecentWorkspaces == nil {
		in.RecentWorkspaces = []string{}
	}
	if len(in.RecentWorkspaces) > maxRecent {
		return fmt.Errorf("recent workspace count exceeds the limit of %d", maxRecent)
	}
	in.LLM = in.LLM.Normalized()
	in.Agent = in.Agent.Normalized()
	in.Editor = in.Editor.Normalized()
	in.UIFontSize = normalizedUIFontSize(in.UIFontSize)
	// Never persist credentials embedded in the LLM base URL.
	in.LLM.BaseURL = sanitizeBaseURL(in.LLM.BaseURL)
	if err := validateSettingsStrings(in); err != nil {
		return err
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxSettingsBytes {
		return fmt.Errorf("settings would exceed the %d-byte safety limit", maxSettingsBytes)
	}
	return persistfile.WriteAtomic(s.path, data, 0o600)
}

// SetLLMAPIKey stores an API key under the backend-owned ref for the currently
// persisted provider/origin and attaches that ref to settings. The renderer
// supplies only the new value; it cannot select or reattach an existing ref.
func (s *Service) SetLLMAPIKey(value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.secrets == nil {
		return errors.New("credential storage is unavailable")
	}
	out, changed, deleteAfter := s.loadLocked()
	if s.loadErr != nil {
		return s.loadErr
	}
	if changed {
		if err := s.saveLocked(out); err != nil {
			return fmt.Errorf("persist sanitized settings before credential update: %w", err)
		}
		if deleteAfter != "" {
			if err := s.secrets.Delete(deleteAfter); err != nil {
				log.Printf("settings: could not delete migrated legacy LLM credential %q: %v", deleteAfter, err)
			}
		}
	}
	ref, err := llmCredentialRef(out.LLM)
	if err != nil {
		return err
	}
	if value == "" {
		if err := secretStoreHealth(s.secrets); err != nil {
			return fmt.Errorf("credential storage is unavailable; the LLM credential remains attached: %w", err)
		}
		return s.deleteLLMAPIKeyLocked(out)
	}
	priorRef := strings.TrimSpace(out.LLM.APIKeyRef)
	var oldValue string
	var hadOld bool
	replacedLegacy := priorRef == legacyLLMAPIKeyRef
	if replacedLegacy {
		replacer, ok := s.secrets.(replacingSecretStore)
		if !ok {
			return errors.New("credential storage cannot atomically replace the quarantined legacy LLM credential")
		}
		if err := replacer.Replace(priorRef, ref, value); err != nil {
			return fmt.Errorf("replace quarantined legacy LLM credential: %w", err)
		}
	} else {
		oldValue, hadOld, err = secretStoreGetChecked(s.secrets, ref)
		if err != nil {
			return fmt.Errorf("credential storage is unavailable; no LLM credential was changed: %w", err)
		}
		if err := s.secrets.Set(ref, value); err != nil {
			return err
		}
	}
	out.LLM.APIKeyRef = ref
	if err := s.saveLocked(out); err != nil {
		// Best-effort compensation keeps a settings-write failure from silently
		// replacing the credential used by the still-persisted configuration. If
		// the new settings bytes were already published, compensation would instead
		// create a dangling reference, so keep both stores aligned and report the
		// durability error.
		// Atomic legacy replacement intentionally remains forward-committed. Its
		// old ref is already gone, and loadLocked can idempotently attach the exact
		// derived ref after a pre-publication settings failure. Reversing it here
		// could also destroy a pre-existing destination credential.
		if !replacedLegacy && !persistfile.IsPublished(err) {
			if hadOld {
				_ = s.secrets.Set(ref, oldValue)
			} else {
				_ = s.secrets.Delete(ref)
			}
		}
		return err
	}
	return nil
}

func (s *Service) deleteLLMAPIKeyLocked(out Settings) error {
	ref := strings.TrimSpace(out.LLM.APIKeyRef)
	if expected, err := llmCredentialRef(out.LLM); err != nil || (ref != expected && ref != legacyLLMAPIKeyRef) {
		// Never let caller-controlled/foreign references influence deletion. The
		// exact quarantined legacy ref is deliberately allowed so users can free
		// its slot before re-entering a credential for the current origin.
		ref = ""
	}
	out.LLM.APIKeyRef = ""
	if err := s.saveLocked(out); err != nil {
		return err
	}
	if ref != "" && isLLMAPIKeyRef(ref) {
		return s.secrets.Delete(ref)
	}
	return nil
}

// DeleteLLMAPIKey detaches then removes only the current provider-owned key.
func (s *Service) DeleteLLMAPIKey() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, changed, deleteAfter := s.loadLocked()
	if s.loadErr != nil {
		return s.loadErr
	}
	if s.secrets == nil {
		return errors.New("credential storage is unavailable")
	}
	if err := secretStoreHealth(s.secrets); err != nil {
		return fmt.Errorf("credential storage is unavailable; the LLM credential remains attached: %w", err)
	}
	if changed {
		if err := s.saveLocked(out); err != nil {
			return fmt.Errorf("persist sanitized settings before credential removal: %w", err)
		}
		if deleteAfter != "" {
			if err := s.secrets.Delete(deleteAfter); err != nil {
				log.Printf("settings: could not delete migrated legacy LLM credential %q: %v", deleteAfter, err)
			}
		}
	}
	return s.deleteLLMAPIKeyLocked(out)
}

func (s *Service) llmAPIKeyStatusLocked() LLMAPIKeyStatus {
	out, _, _ := s.loadLocked()
	if s.loadErr != nil {
		return LLMAPIKeyStatus{State: "unavailable", Message: s.loadErr.Error()}
	}
	ref := strings.TrimSpace(out.LLM.APIKeyRef)
	if ref == "" {
		return LLMAPIKeyStatus{State: "missing"}
	}
	if s.secrets == nil {
		return LLMAPIKeyStatus{State: "unavailable", Message: "Credential storage is unavailable."}
	}
	if ref == legacyLLMAPIKeyRef {
		_, found, err := secretStoreGetChecked(s.secrets, ref)
		if err != nil {
			return LLMAPIKeyStatus{State: "unavailable", Message: fmt.Sprintf("Credential storage is unavailable: %v", err)}
		}
		if found {
			return LLMAPIKeyStatus{
				State:   "quarantined",
				Message: "A legacy API key is quarantined and cannot be used. Re-enter it for this provider origin.",
			}
		}
		return LLMAPIKeyStatus{State: "missing"}
	}
	expected, err := llmCredentialRef(out.LLM)
	if err != nil || ref != expected {
		return LLMAPIKeyStatus{State: "missing"}
	}
	_, found, readErr := secretStoreGetChecked(s.secrets, expected)
	if readErr != nil {
		return LLMAPIKeyStatus{State: "unavailable", Message: fmt.Sprintf("Credential storage is unavailable: %v", readErr)}
	}
	if !found {
		return LLMAPIKeyStatus{State: "missing"}
	}
	return LLMAPIKeyStatus{State: "verified"}
}

// GetLLMAPIKeyStatus reports whether the current credential is verified,
// quarantined legacy evidence, absent, or temporarily unverifiable.
func (s *Service) GetLLMAPIKeyStatus() LLMAPIKeyStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.llmAPIKeyStatusLocked()
}

// HasLLMAPIKey is retained for compatibility and reports true only for a
// backend-verified current origin-bound key.
func (s *Service) HasLLMAPIKey() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.llmAPIKeyStatusLocked().State == "verified"
}

// RememberWorkspace records path as the last-opened workspace and pushes it to
// the front of the recents list, returning the updated settings.
func (s *Service) RememberWorkspace(path string) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out, _, deleteAfter := s.loadLocked()
	if s.loadErr != nil {
		return out, s.loadErr
	}
	out.LastWorkspace = path
	recents := make([]string, 0, maxRecent)
	recents = append(recents, path)
	for _, r := range out.RecentWorkspaces {
		if r == path {
			continue
		}
		recents = append(recents, r)
		if len(recents) >= maxRecent {
			break
		}
	}
	out.RecentWorkspaces = recents
	if err := s.saveLocked(out); err != nil {
		return out, err
	}
	if deleteAfter != "" && s.secrets != nil {
		if err := s.secrets.Delete(deleteAfter); err != nil {
			log.Printf("settings: could not delete migrated legacy LLM credential %q: %v", deleteAfter, err)
		}
	}
	return out, nil
}
