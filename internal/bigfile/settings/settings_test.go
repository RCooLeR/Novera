package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type memStore struct {
	strings map[string]string
	bools   map[string]bool
}

func newMemStore() *memStore {
	return &memStore{
		strings: make(map[string]string),
		bools:   make(map[string]bool),
	}
}

func (m *memStore) StringWithFallback(key string, fallback string) string {
	if value, ok := m.strings[key]; ok {
		return value
	}
	return fallback
}

func (m *memStore) BoolWithFallback(key string, fallback bool) bool {
	if value, ok := m.bools[key]; ok {
		return value
	}
	return fallback
}

func (m *memStore) SetString(key string, value string) {
	m.strings[key] = value
}

func (m *memStore) SetBool(key string, value bool) {
	m.bools[key] = value
}

func TestDefaultsValidate(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("defaults validate: %v", err)
	}
}

func TestLoadAndSaveRoundTrip(t *testing.T) {
	store := newMemStore()
	want := AppSettings{
		CacheMaxBytes:         "32 MiB",
		SmallAutoLoadBytes:    "4 MiB",
		EditableWindowBytes:   "256 MiB",
		SearchChunkSize:       "8 MiB",
		ReplaceChunkSize:      "32 MiB",
		RegexMatchWindow:      "4 MiB",
		PersistIndexCache:     false,
		DeletePartialOnCancel: false,
		SwapOriginalByDefault: true,
		MaxVisualLineBytes:    "8 KiB",
		EditorFontSize:        "15",
		ShowLineNumbers:       false,
		ShowByteOffsets:       false,
		ShowWhitespace:        true,
		WrapLines:             true,
		HighlightCurrentLine:  false,
		EditorSyntax:          false,
		EditorDecorations:     false,
		EditorSmartTyping:     false,
		EditorAutoIndent:      false,
		EditorAutoPairs:       false,
		EditorSymbols:         false,
		EditorCompletions:     false,
		EditorCompletionWords: false,
		ShowToolPanels:        false,
		ShowProjectSidebar:    false,
		ShowBottomPanel:       false,
		ShowWorkspaceSummary:  false,
		ShowStatusBar:         false,
		ShowOverviewRuler:     false,
		ActiveBottomPanel:     "SQL",
		ActiveControlTab:      "Transform",
		ThemeMode:             ThemeModeLight,
	}
	if err := want.Save(store); err != nil {
		t.Fatal(err)
	}
	got := Load(store)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded settings = %#v, want %#v", got, want)
	}
}

func TestDefaultsEnableEditorFeatures(t *testing.T) {
	cfg := Defaults()
	if !cfg.EditorSyntax || !cfg.EditorDecorations || !cfg.EditorSmartTyping || !cfg.EditorAutoIndent || !cfg.EditorAutoPairs || !cfg.EditorSymbols || !cfg.EditorCompletions || !cfg.EditorCompletionWords {
		t.Fatalf("default editor features should be enabled: %#v", cfg)
	}
}

func TestValidateRejectsRegexWindowLargerThanChunk(t *testing.T) {
	cfg := Defaults()
	cfg.SearchChunkSize = "1 MiB"
	cfg.ReplaceChunkSize = "2 MiB"
	cfg.RegexMatchWindow = "3 MiB"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestValidateRejectsInvalidDisplaySettings(t *testing.T) {
	cfg := Defaults()
	cfg.MaxVisualLineBytes = "nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected max visual line validation error")
	}

	cfg = Defaults()
	cfg.EditorFontSize = "0"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected font size validation error")
	}
}

func TestValidateRejectsInvalidActiveBottomPanel(t *testing.T) {
	cfg := Defaults()
	cfg.ActiveBottomPanel = "Nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected active bottom panel validation error")
	}
}

func TestValidateRejectsInvalidActiveControlTab(t *testing.T) {
	cfg := Defaults()
	cfg.ActiveControlTab = "Nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected active control tab validation error")
	}
}

func TestValidateRejectsInvalidThemeMode(t *testing.T) {
	cfg := Defaults()
	cfg.ThemeMode = "sepia"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected theme mode validation error")
	}
}

func TestLoadSanitizesInvalidPersistedValues(t *testing.T) {
	store := newMemStore()
	store.SetString(keyCacheMaxBytes, "nope")
	store.SetString(keySmallAutoLoadBytes, "bad")
	store.SetString(keyEditableWindowBytes, "bad")
	store.SetString(keySearchChunkSize, "1 MiB")
	store.SetString(keyReplaceChunkSize, "2 MiB")
	store.SetString(keyRegexMatchWindow, "3 MiB")
	store.SetString(keyMaxVisualLineBytes, "bad")
	store.SetString(keyEditorFontSize, "0")
	store.SetString(keyActiveBottomPanel, "Nope")
	store.SetString(keyActiveControlTab, "Also nope")
	store.SetString(keyThemeMode, "sepia")

	got := Load(store)
	def := Defaults()
	if got.CacheMaxBytes != def.CacheMaxBytes {
		t.Fatalf("cache max bytes = %q, want %q", got.CacheMaxBytes, def.CacheMaxBytes)
	}
	if got.SmallAutoLoadBytes != def.SmallAutoLoadBytes {
		t.Fatalf("small auto-load bytes = %q, want %q", got.SmallAutoLoadBytes, def.SmallAutoLoadBytes)
	}
	if got.EditableWindowBytes != def.EditableWindowBytes {
		t.Fatalf("editable window bytes = %q, want %q", got.EditableWindowBytes, def.EditableWindowBytes)
	}
	if got.RegexMatchWindow != def.RegexMatchWindow {
		t.Fatalf("regex window = %q, want %q", got.RegexMatchWindow, def.RegexMatchWindow)
	}
	if got.MaxVisualLineBytes != def.MaxVisualLineBytes {
		t.Fatalf("max visual line bytes = %q, want %q", got.MaxVisualLineBytes, def.MaxVisualLineBytes)
	}
	if got.EditorFontSize != def.EditorFontSize {
		t.Fatalf("editor font size = %q, want %q", got.EditorFontSize, def.EditorFontSize)
	}
	if got.ActiveBottomPanel != def.ActiveBottomPanel {
		t.Fatalf("active bottom panel = %q, want %q", got.ActiveBottomPanel, def.ActiveBottomPanel)
	}
	if got.ActiveControlTab != def.ActiveControlTab {
		t.Fatalf("active control tab = %q, want %q", got.ActiveControlTab, def.ActiveControlTab)
	}
	if got.ThemeMode != def.ThemeMode {
		t.Fatalf("theme mode = %q, want %q", got.ThemeMode, def.ThemeMode)
	}
	if got.SearchChunkSize != "1 MiB" {
		t.Fatalf("search chunk size = %q, want valid persisted value", got.SearchChunkSize)
	}
	if got.ReplaceChunkSize != "2 MiB" {
		t.Fatalf("replace chunk size = %q, want valid persisted value", got.ReplaceChunkSize)
	}
}

func TestMustAccessorsFallbackToDefaults(t *testing.T) {
	cfg := AppSettings{
		CacheMaxBytes:       "nope",
		SmallAutoLoadBytes:  "bad",
		EditableWindowBytes: "invalid",
		SearchChunkSize:     "bad",
		ReplaceChunkSize:    "broken",
		RegexMatchWindow:    "invalid",
		MaxVisualLineBytes:  "NaN",
		EditorFontSize:      "0",
	}
	def := Defaults()
	if got := cfg.MustCacheMaxBytes(); got != def.MustCacheMaxBytes() {
		t.Fatalf("cache bytes = %d, want %d", got, def.MustCacheMaxBytes())
	}
	if got := cfg.MustSmallAutoLoadBytes(); got != def.MustSmallAutoLoadBytes() {
		t.Fatalf("small auto-load bytes = %d, want %d", got, def.MustSmallAutoLoadBytes())
	}
	if got := cfg.MustEditableWindowBytes(); got != def.MustEditableWindowBytes() {
		t.Fatalf("editable window bytes = %d, want %d", got, def.MustEditableWindowBytes())
	}
	if got := cfg.MustSearchChunkBytes(); got != def.MustSearchChunkBytes() {
		t.Fatalf("search bytes = %d, want %d", got, def.MustSearchChunkBytes())
	}
	if got := cfg.MustReplaceChunkBytes(); got != def.MustReplaceChunkBytes() {
		t.Fatalf("replace bytes = %d, want %d", got, def.MustReplaceChunkBytes())
	}
	if got := cfg.MustRegexMatchWindowBytes(); got != def.MustRegexMatchWindowBytes() {
		t.Fatalf("regex bytes = %d, want %d", got, def.MustRegexMatchWindowBytes())
	}
	if got := cfg.MustMaxVisualLineBytes(); got != def.MustMaxVisualLineBytes() {
		t.Fatalf("max visual line bytes = %d, want %d", got, def.MustMaxVisualLineBytes())
	}
	if got := cfg.MustEditorFontSize(); got != def.MustEditorFontSize() {
		t.Fatalf("editor font size = %d, want %d", got, def.MustEditorFontSize())
	}
}

func TestDefaultsUseLargeEditableWindow(t *testing.T) {
	if got, want := Defaults().MustEditableWindowBytes(), 500*1024*1024; got != want {
		t.Fatalf("editable window bytes = %d, want %d", got, want)
	}
}

func TestDefaultsUseSmallAutoLoadThreshold(t *testing.T) {
	if got, want := Defaults().MustSmallAutoLoadBytes(), 8*1024*1024; got != want {
		t.Fatalf("small auto-load bytes = %d, want %d", got, want)
	}
}

func TestDefaultsShowProjectSidebar(t *testing.T) {
	if !Defaults().ShowProjectSidebar {
		t.Fatal("expected project sidebar to be shown by default")
	}
}

func TestDefaultsHideDockedToolPanels(t *testing.T) {
	if Defaults().ShowToolPanels {
		t.Fatal("expected docked tool panels to be hidden by default")
	}
	if Defaults().ShowBottomPanel {
		t.Fatal("expected docked support panels to be hidden by default")
	}
}

func TestTaskToolsAreWindowOnly(t *testing.T) {
	if !TaskToolsWindowOnly() {
		t.Fatal("expected task tools to be window-only")
	}
	if NormalizeShowToolPanels(true) {
		t.Fatal("expected docked task-panel preference to normalize off")
	}
}

func TestSupportPanelsAreWindowOnly(t *testing.T) {
	if !SupportPanelsWindowOnly() {
		t.Fatal("expected support panels to be window-only")
	}
	if NormalizeShowBottomPanel(true) {
		t.Fatal("expected docked support-panel preference to normalize off")
	}
}

func TestLoadNormalizesLegacyDockedToolPanelPreference(t *testing.T) {
	store := newMemStore()
	store.SetBool(keyShowToolPanels, true)

	got := Load(store)
	if got.ShowToolPanels {
		t.Fatal("legacy docked task-panel preference should load as disabled")
	}
}

func TestLoadNormalizesLegacyDockedSupportPanelPreference(t *testing.T) {
	store := newMemStore()
	store.SetBool(keyShowBottomPanel, true)

	got := Load(store)
	if got.ShowBottomPanel {
		t.Fatal("legacy docked support-panel preference should load as disabled")
	}
}

func TestSaveNormalizesLegacyDockedToolPanelPreference(t *testing.T) {
	store := newMemStore()
	cfg := Defaults()
	cfg.ShowToolPanels = true

	if err := cfg.Save(store); err != nil {
		t.Fatal(err)
	}
	if got := store.BoolWithFallback(keyShowToolPanels, true); got {
		t.Fatal("saved docked task-panel preference should be disabled")
	}
}

func TestSaveNormalizesLegacyDockedSupportPanelPreference(t *testing.T) {
	store := newMemStore()
	cfg := Defaults()
	cfg.ShowBottomPanel = true

	if err := cfg.Save(store); err != nil {
		t.Fatal(err)
	}
	if got := store.BoolWithFallback(keyShowBottomPanel, true); got {
		t.Fatal("saved docked support-panel preference should be disabled")
	}
}

func TestSaveFileNormalizesLegacyDockedToolPanelPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	cfg := Defaults()
	cfg.ShowToolPanels = true

	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ShowToolPanels {
		t.Fatal("settings file should persist docked task panels as disabled")
	}
}

func TestSaveFileNormalizesLegacyDockedSupportPanelPreference(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	cfg := Defaults()
	cfg.ShowBottomPanel = true

	if err := cfg.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.ShowBottomPanel {
		t.Fatal("settings file should persist docked support panels as disabled")
	}
}

func TestDefaultsUseDarkIDETheme(t *testing.T) {
	if got := Defaults().ThemeMode; got != ThemeModeDark {
		t.Fatalf("theme mode = %q, want %q", got, ThemeModeDark)
	}
}

func TestValidateRejectsInvalidEditableWindow(t *testing.T) {
	cfg := Defaults()
	cfg.EditableWindowBytes = "nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected editable window validation error")
	}
}

func TestValidateRejectsInvalidSmallAutoLoad(t *testing.T) {
	cfg := Defaults()
	cfg.SmallAutoLoadBytes = "nope"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected small auto-load validation error")
	}
}

func TestSettingsPathUsesQuarryHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigDirEnv, dir)
	got, err := SettingsPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, SettingsFileName); got != want {
		t.Fatalf("settings path = %q, want %q", got, want)
	}
}

func TestSaveAndLoadFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", SettingsFileName)
	want := Defaults()
	want.EditableWindowBytes = "1 GiB"
	want.ThemeMode = ThemeModeDark

	if err := want.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded file settings = %#v, want %#v", got, want)
	}
}

func TestSaveFileAtomicallyOverwritesExistingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	if err := os.WriteFile(path, []byte(`{"editableWindowBytes":"64 MiB"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := Defaults()
	want.EditableWindowBytes = "1 GiB"
	want.ThemeMode = ThemeModeLight

	if err := want.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EditableWindowBytes != "1 GiB" || got.ThemeMode != ThemeModeLight {
		t.Fatalf("loaded settings = %#v, want overwritten file settings", got)
	}
	if _, err := os.Stat(path + ".settings.tmp"); !os.IsNotExist(err) {
		t.Fatalf("temp settings file remained: %v", err)
	}
	if _, err := os.Stat(path + ".quarry.overwrite.bak"); !os.IsNotExist(err) {
		t.Fatalf("overwrite backup remained: %v", err)
	}
}

func TestLoadFileDefaultsMissingEditorFeatureFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"editableWindowBytes":"750 MiB"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("load settings file: %v", err)
	}
	if got.EditableWindowBytes != "750 MiB" {
		t.Fatalf("editable window bytes = %q, want persisted value", got.EditableWindowBytes)
	}
	if !got.EditorSyntax || !got.EditorDecorations || !got.EditorSmartTyping || !got.EditorAutoIndent || !got.EditorAutoPairs || !got.EditorSymbols || !got.EditorCompletions || !got.EditorCompletionWords {
		t.Fatalf("missing editor feature fields should default on: %#v", got)
	}
}

func TestLoadPersistentFallsBackToStoreWhenFileMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigDirEnv, dir)
	store := newMemStore()
	store.SetString(keySmallAutoLoadBytes, "4 MiB")
	store.SetString(keyEditableWindowBytes, "128 MiB")

	got, err := LoadPersistent(store)
	if err != nil {
		t.Fatal(err)
	}
	if got.EditableWindowBytes != "128 MiB" {
		t.Fatalf("editable window bytes = %q, want persisted store fallback", got.EditableWindowBytes)
	}
	if got.SmallAutoLoadBytes != "4 MiB" {
		t.Fatalf("small auto-load bytes = %q, want persisted store fallback", got.SmallAutoLoadBytes)
	}
}

func TestSavePersistentWritesConfigDirAndStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(ConfigDirEnv, dir)
	store := newMemStore()
	want := Defaults()
	want.SmallAutoLoadBytes = "16 MiB"
	want.EditableWindowBytes = "750 MiB"

	if err := want.SavePersistent(store); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, SettingsFileName)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected settings file: %v", err)
	}
	if got := store.StringWithFallback(keyEditableWindowBytes, ""); got != "750 MiB" {
		t.Fatalf("store editable window bytes = %q, want saved value", got)
	}
	if got := store.StringWithFallback(keySmallAutoLoadBytes, ""); got != "16 MiB" {
		t.Fatalf("store small auto-load bytes = %q, want saved value", got)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EditableWindowBytes != "750 MiB" {
		t.Fatalf("file editable window bytes = %q, want saved value", got.EditableWindowBytes)
	}
	if got.SmallAutoLoadBytes != "16 MiB" {
		t.Fatalf("file small auto-load bytes = %q, want saved value", got.SmallAutoLoadBytes)
	}
}

func TestRecentFilesAreSanitizedAndCapped(t *testing.T) {
	cfg := Defaults()
	for i := 0; i < MaxRecentFiles+3; i++ {
		cfg.RecentFiles = append(cfg.RecentFiles, filepath.Join("C:\\data", "file"+string(rune('a'+i))+".sql"))
	}
	cfg.RecentFiles = append(cfg.RecentFiles, "", cfg.RecentFiles[0])

	got := cfg.sanitized()
	if len(got.RecentFiles) != MaxRecentFiles {
		t.Fatalf("recent files len = %d, want %d", len(got.RecentFiles), MaxRecentFiles)
	}
	seen := map[string]bool{}
	for _, path := range got.RecentFiles {
		if path == "" {
			t.Fatal("recent files contain empty path")
		}
		if seen[path] {
			t.Fatalf("recent files contain duplicate path %q", path)
		}
		seen[path] = true
	}
}

func TestWithRecentFilePrependsAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.sql")
	second := filepath.Join(dir, "second.sql")

	cfg := Defaults().WithRecentFile(first).WithRecentFile(second).WithRecentFile(first)
	if len(cfg.RecentFiles) != 2 {
		t.Fatalf("recent files len = %d, want 2", len(cfg.RecentFiles))
	}
	if cfg.RecentFiles[0] != filepath.Clean(first) {
		t.Fatalf("first recent = %q, want %q", cfg.RecentFiles[0], filepath.Clean(first))
	}
	if cfg.RecentFiles[1] != filepath.Clean(second) {
		t.Fatalf("second recent = %q, want %q", cfg.RecentFiles[1], filepath.Clean(second))
	}
}

func TestRecentFilesPersistInSettingsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), SettingsFileName)
	want := Defaults().WithRecentFile(filepath.Join(t.TempDir(), "dump.sql"))
	if err := want.SaveFile(path); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.RecentFiles) != 1 || got.RecentFiles[0] != want.RecentFiles[0] {
		t.Fatalf("recent files = %#v, want %#v", got.RecentFiles, want.RecentFiles)
	}
}
