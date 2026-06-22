package extract

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"novera/internal/bigfile/plugins/sql/analyze"
)

type PlanOptions struct {
	OutputDir string
	Extension string
}

type TableRange struct {
	Name         string
	StartOffset  int64
	EndOffset    int64
	Bytes        int64
	CreateOffset int64
	InsertOffset int64
	OutputPath   string
	SHA256       string `json:",omitempty"`
}

type ManifestPreview struct {
	Operation  string
	SourceSize int64
	Tables     []TableRange
	// HeaderIncluded is always false today: each slice begins at its CREATE/INSERT
	// statement, so the dump preamble (SET NAMES / charset, SET FOREIGN_KEY_CHECKS)
	// is not part of any slice. DetectedCharsets + Note tell the user what to
	// prepend for a correct standalone re-import.
	HeaderIncluded   bool
	DetectedCharsets []string `json:",omitempty"`
	Note             string   `json:",omitempty"`
}

const headerlessSliceNote = "Each slice begins at its CREATE/INSERT statement and omits the dump preamble (SET NAMES / charset, SET FOREIGN_KEY_CHECKS=0). For a correct standalone re-import, prepend an appropriate SET NAMES and SET FOREIGN_KEY_CHECKS=0; the last slice also includes the dump's trailing footer."

// detectedCharsets returns the charsets the analyzer saw (sorted), so a caller
// re-importing a slice knows which SET NAMES to prepend.
func detectedCharsets(summary analyze.Summary) []string {
	charsets := make([]string, 0, len(summary.Charsets))
	for cs := range summary.Charsets {
		charsets = append(charsets, cs)
	}
	sort.Strings(charsets)
	return charsets
}

func SplitByTablePreview(summary analyze.Summary, sourceSize int64, opts PlanOptions) (ManifestPreview, error) {
	ranges, err := PlanTableRanges(summary, sourceSize, opts)
	if err != nil {
		return ManifestPreview{}, err
	}
	return ManifestPreview{
		Operation:        "sql-split-by-table",
		SourceSize:       sourceSize,
		Tables:           ranges,
		HeaderIncluded:   false,
		DetectedCharsets: detectedCharsets(summary),
		Note:             headerlessSliceNote,
	}, nil
}

func ExtractTablePreview(summary analyze.Summary, sourceSize int64, tableName string, opts PlanOptions) (ManifestPreview, error) {
	rangePlan, err := PlanExtractTable(summary, sourceSize, tableName, opts)
	if err != nil {
		return ManifestPreview{}, err
	}
	return ManifestPreview{
		Operation:        "sql-extract-table",
		SourceSize:       sourceSize,
		Tables:           []TableRange{rangePlan},
		HeaderIncluded:   false,
		DetectedCharsets: detectedCharsets(summary),
		Note:             headerlessSliceNote,
	}, nil
}

func PlanExtractTable(summary analyze.Summary, sourceSize int64, tableName string, opts PlanOptions) (TableRange, error) {
	if strings.TrimSpace(tableName) == "" {
		return TableRange{}, errors.New("table name is required")
	}
	ranges, err := PlanTableRanges(summary, sourceSize, opts)
	if err != nil {
		return TableRange{}, err
	}
	for _, table := range ranges {
		if table.Name == tableName {
			return table, nil
		}
	}
	return TableRange{}, fmt.Errorf("table %q was not discovered", tableName)
}

func PlanTableRanges(summary analyze.Summary, sourceSize int64, opts PlanOptions) ([]TableRange, error) {
	if sourceSize < 0 {
		return nil, errors.New("source size must be non-negative")
	}
	candidates := make([]TableRange, 0, len(summary.Tables))
	for _, table := range summary.Tables {
		start, ok := tableStartOffset(table)
		if !ok || start >= sourceSize {
			continue
		}
		if start < 0 {
			start = 0
		}
		candidates = append(candidates, TableRange{
			Name:         table.Name,
			StartOffset:  start,
			CreateOffset: table.CreateOffset,
			InsertOffset: table.InsertOffset,
		})
	}
	if len(candidates) == 0 {
		return nil, errors.New("no SQL tables with known offsets were discovered")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].StartOffset != candidates[j].StartOffset {
			return candidates[i].StartOffset < candidates[j].StartOffset
		}
		return candidates[i].Name < candidates[j].Name
	})
	usedOutputs := make(map[string]int, len(candidates))
	for i := range candidates {
		end := sourceSize
		if i+1 < len(candidates) {
			end = candidates[i+1].StartOffset
		}
		if end < candidates[i].StartOffset {
			return nil, fmt.Errorf("table %q has an invalid byte range", candidates[i].Name)
		}
		candidates[i].EndOffset = end
		candidates[i].Bytes = end - candidates[i].StartOffset
		candidates[i].OutputPath = outputPathForTable(candidates[i].Name, opts, usedOutputs)
	}
	return candidates, nil
}

func tableStartOffset(table analyze.Table) (int64, bool) {
	switch {
	case table.CreateOffset >= 0:
		return table.CreateOffset, true
	case table.InsertOffset >= 0:
		return table.InsertOffset, true
	default:
		return 0, false
	}
}

func outputPathForTable(tableName string, opts PlanOptions, used map[string]int) string {
	if strings.TrimSpace(opts.OutputDir) == "" {
		return ""
	}
	ext := opts.Extension
	if ext == "" {
		ext = ".sql"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	base := sanitizeTableName(tableName)
	used[base]++
	if used[base] > 1 {
		base = fmt.Sprintf("%s_%02d", base, used[base])
	}
	return filepath.Join(opts.OutputDir, base+ext)
}

func sanitizeTableName(name string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range strings.TrimSpace(name) {
		keep := r == '_' || r == '-' || unicode.IsLetter(r) || unicode.IsDigit(r)
		if keep {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_-")
	if out == "" || out == "." || out == ".." {
		return "table"
	}
	return out
}
