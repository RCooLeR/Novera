package plugins

import (
	"errors"
	"fmt"
	"path"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxRegistryPlugins              = 128
	maxDescriptorPatterns           = 128
	maxDescriptorCapabilities       = 32
	maxDescriptorModes              = 8
	maxDescriptorHooks              = 128
	maxDescriptorOperations         = 256
	maxMetadataIDBytes              = 64
	maxMetadataNameBytes            = 256
	maxMetadataDescription          = 4096
	maxMetadataPatternBytes         = 256
	maxMetadataNotes                = 64
	maxMetadataNoteBytes            = 1024
	maxHookArgs                     = 64
	maxHookArgBytes                 = 1024
	maxHookValues                   = 128
	maxHookValueBytes               = 128
	maxFormatterCommandBytes        = 256
	maxHookBytes              int64 = 1 << 30
	maxOperationInput         int64 = 1 << 50
	maxOperationUnit          int64 = 1 << 40
)

func snapshotRuntimeDescriptor(plugin RuntimePlugin) (descriptor Descriptor, err error) {
	if isNilRuntimePlugin(plugin) {
		return Descriptor{}, errors.New("plugin is nil")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("Descriptor panicked: %v", recovered)
			descriptor = Descriptor{}
		}
	}()
	return plugin.Descriptor().Clone(), nil
}

func isNilRuntimePlugin(plugin RuntimePlugin) bool {
	if plugin == nil {
		return true
	}
	value := reflect.ValueOf(plugin)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validateDescriptor(d Descriptor) error {
	if err := validateIdentifier("plugin id", d.ID); err != nil {
		return err
	}
	if err := validateText("plugin "+quoted(d.ID)+" display name", d.DisplayName, true, maxMetadataNameBytes); err != nil {
		return err
	}
	if err := validateIdentifier("plugin "+quoted(d.ID)+" category", d.Category); err != nil {
		return err
	}
	if err := validateText("plugin "+quoted(d.ID)+" description", d.Description, false, maxMetadataDescription); err != nil {
		return err
	}
	if err := validatePatterns("plugin "+quoted(d.ID)+" file patterns", d.FilePatterns, true); err != nil {
		return err
	}
	if err := validateNotes("plugin "+quoted(d.ID)+" notes", d.Notes); err != nil {
		return err
	}
	if d.HugeFileSafe {
		return fmt.Errorf("plugin %q hugeFileSafe is deprecated and must be false; declare operation capabilities instead", d.ID)
	}
	if len(d.Modes) == 0 {
		return fmt.Errorf("plugin %q must declare at least one mode", d.ID)
	}
	if len(d.Modes) > maxDescriptorModes {
		return fmt.Errorf("plugin %q modes has %d entries; maximum is %d", d.ID, len(d.Modes), maxDescriptorModes)
	}
	seenModes := make(map[Mode]int, len(d.Modes))
	for i, mode := range d.Modes {
		if !validMode(mode) {
			return fmt.Errorf("plugin %q modes[%d]: unknown mode %q", d.ID, i, mode)
		}
		if previous, exists := seenModes[mode]; exists {
			return fmt.Errorf("plugin %q modes[%d]: duplicate mode %q (already at modes[%d])", d.ID, i, mode, previous)
		}
		seenModes[mode] = i
	}
	if len(d.Capabilities) > maxDescriptorCapabilities {
		return fmt.Errorf("plugin %q capabilities has %d entries; maximum is %d", d.ID, len(d.Capabilities), maxDescriptorCapabilities)
	}
	seenCapabilities := make(map[Capability]int, len(d.Capabilities))
	for i, capability := range d.Capabilities {
		if !validCapability(capability) {
			return fmt.Errorf("plugin %q capabilities[%d]: unknown capability %q", d.ID, i, capability)
		}
		if previous, exists := seenCapabilities[capability]; exists {
			return fmt.Errorf("plugin %q capabilities[%d]: duplicate capability %q (already at capabilities[%d])", d.ID, i, capability, previous)
		}
		seenCapabilities[capability] = i
	}

	if len(d.Formatters) > maxDescriptorHooks || len(d.Symbols) > maxDescriptorHooks || len(d.Completions) > maxDescriptorHooks || len(d.Decorations) > maxDescriptorHooks {
		return fmt.Errorf("plugin %q exceeds the maximum of %d hooks in one hook category", d.ID, maxDescriptorHooks)
	}
	if len(d.Operations) > maxDescriptorOperations {
		return fmt.Errorf("plugin %q operations has %d entries; maximum is %d", d.ID, len(d.Operations), maxDescriptorOperations)
	}

	childIDs := map[string]string{d.ID: "plugin id"}
	reserveID := func(id, location string) error {
		if previous, exists := childIDs[id]; exists {
			return fmt.Errorf("plugin %q %s id %q collides with %s", d.ID, location, id, previous)
		}
		childIDs[id] = location
		return nil
	}
	for i, hook := range d.Formatters {
		if err := hook.Validate(d.ID); err != nil {
			return fmt.Errorf("plugin %q formatters[%d]: %w", d.ID, i, err)
		}
		if !d.HasCapability(CapabilityFormat) {
			return fmt.Errorf("plugin %q formatters[%d] requires capability %q", d.ID, i, CapabilityFormat)
		}
		if err := reserveID(hook.ID, fmt.Sprintf("formatters[%d]", i)); err != nil {
			return err
		}
	}
	for i, hook := range d.Symbols {
		if err := hook.Validate(d.ID); err != nil {
			return fmt.Errorf("plugin %q symbols[%d]: %w", d.ID, i, err)
		}
		if !d.HasCapability(CapabilityNavigate) && !d.HasCapability(CapabilityAutocomplete) {
			return fmt.Errorf("plugin %q symbols[%d] requires capability %q or %q", d.ID, i, CapabilityNavigate, CapabilityAutocomplete)
		}
		if err := reserveID(hook.ID, fmt.Sprintf("symbols[%d]", i)); err != nil {
			return err
		}
	}
	for i, hook := range d.Completions {
		if err := hook.Validate(d.ID); err != nil {
			return fmt.Errorf("plugin %q completions[%d]: %w", d.ID, i, err)
		}
		if !d.HasCapability(CapabilityAutocomplete) {
			return fmt.Errorf("plugin %q completions[%d] requires capability %q", d.ID, i, CapabilityAutocomplete)
		}
		if err := reserveID(hook.ID, fmt.Sprintf("completions[%d]", i)); err != nil {
			return err
		}
	}
	for i, hook := range d.Decorations {
		if err := hook.Validate(d.ID); err != nil {
			return fmt.Errorf("plugin %q decorations[%d]: %w", d.ID, i, err)
		}
		if !d.HasCapability(CapabilityDecorate) {
			return fmt.Errorf("plugin %q decorations[%d] requires capability %q", d.ID, i, CapabilityDecorate)
		}
		if err := reserveID(hook.ID, fmt.Sprintf("decorations[%d]", i)); err != nil {
			return err
		}
	}
	for i, operation := range d.Operations {
		if err := operation.Validate(d.ID); err != nil {
			return fmt.Errorf("plugin %q operations[%d]: %w", d.ID, i, err)
		}
		if !d.HasCapability(operation.Capability) {
			return fmt.Errorf("plugin %q operations[%d] references undeclared capability %q", d.ID, i, operation.Capability)
		}
		if err := reserveID(operation.ID, fmt.Sprintf("operations[%d]", i)); err != nil {
			return err
		}
	}
	if d.HasCapability(CapabilityFormat) && len(d.Formatters) == 0 {
		return fmt.Errorf("plugin %q declares format capability but no formatter hooks", d.ID)
	}
	if d.HasCapability(CapabilityDecorate) && len(d.Decorations) == 0 {
		return fmt.Errorf("plugin %q declares decorate capability but no decoration hooks", d.ID)
	}
	return nil
}

func validateFormatterHook(h FormatterHook, pluginID string) error {
	if err := validateIdentifier("formatter id", h.ID); err != nil {
		return err
	}
	if err := validateText("formatter display name", h.DisplayName, true, maxMetadataNameBytes); err != nil {
		return err
	}
	if err := validateCommand(h.Command); err != nil {
		return fmt.Errorf("formatter %q: %w", h.ID, err)
	}
	if len(h.Args) > maxHookArgs {
		return fmt.Errorf("formatter %q args has %d entries; maximum is %d", h.ID, len(h.Args), maxHookArgs)
	}
	for i, arg := range h.Args {
		if err := validateText(fmt.Sprintf("formatter %q args[%d]", h.ID, i), arg, false, maxHookArgBytes); err != nil {
			return err
		}
	}
	if err := validatePatterns("formatter "+quoted(h.ID)+" file patterns", h.FilePatterns, false); err != nil {
		return err
	}
	if err := validateNotes("formatter "+quoted(h.ID)+" notes", h.Notes); err != nil {
		return err
	}
	if !h.Stdin && !h.Stdout && !h.RequiresTempCopy {
		return fmt.Errorf("formatter %q must declare stdin/stdout or a required temp copy", h.ID)
	}
	return nil
}

func validateSymbolHook(h SymbolHook, pluginID string) error {
	if err := validateStrategyHook("symbol hook", h.ID, h.DisplayName, h.Strategy, h.FilePatterns, h.MaxBytes, h.Notes); err != nil {
		return err
	}
	return validateValues("symbol hook "+quoted(h.ID)+" kinds", h.Kinds, maxHookValues, maxHookValueBytes, true, false)
}

func validateCompletionHook(h CompletionHook, pluginID string) error {
	if err := validateStrategyHook("completion hook", h.ID, h.DisplayName, h.Strategy, h.FilePatterns, h.MaxBytes, h.Notes); err != nil {
		return err
	}
	if err := validateValues("completion hook "+quoted(h.ID)+" sources", h.Sources, maxHookValues, maxHookValueBytes, true, false); err != nil {
		return err
	}
	if err := validateValues("completion hook "+quoted(h.ID)+" trigger characters", h.TriggerCharacters, maxHookValues, 16, true, false); err != nil {
		return err
	}
	for i, trigger := range h.TriggerCharacters {
		if utf8.RuneCountInString(trigger) != 1 {
			return fmt.Errorf("completion hook %q triggerCharacters[%d] must contain exactly one Unicode character", h.ID, i)
		}
	}
	return nil
}

func validateDecorationHook(h DecorationHook, pluginID string) error {
	return validateStrategyHook("decoration hook", h.ID, h.DisplayName, h.Strategy, h.FilePatterns, h.MaxBytes, h.Notes)
}

func validateStrategyHook(kind, id, displayName string, strategy HookStrategy, patterns []string, maxBytes int64, notes []string) error {
	if err := validateIdentifier(kind+" id", id); err != nil {
		return err
	}
	if err := validateText(kind+" "+quoted(id)+" display name", displayName, true, maxMetadataNameBytes); err != nil {
		return err
	}
	if !validHookStrategy(strategy) {
		return fmt.Errorf("%s %q has unknown strategy %q", kind, id, strategy)
	}
	if maxBytes < 0 || maxBytes > maxHookBytes {
		return fmt.Errorf("%s %q max bytes must be in [0, %d]", kind, id, maxHookBytes)
	}
	if strategyRequiresMaxBytes(strategy) && maxBytes == 0 {
		return fmt.Errorf("%s %q strategy %q requires a positive max bytes", kind, id, strategy)
	}
	if err := validatePatterns(kind+" "+quoted(id)+" file patterns", patterns, false); err != nil {
		return err
	}
	return validateNotes(kind+" "+quoted(id)+" notes", notes)
}

func (operation OperationCapability) Validate(pluginID string) error {
	if err := validateIdentifier("operation id", operation.ID); err != nil {
		return err
	}
	if !validCapability(operation.Capability) {
		return fmt.Errorf("operation %q has unknown capability %q", operation.ID, operation.Capability)
	}
	if !validProcessingModel(operation.Processing) {
		return fmt.Errorf("operation %q has unknown processing model %q", operation.ID, operation.Processing)
	}
	if !validMemoryModel(operation.Memory) {
		return fmt.Errorf("operation %q has unknown memory model %q", operation.ID, operation.Memory)
	}
	if operation.MaxInputBytes < 0 || operation.MaxInputBytes > maxOperationInput {
		return fmt.Errorf("operation %q max input bytes must be in [0, %d]", operation.ID, maxOperationInput)
	}
	if operation.MaxUnitBytes < 0 || operation.MaxUnitBytes > maxOperationUnit {
		return fmt.Errorf("operation %q max unit bytes must be in [0, %d]", operation.ID, maxOperationUnit)
	}
	if (operation.Processing == ProcessingBoundedSample || operation.Processing == ProcessingBoundedWindow) && operation.MaxInputBytes <= 0 {
		return fmt.Errorf("operation %q processing model %q requires a positive max input bytes", operation.ID, operation.Processing)
	}
	if operation.Processing == ProcessingConfigurableSample && operation.MaxInputBytes != 0 {
		return fmt.Errorf("operation %q configurable sample must not advertise a hard max input bytes", operation.ID)
	}
	switch operation.Processing {
	case ProcessingBoundedSample:
		if operation.Memory != MemoryBounded && operation.Memory != MemorySampleProportional {
			return fmt.Errorf("operation %q bounded sample has incompatible memory model %q", operation.ID, operation.Memory)
		}
	case ProcessingConfigurableSample:
		if operation.Memory != MemorySampleProportional {
			return fmt.Errorf("operation %q configurable sample must use memory model %q", operation.ID, MemorySampleProportional)
		}
	case ProcessingBoundedWindow:
		if operation.Memory != MemoryBounded {
			return fmt.Errorf("operation %q bounded window must use memory model %q", operation.ID, MemoryBounded)
		}
	case ProcessingStreaming:
		if operation.Memory == MemoryInputProportional || operation.Memory == MemoryCardinalityProportional {
			return fmt.Errorf("operation %q cannot call %q memory %q; declare materialized processing", operation.ID, operation.Processing, operation.Memory)
		}
	case ProcessingMaterialized:
		if operation.Memory == MemoryBounded {
			return fmt.Errorf("operation %q materialized processing must expose its proportional memory model", operation.ID)
		}
	}
	if operation.AtomicOutput {
		switch operation.Capability {
		case CapabilityTransform, CapabilityConvert, CapabilityExtract, CapabilityFormat:
		default:
			return fmt.Errorf("operation %q capability %q cannot advertise atomic output", operation.ID, operation.Capability)
		}
	}
	return validateNotes("operation "+quoted(operation.ID)+" notes", operation.Notes)
}

func validatePatterns(field string, patterns []string, required bool) error {
	if required && len(patterns) == 0 {
		return fmt.Errorf("%s must contain at least one filename pattern", field)
	}
	if len(patterns) > maxDescriptorPatterns {
		return fmt.Errorf("%s has %d entries; maximum is %d", field, len(patterns), maxDescriptorPatterns)
	}
	seen := make(map[string]int, len(patterns))
	for i, pattern := range patterns {
		if err := validateText(fmt.Sprintf("%s[%d]", field, i), pattern, true, maxMetadataPatternBytes); err != nil {
			return err
		}
		if strings.ContainsAny(pattern, `/\\:<>"|`) {
			return fmt.Errorf("%s[%d] %q is not a platform-independent filename-only pattern", field, i, pattern)
		}
		if _, err := path.Match(pattern, "filename"); err != nil {
			return fmt.Errorf("%s[%d] %q is invalid: %w", field, i, pattern, err)
		}
		key := strings.ToLower(pattern)
		if previous, exists := seen[key]; exists {
			return fmt.Errorf("%s[%d] duplicates pattern at index %d", field, i, previous)
		}
		seen[key] = i
	}
	return nil
}

func validateNotes(field string, notes []string) error {
	return validateValues(field, notes, maxMetadataNotes, maxMetadataNoteBytes, false, false)
}

func validateValues(field string, values []string, maxCount, maxBytes int, unique, allowEmpty bool) error {
	if len(values) > maxCount {
		return fmt.Errorf("%s has %d entries; maximum is %d", field, len(values), maxCount)
	}
	seen := make(map[string]int, len(values))
	for i, value := range values {
		if err := validateText(fmt.Sprintf("%s[%d]", field, i), value, !allowEmpty, maxBytes); err != nil {
			return err
		}
		if unique {
			if previous, exists := seen[value]; exists {
				return fmt.Errorf("%s[%d] duplicates index %d", field, i, previous)
			}
			seen[value] = i
		}
	}
	return nil
}

func validateText(field, value string, required bool, maxBytes int) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s is not valid UTF-8", field)
	}
	if value != strings.TrimSpace(value) {
		return fmt.Errorf("%s has leading or trailing whitespace", field)
	}
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%s is %d bytes; maximum is %d", field, len(value), maxBytes)
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%s contains a control character", field)
		}
	}
	return nil
}

func validateIdentifier(field, value string) error {
	if err := validateText(field, value, true, maxMetadataIDBytes); err != nil {
		return err
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9') || (i > 0 && r == '-') {
			continue
		}
		return fmt.Errorf("%s %q must use lowercase ASCII letters, digits, and interior hyphens", field, value)
	}
	if strings.HasSuffix(value, "-") || strings.Contains(value, "--") {
		return fmt.Errorf("%s %q has an invalid hyphen placement", field, value)
	}
	return nil
}

func validateCommand(command string) error {
	if err := validateText("formatter command", command, true, maxFormatterCommandBytes); err != nil {
		return err
	}
	for i, r := range command {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (i > 0 && r >= '0' && r <= '9') || (i > 0 && strings.ContainsRune("._+-", r)) {
			continue
		}
		return fmt.Errorf("formatter command %q must be one executable name without a path, whitespace, or shell syntax", command)
	}
	return nil
}

func validMode(mode Mode) bool {
	return mode == ModeInteractive || mode == ModeStreaming
}

func validCapability(capability Capability) bool {
	switch capability {
	case CapabilitySyntax, CapabilityAnalyze, CapabilityTransform, CapabilityConvert, CapabilityFormat,
		CapabilityAutocomplete, CapabilityNavigate, CapabilityExtract, CapabilityValidate, CapabilityDecorate:
		return true
	default:
		return false
	}
}

func validHookStrategy(strategy HookStrategy) bool {
	switch strategy {
	case StrategyBoundedLexicalScan, StrategyStreamingTableAnalyzer, StrategyBoundedIndentationScan,
		StrategyBoundedSymbolSuggestions, StrategyTableAnalyzerSuggestions, StrategyBoundedBracketStack:
		return true
	default:
		return false
	}
}

func strategyRequiresMaxBytes(strategy HookStrategy) bool {
	switch strategy {
	case StrategyBoundedLexicalScan, StrategyBoundedIndentationScan, StrategyBoundedSymbolSuggestions, StrategyBoundedBracketStack:
		return true
	default:
		return false
	}
}

func validProcessingModel(model ProcessingModel) bool {
	switch model {
	case ProcessingBoundedSample, ProcessingConfigurableSample, ProcessingBoundedWindow, ProcessingStreaming, ProcessingMaterialized:
		return true
	default:
		return false
	}
}

func validMemoryModel(model MemoryModel) bool {
	switch model {
	case MemoryBounded, MemorySampleProportional, MemoryRecordProportional, MemoryStatementProportional,
		MemoryCardinalityProportional, MemoryBatchProportional, MemoryMetadataProportional, MemoryInputProportional:
		return true
	default:
		return false
	}
}

func quoted(value string) string {
	return fmt.Sprintf("%q", value)
}
