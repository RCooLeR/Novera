package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/units"
)

const (
	keyCacheMaxBytes         = "settings.cache_max_bytes"
	keySmallAutoLoadBytes    = "settings.small_auto_load_bytes"
	keyEditableWindowBytes   = "settings.editable_window_bytes"
	keySearchChunkSize       = "settings.search_chunk_size"
	keyReplaceChunkSize      = "settings.replace_chunk_size"
	keyRegexMatchWindow      = "settings.regex_match_window"
	keyPersistIndexCache     = "settings.persist_index_cache"
	keyDeletePartialOnCancel = "settings.delete_partial_on_cancel"
	keySwapOriginalByDefault = "settings.swap_original_by_default"
	keyMaxVisualLineBytes    = "settings.max_visual_line_bytes"
	keyEditorFontSize        = "settings.editor_font_size"
	keyShowLineNumbers       = "settings.show_line_numbers"
	keyShowByteOffsets       = "settings.show_byte_offsets"
	keyShowWhitespace        = "settings.show_whitespace"
	keyWrapLines             = "settings.wrap_lines"
	keyHighlightCurrentLine  = "settings.highlight_current_line"
	keyEditorSyntax          = "settings.editor_syntax"
	keyEditorDecorations     = "settings.editor_decorations"
	keyEditorSmartTyping     = "settings.editor_smart_typing"
	keyEditorAutoIndent      = "settings.editor_auto_indent"
	keyEditorAutoPairs       = "settings.editor_auto_pairs"
	keyEditorSymbols         = "settings.editor_symbols"
	keyEditorCompletions     = "settings.editor_completions"
	keyEditorCompletionWords = "settings.editor_completion_words"
	keyShowToolPanels        = "settings.show_tool_panels"
	keyShowProjectSidebar    = "settings.show_project_sidebar"
	keyShowBottomPanel       = "settings.show_bottom_panel"
	keyShowWorkspaceSummary  = "settings.show_workspace_summary"
	keyShowStatusBar         = "settings.show_status_bar"
	keyShowOverviewRuler     = "settings.show_overview_ruler"
	keyActiveBottomPanel     = "settings.active_bottom_panel"
	keyActiveControlTab      = "settings.active_control_tab"
	keyThemeMode             = "settings.theme_mode"
)

const (
	ThemeModeAuto  = "auto"
	ThemeModeDark  = "dark"
	ThemeModeLight = "light"
)

const (
	DockedTaskPanelsSupported    = false
	DockedSupportPanelsSupported = false
)

const (
	ConfigDirEnv     = "QUARRY_HOME"
	ConfigDirName    = ".quarry"
	SettingsFileName = "settings.json"
	MaxRecentFiles   = 12
)

type Store interface {
	StringWithFallback(key string, fallback string) string
	BoolWithFallback(key string, fallback bool) bool
	SetString(key string, value string)
	SetBool(key string, value bool)
}

type AppSettings struct {
	CacheMaxBytes         string   `json:"cacheMaxBytes"`
	SmallAutoLoadBytes    string   `json:"smallAutoLoadBytes"`
	EditableWindowBytes   string   `json:"editableWindowBytes"`
	SearchChunkSize       string   `json:"searchChunkSize"`
	ReplaceChunkSize      string   `json:"replaceChunkSize"`
	RegexMatchWindow      string   `json:"regexMatchWindow"`
	PersistIndexCache     bool     `json:"persistIndexCache"`
	DeletePartialOnCancel bool     `json:"deletePartialOnCancel"`
	SwapOriginalByDefault bool     `json:"swapOriginalByDefault"`
	MaxVisualLineBytes    string   `json:"maxVisualLineBytes"`
	EditorFontSize        string   `json:"editorFontSize"`
	ShowLineNumbers       bool     `json:"showLineNumbers"`
	ShowByteOffsets       bool     `json:"showByteOffsets"`
	ShowWhitespace        bool     `json:"showWhitespace"`
	WrapLines             bool     `json:"wrapLines"`
	HighlightCurrentLine  bool     `json:"highlightCurrentLine"`
	EditorSyntax          bool     `json:"editorSyntax"`
	EditorDecorations     bool     `json:"editorDecorations"`
	EditorSmartTyping     bool     `json:"editorSmartTyping"`
	EditorAutoIndent      bool     `json:"editorAutoIndent"`
	EditorAutoPairs       bool     `json:"editorAutoPairs"`
	EditorSymbols         bool     `json:"editorSymbols"`
	EditorCompletions     bool     `json:"editorCompletions"`
	EditorCompletionWords bool     `json:"editorCompletionWords"`
	ShowToolPanels        bool     `json:"showToolPanels"`
	ShowProjectSidebar    bool     `json:"showProjectSidebar"`
	ShowBottomPanel       bool     `json:"showBottomPanel"`
	ShowWorkspaceSummary  bool     `json:"showWorkspaceSummary"`
	ShowStatusBar         bool     `json:"showStatusBar"`
	ShowOverviewRuler     bool     `json:"showOverviewRuler"`
	ActiveBottomPanel     string   `json:"activeBottomPanel"`
	ActiveControlTab      string   `json:"activeControlTab"`
	ThemeMode             string   `json:"themeMode"`
	RecentFiles           []string `json:"recentFiles"`
}

func Defaults() AppSettings {
	return AppSettings{
		CacheMaxBytes:         "8 MiB",
		SmallAutoLoadBytes:    "8 MiB",
		EditableWindowBytes:   "500 MiB",
		SearchChunkSize:       "4 MiB",
		ReplaceChunkSize:      "16 MiB",
		RegexMatchWindow:      "1 MiB",
		PersistIndexCache:     true,
		DeletePartialOnCancel: true,
		SwapOriginalByDefault: false,
		MaxVisualLineBytes:    "4 KiB",
		EditorFontSize:        "13",
		ShowLineNumbers:       true,
		ShowByteOffsets:       true,
		ShowWhitespace:        false,
		WrapLines:             false,
		HighlightCurrentLine:  true,
		EditorSyntax:          true,
		EditorDecorations:     true,
		EditorSmartTyping:     true,
		EditorAutoIndent:      true,
		EditorAutoPairs:       true,
		EditorSymbols:         true,
		EditorCompletions:     true,
		EditorCompletionWords: true,
		ShowToolPanels:        false,
		ShowProjectSidebar:    true,
		ShowBottomPanel:       false,
		ShowWorkspaceSummary:  true,
		ShowStatusBar:         true,
		ShowOverviewRuler:     true,
		ActiveBottomPanel:     "Results",
		ActiveControlTab:      "Search",
		ThemeMode:             ThemeModeDark,
	}
}

func Load(store Store) AppSettings {
	def := Defaults()
	if store == nil {
		return def
	}
	loaded := AppSettings{
		CacheMaxBytes:         store.StringWithFallback(keyCacheMaxBytes, def.CacheMaxBytes),
		SmallAutoLoadBytes:    store.StringWithFallback(keySmallAutoLoadBytes, def.SmallAutoLoadBytes),
		EditableWindowBytes:   store.StringWithFallback(keyEditableWindowBytes, def.EditableWindowBytes),
		SearchChunkSize:       store.StringWithFallback(keySearchChunkSize, def.SearchChunkSize),
		ReplaceChunkSize:      store.StringWithFallback(keyReplaceChunkSize, def.ReplaceChunkSize),
		RegexMatchWindow:      store.StringWithFallback(keyRegexMatchWindow, def.RegexMatchWindow),
		PersistIndexCache:     store.BoolWithFallback(keyPersistIndexCache, def.PersistIndexCache),
		DeletePartialOnCancel: store.BoolWithFallback(keyDeletePartialOnCancel, def.DeletePartialOnCancel),
		SwapOriginalByDefault: store.BoolWithFallback(keySwapOriginalByDefault, def.SwapOriginalByDefault),
		MaxVisualLineBytes:    store.StringWithFallback(keyMaxVisualLineBytes, def.MaxVisualLineBytes),
		EditorFontSize:        store.StringWithFallback(keyEditorFontSize, def.EditorFontSize),
		ShowLineNumbers:       store.BoolWithFallback(keyShowLineNumbers, def.ShowLineNumbers),
		ShowByteOffsets:       store.BoolWithFallback(keyShowByteOffsets, def.ShowByteOffsets),
		ShowWhitespace:        store.BoolWithFallback(keyShowWhitespace, def.ShowWhitespace),
		WrapLines:             store.BoolWithFallback(keyWrapLines, def.WrapLines),
		HighlightCurrentLine:  store.BoolWithFallback(keyHighlightCurrentLine, def.HighlightCurrentLine),
		EditorSyntax:          store.BoolWithFallback(keyEditorSyntax, def.EditorSyntax),
		EditorDecorations:     store.BoolWithFallback(keyEditorDecorations, def.EditorDecorations),
		EditorSmartTyping:     store.BoolWithFallback(keyEditorSmartTyping, def.EditorSmartTyping),
		EditorAutoIndent:      store.BoolWithFallback(keyEditorAutoIndent, def.EditorAutoIndent),
		EditorAutoPairs:       store.BoolWithFallback(keyEditorAutoPairs, def.EditorAutoPairs),
		EditorSymbols:         store.BoolWithFallback(keyEditorSymbols, def.EditorSymbols),
		EditorCompletions:     store.BoolWithFallback(keyEditorCompletions, def.EditorCompletions),
		EditorCompletionWords: store.BoolWithFallback(keyEditorCompletionWords, def.EditorCompletionWords),
		ShowToolPanels:        store.BoolWithFallback(keyShowToolPanels, def.ShowToolPanels),
		ShowProjectSidebar:    store.BoolWithFallback(keyShowProjectSidebar, def.ShowProjectSidebar),
		ShowBottomPanel:       store.BoolWithFallback(keyShowBottomPanel, def.ShowBottomPanel),
		ShowWorkspaceSummary:  store.BoolWithFallback(keyShowWorkspaceSummary, def.ShowWorkspaceSummary),
		ShowStatusBar:         store.BoolWithFallback(keyShowStatusBar, def.ShowStatusBar),
		ShowOverviewRuler:     store.BoolWithFallback(keyShowOverviewRuler, def.ShowOverviewRuler),
		ActiveBottomPanel:     store.StringWithFallback(keyActiveBottomPanel, def.ActiveBottomPanel),
		ActiveControlTab:      store.StringWithFallback(keyActiveControlTab, def.ActiveControlTab),
		ThemeMode:             store.StringWithFallback(keyThemeMode, def.ThemeMode),
	}
	return loaded.sanitized()
}

func TaskToolsWindowOnly() bool {
	return !DockedTaskPanelsSupported
}

func SupportPanelsWindowOnly() bool {
	return !DockedSupportPanelsSupported
}

func NormalizeShowToolPanels(value bool) bool {
	if TaskToolsWindowOnly() {
		return false
	}
	return value
}

func NormalizeShowBottomPanel(value bool) bool {
	if SupportPanelsWindowOnly() {
		return false
	}
	return value
}

func ConfigDir() (string, error) {
	if override := strings.TrimSpace(os.Getenv(ConfigDirEnv)); override != "" {
		return filepath.Clean(override), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("user home directory is empty")
	}
	return filepath.Join(home, ConfigDirName), nil
}

func SettingsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, SettingsFileName), nil
}

func LoadFile(path string) (AppSettings, error) {
	if strings.TrimSpace(path) == "" {
		return AppSettings{}, errors.New("settings path is empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return AppSettings{}, err
	}
	cfg := Defaults()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AppSettings{}, err
	}
	return cfg.sanitized(), nil
}

func LoadPersistent(store Store) (AppSettings, error) {
	// The JSON settings file is the source of truth when present. Fyne
	// preferences remain a legacy fallback for first-run and migration cases.
	path, err := SettingsPath()
	if err != nil {
		return Load(store), err
	}
	cfg, err := LoadFile(path)
	if err == nil {
		return cfg, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return Load(store), nil
	}
	return Load(store), err
}

func (s AppSettings) Save(store Store) error {
	if store == nil {
		return fmt.Errorf("nil settings store")
	}
	s = s.withSupportedWorkspaceFeatures()
	if err := s.Validate(); err != nil {
		return err
	}
	store.SetString(keyCacheMaxBytes, s.CacheMaxBytes)
	store.SetString(keySmallAutoLoadBytes, s.SmallAutoLoadBytes)
	store.SetString(keyEditableWindowBytes, s.EditableWindowBytes)
	store.SetString(keySearchChunkSize, s.SearchChunkSize)
	store.SetString(keyReplaceChunkSize, s.ReplaceChunkSize)
	store.SetString(keyRegexMatchWindow, s.RegexMatchWindow)
	store.SetBool(keyPersistIndexCache, s.PersistIndexCache)
	store.SetBool(keyDeletePartialOnCancel, s.DeletePartialOnCancel)
	store.SetBool(keySwapOriginalByDefault, s.SwapOriginalByDefault)
	store.SetString(keyMaxVisualLineBytes, s.MaxVisualLineBytes)
	store.SetString(keyEditorFontSize, s.EditorFontSize)
	store.SetBool(keyShowLineNumbers, s.ShowLineNumbers)
	store.SetBool(keyShowByteOffsets, s.ShowByteOffsets)
	store.SetBool(keyShowWhitespace, s.ShowWhitespace)
	store.SetBool(keyWrapLines, s.WrapLines)
	store.SetBool(keyHighlightCurrentLine, s.HighlightCurrentLine)
	store.SetBool(keyEditorSyntax, s.EditorSyntax)
	store.SetBool(keyEditorDecorations, s.EditorDecorations)
	store.SetBool(keyEditorSmartTyping, s.EditorSmartTyping)
	store.SetBool(keyEditorAutoIndent, s.EditorAutoIndent)
	store.SetBool(keyEditorAutoPairs, s.EditorAutoPairs)
	store.SetBool(keyEditorSymbols, s.EditorSymbols)
	store.SetBool(keyEditorCompletions, s.EditorCompletions)
	store.SetBool(keyEditorCompletionWords, s.EditorCompletionWords)
	store.SetBool(keyShowToolPanels, s.ShowToolPanels)
	store.SetBool(keyShowProjectSidebar, s.ShowProjectSidebar)
	store.SetBool(keyShowBottomPanel, s.ShowBottomPanel)
	store.SetBool(keyShowWorkspaceSummary, s.ShowWorkspaceSummary)
	store.SetBool(keyShowStatusBar, s.ShowStatusBar)
	store.SetBool(keyShowOverviewRuler, s.ShowOverviewRuler)
	store.SetString(keyActiveBottomPanel, s.ActiveBottomPanel)
	store.SetString(keyActiveControlTab, s.ActiveControlTab)
	store.SetString(keyThemeMode, s.ThemeMode)
	return nil
}

func (s AppSettings) SaveFile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("settings path is empty")
	}
	s = s.withSupportedWorkspaceFeatures()
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	_, err = fileio.WriteFileAtomic(path, data, fileio.AtomicWriteOptions{
		Mode:       0o600,
		Overwrite:  true,
		TempSuffix: ".settings.tmp",
	})
	return err
}

func (s AppSettings) SavePersistent(store Store) error {
	s = s.withSupportedWorkspaceFeatures()
	if err := s.Validate(); err != nil {
		return err
	}
	path, err := SettingsPath()
	if err != nil {
		return err
	}
	if err := s.SaveFile(path); err != nil {
		return err
	}
	if store != nil {
		if err := s.Save(store); err != nil {
			return err
		}
	}
	return nil
}

func (s AppSettings) Validate() error {
	if _, err := units.ParseByteSize(s.CacheMaxBytes); err != nil {
		return fmt.Errorf("invalid cache memory setting: %w", err)
	}
	if _, err := units.ParseByteSize(s.SmallAutoLoadBytes); err != nil {
		return fmt.Errorf("invalid small auto-load size: %w", err)
	}
	if _, err := units.ParseByteSize(s.EditableWindowBytes); err != nil {
		return fmt.Errorf("invalid editable window size: %w", err)
	}
	searchBytes, err := units.ParseByteSize(s.SearchChunkSize)
	if err != nil {
		return fmt.Errorf("invalid search chunk size: %w", err)
	}
	replaceBytes, err := units.ParseByteSize(s.ReplaceChunkSize)
	if err != nil {
		return fmt.Errorf("invalid replace chunk size: %w", err)
	}
	regexBytes, err := units.ParseByteSize(s.RegexMatchWindow)
	if err != nil {
		return fmt.Errorf("invalid regex match window: %w", err)
	}
	if regexBytes > replaceBytes {
		return fmt.Errorf("regex match window must be less than or equal to replace chunk size")
	}
	if regexBytes > searchBytes {
		return fmt.Errorf("regex match window must be less than or equal to search chunk size")
	}
	if _, err := units.ParseByteSize(s.MaxVisualLineBytes); err != nil {
		return fmt.Errorf("invalid max visual line size: %w", err)
	}
	fontSize, err := strconv.Atoi(s.EditorFontSize)
	if err != nil || fontSize <= 0 {
		return fmt.Errorf("invalid editor font size")
	}
	switch s.ActiveBottomPanel {
	case "", "Results", "Bookmarks", "SQL", "Activity":
	default:
		return fmt.Errorf("invalid active bottom panel")
	}
	switch s.ActiveControlTab {
	case "", "Navigate", "Search", "Transform", "SQL / Tools":
	default:
		return fmt.Errorf("invalid active control tab")
	}
	switch s.ThemeMode {
	case "", ThemeModeAuto, ThemeModeDark, ThemeModeLight:
	default:
		return fmt.Errorf("invalid theme mode")
	}
	return nil
}

func (s AppSettings) WithRecentFile(path string) AppSettings {
	clean := strings.TrimSpace(path)
	if clean == "" {
		return s
	}
	if absPath, err := filepath.Abs(clean); err == nil {
		clean = absPath
	}
	clean = filepath.Clean(clean)

	next := s
	next.RecentFiles = prependRecentFile(clean, next.RecentFiles)
	return next
}

func (s AppSettings) WithoutRecentFiles() AppSettings {
	next := s
	next.RecentFiles = nil
	return next
}

func (s AppSettings) MustCacheMaxBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.CacheMaxBytes, def.CacheMaxBytes)
}

func (s AppSettings) MustEditableWindowBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.EditableWindowBytes, def.EditableWindowBytes)
}

func (s AppSettings) MustSmallAutoLoadBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.SmallAutoLoadBytes, def.SmallAutoLoadBytes)
}

func (s AppSettings) MustSearchChunkBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.SearchChunkSize, def.SearchChunkSize)
}

func (s AppSettings) MustReplaceChunkBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.ReplaceChunkSize, def.ReplaceChunkSize)
}

func (s AppSettings) MustRegexMatchWindowBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.RegexMatchWindow, def.RegexMatchWindow)
}

func (s AppSettings) MustMaxVisualLineBytes() int {
	def := Defaults()
	return parseSizeOrDefault(s.MaxVisualLineBytes, def.MaxVisualLineBytes)
}

func (s AppSettings) MustEditorFontSize() int {
	def := Defaults()
	size, err := strconv.Atoi(s.EditorFontSize)
	if err != nil || size <= 0 {
		fallback, fallbackErr := strconv.Atoi(def.EditorFontSize)
		if fallbackErr != nil || fallback <= 0 {
			return 13
		}
		return fallback
	}
	return size
}

func parseSizeOrDefault(input string, fallback string) int {
	size, err := units.ParseByteSize(input)
	if err != nil || size <= 0 {
		fallbackSize, fallbackErr := units.ParseByteSize(fallback)
		if fallbackErr != nil || fallbackSize <= 0 {
			return 1
		}
		return int(fallbackSize)
	}
	return int(size)
}

func (s AppSettings) sanitized() AppSettings {
	def := Defaults()
	clean := s
	clean = clean.withSupportedWorkspaceFeatures()
	clean.CacheMaxBytes = sanitizeByteSize(clean.CacheMaxBytes, def.CacheMaxBytes)
	clean.SmallAutoLoadBytes = sanitizeByteSize(clean.SmallAutoLoadBytes, def.SmallAutoLoadBytes)
	clean.EditableWindowBytes = sanitizeByteSize(clean.EditableWindowBytes, def.EditableWindowBytes)
	clean.SearchChunkSize = sanitizeByteSize(clean.SearchChunkSize, def.SearchChunkSize)
	clean.ReplaceChunkSize = sanitizeByteSize(clean.ReplaceChunkSize, def.ReplaceChunkSize)
	clean.RegexMatchWindow = sanitizeByteSize(clean.RegexMatchWindow, def.RegexMatchWindow)
	clean.MaxVisualLineBytes = sanitizeByteSize(clean.MaxVisualLineBytes, def.MaxVisualLineBytes)
	clean.EditorFontSize = sanitizePositiveInt(clean.EditorFontSize, def.EditorFontSize)
	if !isAllowedValue(clean.ActiveBottomPanel, "", "Results", "Bookmarks", "SQL", "Activity") {
		clean.ActiveBottomPanel = def.ActiveBottomPanel
	}
	if !isAllowedValue(clean.ActiveControlTab, "", "Navigate", "Search", "Transform", "SQL / Tools") {
		clean.ActiveControlTab = def.ActiveControlTab
	}
	if !isAllowedValue(clean.ThemeMode, "", ThemeModeAuto, ThemeModeDark, ThemeModeLight) {
		clean.ThemeMode = def.ThemeMode
	}
	clean.RecentFiles = sanitizeRecentFiles(clean.RecentFiles)

	searchBytes := clean.MustSearchChunkBytes()
	replaceBytes := clean.MustReplaceChunkBytes()
	regexBytes := clean.MustRegexMatchWindowBytes()
	if regexBytes > searchBytes || regexBytes > replaceBytes {
		clean.RegexMatchWindow = def.RegexMatchWindow
	}

	return clean
}

func (s AppSettings) withSupportedWorkspaceFeatures() AppSettings {
	s.ShowToolPanels = NormalizeShowToolPanels(s.ShowToolPanels)
	s.ShowBottomPanel = NormalizeShowBottomPanel(s.ShowBottomPanel)
	return s
}

func sanitizeRecentFiles(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if absPath, err := filepath.Abs(path); err == nil {
			path = absPath
		}
		path = filepath.Clean(path)
		if containsPath(out, path) {
			continue
		}
		out = append(out, path)
		if len(out) >= MaxRecentFiles {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func prependRecentFile(path string, existing []string) []string {
	out := make([]string, 0, MaxRecentFiles)
	out = append(out, path)
	for _, candidate := range sanitizeRecentFiles(existing) {
		if containsPath(out, candidate) {
			continue
		}
		out = append(out, candidate)
		if len(out) >= MaxRecentFiles {
			break
		}
	}
	return out
}

func containsPath(paths []string, candidate string) bool {
	for _, path := range paths {
		if strings.EqualFold(filepath.Clean(path), filepath.Clean(candidate)) {
			return true
		}
	}
	return false
}

func sanitizeByteSize(value string, fallback string) string {
	if size, err := units.ParseByteSize(value); err == nil && size > 0 {
		return value
	}
	return fallback
}

func sanitizePositiveInt(value string, fallback string) string {
	size, err := strconv.Atoi(value)
	if err == nil && size > 0 {
		return value
	}
	return fallback
}

func isAllowedValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
