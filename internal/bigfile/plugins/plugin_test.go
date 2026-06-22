package plugins

import "testing"

func TestRegistryValidatesAndFindsPlugins(t *testing.T) {
	sql := NewStaticPlugin(Descriptor{
		ID:           "sql",
		DisplayName:  "SQL",
		Category:     "data",
		FilePatterns: []string{"*.sql"},
		Modes:        []Mode{ModeStreaming},
	})
	registry, err := NewRegistry(sql)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.Descriptors(); len(got) != 1 || got[0].ID != "sql" {
		t.Fatalf("descriptors = %#v", got)
	}
	if plugin, ok := registry.ByID(" sql "); !ok || plugin.Descriptor().DisplayName != "SQL" {
		t.Fatalf("ByID returned %#v/%v", plugin, ok)
	}
	if matches := registry.MatchPath("dump.sql"); len(matches) != 1 || matches[0].ID != "sql" {
		t.Fatalf("matches = %#v", matches)
	}
}

func TestRegistryRejectsDuplicateIDs(t *testing.T) {
	descriptor := Descriptor{ID: "sql", DisplayName: "SQL", Category: "data", Modes: []Mode{ModeStreaming}}
	_, err := NewRegistry(NewStaticPlugin(descriptor), NewStaticPlugin(descriptor))
	if err == nil {
		t.Fatal("expected duplicate id error")
	}
}

func TestRegistryRejectsInvalidPlugins(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{ID: "missing-fields"}))
	if err == nil {
		t.Fatal("expected descriptor validation error")
	}
}

func TestRegistryRoutesFormatterHooksForPath(t *testing.T) {
	goPlugin := NewStaticPlugin(Descriptor{
		ID:           "golang",
		DisplayName:  "Go",
		Category:     "language",
		FilePatterns: []string{"*.go"},
		Capabilities: []Capability{CapabilitySyntax, CapabilityFormat},
		Formatters: []FormatterHook{
			{ID: "gofmt", DisplayName: "gofmt", Command: "gofmt", Stdin: true, Stdout: true},
		},
		Modes: []Mode{ModeInteractive},
	})
	sqlPlugin := NewStaticPlugin(Descriptor{
		ID:           "sql",
		DisplayName:  "SQL",
		Category:     "data",
		FilePatterns: []string{"*.sql"},
		Capabilities: []Capability{CapabilitySyntax},
		Modes:        []Mode{ModeStreaming},
	})
	registry, err := NewRegistry(goPlugin, sqlPlugin)
	if err != nil {
		t.Fatal(err)
	}

	matches := registry.FormatterHooksForPath("main.go")
	if len(matches) != 1 {
		t.Fatalf("formatter matches = %#v, want one go formatter", matches)
	}
	if matches[0].Plugin.ID != "golang" || matches[0].Formatter.ID != "gofmt" {
		t.Fatalf("formatter match = %#v", matches[0])
	}
	if sqlMatches := registry.FormatterHooksForPath("dump.sql"); len(sqlMatches) != 0 {
		t.Fatalf("sql formatter matches = %#v, want none without format capability", sqlMatches)
	}
}

func TestRegistryRejectsInvalidFormatterHooks(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{
		ID:           "golang",
		DisplayName:  "Go",
		Category:     "language",
		FilePatterns: []string{"*.go"},
		Capabilities: []Capability{CapabilityFormat},
		Formatters:   []FormatterHook{{ID: "gofmt", DisplayName: "gofmt"}},
		Modes:        []Mode{ModeInteractive},
	}))
	if err == nil {
		t.Fatal("expected formatter validation error")
	}
}

func TestRegistryRejectsFormatCapabilityWithoutFormatterHooks(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{
		ID:           "source",
		DisplayName:  "Source",
		Category:     "language",
		FilePatterns: []string{"*.lua"},
		Capabilities: []Capability{CapabilityFormat},
		Modes:        []Mode{ModeInteractive},
	}))
	if err == nil {
		t.Fatal("expected format capability without formatter hooks to fail validation")
	}
}

func TestRegistryFiltersFormatterHooksByHookFilePattern(t *testing.T) {
	sourcePlugin := NewStaticPlugin(Descriptor{
		ID:           "source",
		DisplayName:  "Source",
		Category:     "language",
		FilePatterns: []string{"*.lua", "*.pl"},
		Capabilities: []Capability{CapabilityFormat},
		Formatters: []FormatterHook{
			{ID: "stylua", DisplayName: "StyLua", Command: "stylua", FilePatterns: []string{"*.lua"}, Stdin: true, Stdout: true},
			{ID: "perltidy", DisplayName: "perltidy", Command: "perltidy", FilePatterns: []string{"*.pl"}, RequiresTempCopy: true},
		},
		Modes: []Mode{ModeInteractive},
	})
	registry, err := NewRegistry(sourcePlugin)
	if err != nil {
		t.Fatal(err)
	}

	matches := registry.FormatterHooksForPath("script.pl")
	if len(matches) != 1 || matches[0].Formatter.ID != "perltidy" {
		t.Fatalf("formatter matches = %#v, want only perltidy", matches)
	}
}

func TestRegistryRoutesSymbolHooksForPath(t *testing.T) {
	goPlugin := NewStaticPlugin(Descriptor{
		ID:           "golang",
		DisplayName:  "Go",
		Category:     "language",
		FilePatterns: []string{"*.go"},
		Capabilities: []Capability{CapabilitySyntax, CapabilityNavigate},
		Symbols: []SymbolHook{
			{ID: "go-local-symbols", DisplayName: "Go local symbols", Strategy: "bounded lexical scan", MaxBytes: DefaultSymbolMaxBytes},
		},
		Modes: []Mode{ModeInteractive},
	})
	sqlPlugin := NewStaticPlugin(Descriptor{
		ID:           "sql",
		DisplayName:  "SQL",
		Category:     "data",
		FilePatterns: []string{"*.sql"},
		Capabilities: []Capability{CapabilitySyntax},
		Symbols: []SymbolHook{
			{ID: "sql-table-symbols", DisplayName: "SQL table symbols", Strategy: "streaming table analyzer"},
		},
		Modes: []Mode{ModeStreaming},
	})
	registry, err := NewRegistry(goPlugin, sqlPlugin)
	if err != nil {
		t.Fatal(err)
	}

	matches := registry.SymbolHooksForPath("main.go")
	if len(matches) != 1 {
		t.Fatalf("symbol matches = %#v, want one go symbol hook", matches)
	}
	if matches[0].Plugin.ID != "golang" || matches[0].Symbol.ID != "go-local-symbols" {
		t.Fatalf("symbol match = %#v", matches[0])
	}
	if sqlMatches := registry.SymbolHooksForPath("dump.sql"); len(sqlMatches) != 0 {
		t.Fatalf("sql symbol matches = %#v, want none without navigate/autocomplete capability", sqlMatches)
	}
}

func TestRegistryRejectsInvalidSymbolHooks(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{
		ID:           "golang",
		DisplayName:  "Go",
		Category:     "language",
		FilePatterns: []string{"*.go"},
		Capabilities: []Capability{CapabilityNavigate},
		Symbols:      []SymbolHook{{ID: "go-local-symbols", DisplayName: "Go local symbols"}},
		Modes:        []Mode{ModeInteractive},
	}))
	if err == nil {
		t.Fatal("expected symbol hook validation error")
	}
}

func TestRegistryFiltersSymbolHooksByHookFilePattern(t *testing.T) {
	pythonPlugin := NewStaticPlugin(Descriptor{
		ID:           "python",
		DisplayName:  "Python",
		Category:     "language",
		FilePatterns: []string{"*.py", "pyproject.toml"},
		Capabilities: []Capability{CapabilityNavigate},
		Symbols: []SymbolHook{
			{
				ID:           "python-local-symbols",
				DisplayName:  "Python local symbols",
				Strategy:     "bounded indentation-aware scan",
				FilePatterns: []string{"*.py"},
				MaxBytes:     DefaultSymbolMaxBytes,
			},
		},
		Modes: []Mode{ModeInteractive},
	})
	registry, err := NewRegistry(pythonPlugin)
	if err != nil {
		t.Fatal(err)
	}

	if matches := registry.SymbolHooksForPath("app.py"); len(matches) != 1 {
		t.Fatalf("python source symbol matches = %#v, want one", matches)
	}
	if matches := registry.SymbolHooksForPath("pyproject.toml"); len(matches) != 0 {
		t.Fatalf("pyproject symbol matches = %#v, want none", matches)
	}
}

func TestRegistryRoutesCompletionHooksForPath(t *testing.T) {
	goPlugin := NewStaticPlugin(Descriptor{
		ID:           "golang",
		DisplayName:  "Go",
		Category:     "language",
		FilePatterns: []string{"*.go"},
		Capabilities: []Capability{CapabilitySyntax, CapabilityAutocomplete},
		Completions: []CompletionHook{
			{ID: "go-local-completions", DisplayName: "Go local completions", Strategy: "bounded symbol and keyword suggestions", MaxBytes: DefaultSymbolMaxBytes},
		},
		Modes: []Mode{ModeInteractive},
	})
	sqlPlugin := NewStaticPlugin(Descriptor{
		ID:           "sql",
		DisplayName:  "SQL",
		Category:     "data",
		FilePatterns: []string{"*.sql"},
		Capabilities: []Capability{CapabilitySyntax},
		Completions: []CompletionHook{
			{ID: "sql-completions", DisplayName: "SQL completions", Strategy: "table analyzer suggestions"},
		},
		Modes: []Mode{ModeStreaming},
	})
	registry, err := NewRegistry(goPlugin, sqlPlugin)
	if err != nil {
		t.Fatal(err)
	}

	matches := registry.CompletionHooksForPath("main.go")
	if len(matches) != 1 {
		t.Fatalf("completion matches = %#v, want one go completion hook", matches)
	}
	if matches[0].Plugin.ID != "golang" || matches[0].Completion.ID != "go-local-completions" {
		t.Fatalf("completion match = %#v", matches[0])
	}
	if sqlMatches := registry.CompletionHooksForPath("dump.sql"); len(sqlMatches) != 0 {
		t.Fatalf("sql completion matches = %#v, want none without autocomplete capability", sqlMatches)
	}
}

func TestRegistryRejectsInvalidCompletionHooks(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{
		ID:           "golang",
		DisplayName:  "Go",
		Category:     "language",
		FilePatterns: []string{"*.go"},
		Capabilities: []Capability{CapabilityAutocomplete},
		Completions:  []CompletionHook{{ID: "go-local-completions", DisplayName: "Go local completions"}},
		Modes:        []Mode{ModeInteractive},
	}))
	if err == nil {
		t.Fatal("expected completion hook validation error")
	}
}

func TestRegistryRoutesDecorationHooksForPath(t *testing.T) {
	rainbowPlugin := NewStaticPlugin(Descriptor{
		ID:           "rainbow",
		DisplayName:  "Rainbow Brackets",
		Category:     "editor-assist",
		FilePatterns: []string{"*"},
		Capabilities: []Capability{CapabilityDecorate},
		Decorations: []DecorationHook{
			{ID: "rainbow-brackets", DisplayName: "Rainbow brackets", Strategy: "bounded bracket stack", MaxBytes: DefaultSymbolMaxBytes},
		},
		Modes: []Mode{ModeInteractive},
	})
	registry, err := NewRegistry(rainbowPlugin)
	if err != nil {
		t.Fatal(err)
	}

	matches := registry.DecorationHooksForPath("main.go")
	if len(matches) != 1 {
		t.Fatalf("decoration matches = %#v, want one rainbow hook", matches)
	}
	if matches[0].Plugin.ID != "rainbow" || matches[0].Decoration.ID != "rainbow-brackets" {
		t.Fatalf("decoration match = %#v", matches[0])
	}
}

func TestRegistryRejectsInvalidDecorationHooks(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{
		ID:           "rainbow",
		DisplayName:  "Rainbow Brackets",
		Category:     "editor-assist",
		FilePatterns: []string{"*"},
		Capabilities: []Capability{CapabilityDecorate},
		Decorations:  []DecorationHook{{ID: "rainbow-brackets", DisplayName: "Rainbow brackets"}},
		Modes:        []Mode{ModeInteractive},
	}))
	if err == nil {
		t.Fatal("expected decoration hook validation error")
	}
}

func TestRegistryRejectsDecorateCapabilityWithoutDecorationHooks(t *testing.T) {
	_, err := NewRegistry(NewStaticPlugin(Descriptor{
		ID:           "rainbow",
		DisplayName:  "Rainbow Brackets",
		Category:     "editor-assist",
		FilePatterns: []string{"*"},
		Capabilities: []Capability{CapabilityDecorate},
		Modes:        []Mode{ModeInteractive},
	}))
	if err == nil {
		t.Fatal("expected decorate capability without decoration hooks to fail validation")
	}
}

func TestRegistryFiltersCompletionHooksByHookFilePattern(t *testing.T) {
	rustPlugin := NewStaticPlugin(Descriptor{
		ID:           "rust",
		DisplayName:  "Rust",
		Category:     "language",
		FilePatterns: []string{"*.rs", "cargo.toml"},
		Capabilities: []Capability{CapabilityAutocomplete},
		Completions: []CompletionHook{
			{
				ID:           "rust-local-completions",
				DisplayName:  "Rust local completions",
				Strategy:     "bounded symbol and keyword suggestions",
				FilePatterns: []string{"*.rs"},
				MaxBytes:     DefaultSymbolMaxBytes,
			},
		},
		Modes: []Mode{ModeInteractive},
	})
	registry, err := NewRegistry(rustPlugin)
	if err != nil {
		t.Fatal(err)
	}

	if matches := registry.CompletionHooksForPath("lib.rs"); len(matches) != 1 {
		t.Fatalf("rust source completion matches = %#v, want one", matches)
	}
	if matches := registry.CompletionHooksForPath("Cargo.toml"); len(matches) != 0 {
		t.Fatalf("Cargo.toml completion matches = %#v, want none", matches)
	}
}

func TestMatchPathOrdersExactBeforeWildcard(t *testing.T) {
	descriptors := []Descriptor{
		{ID: "source", FilePatterns: []string{"*.go"}},
		{ID: "golang", FilePatterns: []string{"*.go", "go.mod"}},
		{ID: "converters", FilePatterns: []string{"*"}},
	}

	matches := MatchPathIncludingWildcard("go.mod", descriptors)
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want exact plus wildcard", len(matches))
	}
	if matches[0].ID != "golang" {
		t.Fatalf("first match = %q, want golang", matches[0].ID)
	}
	if matches[1].ID != "converters" {
		t.Fatalf("last match = %q, want converters", matches[1].ID)
	}
}

func TestMatchPathOrdersMoreSpecificGlobsBeforeGenericGlobs(t *testing.T) {
	descriptors := []Descriptor{
		{ID: "generic-ts", FilePatterns: []string{"*.ts"}},
		{ID: "definition-ts", FilePatterns: []string{"*.d.ts"}},
		{ID: "generic-js", FilePatterns: []string{"*.js"}},
		{ID: "test-js", FilePatterns: []string{"*.test.js"}},
	}

	tsMatches := MatchPath("types.d.ts", descriptors)
	if len(tsMatches) == 0 || tsMatches[0].ID != "definition-ts" {
		t.Fatalf("TypeScript matches = %#v, want definition-ts first", tsMatches)
	}
	jsMatches := MatchPath("widget.test.js", descriptors)
	if len(jsMatches) == 0 || jsMatches[0].ID != "test-js" {
		t.Fatalf("JavaScript matches = %#v, want test-js first", jsMatches)
	}
}

func TestMatchPathSkipsWildcardForPrimaryRouting(t *testing.T) {
	descriptors := []Descriptor{
		{ID: "converters", FilePatterns: []string{"*"}},
	}
	if matches := MatchPath("notes.txt", descriptors); len(matches) != 0 {
		t.Fatalf("matches = %+v, want none", matches)
	}
}
