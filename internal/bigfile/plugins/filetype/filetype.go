package filetype

import (
	"path/filepath"
	"strings"

	"novera/internal/bigfile/plugins"
)

// Detect returns an advisory file type label for metadata and routing. Unknown
// text-like files intentionally fall back to generic text so open remains permissive.
func Detect(path string, sample string, binary bool) string {
	if binary {
		return "binary"
	}

	if strings.EqualFold(filepath.Ext(path), ".txt") || strings.EqualFold(filepath.Ext(path), ".text") {
		return "text"
	}

	if label, ok := LabelForPath(path); ok {
		return label
	}

	return LabelForSample(sample)
}

// LabelForPath maps built-in plugin file patterns to the labels currently shown
// by document metadata and the UI.
func LabelForPath(path string) (string, bool) {
	matches := plugins.MatchPath(path, descriptors())
	if len(matches) == 0 {
		return "", false
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch matches[0].ID {
	case "sql":
		return "SQL", true
	case "csv":
		if ext == ".tsv" || ext == ".tab" {
			return "TSV", true
		}
		return "CSV", true
	case "log":
		if ext == ".jsonl" || ext == ".ndjson" {
			return "JSONL", true
		}
		return "log", true
	case "yaml":
		return "YAML", true
	case "config":
		switch ext {
		case ".toml":
			return "TOML", true
		case ".json":
			return "JSON", true
		default:
			return "config", true
		}
	case "markup":
		switch ext {
		case ".xml", ".svg":
			return "XML", true
		case ".md", ".markdown":
			return "Markdown", true
		case ".css", ".scss", ".less":
			return "CSS", true
		case ".html", ".htm", ".vue", ".svelte":
			return "web", true
		default:
			return "markup", true
		}
	case "source":
		return "source", true
	case "lua":
		return "Lua", true
	case "perl":
		return "Perl", true
	case "scala":
		return "Scala", true
	case "golang":
		return "Go", true
	case "php":
		return "PHP", true
	case "javascript":
		base := filepath.Base(path)
		if strings.EqualFold(base, "package.json") ||
			strings.EqualFold(base, "tsconfig.json") ||
			strings.EqualFold(base, "jsconfig.json") {
			return "JSON", true
		}
		switch ext {
		case ".ts", ".tsx":
			return "TypeScript", true
		default:
			return "JavaScript", true
		}
	case "python":
		return "Python", true
	case "java":
		return "Java", true
	case "csharp":
		return "C#", true
	case "cpp":
		return "C++", true
	case "c":
		return "C", true
	case "rust":
		return "Rust", true
	case "kotlin":
		return "Kotlin", true
	case "swift":
		return "Swift", true
	case "ruby":
		return "Ruby", true
	case "dart":
		return "Dart", true
	case "r":
		return "R", true
	case "shell":
		switch ext {
		case ".ps1", ".psm1", ".psd1":
			return "PowerShell", true
		default:
			return "Shell", true
		}
	default:
		return "", false
	}
}

// LabelForSample sniffs small text samples when extension-based plugin routing
// cannot identify the file. The sample is bounded by the document opener.
func LabelForSample(sample string) string {
	upper := strings.ToUpper(sample)
	trimmed := strings.TrimSpace(sample)
	switch {
	case strings.Contains(upper, "CREATE TABLE") || strings.Contains(upper, "INSERT INTO"):
		return "SQL"
	case strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "["):
		return "JSON"
	case strings.HasPrefix(trimmed, "---") || strings.Contains(sample, ":\n"):
		return "YAML"
	default:
		return "text"
	}
}

func descriptors() []plugins.Descriptor {
	return []plugins.Descriptor{
		{ID: "sql", FilePatterns: plugins.SQLFilePatterns},
		{ID: "csv", FilePatterns: plugins.CSVFilePatterns},
		{ID: "log", FilePatterns: plugins.LogFilePatterns},
		{ID: "yaml", FilePatterns: plugins.YAMLFilePatterns},
		{ID: "config", FilePatterns: plugins.ConfigFilePatterns},
		{ID: "markup", FilePatterns: plugins.MarkupFilePatterns},
		{ID: "python", FilePatterns: plugins.PythonFilePatterns},
		{ID: "java", FilePatterns: plugins.JavaFilePatterns},
		{ID: "csharp", FilePatterns: plugins.CSharpFilePatterns},
		{ID: "cpp", FilePatterns: plugins.CPPFilePatterns},
		{ID: "c", FilePatterns: plugins.CFilePatterns},
		{ID: "golang", FilePatterns: plugins.GoFilePatterns},
		{ID: "php", FilePatterns: plugins.PHPFilePatterns},
		{ID: "javascript", FilePatterns: plugins.JavaScriptFilePatterns},
		{ID: "rust", FilePatterns: plugins.RustFilePatterns},
		{ID: "kotlin", FilePatterns: plugins.KotlinFilePatterns},
		{ID: "swift", FilePatterns: plugins.SwiftFilePatterns},
		{ID: "ruby", FilePatterns: plugins.RubyFilePatterns},
		{ID: "dart", FilePatterns: plugins.DartFilePatterns},
		{ID: "r", FilePatterns: plugins.RFilePatterns},
		{ID: "shell", FilePatterns: plugins.ShellFilePatterns},
		{ID: "lua", FilePatterns: plugins.LuaFilePatterns},
		{ID: "perl", FilePatterns: plugins.PerlFilePatterns},
		{ID: "scala", FilePatterns: plugins.ScalaFilePatterns},
		{ID: "source", FilePatterns: plugins.SourceFilePatterns},
	}
}
