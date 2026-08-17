package plugins

import (
	"reflect"
	"strings"
	"sync"
	"testing"
)

type mutableRuntimePlugin struct {
	descriptor Descriptor
}

func (p *mutableRuntimePlugin) Descriptor() Descriptor {
	return p.descriptor
}

type panickingRuntimePlugin struct{}

func (*panickingRuntimePlugin) Descriptor() Descriptor {
	panic("descriptor failure")
}

func TestRegistryRejectsAdversarialDescriptors(t *testing.T) {
	valid := completeTestDescriptor()
	tests := []struct {
		name string
		edit func(*Descriptor)
		want string
	}{
		{name: "empty routing patterns", edit: func(d *Descriptor) { d.FilePatterns = nil }, want: "file patterns"},
		{name: "invalid glob", edit: func(d *Descriptor) { d.FilePatterns = []string{"[broken"} }, want: "invalid"},
		{name: "path glob", edit: func(d *Descriptor) { d.FilePatterns = []string{"dir/*.fx"} }, want: "filename-only"},
		{name: "duplicate pattern", edit: func(d *Descriptor) { d.FilePatterns = []string{"*.fx", "*.FX"} }, want: "duplicates pattern"},
		{name: "non-normalized id", edit: func(d *Descriptor) { d.ID = " Fixture " }, want: "leading or trailing whitespace"},
		{name: "uppercase id", edit: func(d *Descriptor) { d.ID = "Fixture" }, want: "lowercase ASCII"},
		{name: "unknown mode", edit: func(d *Descriptor) { d.Modes = []Mode{"ambient"} }, want: "unknown mode"},
		{name: "duplicate mode", edit: func(d *Descriptor) { d.Modes = []Mode{ModeInteractive, ModeInteractive} }, want: "duplicate mode"},
		{name: "unknown capability", edit: func(d *Descriptor) { d.Capabilities = append(d.Capabilities, Capability("execute")) }, want: "unknown capability"},
		{name: "duplicate capability", edit: func(d *Descriptor) { d.Capabilities = append(d.Capabilities, CapabilityAnalyze) }, want: "duplicate capability"},
		{name: "duplicate hook id across kinds", edit: func(d *Descriptor) { d.Completions[0].ID = d.Symbols[0].ID }, want: "collides"},
		{name: "child id collides with plugin", edit: func(d *Descriptor) { d.Formatters[0].ID = d.ID }, want: "collides"},
		{name: "unknown strategy", edit: func(d *Descriptor) { d.Symbols[0].Strategy = "read everything" }, want: "unknown strategy"},
		{name: "unbounded bounded strategy", edit: func(d *Descriptor) { d.Symbols[0].MaxBytes = 0 }, want: "requires a positive max bytes"},
		{name: "shell formatter command", edit: func(d *Descriptor) { d.Formatters[0].Command = "gofmt --write" }, want: "without a path, whitespace, or shell syntax"},
		{name: "path formatter command", edit: func(d *Descriptor) { d.Formatters[0].Command = `bin\gofmt` }, want: "without a path, whitespace, or shell syntax"},
		{name: "formatter without transport", edit: func(d *Descriptor) {
			d.Formatters[0].Stdin = false
			d.Formatters[0].Stdout = false
		}, want: "must declare stdin/stdout"},
		{name: "hook without capability", edit: func(d *Descriptor) { d.Capabilities = removeCapability(d.Capabilities, CapabilityAutocomplete) }, want: "requires capability"},
		{name: "deprecated huge file flag", edit: func(d *Descriptor) { d.HugeFileSafe = true }, want: "deprecated"},
		{name: "unknown processing model", edit: func(d *Descriptor) { d.Operations[0].Processing = "magic" }, want: "unknown processing model"},
		{name: "unknown memory model", edit: func(d *Descriptor) { d.Operations[0].Memory = "free" }, want: "unknown memory model"},
		{name: "operation undeclared capability", edit: func(d *Descriptor) { d.Operations[0].Capability = CapabilityConvert }, want: "undeclared capability"},
		{name: "bounded operation without bound", edit: func(d *Descriptor) { d.Operations[0].MaxInputBytes = 0 }, want: "requires a positive max input bytes"},
		{name: "oversized id", edit: func(d *Descriptor) { d.ID = strings.Repeat("a", maxMetadataIDBytes+1) }, want: "maximum"},
		{name: "oversized notes", edit: func(d *Descriptor) { d.Notes = []string{strings.Repeat("n", maxMetadataNoteBytes+1)} }, want: "maximum"},
		{name: "too many patterns", edit: func(d *Descriptor) {
			d.FilePatterns = make([]string, maxDescriptorPatterns+1)
			for i := range d.FilePatterns {
				d.FilePatterns[i] = "file" + strings.Repeat("x", i) + ".fx"
			}
		}, want: "maximum"},
		{name: "too many formatter args", edit: func(d *Descriptor) { d.Formatters[0].Args = make([]string, maxHookArgs+1) }, want: "maximum"},
		{name: "too many operations", edit: func(d *Descriptor) { d.Operations = make([]OperationCapability, maxDescriptorOperations+1) }, want: "maximum"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := valid.Clone()
			tc.edit(&descriptor)
			registry, err := NewRegistry(NewStaticPlugin(descriptor))
			if err == nil {
				t.Fatalf("descriptor was accepted: %+v", descriptor)
			}
			if registry != nil {
				t.Fatal("failed construction returned a partially populated registry")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want path/reason containing %q", err, tc.want)
			}
		})
	}
}

func TestRegistryRejectsTypedNilAndDescriptorPanic(t *testing.T) {
	var typedNil *mutableRuntimePlugin
	if registry, err := NewRegistry(typedNil); err == nil || registry != nil {
		t.Fatalf("typed nil result = %#v, %v", registry, err)
	}
	if registry, err := NewRegistry(&panickingRuntimePlugin{}); err == nil || registry != nil || !strings.Contains(err.Error(), "panicked") {
		t.Fatalf("panicking descriptor result = %#v, %v", registry, err)
	}
}

func TestRegistryRejectsBoundedCatalogOverflow(t *testing.T) {
	descriptors := make([]Descriptor, maxRegistryPlugins+1)
	if registry, err := NewStaticRegistry(descriptors); err == nil || registry != nil {
		t.Fatalf("oversized static registry result = %#v, %v", registry, err)
	}
	plugins := make([]RuntimePlugin, maxRegistryPlugins+1)
	if registry, err := NewRegistry(plugins...); err == nil || registry != nil {
		t.Fatalf("oversized runtime registry result = %#v, %v", registry, err)
	}
}

func TestCanonicalPatternBuildersIgnoreLegacySliceMutation(t *testing.T) {
	sqlLegacy := SQLFilePatterns[0]
	csvLegacy := CSVFilePatterns[0]
	t.Cleanup(func() {
		SQLFilePatterns[0] = sqlLegacy
		CSVFilePatterns[0] = csvLegacy
	})
	SQLFilePatterns[0] = "*.changed"
	CSVFilePatterns[0] = "*.changed"
	if got := SQLPatterns()[0]; got != "*.sql" {
		t.Fatalf("SQLPatterns = %q after legacy slice mutation", got)
	}
	if got := CSVPatterns()[0]; got != "*.csv" {
		t.Fatalf("CSVPatterns = %q after legacy slice mutation", got)
	}

	patterns := SQLPatterns()
	patterns[0] = "*.also-changed"
	if got := SQLPatterns()[0]; got != "*.sql" {
		t.Fatalf("mutating builder result changed canonical patterns: %q", got)
	}
}

func TestRegistryAndStaticPluginDeepCopyAllMetadata(t *testing.T) {
	original := completeTestDescriptor()
	want := original.Clone()
	static := NewStaticPlugin(original)
	mutateDescriptor(&original)
	assertDescriptorEqual(t, static.Descriptor(), want)

	returned := static.Descriptor()
	mutateDescriptor(&returned)
	assertDescriptorEqual(t, static.Descriptor(), want)

	dynamic := &mutableRuntimePlugin{descriptor: want.Clone()}
	registry, err := NewRegistry(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	mutateDescriptor(&dynamic.descriptor)
	assertDescriptorEqual(t, registry.Descriptors()[0], want)

	listed := registry.Descriptors()
	mutateDescriptor(&listed[0])
	assertDescriptorEqual(t, registry.Descriptors()[0], want)

	plugins := registry.Plugins()
	pluginDescriptor := plugins[0].Descriptor()
	mutateDescriptor(&pluginDescriptor)
	assertDescriptorEqual(t, registry.Descriptors()[0], want)

	byID, ok := registry.ByID(want.ID)
	if !ok {
		t.Fatal("ByID did not find frozen plugin metadata")
	}
	gotByID := byID.Descriptor()
	mutateDescriptor(&gotByID)
	assertDescriptorEqual(t, registry.Descriptors()[0], want)

	matches := registry.FormatterHooksForPath("sample.fx")
	if len(matches) != 1 {
		t.Fatalf("formatter matches = %#v", matches)
	}
	matches[0].Formatter.Args[0] = "changed"
	if matches[0].Plugin.Formatters[0].Args[0] != "-w" {
		t.Fatal("formatter match aliases its nested plugin descriptor")
	}
	mutateDescriptor(&matches[0].Plugin)
	assertDescriptorEqual(t, registry.Descriptors()[0], want)
}

func TestDescriptorClonePreservesNilSliceShape(t *testing.T) {
	original := Descriptor{ID: "shape"}
	if got := original.Clone(); !reflect.DeepEqual(got, original) {
		t.Fatalf("Clone changed nil slice shape:\n got: %#v\nwant: %#v", got, original)
	}
}

func TestRegistryFrozenMetadataConcurrentReadsAndCallerMutation(t *testing.T) {
	dynamic := &mutableRuntimePlugin{descriptor: completeTestDescriptor()}
	registry, err := NewRegistry(dynamic)
	if err != nil {
		t.Fatal(err)
	}
	want := registry.Descriptors()[0]

	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				descriptors := registry.Descriptors()
				mutateDescriptor(&descriptors[0])
				plugin, ok := registry.ByID(want.ID)
				if !ok {
					t.Error("ByID lost a frozen descriptor")
					return
				}
				copy := plugin.Descriptor()
				mutateDescriptor(&copy)
				if matches := registry.MatchPath("sample.fx"); len(matches) != 1 || matches[0].ID != want.ID {
					t.Errorf("matches = %#v", matches)
					return
				}
			}
		}()
	}
	wg.Wait()
	assertDescriptorEqual(t, registry.Descriptors()[0], want)
}

func completeTestDescriptor() Descriptor {
	return Descriptor{
		ID: "fixture", DisplayName: "Fixture", Category: "test", Description: "Complete nested metadata.",
		FilePatterns: []string{"*.fx"},
		Capabilities: []Capability{CapabilityAnalyze, CapabilityFormat, CapabilityNavigate, CapabilityAutocomplete, CapabilityDecorate},
		Operations: []OperationCapability{{
			ID: "sample", Capability: CapabilityAnalyze, Processing: ProcessingBoundedSample, Memory: MemoryBounded,
			MaxInputBytes: 1024, Cancellable: true, Notes: []string{"bounded operation"},
		}},
		Formatters: []FormatterHook{{
			ID: "format", DisplayName: "Format", Command: "gofmt", Args: []string{"-w"}, FilePatterns: []string{"*.fx"},
			Stdin: true, Stdout: true, Notes: []string{"formatter note"},
		}},
		Symbols: []SymbolHook{{
			ID: "symbols", DisplayName: "Symbols", Strategy: StrategyBoundedLexicalScan,
			FilePatterns: []string{"*.fx"}, Kinds: []string{"function"}, MaxBytes: 1024, Notes: []string{"symbol note"},
		}},
		Completions: []CompletionHook{{
			ID: "completions", DisplayName: "Completions", Strategy: StrategyBoundedSymbolSuggestions,
			FilePatterns: []string{"*.fx"}, Sources: []string{"symbols"}, TriggerCharacters: []string{"."}, MaxBytes: 1024,
			Notes: []string{"completion note"},
		}},
		Decorations: []DecorationHook{{
			ID: "decorations", DisplayName: "Decorations", Strategy: StrategyBoundedBracketStack,
			FilePatterns: []string{"*.fx"}, MaxBytes: 1024, Notes: []string{"decoration note"},
		}},
		Modes: []Mode{ModeInteractive, ModeStreaming}, Notes: []string{"descriptor note"},
	}
}

func mutateDescriptor(d *Descriptor) {
	d.FilePatterns[0] = "*.changed"
	d.Capabilities[0] = CapabilityConvert
	d.Modes[0] = Mode("changed")
	d.Notes[0] = "changed"
	d.Operations[0].Notes[0] = "changed"
	d.Formatters[0].Args[0] = "changed"
	d.Formatters[0].FilePatterns[0] = "*.changed"
	d.Formatters[0].Notes[0] = "changed"
	d.Symbols[0].FilePatterns[0] = "*.changed"
	d.Symbols[0].Kinds[0] = "changed"
	d.Symbols[0].Notes[0] = "changed"
	d.Completions[0].FilePatterns[0] = "*.changed"
	d.Completions[0].Sources[0] = "changed"
	d.Completions[0].TriggerCharacters[0] = "!"
	d.Completions[0].Notes[0] = "changed"
	d.Decorations[0].FilePatterns[0] = "*.changed"
	d.Decorations[0].Notes[0] = "changed"
}

func assertDescriptorEqual(t *testing.T, got, want Descriptor) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("descriptor changed through an outward metadata value:\n got: %#v\nwant: %#v", got, want)
	}
}

func removeCapability(capabilities []Capability, remove Capability) []Capability {
	out := make([]Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		if capability != remove {
			out = append(out, capability)
		}
	}
	return out
}
