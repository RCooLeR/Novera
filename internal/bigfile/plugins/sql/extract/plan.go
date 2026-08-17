package extract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"novera/internal/bigfile/fileio"
	"novera/internal/bigfile/plugins/sql/analyze"
)

var ErrTableNameTooLong = errors.New("table name exceeds the SQL identifier byte limit")

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
	Regions      []analyze.Region `json:",omitempty"`
	OutputPath   string
	SHA256       string `json:",omitempty"`
}

type ManifestPreview struct {
	Operation  string
	SourceSize int64
	Tables     []TableRange
	// HeaderIncluded is always false today: each slice begins at its CREATE/INSERT
	// statement, so the dump preamble (SET NAMES / charset, SET FOREIGN_KEY_CHECKS)
	// and unrelated footer/DML are not part of any slice. DetectedCharsets + Note
	// tell the user what to prepend for a correct standalone re-import.
	HeaderIncluded   bool
	DetectedCharsets []string `json:",omitempty"`
	Note             string   `json:",omitempty"`
}

const headerlessSliceNote = "Each slice contains only the discovered table's exact CREATE/INSERT/REPLACE regions. Session preamble, ALTER/DROP, triggers, other DML, and footer SQL are omitted. The slice is not a standalone dump unless every required SET NAMES, session setting, and surrounding statement is added explicitly."

const (
	maxExtractExtensionBytes = 16
	maxExtractBaseBytes      = 180
	// MaxTableSelectionBytes is the exact worst-case display identity emitted
	// by the analyzer: two MaxIdentifierBytes components, each backtick-quoted
	// with every embedded backtick doubled, plus four wrapper backticks and the
	// separating dot. Unqualified identities remain bounded by the smaller
	// analyzer component limit.
	MaxTableSelectionBytes = 4*analyze.MaxIdentifierBytes + 5
)

var portableNameFold = cases.Fold()

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
	// Check raw input length before TrimSpace, lookup, or formatting it into an
	// error. Qualified analyzer display identities can be larger than either
	// individual identifier because quoting doubles embedded backticks.
	if len(tableName) > MaxTableSelectionBytes {
		return TableRange{}, ErrTableNameTooLong
	}
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
	var extension string
	if opts.OutputDir != "" {
		if err := fileio.ValidateExactDirectoryPath(opts.OutputDir); err != nil {
			return nil, err
		}
		var err error
		extension, err = normalizeExtractExtension(opts.Extension)
		if err != nil {
			return nil, err
		}
	} else if opts.Extension != "" {
		return nil, errors.New("output directory is required when an extension is provided")
	}
	regionMode := false
	legacyMode := false
	for _, table := range summary.Tables {
		if len(table.Regions) > 0 {
			regionMode = true
		} else if _, ok := tableStartOffset(table); ok {
			legacyMode = true
		}
	}
	if regionMode && legacyMode {
		return nil, errors.New("SQL analysis mixes region-aware and legacy table offsets")
	}

	candidates := make([]TableRange, 0, len(summary.Tables))
	if regionMode {
		for _, table := range summary.Tables {
			if len(table.Regions) == 0 {
				continue
			}
			regions := append([]analyze.Region(nil), table.Regions...)
			sort.SliceStable(regions, func(i, j int) bool {
				if regions[i].StartOffset != regions[j].StartOffset {
					return regions[i].StartOffset < regions[j].StartOffset
				}
				return regions[i].Kind < regions[j].Kind
			})
			planned := TableRange{
				Name:         table.Name,
				CreateOffset: -1,
				InsertOffset: -1,
				Regions:      regions,
			}
			previousEnd := int64(-1)
			for index, region := range regions {
				if region.StartOffset < 0 || region.EndOffset <= region.StartOffset || region.EndOffset > sourceSize {
					return nil, fmt.Errorf("table %q has invalid %s region [%d,%d)", table.Name, region.Kind, region.StartOffset, region.EndOffset)
				}
				if previousEnd > region.StartOffset {
					return nil, fmt.Errorf("table %q has overlapping SQL regions", table.Name)
				}
				switch region.Kind {
				case analyze.RegionCreate:
					if planned.CreateOffset < 0 {
						planned.CreateOffset = region.StartOffset
					}
				case analyze.RegionInsert, analyze.RegionReplace:
					if planned.InsertOffset < 0 {
						planned.InsertOffset = region.StartOffset
					}
				default:
					return nil, fmt.Errorf("table %q has unsupported SQL region kind %q", table.Name, region.Kind)
				}
				if planned.Bytes > sourceSize-(region.EndOffset-region.StartOffset) {
					return nil, fmt.Errorf("table %q region bytes overflow source size", table.Name)
				}
				planned.Bytes += region.EndOffset - region.StartOffset
				if index == 0 {
					planned.StartOffset = region.StartOffset
				}
				planned.EndOffset = region.EndOffset
				previousEnd = region.EndOffset
			}
			candidates = append(candidates, planned)
		}
	} else {
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
		if !regionMode {
			end := sourceSize
			if i+1 < len(candidates) {
				end = candidates[i+1].StartOffset
			}
			if end < candidates[i].StartOffset {
				return nil, fmt.Errorf("table %q has an invalid byte range", candidates[i].Name)
			}
			candidates[i].EndOffset = end
			candidates[i].Bytes = end - candidates[i].StartOffset
		}
		if opts.OutputDir != "" {
			outputPath, err := outputPathForTable(candidates[i].Name, opts.OutputDir, extension, usedOutputs)
			if err != nil {
				return nil, err
			}
			candidates[i].OutputPath = outputPath
		}
	}
	return candidates, nil
}

// ByteRanges returns the exact ordered source regions that belong to one table
// identity. Callers must not replace these with the enclosing Start/End span,
// which may contain other tables when dump blocks are interleaved.
func ByteRanges(table TableRange) [][2]int64 {
	if len(table.Regions) == 0 {
		if table.EndOffset <= table.StartOffset {
			return nil
		}
		return [][2]int64{{table.StartOffset, table.EndOffset}}
	}
	ranges := make([][2]int64, 0, len(table.Regions))
	for _, region := range table.Regions {
		ranges = append(ranges, [2]int64{region.StartOffset, region.EndOffset})
	}
	return ranges
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

func outputPathForTable(tableName string, outputDir string, extension string, used map[string]int) (string, error) {
	base := SafeFilenameComponent(tableName)
	base = allocatePortableName(base, extension, used)
	outputPath, err := fileio.ExactChildPath(outputDir, base+extension)
	if err != nil {
		return "", err
	}
	if err := requireDirectChild(outputDir, outputPath); err != nil {
		return "", fmt.Errorf("unsafe output path for table %q: %w", tableName, err)
	}
	return outputPath, nil
}

// SafeFilenameComponent converts an untrusted table name into one bounded,
// portable filename component. It deliberately excludes dots and separators,
// escapes Windows device basenames, normalizes Unicode, and adds a stable hash
// when truncation is required.
func SafeFilenameComponent(name string) string {
	normalized := norm.NFC.String(strings.TrimSpace(name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range normalized {
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
		out = "table"
	}
	if isWindowsDeviceBase(out) {
		out = "_" + out
	}
	if len(out) > maxExtractBaseBytes {
		sum := sha256.Sum256([]byte(normalized))
		suffix := "_" + hex.EncodeToString(sum[:8])
		out = truncateUTF8(out, maxExtractBaseBytes-len(suffix)) + suffix
		out = strings.Trim(out, "_-")
		if out == "" {
			out = "table" + suffix
		}
	}
	return out
}

func normalizeExtractExtension(extension string) (string, error) {
	if extension == "" {
		return ".sql", nil
	}
	if strings.TrimSpace(extension) != extension {
		return "", errors.New("output extension must not contain surrounding whitespace")
	}
	if !strings.HasPrefix(extension, ".") {
		extension = "." + extension
	}
	if len(extension) < 2 || len(extension) > maxExtractExtensionBytes {
		return "", fmt.Errorf("output extension must contain 1 to %d characters", maxExtractExtensionBytes-1)
	}
	for i := 1; i < len(extension); i++ {
		c := extension[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "", errors.New("output extension must be one short alphanumeric suffix")
		}
	}
	return extension, nil
}

func allocatePortableName(base string, extension string, used map[string]int) string {
	key := canonicalPortableName(base + extension)
	used[key]++
	count := used[key]
	if count == 1 {
		return base
	}
	for {
		suffix := fmt.Sprintf("_%02d", count)
		candidate := truncateUTF8(base, maxExtractBaseBytes-len(suffix)) + suffix
		candidateKey := canonicalPortableName(candidate + extension)
		if used[candidateKey] == 0 {
			used[candidateKey] = 1
			return candidate
		}
		count++
	}
}

func canonicalPortableName(name string) string {
	return portableNameFold.String(norm.NFC.String(name))
}

func isWindowsDeviceBase(base string) bool {
	upper := strings.ToUpper(strings.TrimRight(base, ". "))
	switch upper {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return true
	}
	if len(upper) == 4 && (strings.HasPrefix(upper, "COM") || strings.HasPrefix(upper, "LPT")) {
		return upper[3] >= '1' && upper[3] <= '9'
	}
	return false
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(value) <= maxBytes {
		return value
	}
	end := maxBytes
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end]
}

func requireDirectChild(dir string, path string) error {
	if err := fileio.ValidateExactDirectoryPath(dir); err != nil {
		return err
	}
	if err := fileio.ValidateExactOutputPath(path); err != nil {
		return err
	}
	_, base := filepath.Split(path)
	expected, err := fileio.ExactChildPath(dir, base)
	if err != nil {
		return err
	}
	if expected != path {
		return errors.New("path must be a direct child of the selected output directory")
	}
	return nil
}
