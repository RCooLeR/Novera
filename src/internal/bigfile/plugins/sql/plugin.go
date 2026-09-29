package sql

import (
	"context"

	"novera/internal/bigfile/highlight"
	"novera/internal/bigfile/plugins"
	analyzepkg "novera/internal/bigfile/plugins/sql/analyze"
	extractpkg "novera/internal/bigfile/plugins/sql/extract"
	sqlhighlight "novera/internal/bigfile/plugins/sql/highlight"
	presetpkg "novera/internal/bigfile/plugins/sql/preset"
	replacepkg "novera/internal/bigfile/replace"
)

type ReaderAtSize = analyzepkg.ReaderAtSize
type AnalyzeOptions = analyzepkg.Options
type AnalyzeProgress = analyzepkg.Progress
type Summary = analyzepkg.Summary
type Table = analyzepkg.Table
type ExtractPlanOptions = extractpkg.PlanOptions
type ExtractTableRange = extractpkg.TableRange
type ExtractManifestPreview = extractpkg.ManifestPreview
type ExtractWriteOptions = extractpkg.WriteOptions
type ExtractWriteSummary = extractpkg.WriteSummary

type PresetMode = presetpkg.Mode
type PresetConfig = presetpkg.Config

const (
	PresetModePlain = presetpkg.ModePlain
	PresetModeRegex = presetpkg.ModeRegex
	PresetModeBatch = presetpkg.ModeBatch

	RemoveDefinerPreset       = presetpkg.RemoveDefinerPreset
	ChangeDatabasePreset      = presetpkg.ChangeDatabasePreset
	ChangeCharsetPreset       = presetpkg.ChangeCharsetPreset
	ConvertEnginePreset       = presetpkg.ConvertEnginePreset
	RemoveAutoIncrementPreset = presetpkg.RemoveAutoIncrementPreset
)

var PresetNames = append([]string(nil), presetpkg.PresetNames...)

type TokenKind = highlight.TokenKind
type Token = highlight.Token

const (
	TokenText       = highlight.TokenText
	TokenKeyword    = highlight.TokenKeyword
	TokenString     = highlight.TokenString
	TokenComment    = highlight.TokenComment
	TokenNumber     = highlight.TokenNumber
	TokenIdentifier = highlight.TokenIdentifier
	TokenOperator   = highlight.TokenOperator
)

type Runtime interface {
	plugins.RuntimePlugin
	Analyze(context.Context, ReaderAtSize, AnalyzeOptions) (Summary, error)
	AnalyzeFile(context.Context, string, AnalyzeOptions) (Summary, error)
	BuildPreset(name string, arg1 string, arg2 string, arg3 string, arg4 string) (PresetConfig, error)
	HighlightVisible(text string) []Token
	BatchRules(PresetConfig) []replacepkg.BatchRule
	SplitByTablePreview(Summary, int64, ExtractPlanOptions) (ExtractManifestPreview, error)
	ExtractTablePreview(Summary, int64, string, ExtractPlanOptions) (ExtractManifestPreview, error)
	SplitByTable(context.Context, ReaderAtSize, string, Summary, ExtractWriteOptions) (ExtractWriteSummary, error)
	ExtractTable(context.Context, ReaderAtSize, string, Summary, string, ExtractWriteOptions) (ExtractWriteSummary, error)
}

type BuiltIn struct{}

func RuntimePlugin() BuiltIn {
	return BuiltIn{}
}

func (BuiltIn) Descriptor() plugins.Descriptor {
	return Plugin()
}

func (BuiltIn) Analyze(ctx context.Context, r ReaderAtSize, opts AnalyzeOptions) (Summary, error) {
	return Analyze(ctx, r, opts)
}

func (BuiltIn) AnalyzeFile(ctx context.Context, path string, opts AnalyzeOptions) (Summary, error) {
	return AnalyzeFile(ctx, path, opts)
}

func (BuiltIn) BuildPreset(name string, arg1 string, arg2 string, arg3 string, arg4 string) (PresetConfig, error) {
	return BuildPreset(name, arg1, arg2, arg3, arg4)
}

func (BuiltIn) HighlightVisible(text string) []Token {
	return HighlightVisible(text)
}

func (BuiltIn) BatchRules(cfg PresetConfig) []replacepkg.BatchRule {
	return BatchRules(cfg)
}

func (BuiltIn) SplitByTablePreview(summary Summary, sourceSize int64, opts ExtractPlanOptions) (ExtractManifestPreview, error) {
	return SplitByTablePreview(summary, sourceSize, opts)
}

func (BuiltIn) ExtractTablePreview(summary Summary, sourceSize int64, tableName string, opts ExtractPlanOptions) (ExtractManifestPreview, error) {
	return ExtractTablePreview(summary, sourceSize, tableName, opts)
}

func (BuiltIn) SplitByTable(ctx context.Context, doc ReaderAtSize, sourcePath string, summary Summary, opts ExtractWriteOptions) (ExtractWriteSummary, error) {
	return SplitByTable(ctx, doc, sourcePath, summary, opts)
}

func (BuiltIn) ExtractTable(ctx context.Context, doc ReaderAtSize, sourcePath string, summary Summary, tableName string, opts ExtractWriteOptions) (ExtractWriteSummary, error) {
	return ExtractTable(ctx, doc, sourcePath, summary, tableName, opts)
}

func Plugin() plugins.Descriptor {
	const (
		visibleWindowLimit   = int64(8 << 20)
		reshapeUnitLimit     = int64(64 << 20)
		fixtureSampleLimit   = int64(8 << 20)
		schemaStatementLimit = int64(2 << 20)
	)
	return plugins.Descriptor{
		ID:           "sql",
		DisplayName:  "SQL Dumps",
		Category:     "data",
		Description:  "SQL dump highlighting, analysis, navigation, reshape, fixture, schema-diff, and table extraction/splitting tools.",
		FilePatterns: plugins.SQLPatterns(),
		Capabilities: []plugins.Capability{
			plugins.CapabilitySyntax,
			plugins.CapabilityAnalyze,
			plugins.CapabilityTransform,
			plugins.CapabilityNavigate,
			plugins.CapabilityExtract,
		},
		Operations: []plugins.OperationCapability{
			{
				ID: "highlight-visible", Capability: plugins.CapabilitySyntax,
				Processing: plugins.ProcessingBoundedWindow, Memory: plugins.MemoryBounded,
				MaxInputBytes: visibleWindowLimit,
				Notes:         []string{"Highlighting consumes only the bounded visible window supplied by the editor."},
			},
			{
				ID: "analyze-dump", Capability: plugins.CapabilityAnalyze,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryMetadataProportional,
				Cancellable: true,
				Notes:       []string{"Input scanning is streaming; retained table and statement metadata grows with dump cardinality."},
			},
			{
				ID: "navigate-tables", Capability: plugins.CapabilityNavigate,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryMetadataProportional,
				Cancellable: true,
				Notes:       []string{"Navigation uses analyzer metadata and inherits its cardinality-proportional retention."},
			},
			{
				ID: "reshape-inserts", Capability: plugins.CapabilityTransform,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryStatementProportional,
				MaxUnitBytes: reshapeUnitLimit, Cancellable: true, AtomicOutput: true,
				Notes: []string{"The integrated tool stages output and caps one parsed statement at 64 MiB; batching is additionally capped at 10,000 rows and 8 MiB."},
			},
			{
				ID: "extract-tables", Capability: plugins.CapabilityExtract,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryMetadataProportional,
				Cancellable: true,
				Notes:       []string{"Table ranges stream from the source, but split extraction is a multi-output operation and is not all-or-none; committed outputs can remain if a later output or manifest fails."},
			},
			{
				ID: "fixture-sample", Capability: plugins.CapabilityExtract,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemorySampleProportional,
				MaxUnitBytes: fixtureSampleLimit, Cancellable: true, AtomicOutput: true,
				Notes: []string{"Fixture generation streams table ranges and materializes at most 8 MiB of INSERT input per table, returns at most 10,000 rows per table, and refuses when a complete requested table sample cannot be proven inside that per-table cap."},
			},
			{
				ID: "schema-diff", Capability: plugins.CapabilityAnalyze,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryMetadataProportional,
				MaxUnitBytes: schemaStatementLimit, Cancellable: true,
				Notes: []string{"Dump discovery is streaming; each CREATE TABLE statement is capped at 2 MiB while retained table/column metadata grows with schema cardinality."},
			},
		},
		Modes: []plugins.Mode{plugins.ModeInteractive, plugins.ModeStreaming},
		Notes: []string{
			"Safety is declared per operation; no plugin-wide huge-file-safety promise is made.",
			"Cleanup presets are not advertised because service execution remains disabled until token-aware transformations preserve structural SQL and serialized values.",
		},
	}
}

func Analyze(ctx context.Context, r ReaderAtSize, opts AnalyzeOptions) (Summary, error) {
	return analyzepkg.Analyze(ctx, r, opts)
}

func AnalyzeFile(ctx context.Context, path string, opts AnalyzeOptions) (Summary, error) {
	return analyzepkg.AnalyzeFile(ctx, path, opts)
}

func BuildPreset(name string, arg1 string, arg2 string, arg3 string, arg4 string) (PresetConfig, error) {
	return presetpkg.Build(name, arg1, arg2, arg3, arg4)
}

func HighlightVisible(text string) []Token {
	return sqlhighlight.SQLVisible(text)
}

// BatchRules returns the replacement rules for callers that need an explicit
// replace package type while keeping preset construction owned by the SQL plugin.
func BatchRules(cfg PresetConfig) []replacepkg.BatchRule {
	if cfg.BatchRules == nil {
		return nil
	}
	rules := make([]replacepkg.BatchRule, len(cfg.BatchRules))
	for i, rule := range cfg.BatchRules {
		rules[i] = rule
		rules[i].Find = append([]byte(nil), rule.Find...)
		rules[i].Replace = append([]byte(nil), rule.Replace...)
	}
	return rules
}

func SplitByTablePreview(summary Summary, sourceSize int64, opts ExtractPlanOptions) (ExtractManifestPreview, error) {
	return extractpkg.SplitByTablePreview(summary, sourceSize, opts)
}

func ExtractTablePreview(summary Summary, sourceSize int64, tableName string, opts ExtractPlanOptions) (ExtractManifestPreview, error) {
	return extractpkg.ExtractTablePreview(summary, sourceSize, tableName, opts)
}

func SplitByTable(ctx context.Context, doc ReaderAtSize, sourcePath string, summary Summary, opts ExtractWriteOptions) (ExtractWriteSummary, error) {
	return extractpkg.SplitByTable(ctx, doc, sourcePath, summary, opts)
}

func ExtractTable(ctx context.Context, doc ReaderAtSize, sourcePath string, summary Summary, tableName string, opts ExtractWriteOptions) (ExtractWriteSummary, error) {
	return extractpkg.ExtractTable(ctx, doc, sourcePath, summary, tableName, opts)
}
