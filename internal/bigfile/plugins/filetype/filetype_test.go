package filetype

import "testing"

func TestDetectUsesPluginPathLabels(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"dump.sql", "SQL"},
		{"database.dump", "SQL"},
		{"users.tsv", "TSV"},
		{"users.csv", "CSV"},
		{"events.jsonl", "JSONL"},
		{"events.ndjson", "JSONL"},
		{"settings.toml", "TOML"},
		{"settings.json", "JSON"},
		{"settings.conf", "config"},
		{"index.html", "web"},
		{"doc.xml", "XML"},
		{"icon.svg", "XML"},
		{"Component.vue", "web"},
		{"Widget.svelte", "web"},
		{"README.md", "Markdown"},
		{"styles.css", "CSS"},
		{"main.go", "Go"},
		{"package.json", "JSON"},
		{"jsconfig.json", "JSON"},
		{"app.ts", "TypeScript"},
		{"app.py", "Python"},
		{"App.java", "Java"},
		{"Program.cs", "C#"},
		{"main.cpp", "C++"},
		{"main.c", "C"},
		{"lib.rs", "Rust"},
		{"Main.kt", "Kotlin"},
		{"App.swift", "Swift"},
		{"app.rb", "Ruby"},
		{"main.dart", "Dart"},
		{"analysis.R", "R"},
		{"script.sh", "Shell"},
		{"script.ps1", "PowerShell"},
		{"module.lua", "Lua"},
		{"legacy.pl", "Perl"},
		{"Legacy.pm", "Perl"},
		{"Job.scala", "Scala"},
		{"server.ex", "source"},
		{"handler.erl", "source"},
		{"Program.fs", "source"},
		{"core.clj", "source"},
		{"main.zig", "source"},
		{"solver.hs", "source"},
		{"model.ml", "source"},
		{"Legacy.vb", "source"},
		{"notes.txt", "text"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := Detect(tt.path, "ordinary text", false); got != tt.want {
				t.Fatalf("Detect(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestDetectFallsBackToBoundedSampleSniffing(t *testing.T) {
	tests := []struct {
		name   string
		sample string
		want   string
	}{
		{"sql", "CREATE TABLE users (id int);", "SQL"},
		{"json", "{\"name\":\"quarry\"}", "JSON"},
		{"yaml", "---\nname: quarry\n", "YAML"},
		{"text", "plain words\nwithout structure", "text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Detect("unknown.weird", tt.sample, false); got != tt.want {
				t.Fatalf("Detect sample = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectKeepsBinaryAsBinary(t *testing.T) {
	if got := Detect("dump.sql", "CREATE TABLE users (id int);", true); got != "binary" {
		t.Fatalf("binary Detect = %q, want binary", got)
	}
}
