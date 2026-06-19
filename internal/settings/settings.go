// Package settings persists user/application preferences as JSON under the OS
// config directory (e.g. %AppData%/Novera on Windows). Secrets are NOT stored
// here — credential material lives in the dedicated secrets store so it never
// lands in plaintext config (a defect in the previous generation).
package settings

import (
	"encoding/json"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxRecent = 12

// DefaultRequestTimeoutSec is the fallback per-request LLM timeout. It is
// generous on purpose: a local model's first call also pays model-load time,
// and agent completions are non-streaming (the whole answer must be generated
// before the request returns), so a large model on modest hardware can
// legitimately need many minutes.
const DefaultRequestTimeoutSec = 1800
const legacyDefaultRequestTimeoutSec = 600

// Editor holds editor-pane preferences mirrored into Monaco on the frontend.
type Editor struct {
	FontSize     int    `json:"fontSize"`
	TabSize      int    `json:"tabSize"`
	WordWrap     bool   `json:"wordWrap"`
	Minimap      bool   `json:"minimap"`
	Theme        string `json:"theme"` // monaco theme id
	FormatOnSave bool   `json:"formatOnSave"`
}

// LLM holds non-secret provider configuration. The API key itself is NEVER
// stored here — only a reference (APIKeyRef) into the dedicated secret store.
type LLM struct {
	Provider  string `json:"provider"` // "ollama" | "openai" | "custom"
	BaseURL   string `json:"baseURL"`
	Model     string `json:"model"`
	APIKeyRef string `json:"apiKeyRef"`
	// RequestTimeoutSec bounds a single LLM request (one agent completion, or
	// the whole Ask-mode stream). User-tunable because local-model latency
	// varies wildly with hardware. 0 means "use the default" — see RequestTimeout.
	RequestTimeoutSec int `json:"requestTimeoutSec"`
}

// RequestTimeout resolves the per-request LLM timeout: the configured value, the
// default when unset, and a 30s floor so a too-small value can't make every
// request fail before the model has a chance to answer.
func (l LLM) RequestTimeout() time.Duration {
	sec := l.RequestTimeoutSec
	if sec <= 0 {
		sec = DefaultRequestTimeoutSec
	}
	if sec < 30 {
		sec = 30
	}
	return time.Duration(sec) * time.Second
}

// Settings is the full persisted preference set.
type Settings struct {
	Theme            string   `json:"theme"` // app theme: "dark" | "light"
	LastWorkspace    string   `json:"lastWorkspace"`
	RecentWorkspaces []string `json:"recentWorkspaces"`
	UIFontSize       int      `json:"uiFontSize"` // base font size for the app chrome (px)
	Editor           Editor   `json:"editor"`
	LLM              LLM      `json:"llm"`
}

func defaults() Settings {
	return Settings{
		Theme:            "dark",
		RecentWorkspaces: []string{},
		UIFontSize:       13,
		Editor: Editor{
			FontSize: 13,
			TabSize:  4,
			WordWrap: false,
			Minimap:  true,
			Theme:    "novera-dark",
		},
		LLM: LLM{
			Provider:          "ollama",
			BaseURL:           "http://localhost:11434/v1",
			Model:             "",
			RequestTimeoutSec: DefaultRequestTimeoutSec,
		},
	}
}

// Service is the bound Wails service for settings.
type Service struct {
	mu   sync.Mutex
	path string
}

// New constructs the settings service, resolving the on-disk config path.
func New() *Service {
	return &Service{path: filepath.Join(configDir(), "Novera", "settings.json")}
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

// Load returns the persisted settings, falling back to defaults for any missing
// fields or if no file exists yet.
func (s *Service) Load() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := defaults()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil {
		// Preserve the corrupt file instead of discarding + overwriting it.
		_ = os.Rename(s.path, s.path+".corrupt")
		return defaults()
	}
	if out.RecentWorkspaces == nil {
		out.RecentWorkspaces = []string{}
	}
	// Existing installs may have the previous default persisted explicitly. Move
	// that old default forward so slow local models benefit without asking users
	// to hand-edit settings.
	if out.LLM.RequestTimeoutSec == legacyDefaultRequestTimeoutSec {
		out.LLM.RequestTimeoutSec = DefaultRequestTimeoutSec
	}
	return out
}

// Save persists the full settings object atomically.
func (s *Service) Save(in Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked(in)
}

func (s *Service) saveLocked(in Settings) error {
	if in.RecentWorkspaces == nil {
		in.RecentWorkspaces = []string{}
	}
	// Never persist credentials embedded in the LLM base URL.
	in.LLM.BaseURL = sanitizeBaseURL(in.LLM.BaseURL)
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// RememberWorkspace records path as the last-opened workspace and pushes it to
// the front of the recents list, returning the updated settings.
func (s *Service) RememberWorkspace(path string) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := defaults()
	if data, err := os.ReadFile(s.path); err == nil {
		if uerr := json.Unmarshal(data, &out); uerr != nil {
			// Don't clobber a corrupt file by saving defaults over it.
			_ = os.Rename(s.path, s.path+".corrupt")
			out = defaults()
		}
	}
	if out.LLM.RequestTimeoutSec == legacyDefaultRequestTimeoutSec {
		out.LLM.RequestTimeoutSec = DefaultRequestTimeoutSec
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
	return out, nil
}
