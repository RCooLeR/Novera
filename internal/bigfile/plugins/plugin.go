package plugins

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Mode describes whether a plugin can operate on a bounded in-memory editor buffer,
// a streaming huge-file pipeline, or both.
type Mode string

const (
	ModeInteractive Mode = "interactive"
	ModeStreaming   Mode = "streaming"
)

const DefaultSymbolMaxBytes int64 = 8 * 1024 * 1024

// Capability names the kind of editor/tool behavior a built-in plugin contributes.
type Capability string

const (
	CapabilitySyntax       Capability = "syntax"
	CapabilityAnalyze      Capability = "analyze"
	CapabilityTransform    Capability = "transform"
	CapabilityConvert      Capability = "convert"
	CapabilityFormat       Capability = "format"
	CapabilityAutocomplete Capability = "autocomplete"
	CapabilityNavigate     Capability = "navigate"
	CapabilityExtract      Capability = "extract"
	CapabilityValidate     Capability = "validate"
	CapabilityDecorate     Capability = "decorate"
)

// Descriptor is intentionally declarative for now. Implementations can later hang
// handlers off these IDs without hard-wiring every tool into the main window.
type Descriptor struct {
	ID           string                `json:"id"`
	DisplayName  string                `json:"displayName"`
	Category     string                `json:"category"`
	Description  string                `json:"description"`
	FilePatterns []string              `json:"filePatterns"`
	Capabilities []Capability          `json:"capabilities"`
	Operations   []OperationCapability `json:"operations,omitempty"`
	Formatters   []FormatterHook       `json:"formatters,omitempty"`
	Symbols      []SymbolHook          `json:"symbols,omitempty"`
	Completions  []CompletionHook      `json:"completions,omitempty"`
	Decorations  []DecorationHook      `json:"decorations,omitempty"`
	Modes        []Mode                `json:"modes"`
	// Deprecated: a plugin-wide boolean cannot describe mixed bounded,
	// streaming, and materializing operations. Validation rejects true; use
	// Operations to declare each execution model explicitly.
	HugeFileSafe bool     `json:"hugeFileSafe,omitempty"`
	Notes        []string `json:"notes,omitempty"`
}

// FormatterHook is a declarative formatter integration point. It is discovery
// metadata only; callers must still run formatters against normal buffers,
// bounded editable windows, temp copies, or safe outputs instead of mutating a
// huge source file in place.
type FormatterHook struct {
	ID               string   `json:"id"`
	DisplayName      string   `json:"displayName"`
	Command          string   `json:"command"`
	Args             []string `json:"args,omitempty"`
	FilePatterns     []string `json:"filePatterns,omitempty"`
	Stdin            bool     `json:"stdin,omitempty"`
	Stdout           bool     `json:"stdout,omitempty"`
	RequiresTempCopy bool     `json:"requiresTempCopy,omitempty"`
	Notes            []string `json:"notes,omitempty"`
}

type FormatterMatch struct {
	Plugin    Descriptor
	Formatter FormatterHook
}

// SymbolHook is a declarative local-symbol provider. It describes how Novera
// can later populate outlines and autocomplete seeds from normal buffers or
// explicit editable slices without parsing an unbounded huge file.
type SymbolHook struct {
	ID           string       `json:"id"`
	DisplayName  string       `json:"displayName"`
	Strategy     HookStrategy `json:"strategy"`
	FilePatterns []string     `json:"filePatterns,omitempty"`
	Kinds        []string     `json:"kinds,omitempty"`
	MaxBytes     int64        `json:"maxBytes,omitempty"`
	Notes        []string     `json:"notes,omitempty"`
}

type SymbolMatch struct {
	Plugin Descriptor
	Symbol SymbolHook
}

// CompletionHook is a declarative autocomplete provider. It advertises where
// completions can come from; execution remains bounded to normal buffers or
// explicit editable slices until deeper language integration is proven safe.
type CompletionHook struct {
	ID                string       `json:"id"`
	DisplayName       string       `json:"displayName"`
	Strategy          HookStrategy `json:"strategy"`
	FilePatterns      []string     `json:"filePatterns,omitempty"`
	Sources           []string     `json:"sources,omitempty"`
	TriggerCharacters []string     `json:"triggerCharacters,omitempty"`
	MaxBytes          int64        `json:"maxBytes,omitempty"`
	Notes             []string     `json:"notes,omitempty"`
}

type CompletionMatch struct {
	Plugin     Descriptor
	Completion CompletionHook
}

// DecorationHook advertises bounded editor decorations such as rainbow
// brackets. Implementations must operate only on normal buffers, active slices,
// or visible ranges supplied by the caller.
type DecorationHook struct {
	ID           string       `json:"id"`
	DisplayName  string       `json:"displayName"`
	Strategy     HookStrategy `json:"strategy"`
	FilePatterns []string     `json:"filePatterns,omitempty"`
	MaxBytes     int64        `json:"maxBytes,omitempty"`
	Notes        []string     `json:"notes,omitempty"`
}

type DecorationMatch struct {
	Plugin     Descriptor
	Decoration DecorationHook
}

// RuntimePlugin is the stable runtime-facing contract for built-in and future
// loaded plugins. Tool-specific optional interfaces can hang off this root
// without requiring the editor shell to know every plugin package. Registry
// construction captures only immutable descriptor snapshots; callers that need
// optional executable interfaces must retain the validated concrete adapter.
type RuntimePlugin interface {
	Descriptor() Descriptor
}

type StaticPlugin struct {
	descriptor Descriptor
}

func NewStaticPlugin(descriptor Descriptor) StaticPlugin {
	return StaticPlugin{descriptor: descriptor.Clone()}
}

func (p StaticPlugin) Descriptor() Descriptor {
	return p.descriptor.Clone()
}

type Registry struct {
	descriptors []Descriptor
	byID        map[string]int
}

func NewRegistry(plugins ...RuntimePlugin) (*Registry, error) {
	if len(plugins) > maxRegistryPlugins {
		return nil, fmt.Errorf("plugin registry contains %d plugins; maximum is %d", len(plugins), maxRegistryPlugins)
	}

	descriptors := make([]Descriptor, 0, len(plugins))
	seenIDs := make(map[string]int, len(plugins))
	for i, plugin := range plugins {
		descriptor, err := snapshotRuntimeDescriptor(plugin)
		if err != nil {
			return nil, fmt.Errorf("plugins[%d]: %w", i, err)
		}
		if err := descriptor.Validate(); err != nil {
			return nil, fmt.Errorf("plugins[%d]: %w", i, err)
		}
		if previous, exists := seenIDs[descriptor.ID]; exists {
			return nil, fmt.Errorf("plugins[%d].id: duplicate plugin id %q (already used by plugins[%d])", i, descriptor.ID, previous)
		}
		seenIDs[descriptor.ID] = i
		descriptors = append(descriptors, descriptor)
	}

	registry := &Registry{
		descriptors: descriptors,
		byID:        make(map[string]int, len(descriptors)),
	}
	for i, descriptor := range descriptors {
		registry.byID[descriptor.ID] = i
	}
	return registry, nil
}

// MustRegistry is for static built-in plugin catalogs only. Dynamic or user-provided
// plugin descriptors must use NewRegistry so validation errors can be surfaced in the UI.
func MustRegistry(plugins ...RuntimePlugin) *Registry {
	registry, err := NewRegistry(plugins...)
	if err != nil {
		panic(err)
	}
	return registry
}

func NewStaticRegistry(descriptors []Descriptor) (*Registry, error) {
	if len(descriptors) > maxRegistryPlugins {
		return nil, fmt.Errorf("plugin registry contains %d descriptors; maximum is %d", len(descriptors), maxRegistryPlugins)
	}
	plugins := make([]RuntimePlugin, 0, len(descriptors))
	for _, descriptor := range descriptors {
		plugins = append(plugins, NewStaticPlugin(descriptor))
	}
	return NewRegistry(plugins...)
}

// MustStaticRegistry is for static built-in plugin catalogs only. Dynamic or user-provided
// plugin descriptors must use NewStaticRegistry so bad metadata never crashes the editor.
func MustStaticRegistry(descriptors []Descriptor) *Registry {
	registry, err := NewStaticRegistry(descriptors)
	if err != nil {
		panic(err)
	}
	return registry
}

func (r *Registry) Plugins() []RuntimePlugin {
	if r == nil {
		return nil
	}
	out := make([]RuntimePlugin, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		out = append(out, NewStaticPlugin(descriptor))
	}
	return out
}

func (r *Registry) Descriptors() []Descriptor {
	if r == nil {
		return nil
	}
	out := make([]Descriptor, 0, len(r.descriptors))
	for _, descriptor := range r.descriptors {
		out = append(out, descriptor.Clone())
	}
	return out
}

func (r *Registry) ByID(id string) (RuntimePlugin, bool) {
	if r == nil {
		return nil, false
	}
	index, ok := r.byID[strings.TrimSpace(id)]
	if !ok {
		return nil, false
	}
	return NewStaticPlugin(r.descriptors[index]), true
}

func (r *Registry) MatchPath(path string) []Descriptor {
	return MatchPath(path, r.Descriptors())
}

func (r *Registry) MatchPathIncludingWildcard(path string) []Descriptor {
	return MatchPathIncludingWildcard(path, r.Descriptors())
}

func (r *Registry) FormatterHooksForPath(path string) []FormatterMatch {
	if r == nil {
		return nil
	}
	matches := r.MatchPath(path)
	out := make([]FormatterMatch, 0)
	for _, descriptor := range matches {
		if !descriptor.HasCapability(CapabilityFormat) {
			continue
		}
		for _, formatter := range descriptor.Formatters {
			if !formatter.matchesPath(path) {
				continue
			}
			out = append(out, FormatterMatch{Plugin: descriptor.Clone(), Formatter: cloneFormatterHook(formatter)})
		}
	}
	return out
}

func (r *Registry) SymbolHooksForPath(path string) []SymbolMatch {
	if r == nil {
		return nil
	}
	matches := r.MatchPath(path)
	out := make([]SymbolMatch, 0)
	for _, descriptor := range matches {
		if !descriptor.HasCapability(CapabilityNavigate) && !descriptor.HasCapability(CapabilityAutocomplete) {
			continue
		}
		for _, symbol := range descriptor.Symbols {
			if !symbol.matchesPath(path) {
				continue
			}
			out = append(out, SymbolMatch{Plugin: descriptor.Clone(), Symbol: cloneSymbolHook(symbol)})
		}
	}
	return out
}

func (r *Registry) CompletionHooksForPath(path string) []CompletionMatch {
	if r == nil {
		return nil
	}
	matches := r.MatchPath(path)
	out := make([]CompletionMatch, 0)
	for _, descriptor := range matches {
		if !descriptor.HasCapability(CapabilityAutocomplete) {
			continue
		}
		for _, completion := range descriptor.Completions {
			if !completion.matchesPath(path) {
				continue
			}
			out = append(out, CompletionMatch{Plugin: descriptor.Clone(), Completion: cloneCompletionHook(completion)})
		}
	}
	return out
}

func (r *Registry) DecorationHooksForPath(path string) []DecorationMatch {
	if r == nil {
		return nil
	}
	matches := r.MatchPathIncludingWildcard(path)
	out := make([]DecorationMatch, 0)
	for _, descriptor := range matches {
		if !descriptor.HasCapability(CapabilityDecorate) {
			continue
		}
		for _, decoration := range descriptor.Decorations {
			if !decoration.matchesPath(path) {
				continue
			}
			out = append(out, DecorationMatch{Plugin: descriptor.Clone(), Decoration: cloneDecorationHook(decoration)})
		}
	}
	return out
}

var (
	canonicalSQLFilePatterns = []string{"*.sql", "*.dump"}
	canonicalCSVFilePatterns = []string{"*.csv", "*.tsv", "*.tab"}
	// SQLFilePatterns and CSVFilePatterns remain for source compatibility.
	// Deprecated: use SQLPatterns and CSVPatterns so callers receive a copy.
	SQLFilePatterns        = cloneStrings(canonicalSQLFilePatterns)
	CSVFilePatterns        = cloneStrings(canonicalCSVFilePatterns)
	LogFilePatterns        = []string{"*.log", "*.out", "*.trace", "*.jsonl", "*.ndjson"}
	YAMLFilePatterns       = []string{"*.yaml", "*.yml"}
	MarkupFilePatterns     = []string{"*.html", "*.htm", "*.xml", "*.svg", "*.vue", "*.svelte", "*.md", "*.markdown", "*.rst", "*.adoc", "*.css", "*.scss", "*.less"}
	ConfigFilePatterns     = []string{"*.ini", "*.toml", "*.json", "*.env", "*.conf", "*.cfg", "*.properties", "dockerfile", "makefile", "justfile", "rakefile", "gemfile", ".gitignore", ".gitattributes"}
	SourceFilePatterns     = []string{"*.ex", "*.exs", "*.erl", "*.hrl", "*.fs", "*.fsx", "*.fsi", "*.clj", "*.cljs", "*.cljc", "*.zig", "*.nim", "*.v", "*.vh", "*.hs", "*.lhs", "*.ml", "*.mli", "*.m", "*.mm", "*.vb"}
	LuaFilePatterns        = []string{"*.lua"}
	PerlFilePatterns       = []string{"*.pl", "*.pm"}
	ScalaFilePatterns      = []string{"*.scala"}
	GoFilePatterns         = []string{"*.go", "go.mod", "go.sum", "go.work"}
	PHPFilePatterns        = []string{"*.php", "*.phtml", "*.inc"}
	JavaScriptFilePatterns = []string{"*.js", "*.jsx", "*.ts", "*.tsx", "*.mjs", "*.cjs", "package.json", "tsconfig.json", "jsconfig.json"}
	PythonFilePatterns     = []string{"*.py", "*.pyw", "requirements.txt", "pyproject.toml", "poetry.lock", "pipfile"}
	JavaFilePatterns       = []string{"*.java", "pom.xml", "build.gradle", "settings.gradle", "gradle.properties"}
	CSharpFilePatterns     = []string{"*.cs", "*.csx", "*.csproj", "*.sln"}
	CFilePatterns          = []string{"*.c", "*.h"}
	CPPFilePatterns        = []string{"*.cc", "*.cpp", "*.cxx", "*.c++", "*.hh", "*.hpp", "*.hxx", "*.ipp"}
	RustFilePatterns       = []string{"*.rs", "cargo.toml", "cargo.lock"}
	KotlinFilePatterns     = []string{"*.kt", "*.kts"}
	SwiftFilePatterns      = []string{"*.swift"}
	RubyFilePatterns       = []string{"*.rb", "*.rake", "*.gemspec"}
	DartFilePatterns       = []string{"*.dart", "pubspec.yaml", "pubspec.lock"}
	RFilePatterns          = []string{"*.r", "*.rmd"}
	ShellFilePatterns      = []string{"*.sh", "*.bash", "*.zsh", "*.fish", "*.ps1", "*.psm1", "*.psd1"}
	ConverterFilePatterns  = []string{"*"}
)

func SQLPatterns() []string { return cloneStrings(canonicalSQLFilePatterns) }

func CSVPatterns() []string { return cloneStrings(canonicalCSVFilePatterns) }

func (d Descriptor) Validate() error {
	return validateDescriptor(d)
}

func (d Descriptor) HasCapability(capability Capability) bool {
	for _, candidate := range d.Capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

func (h FormatterHook) Validate(pluginID string) error {
	return validateFormatterHook(h, pluginID)
}

func (h FormatterHook) matchesPath(path string) bool {
	if len(h.FilePatterns) == 0 {
		return true
	}
	name := strings.ToLower(filepath.Base(path))
	for _, pattern := range h.FilePatterns {
		if _, ok := matchPattern(name, pattern, true); ok {
			return true
		}
	}
	return false
}

func (h SymbolHook) Validate(pluginID string) error {
	return validateSymbolHook(h, pluginID)
}

func (h SymbolHook) matchesPath(path string) bool {
	if len(h.FilePatterns) == 0 {
		return true
	}
	name := strings.ToLower(filepath.Base(path))
	for _, pattern := range h.FilePatterns {
		if _, ok := matchPattern(name, pattern, true); ok {
			return true
		}
	}
	return false
}

func (h CompletionHook) Validate(pluginID string) error {
	return validateCompletionHook(h, pluginID)
}

func (h CompletionHook) matchesPath(path string) bool {
	if len(h.FilePatterns) == 0 {
		return true
	}
	name := strings.ToLower(filepath.Base(path))
	for _, pattern := range h.FilePatterns {
		if _, ok := matchPattern(name, pattern, true); ok {
			return true
		}
	}
	return false
}

func (h DecorationHook) Validate(pluginID string) error {
	return validateDecorationHook(h, pluginID)
}

func (h DecorationHook) matchesPath(path string) bool {
	if len(h.FilePatterns) == 0 {
		return true
	}
	name := strings.ToLower(filepath.Base(path))
	for _, pattern := range h.FilePatterns {
		if _, ok := matchPattern(name, pattern, true); ok {
			return true
		}
	}
	return false
}

type descriptorMatch struct {
	descriptor  Descriptor
	specificity int
}

// MatchPath returns descriptors whose file patterns match path.
// Wildcard descriptors are skipped so primary file-type routing remains specific.
func MatchPath(path string, descriptors []Descriptor) []Descriptor {
	return matchPath(path, descriptors, false)
}

// MatchPathIncludingWildcard includes catch-all descriptors such as converters.
func MatchPathIncludingWildcard(path string, descriptors []Descriptor) []Descriptor {
	return matchPath(path, descriptors, true)
}

func matchPath(path string, descriptors []Descriptor, includeWildcard bool) []Descriptor {
	name := strings.ToLower(filepath.Base(path))
	matches := make([]descriptorMatch, 0)
	for _, descriptor := range descriptors {
		best := -1
		for _, pattern := range descriptor.FilePatterns {
			specificity, ok := matchPattern(name, pattern, includeWildcard)
			if ok && specificity > best {
				best = specificity
			}
		}
		if best >= 0 {
			matches = append(matches, descriptorMatch{descriptor: descriptor, specificity: best})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].specificity != matches[j].specificity {
			return matches[i].specificity > matches[j].specificity
		}
		return matches[i].descriptor.ID < matches[j].descriptor.ID
	})
	out := make([]Descriptor, 0, len(matches))
	for _, match := range matches {
		out = append(out, match.descriptor.Clone())
	}
	return out
}

func matchPattern(name string, pattern string, includeWildcard bool) (int, bool) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	if pattern == "" {
		return -1, false
	}
	if pattern == "*" {
		if includeWildcard {
			return 0, true
		}
		return -1, false
	}
	if !strings.ContainsAny(pattern, "*?[") {
		return exactPatternSpecificity(pattern), name == pattern
	}
	ok, err := filepath.Match(pattern, name)
	if err != nil || !ok {
		return -1, false
	}
	return globPatternSpecificity(pattern), true
}

func exactPatternSpecificity(pattern string) int {
	return 3_000_000 + len(pattern)
}

func globPatternSpecificity(pattern string) int {
	score := 1_000_000
	segmentCount := 1
	for _, ch := range pattern {
		switch ch {
		case '*', '?', '[', ']':
			continue
		case '.', '-', '_':
			score += 2
			if ch == '.' {
				segmentCount++
			}
		default:
			score += 10
		}
	}
	return score + segmentCount
}
