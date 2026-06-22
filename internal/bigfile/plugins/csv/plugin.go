package csv

import (
	"context"
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
	return plugins.Descriptor{
		ID:           "csv",
		DisplayName:  "CSV / TSV",
		Category:     "data",
		Description:  "Delimited-file inspection, column removal/reordering, CSV/TSV cleanup, and CSV/TSV-to-SQL conversion workflows.",
		FilePatterns: plugins.CSVFilePatterns,
		Capabilities: []plugins.Capability{
			plugins.CapabilityAnalyze,
			plugins.CapabilityTransform,
			plugins.CapabilityConvert,
			plugins.CapabilityValidate,
		},
		Modes:        []plugins.Mode{plugins.ModeInteractive, plugins.ModeStreaming},
		HugeFileSafe: true,
	}
}
