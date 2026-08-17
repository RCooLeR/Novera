package bigfile

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/plugins/csv"
)

func TestCSVServiceSamplesDecodeDocumentEncoding(t *testing.T) {
	t.Parallel()
	const content = "id,note\r\n1,\u041f\u0440\u0438\u0432\u0435\u0442\r\n2,\u043c\u0438\u0440\r\n"
	tests := []struct {
		name     string
		encoding string
		bom      bool
	}{
		{name: "UTF-8 BOM", encoding: "UTF-8", bom: true},
		{name: "UTF-16LE BOM", encoding: "UTF-16LE", bom: true},
		{name: "UTF-16BE BOM", encoding: "UTF-16BE", bom: true},
		{name: "UTF-16LE no BOM", encoding: "UTF-16LE"},
		{name: "UTF-16BE no BOM", encoding: "UTF-16BE"},
		{name: "Windows-1251", encoding: "Windows-1251"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := writeCSVEncodingFixture(t, test.encoding, test.bom, content)
			service := NewFileService()
			meta, err := service.OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer service.CloseFile(meta.FileID)
			if !strings.EqualFold(meta.Encoding, test.encoding) {
				t.Fatalf("detected encoding = %q, want %q", meta.Encoding, test.encoding)
			}

			inspection, err := service.CsvInspect(meta.FileID)
			if err != nil {
				t.Fatalf("CsvInspect() error = %v", err)
			}
			if inspection.Generation == 0 || inspection.Delimiter != "," || inspection.Columns != 2 {
				t.Fatalf("inspection = %+v, want nonzero generation and comma/two columns", inspection)
			}
			preview, err := service.CsvPreview(meta.FileID, ",", true, 10)
			if err != nil {
				t.Fatalf("CsvPreview() error = %v", err)
			}
			if preview.Generation != inspection.Generation {
				t.Fatalf("preview generation = %d, want %d", preview.Generation, inspection.Generation)
			}
			if !reflect.DeepEqual(preview.Header, []string{"id", "note"}) {
				t.Fatalf("header = %#v", preview.Header)
			}
			wantRows := [][]string{{"1", "\u041f\u0440\u0438\u0432\u0435\u0442"}, {"2", "\u043c\u0438\u0440"}}
			if !reflect.DeepEqual(preview.Rows, wantRows) {
				t.Fatalf("rows = %#v, want %#v", preview.Rows, wantRows)
			}
		})
	}
}

func TestGetCSVGridUsesParserConfirmedRawCursors(t *testing.T) {
	t.Parallel()
	const content = "id,note\n1,\"line one\nline two\"\n2,tail\n"
	path := writeCSVEncodingFixture(t, "UTF-8", false, content)
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	first, err := service.GetCsvGrid(meta.FileID, ",", 0, len("id,note\n1,\"line one\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first.Rows, [][]string{{"id", "note"}}) {
		t.Fatalf("first rows = %#v", first.Rows)
	}
	if first.StartByte != 0 || first.NextByte != int64(len("id,note\n")) {
		t.Fatalf("first cursor = [%d,%d)", first.StartByte, first.NextByte)
	}
	if first.Generation == 0 || first.FileID != meta.FileID || first.Encoding != "UTF-8" {
		t.Fatalf("first identity = %+v", first)
	}

	second, err := service.GetCsvGrid(meta.FileID, ",", first.NextByte, 128)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"1", "line one\nline two"}, {"2", "tail"}}
	if !reflect.DeepEqual(second.Rows, want) {
		t.Fatalf("second rows = %#v, want %#v", second.Rows, want)
	}
	if second.StartByte != first.NextByte || second.NextByte != int64(len(content)) || !second.AtEof {
		t.Fatalf("second cursor = %+v", second)
	}
}

func TestGetCSVGridRejectsUnissuedWrongDelimiterAndStaleGenerationCursors(t *testing.T) {
	t.Parallel()
	const content = "id,note\n1,\"line one\nline two\"\n2,tail\n"
	path := writeCSVEncodingFixture(t, "UTF-8", false, content)
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	first, err := service.GetCsvGrid(meta.FileID, ",", 0, len("id,note\n1,\"line one\n"))
	if err != nil {
		t.Fatal(err)
	}
	midRecord := int64(strings.Index(content, "line one") + 2)
	if _, err := service.GetCsvGrid(meta.FileID, ",", midRecord, 128); !errors.Is(err, ErrCSVGridCursorInvalid) {
		t.Fatalf("mid-record cursor error = %v, want ErrCSVGridCursorInvalid", err)
	}
	if _, err := service.GetCsvGrid(meta.FileID, ";", first.NextByte, 128); !errors.Is(err, ErrCSVGridCursorInvalid) {
		t.Fatalf("wrong-delimiter cursor error = %v, want ErrCSVGridCursorInvalid", err)
	}
	if _, err := service.RefreshFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetCsvGrid(meta.FileID, ",", first.NextByte, 128); !errors.Is(err, ErrCSVGridCursorInvalid) {
		t.Fatalf("stale-generation cursor error = %v, want ErrCSVGridCursorInvalid", err)
	}
}

func TestGetCSVGridMapsUTF16BOMAndSurrogateSeamsToRawOffsets(t *testing.T) {
	t.Parallel()
	const content = "id,note\n1,\"\U0001f600 first\nsecond\"\n2,tail\n"
	path := writeCSVEncodingFixture(t, "UTF-16LE", true, content)
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	headerRawEnd := int64(2 + 2*len("id,note\n"))
	budget := int(headerRawEnd + int64(2*len("1,\"")+2))
	first, err := service.GetCsvGrid(meta.FileID, ",", 0, budget)
	if err != nil {
		t.Fatal(err)
	}
	if first.NextByte != headerRawEnd || !reflect.DeepEqual(first.Rows, [][]string{{"id", "note"}}) {
		t.Fatalf("UTF-16 first page = %+v", first)
	}
	second, err := service.GetCsvGrid(meta.FileID, ",", first.NextByte, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Rows) != 2 || second.Rows[0][1] != "\U0001f600 first\nsecond" || second.NextByte != meta.Size {
		t.Fatalf("UTF-16 second page = %+v", second)
	}
}

func TestGetCSVGridBOMOnlyAndShapeCaps(t *testing.T) {
	t.Parallel()
	for _, encoding := range []string{"UTF-8", "UTF-16LE", "UTF-16BE"} {
		encoding := encoding
		t.Run(encoding, func(t *testing.T) {
			t.Parallel()
			path := writeCSVEncodingFixture(t, encoding, true, "")
			service := NewFileService()
			meta, err := service.OpenFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer service.CloseFile(meta.FileID)
			bomBytes := len(encodingx.BOMBytes(encoding))
			if _, err := service.GetCsvGrid(meta.FileID, ",", 0, bomBytes-1); err == nil {
				t.Fatal("budget splitting the BOM unexpectedly returned a repeatable cursor")
			}
			grid, err := service.GetCsvGrid(meta.FileID, ",", 0, bomBytes)
			if err != nil {
				t.Fatal(err)
			}
			if grid.NextByte != meta.Size || grid.NextByte <= grid.StartByte || !grid.AtEof || len(grid.Rows) != 0 {
				t.Fatalf("BOM-only grid = %+v", grid)
			}
		})
	}

	rows := strings.Repeat("x\n", csvGridMaxRows+1)
	path := writeCSVEncodingFixture(t, "UTF-8", false, rows)
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	first, err := service.GetCsvGrid(meta.FileID, ",", 0, len(rows))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rows) != csvGridMaxRows || first.AtEof || first.NextByte <= 0 || first.NextByte >= meta.Size {
		t.Fatalf("row-capped page = rows %d, cursor %+v", len(first.Rows), first)
	}

	columns := 201
	row := strings.Repeat("x,", columns-1) + "x\n"
	hostile := strings.Repeat(row, csvGridMaxCells/columns+1)
	hostilePath := writeCSVEncodingFixture(t, "UTF-8", false, hostile)
	hostileService := NewFileService()
	hostileMeta, err := hostileService.OpenFile(hostilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer hostileService.CloseFile(hostileMeta.FileID)
	if _, err := hostileService.GetCsvGrid(hostileMeta.FileID, ",", 0, len(hostile)); !errors.Is(err, csv.ErrCSVGridShapeLimit) {
		t.Fatalf("hostile shape error = %v, want ErrCSVGridShapeLimit", err)
	}
}

func TestCSVAnalysisResultsCarryExactSessionGeneration(t *testing.T) {
	path := writeCSVEncodingFixture(t, "UTF-8", false, "id,name\n1,Ada\n")
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	inspection, err := service.CsvInspect(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := service.CsvSchema(meta.FileID, ",", true)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := service.CsvPreview(meta.FileID, ",", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := service.CsvProfile(meta.FileID, ",", true)
	if err != nil {
		t.Fatal(err)
	}
	grid, err := service.GetCsvGrid(meta.FileID, ",", 0, csvGridDefaultRawBytes)
	if err != nil {
		t.Fatal(err)
	}
	markdown, err := service.CsvMarkdownPreview(meta.FileID, ",", true, 10)
	if err != nil {
		t.Fatal(err)
	}
	sqlPreview, err := service.CsvToSQLPreview(meta.FileID, ",", "records", true, false)
	if err != nil {
		t.Fatal(err)
	}
	configuredSQL, err := service.CsvToSQLConfigPreview(meta.FileID, validCSVSQLConfig())
	if err != nil {
		t.Fatal(err)
	}
	for name, generation := range map[string]uint64{
		"schema": schema.Generation, "preview": preview.Generation,
		"profile": profile.Generation, "grid": grid.Generation,
		"markdown": markdown.Generation, "SQL": sqlPreview.Generation,
		"configured SQL": configuredSQL.Generation,
	} {
		if generation != inspection.Generation {
			t.Errorf("%s generation = %d, want %d", name, generation, inspection.Generation)
		}
	}
	if markdown.Text == "" || sqlPreview.Text == "" || configuredSQL.Text == "" {
		t.Fatal("generation-bound text previews returned empty text")
	}
}

type csvGenerationTransformCase struct {
	name string
	run  func(*FileService, string, uint64) (TransformResult, error)
}

func csvGenerationTransformCases() []csvGenerationTransformCase {
	return []csvGenerationTransformCase{
		{"project", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvProjectViaDialog(id, generation, ",", []int{0})
		}},
		{"add", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvAddColumnViaDialog(id, generation, ",", 1, "x")
		}},
		{"SQL", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvToSQLViaDialog(id, generation, ",", "records", true, false)
		}},
		{"redact", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvRedactViaDialog(id, generation, ",", true, []CsvRedactColumn{{Index: 1, Mode: "fixed"}}, "MASK")
		}},
		{"filter", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvFilterViaDialog(id, generation, ",", true, 0, "eq", "1", false)
		}},
		{"dedupe", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvDedupeViaDialog(id, generation, ",", true, -1)
		}},
		{"sample", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvSampleViaDialog(id, generation, ",", true, 2)
		}},
		{"JSONL", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvExportJSONLViaDialog(id, generation, ",", true, false)
		}},
		{"configured SQL", func(s *FileService, id string, generation uint64) (TransformResult, error) {
			return s.CsvToSQLConfigViaDialog(id, generation, validCSVSQLConfig())
		}},
	}
}

func TestCSVArtifactTransformsRequireCurrentGenerationBeforeDialog(t *testing.T) {
	path := writeCSVEncodingFixture(t, "UTF-8", false, "id,name\n1,Ada\n")
	service := NewFileService()
	meta, err := service.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)

	previousDialog := csvTransformSaveDialog
	dialogCalls := 0
	csvTransformSaveDialog = func(string, string) (string, error) {
		dialogCalls++
		return filepath.Join(t.TempDir(), "unexpected.csv"), nil
	}
	defer func() { csvTransformSaveDialog = previousDialog }()

	for _, test := range csvGenerationTransformCases() {
		if _, err := test.run(service, meta.FileID, 0); !errors.Is(err, ErrCSVSourceGenerationRequired) {
			t.Errorf("%s missing-generation error = %v", test.name, err)
		}
	}
	inspection, err := service.CsvInspect(meta.FileID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshFile(meta.FileID); err != nil {
		t.Fatal(err)
	}
	for _, test := range csvGenerationTransformCases() {
		if _, err := test.run(service, meta.FileID, inspection.Generation); !errors.Is(err, ErrOutputSourceChanged) {
			t.Errorf("%s stale-generation error = %v", test.name, err)
		}
	}
	if dialogCalls != 0 {
		t.Fatalf("invalid generations opened save dialog %d times", dialogCalls)
	}
}

func TestCSVArtifactTransformRejectsRefreshDuringDialog(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.csv")
	destination := filepath.Join(dir, "output.csv")
	sentinel := []byte("existing destination remains unchanged")
	if err := os.WriteFile(source, []byte("id,name\n1,Ada\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, sentinel, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewFileService()
	meta, err := service.OpenFile(source)
	if err != nil {
		t.Fatal(err)
	}
	defer service.CloseFile(meta.FileID)
	generation := csvServiceGeneration(t, service, meta.FileID)

	previousDialog := csvTransformSaveDialog
	csvTransformSaveDialog = func(string, string) (string, error) {
		if _, err := service.RefreshFile(meta.FileID); err != nil {
			return "", err
		}
		return destination, nil
	}
	defer func() { csvTransformSaveDialog = previousDialog }()

	if _, err := service.CsvSampleViaDialog(meta.FileID, generation, ",", true, 2); !errors.Is(err, ErrOutputSourceChanged) {
		t.Fatalf("error = %v, want generation rejection", err)
	}
	if data, err := os.ReadFile(destination); err != nil || !reflect.DeepEqual(data, sentinel) {
		t.Fatalf("destination = %q, %v; want sentinel", data, err)
	}
}

func writeCSVEncodingFixture(t *testing.T, encoding string, bom bool, content string) string {
	t.Helper()
	data, err := encodingx.EncodeString(encoding, content)
	if err != nil {
		t.Fatal(err)
	}
	if bom {
		data = append(append([]byte(nil), encodingx.BOMBytes(encoding)...), data...)
	}
	path := filepath.Join(t.TempDir(), "fixture.csv")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
