package plugins

// HookStrategy identifies a built-in, bounded hook implementation. Dynamic
// descriptors may select only strategies supported by the application.
type HookStrategy string

const (
	StrategyBoundedLexicalScan       HookStrategy = "bounded lexical scan"
	StrategyStreamingTableAnalyzer   HookStrategy = "streaming table analyzer"
	StrategyBoundedIndentationScan   HookStrategy = "bounded indentation-aware scan"
	StrategyBoundedSymbolSuggestions HookStrategy = "bounded symbol and keyword suggestions"
	StrategyTableAnalyzerSuggestions HookStrategy = "table analyzer suggestions"
	StrategyBoundedBracketStack      HookStrategy = "bounded bracket stack"
)

// ProcessingModel states how much source input an operation consumes. Sampled
// operations distinguish a hard application cap from a caller-selected cap.
type ProcessingModel string

const (
	ProcessingBoundedSample      ProcessingModel = "bounded-sample"
	ProcessingConfigurableSample ProcessingModel = "configurable-sample"
	ProcessingBoundedWindow      ProcessingModel = "bounded-window"
	ProcessingStreaming          ProcessingModel = "streaming"
	ProcessingMaterialized       ProcessingModel = "materialized"
)

// MemoryModel states what input property can grow retained working memory.
type MemoryModel string

const (
	MemoryBounded                 MemoryModel = "bounded"
	MemorySampleProportional      MemoryModel = "sample-proportional"
	MemoryRecordProportional      MemoryModel = "record-proportional"
	MemoryStatementProportional   MemoryModel = "statement-proportional"
	MemoryCardinalityProportional MemoryModel = "cardinality-proportional"
	MemoryBatchProportional       MemoryModel = "batch-proportional"
	MemoryMetadataProportional    MemoryModel = "metadata-proportional"
	MemoryInputProportional       MemoryModel = "input-proportional"
)

// OperationCapability describes one concrete operation rather than making a
// plugin-wide safety promise. A zero MaxInputBytes or MaxUnitBytes means that
// no hard application limit is advertised for that dimension.
type OperationCapability struct {
	ID            string          `json:"id"`
	Capability    Capability      `json:"capability"`
	Processing    ProcessingModel `json:"processing"`
	Memory        MemoryModel     `json:"memory"`
	MaxInputBytes int64           `json:"maxInputBytes,omitempty"`
	MaxUnitBytes  int64           `json:"maxUnitBytes,omitempty"`
	Cancellable   bool            `json:"cancellable"`
	AtomicOutput  bool            `json:"atomicOutput"`
	Notes         []string        `json:"notes,omitempty"`
}

// Clone returns a descriptor whose slices and all nested slices have distinct
// backing storage.
func (d Descriptor) Clone() Descriptor {
	out := d
	out.FilePatterns = cloneStrings(d.FilePatterns)
	out.Capabilities = append([]Capability(nil), d.Capabilities...)
	out.Modes = append([]Mode(nil), d.Modes...)
	out.Notes = cloneStrings(d.Notes)
	if d.Operations != nil {
		out.Operations = make([]OperationCapability, len(d.Operations))
		for i, operation := range d.Operations {
			out.Operations[i] = operation
			out.Operations[i].Notes = cloneStrings(operation.Notes)
		}
	}
	if d.Formatters != nil {
		out.Formatters = make([]FormatterHook, len(d.Formatters))
		for i, hook := range d.Formatters {
			out.Formatters[i] = cloneFormatterHook(hook)
		}
	}
	if d.Symbols != nil {
		out.Symbols = make([]SymbolHook, len(d.Symbols))
		for i, hook := range d.Symbols {
			out.Symbols[i] = cloneSymbolHook(hook)
		}
	}
	if d.Completions != nil {
		out.Completions = make([]CompletionHook, len(d.Completions))
		for i, hook := range d.Completions {
			out.Completions[i] = cloneCompletionHook(hook)
		}
	}
	if d.Decorations != nil {
		out.Decorations = make([]DecorationHook, len(d.Decorations))
		for i, hook := range d.Decorations {
			out.Decorations[i] = cloneDecorationHook(hook)
		}
	}
	return out
}

func cloneFormatterHook(h FormatterHook) FormatterHook {
	out := h
	out.Args = cloneStrings(h.Args)
	out.FilePatterns = cloneStrings(h.FilePatterns)
	out.Notes = cloneStrings(h.Notes)
	return out
}

func cloneSymbolHook(h SymbolHook) SymbolHook {
	out := h
	out.FilePatterns = cloneStrings(h.FilePatterns)
	out.Kinds = cloneStrings(h.Kinds)
	out.Notes = cloneStrings(h.Notes)
	return out
}

func cloneCompletionHook(h CompletionHook) CompletionHook {
	out := h
	out.FilePatterns = cloneStrings(h.FilePatterns)
	out.Sources = cloneStrings(h.Sources)
	out.TriggerCharacters = cloneStrings(h.TriggerCharacters)
	out.Notes = cloneStrings(h.Notes)
	return out
}

func cloneDecorationHook(h DecorationHook) DecorationHook {
	out := h
	out.FilePatterns = cloneStrings(h.FilePatterns)
	out.Notes = cloneStrings(h.Notes)
	return out
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}
