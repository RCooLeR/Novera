package csv

import (
	"context"
	"fmt"
	"io"

	"novera/internal/bigfile/plugins"
)

type Runtime interface {
	plugins.RuntimePlugin
	InspectReader(io.Reader, InspectOptions) (DelimiterReport, error)
	InspectReaderContext(context.Context, io.Reader, InspectOptions) (DelimiterReport, error)
	InferSchema(io.Reader, SchemaOptions) (SchemaReport, error)
	InferSchemaContext(context.Context, io.Reader, SchemaOptions) (SchemaReport, error)
	PreviewRows(io.Reader, PreviewOptions) (PreviewReport, error)
	PreviewRowsContext(context.Context, io.Reader, PreviewOptions) (PreviewReport, error)
	BuildColumnGuide(io.Reader, ColumnGuideOptions) (ColumnGuideReport, error)
	BuildColumnGuideContext(context.Context, io.Reader, ColumnGuideOptions) (ColumnGuideReport, error)
	PreviewProjectedColumns(io.Reader, ProjectPreviewOptions) (ProjectPreviewReport, error)
	PreviewProjectedColumnsContext(context.Context, io.Reader, ProjectPreviewOptions) (ProjectPreviewReport, error)
	ProjectColumns(context.Context, io.Reader, io.Writer, ProjectOptions) (ProjectSummary, error)
	ProjectColumnsFile(context.Context, string, string, ProjectOptions) (ProjectSummary, error)
	AddColumn(context.Context, io.Reader, io.Writer, AddColumnOptions) (AddColumnSummary, error)
	AddColumnFile(context.Context, string, string, AddColumnOptions) (AddColumnSummary, error)
	PreviewSQLConversion(io.Reader, SQLPreviewOptions) (SQLPreviewReport, error)
	PreviewSQLConversionContext(context.Context, io.Reader, SQLPreviewOptions) (SQLPreviewReport, error)
	ConvertToSQL(context.Context, io.Reader, io.Writer, SQLConvertOptions) (SQLConvertSummary, error)
	ConvertToSQLFile(context.Context, string, string, SQLConvertOptions) (SQLConvertSummary, error)
}

type BuiltIn struct{}

func RuntimePlugin() BuiltIn {
	return BuiltIn{}
}

func (BuiltIn) Descriptor() plugins.Descriptor {
	return Plugin()
}

func (BuiltIn) InspectReader(r io.Reader, opts InspectOptions) (DelimiterReport, error) {
	return InspectReader(r, opts)
}

func (BuiltIn) InspectReaderContext(ctx context.Context, r io.Reader, opts InspectOptions) (DelimiterReport, error) {
	return InspectReaderContext(ctx, r, opts)
}

func (BuiltIn) InferSchema(r io.Reader, opts SchemaOptions) (SchemaReport, error) {
	return InferSchema(r, opts)
}

func (BuiltIn) InferSchemaContext(ctx context.Context, r io.Reader, opts SchemaOptions) (SchemaReport, error) {
	return InferSchemaContext(ctx, r, opts)
}

func (BuiltIn) PreviewRows(r io.Reader, opts PreviewOptions) (PreviewReport, error) {
	return PreviewRows(r, opts)
}

func (BuiltIn) PreviewRowsContext(ctx context.Context, r io.Reader, opts PreviewOptions) (PreviewReport, error) {
	return PreviewRowsContext(ctx, r, opts)
}

func (BuiltIn) BuildColumnGuide(r io.Reader, opts ColumnGuideOptions) (ColumnGuideReport, error) {
	return BuildColumnGuide(r, opts)
}

func (BuiltIn) BuildColumnGuideContext(ctx context.Context, r io.Reader, opts ColumnGuideOptions) (ColumnGuideReport, error) {
	return BuildColumnGuideContext(ctx, r, opts)
}

func (BuiltIn) PreviewProjectedColumns(r io.Reader, opts ProjectPreviewOptions) (ProjectPreviewReport, error) {
	return PreviewProjectedColumns(r, opts)
}

func (BuiltIn) PreviewProjectedColumnsContext(ctx context.Context, r io.Reader, opts ProjectPreviewOptions) (ProjectPreviewReport, error) {
	return PreviewProjectedColumnsContext(ctx, r, opts)
}

func (BuiltIn) ProjectColumns(ctx context.Context, r io.Reader, w io.Writer, opts ProjectOptions) (ProjectSummary, error) {
	return ProjectColumns(ctx, r, w, opts)
}

func (BuiltIn) ProjectColumnsFile(ctx context.Context, inputPath string, outputPath string, opts ProjectOptions) (ProjectSummary, error) {
	return ProjectColumnsFile(ctx, inputPath, outputPath, opts)
}

func (BuiltIn) AddColumn(ctx context.Context, r io.Reader, w io.Writer, opts AddColumnOptions) (AddColumnSummary, error) {
	return AddColumn(ctx, r, w, opts)
}

func (BuiltIn) AddColumnFile(ctx context.Context, inputPath string, outputPath string, opts AddColumnOptions) (AddColumnSummary, error) {
	return AddColumnFile(ctx, inputPath, outputPath, opts)
}

func (BuiltIn) PreviewSQLConversion(r io.Reader, opts SQLPreviewOptions) (SQLPreviewReport, error) {
	return PreviewSQLConversion(r, opts)
}

func (BuiltIn) PreviewSQLConversionContext(ctx context.Context, r io.Reader, opts SQLPreviewOptions) (SQLPreviewReport, error) {
	return PreviewSQLConversionContext(ctx, r, opts)
}

func (BuiltIn) ConvertToSQL(ctx context.Context, r io.Reader, w io.Writer, opts SQLConvertOptions) (SQLConvertSummary, error) {
	return ConvertToSQL(ctx, r, w, opts)
}

func (BuiltIn) ConvertToSQLFile(ctx context.Context, inputPath string, outputPath string, opts SQLConvertOptions) (SQLConvertSummary, error) {
	return ConvertToSQLFile(ctx, inputPath, outputPath, opts)
}

func Plugin() plugins.Descriptor {
	recordLimitMiB := MaxLogicalRecordBytes / (1024 * 1024)
	sampleLimitMiB := MaxSampleBytes / (1024 * 1024)
	sqlBatchLimitMiB := MaxSQLInsertBatchBytes / (1024 * 1024)
	return plugins.Descriptor{
		ID:           "csv",
		DisplayName:  "CSV / TSV",
		Category:     "data",
		Description:  "Delimited-file inspection, column removal/reordering, CSV/TSV cleanup, and CSV/TSV-to-SQL conversion workflows.",
		FilePatterns: plugins.CSVPatterns(),
		Capabilities: []plugins.Capability{
			plugins.CapabilityAnalyze,
			plugins.CapabilityTransform,
			plugins.CapabilityConvert,
			plugins.CapabilityValidate,
		},
		Operations: []plugins.OperationCapability{
			{
				ID: "inspect-sample", Capability: plugins.CapabilityAnalyze,
				Processing: plugins.ProcessingConfigurableSample, Memory: plugins.MemorySampleProportional,
				MaxUnitBytes: MaxSampleBytes, Cancellable: true,
				Notes: []string{fmt.Sprintf("Caller-selected samples are capped at %d MiB and %d rows; each logical CSV record has a %d MiB hard ceiling.", sampleLimitMiB, MaxSampleRows, recordLimitMiB)},
			},
			{
				ID: "infer-schema-sample", Capability: plugins.CapabilityAnalyze,
				Processing: plugins.ProcessingConfigurableSample, Memory: plugins.MemorySampleProportional,
				MaxUnitBytes: MaxSampleBytes, Cancellable: true,
				Notes: []string{fmt.Sprintf("Schema inference is capped at %d MiB and %d rows plus a %d MiB logical-record ceiling and %d-field ceiling.", sampleLimitMiB, MaxSampleRows, recordLimitMiB, MaxCSVFieldsPerRecord)},
			},
			{
				ID: "preview-rows", Capability: plugins.CapabilityValidate,
				Processing: plugins.ProcessingConfigurableSample, Memory: plugins.MemorySampleProportional,
				MaxUnitBytes: MaxSampleBytes, Cancellable: true,
				Notes: []string{fmt.Sprintf("Preview samples are capped at %d MiB and %d rows; each logical CSV record has a %d MiB hard ceiling.", sampleLimitMiB, MaxSampleRows, recordLimitMiB)},
			},
			{
				ID: "project-columns-file", Capability: plugins.CapabilityTransform,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryRecordProportional,
				MaxUnitBytes: MaxLogicalRecordBytes, Cancellable: true, AtomicOutput: true,
				Notes: []string{fmt.Sprintf("Streaming is record-at-a-time with %d-field and %d MiB logical input/output record ceilings and staged no-clobber publication.", MaxCSVFieldsPerRecord, recordLimitMiB)},
			},
			{
				ID: "add-column-file", Capability: plugins.CapabilityTransform,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryRecordProportional,
				MaxUnitBytes: MaxLogicalRecordBytes, Cancellable: true, AtomicOutput: true,
				Notes: []string{fmt.Sprintf("Streaming is record-at-a-time with a %d-field and %d MiB encoded output-record ceiling and staged no-clobber publication.", MaxCSVFieldsPerRecord, recordLimitMiB)},
			},
			{
				ID: "deduplicate-rows", Capability: plugins.CapabilityTransform,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryBounded,
				MaxUnitBytes: MaxLogicalRecordBytes, Cancellable: true, AtomicOutput: true,
				Notes: []string{fmt.Sprintf("Exact deduplication defaults to %d distinct keys and %d MiB retained memory, refuses before either budget is exceeded, and cannot be configured above %d keys or %d MiB; disk-backed deduplication is unavailable.", DefaultDedupeMaxDistinctKeys, DefaultDedupeMaxMemoryBytes/(1024*1024), MaxDedupeDistinctKeys, MaxDedupeMemoryBytes/(1024*1024))},
			},
			{
				ID: "convert-to-sql-file", Capability: plugins.CapabilityConvert,
				Processing: plugins.ProcessingStreaming, Memory: plugins.MemoryBatchProportional,
				MaxUnitBytes: MaxSQLInsertBatchBytes, Cancellable: true, AtomicOutput: true,
				Notes: []string{fmt.Sprintf("CSV logical records are capped at %d MiB; SQL insert batches are capped at %d rows and %d MiB and are published through a staged no-clobber output.", recordLimitMiB, MaxSQLInsertBatchSize, sqlBatchLimitMiB)},
			},
		},
		Modes: []plugins.Mode{plugins.ModeInteractive, plugins.ModeStreaming},
		Notes: []string{
			"Safety is declared per operation; no plugin-wide huge-file-safety promise is made.",
		},
	}
}
