package preset

import (
	"regexp"
	"testing"
)

func TestBuildRemoveDefinerPreset(t *testing.T) {
	cfg, err := Build(RemoveDefinerPreset, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeRegex || !cfg.Regex {
		t.Fatalf("mode = %q regex=%v", cfg.Mode, cfg.Regex)
	}
	if cfg.Search == "" {
		t.Fatal("expected regex search text")
	}
}

func TestBuildRemoveDefinerPresetMatchesQuotedForms(t *testing.T) {
	cfg, err := Build(RemoveDefinerPreset, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	re, err := regexp.Compile(cfg.Search)
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		"CREATE DEFINER=`root`@`localhost` VIEW v AS SELECT 1",
		"CREATE DEFINER='root'@'localhost' VIEW v AS SELECT 1",
		"CREATE DEFINER='weird''user'@'local''host' VIEW v AS SELECT 1",
		"CREATE DEFINER=root@localhost VIEW v AS SELECT 1",
	}
	for _, tc := range cases {
		if !re.MatchString(tc) {
			t.Fatalf("expected DEFINER regex to match %q", tc)
		}
	}
}

func TestBuildChangeDatabasePreset(t *testing.T) {
	cfg, err := Build(ChangeDatabasePreset, "old_db", "new_db", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeBatch {
		t.Fatalf("mode = %q", cfg.Mode)
	}
	if len(cfg.BatchRules) != 3 {
		t.Fatalf("len(rules) = %d, want 3", len(cfg.BatchRules))
	}
	if string(cfg.BatchRules[0].Find) != "`old_db`" || string(cfg.BatchRules[0].Replace) != "`new_db`" {
		t.Fatalf("first rule = %q => %q", cfg.BatchRules[0].Find, cfg.BatchRules[0].Replace)
	}
}

func TestBuildChangeDatabasePresetEscapesBacktickNames(t *testing.T) {
	cfg, err := Build(ChangeDatabasePreset, "old`db", "new`db", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeBatch {
		t.Fatalf("mode = %q", cfg.Mode)
	}
	if len(cfg.BatchRules) != 1 {
		t.Fatalf("len(rules) = %d, want only quoted rule for non-bare names", len(cfg.BatchRules))
	}
	if string(cfg.BatchRules[0].Find) != "`old``db`" || string(cfg.BatchRules[0].Replace) != "`new``db`" {
		t.Fatalf("first rule = %q => %q", cfg.BatchRules[0].Find, cfg.BatchRules[0].Replace)
	}
}

func TestBuildChangeCharsetPresetRequiresCollationPair(t *testing.T) {
	if _, err := Build(ChangeCharsetPreset, "utf8mb4", "utf8", "utf8mb4_unicode_ci", ""); err == nil {
		t.Fatal("expected collation pair validation")
	}
}

func TestBuildChangeCharsetPresetWithCollation(t *testing.T) {
	cfg, err := Build(ChangeCharsetPreset, "utf8mb4", "utf8", "utf8mb4_unicode_ci", "utf8_unicode_ci")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeRegexBatch {
		t.Fatalf("mode = %q, want regex-batch", cfg.Mode)
	}
	if len(cfg.BatchRules) != 3 { // charset + set names + collation
		t.Fatalf("len(rules) = %d, want 3", len(cfg.BatchRules))
	}
}

// The regex rules must rewrite the spaced / CHARACTER SET / backtick forms the
// old literal preset missed, and must NOT rewrite an already-correct value.
func TestChangeCharsetPresetRewritesVariants(t *testing.T) {
	cfg, err := Build(ChangeCharsetPreset, "utf8", "utf8mb4", "", "")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(in string) string {
		out := in
		for _, rule := range cfg.BatchRules {
			re := regexp.MustCompile(string(rule.Find))
			out = re.ReplaceAllString(out, string(rule.Replace))
		}
		return out
	}
	cases := map[string]string{
		"CHARSET=utf8":            "CHARSET=utf8mb4",
		"CHARSET = utf8":          "CHARSET = utf8mb4",
		"DEFAULT CHARSET=utf8":    "DEFAULT CHARSET=utf8mb4",
		"DEFAULT CHARSET = utf8":  "DEFAULT CHARSET = utf8mb4",
		"CHARACTER SET utf8":      "CHARACTER SET utf8mb4",
		"CHARSET=`utf8`":          "CHARSET=`utf8mb4`",
		"SET NAMES utf8":          "SET NAMES utf8mb4",
		"charset=UTF8":            "charset=utf8mb4", // case-insensitive keyword + value
		"CHARSET=utf8mb4":         "CHARSET=utf8mb4", // already correct, must be untouched
		"CHARSET=utf8mb4_general": "CHARSET=utf8mb4_general",
	}
	for in, want := range cases {
		if got := apply(in); got != want {
			t.Fatalf("apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChangeCharsetPresetRewritesCollation(t *testing.T) {
	cfg, err := Build(ChangeCharsetPreset, "utf8", "utf8mb4", "utf8_general_ci", "utf8mb4_general_ci")
	if err != nil {
		t.Fatal(err)
	}
	var collate *regexp.Regexp
	var repl string
	for _, rule := range cfg.BatchRules {
		if rule.Name == "COLLATE" {
			collate = regexp.MustCompile(string(rule.Find))
			repl = string(rule.Replace)
		}
	}
	if collate == nil {
		t.Fatal("no COLLATE rule built")
	}
	for in, want := range map[string]string{
		"COLLATE=utf8_general_ci":     "COLLATE=utf8mb4_general_ci",
		"COLLATE utf8_general_ci":     "COLLATE utf8mb4_general_ci",
		"COLLATE = `utf8_general_ci`": "COLLATE = `utf8mb4_general_ci`",
		"COLLATE=utf8mb4_general_ci":  "COLLATE=utf8mb4_general_ci",
	} {
		if got := collate.ReplaceAllString(in, repl); got != want {
			t.Fatalf("collate apply(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildConvertEnginePreset(t *testing.T) {
	cfg, err := Build(ConvertEnginePreset, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeBatch || len(cfg.BatchRules) != 2 {
		t.Fatalf("mode=%q rules=%d", cfg.Mode, len(cfg.BatchRules))
	}
}

func TestBuildRemoveAutoIncrementPreset(t *testing.T) {
	cfg, err := Build(RemoveAutoIncrementPreset, "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mode != ModeRegex || cfg.Replace != "" {
		t.Fatalf("mode=%q replace=%q", cfg.Mode, cfg.Replace)
	}
}
