package main

import (
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// reviewedBridgeMethods is the complete renderer-callable Wails contract. Any
// addition is security-sensitive: generated bindings make every exported
// function callable by JavaScript executing in the application webview.
var reviewedBridgeMethods = map[string][]string{
	"frontend/bindings/novera/internal/agent/service.ts": {
		"AbortWorkspaceTransition", "Approve", "ApproveIntent", "AuditLog",
		"BeginWorkspaceTransition", "Cancel", "EndWorkspaceTransition",
		"ResetConversation", "Start",
	},
	"frontend/bindings/novera/internal/artifacts/service.ts": {
		"CreateArtifact", "DeleteArtifact", "GetArtifact", "ListArtifacts", "SetArchived",
	},
	"frontend/bindings/novera/internal/bigfile/fileservice.ts": {
		"BeginSearchRequest", "CancelJob", "CancelSearch", "CloseFile", "CsvAddColumnViaDialog", "CsvDedupeViaDialog",
		"CsvExportJSONLViaDialog", "CsvExportSQLiteViaDialog", "CsvExportXLSXViaDialog",
		"CsvFilterViaDialog", "CsvInspect", "CsvMarkdownPreview", "CsvPreview", "CsvProfile",
		"CsvProjectViaDialog", "CsvRedactViaDialog", "CsvSampleViaDialog", "CsvSchema",
		"CsvToSQLConfigPreview", "CsvToSQLConfigViaDialog", "CsvToSQLPreview", "CsvToSQLViaDialog",
		"DiscardEdits", "FileSize", "FileState", "FindNextRequest", "FindPrevRequest",
		"GetCsvGrid", "GetDiffWindow", "GetEditWindow", "GetHexWindow", "GetMatchWindow",
		"GetNextWindow", "GetPrevWindow", "GetStagedEdits", "GetStagingState", "GetTailWindow",
		"GetWindow", "HarvestMatchesViaDialog", "OpenFile", "OpenViaDialog", "PrepareEditSession",
		"RefreshFile", "ReleaseCleanEditSession", "ResolveLine", "SaveCopyViaDialog", "SavePatch", "SearchAllRequest",
		"SqlAnalyze", "SqlApplyPresetViaDialog", "SqlExtractDataViaDialog", "SqlExtractSchemaViaDialog",
		"SqlExtractTableViaDialog", "SqlLint", "SqlListPresets", "SqlReplaceViaDialog",
		"SqlReshapeInsertsViaDialog", "SqlSampleFixtureViaDialog", "SqlSchemaDiff",
		"SqlSplitByTableViaDialog", "StageEdit",
	},
	"frontend/bindings/novera/internal/db/service.ts": {
		"BuildTableQuery", "CredentialStatuses", "DeleteProfile", "DeleteProfileReconciled",
		"ListColumns", "ListProfiles", "ListTables", "LoadError", "Query", "SaveProfile",
		"SaveProfileReconciled", "TestProfile",
	},
	"frontend/bindings/novera/internal/gitsvc/service.ts": {
		"Commit", "Diff", "Stage", "StageAll", "Status", "UnifiedDiff", "Unstage",
	},
	"frontend/bindings/novera/internal/jobs/service.ts": {
		"CancelJob", "ClearFinished", "GetJob", "ListJobs",
	},
	"frontend/bindings/novera/internal/llm/service.ts": {
		"Cancel", "ListModels", "Send",
	},
	"frontend/bindings/novera/internal/settings/service.ts": {
		"DeleteLLMAPIKey", "GetLLMAPIKeyStatus", "HasLLMAPIKey", "Load", "LoadError",
		"RememberWorkspace", "Save", "SetLLMAPIKey",
	},
	"frontend/bindings/novera/internal/terminal/service.ts": {
		"Close", "Resize", "Start", "Write",
	},
	"frontend/bindings/novera/internal/watcher/service.ts": {
		"Watch",
	},
	"frontend/bindings/novera/internal/workspace/service.ts": {
		"AddCsvColumn", "AnalyzeSQLDump", "Close", "ConvertCsvToSql", "CreateDir", "CreateFile",
		"Current", "Delete", "Diagnostics", "DumpTableToCsv", "ExtractDumpTable", "InferTableSchema",
		"ListAllFiles", "ListDir", "Open", "PreviewCsvToSql", "ProjectCsv", "QueryTable", "ReadFile",
		"ReadFileRange", "Rename", "Root", "Search", "SplitDump", "TableInfo", "TransformDump", "WriteFile",
	},
	"frontend/bindings/novera/secretservice.ts": {
		"DeleteKey", "HasKey", "ListKeys", "SetKey",
	},
	"frontend/bindings/novera/shell.ts": {
		"BuildInfo", "OpenExternal", "SaveTextFile", "SelectFolder", "SetUnsavedResources",
	},
}

func TestGeneratedBridgeMethodAllowlist(t *testing.T) {
	t.Parallel()

	exportPattern := regexp.MustCompile(`(?m)^export function ([A-Za-z0-9_]+)\(`)
	discovered := make(map[string]struct{})
	err := filepath.WalkDir(filepath.FromSlash("frontend/bindings/novera"), func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".ts" {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !strings.Contains(string(source), "$Call.ByID(") {
			return nil
		}

		rel := filepath.ToSlash(path)
		discovered[rel] = struct{}{}
		want, ok := reviewedBridgeMethods[rel]
		if !ok {
			t.Errorf("generated RPC binding %q is not in the reviewed bridge contract", rel)
			return nil
		}

		matches := exportPattern.FindAllSubmatch(source, -1)
		got := make([]string, 0, len(matches))
		for _, match := range matches {
			got = append(got, string(match[1]))
		}
		slices.Sort(got)
		want = slices.Clone(want)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("generated bridge methods changed in %s; review the renderer authority before updating this contract: got %v, want %v", rel, got, want)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan generated bindings: %v", err)
	}

	for path := range reviewedBridgeMethods {
		if _, ok := discovered[path]; !ok {
			t.Errorf("reviewed bridge binding %q is missing or no longer contains RPC calls", path)
		}
	}
}

// Wails identifies bound methods with the FNV-1a hash of their fully-qualified
// Go name. Keep this check beside the allowlist so a manually refreshed
// binding cannot silently dispatch an approved method name to the wrong RPC.
func TestBigFileBindingMethodIDsMatchWailsFNV(t *testing.T) {
	t.Parallel()

	path := filepath.FromSlash("frontend/bindings/novera/internal/bigfile/fileservice.ts")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read big-file binding: %v", err)
	}
	callPattern := regexp.MustCompile(`(?m)^export function ([A-Za-z0-9_]+)\([^\r\n]*\)[^{]*\{\r?\n\s+return \$Call\.ByID\(([0-9]+)`)
	matches := callPattern.FindAllSubmatch(source, -1)
	if len(matches) != len(reviewedBridgeMethods[filepath.ToSlash(path)]) {
		t.Fatalf(
			"parse big-file binding IDs: found %d calls, want %d",
			len(matches),
			len(reviewedBridgeMethods[filepath.ToSlash(path)]),
		)
	}
	for _, match := range matches {
		method := string(match[1])
		got, err := strconv.ParseUint(string(match[2]), 10, 32)
		if err != nil {
			t.Fatalf("parse %s binding ID: %v", method, err)
		}
		hash := fnv.New32a()
		_, _ = hash.Write([]byte("novera/internal/bigfile.FileService." + method))
		if want := uint64(hash.Sum32()); got != want {
			t.Errorf("%s binding ID = %d, want %d", method, got, want)
		}
	}
}

// The pre-stable Wails runtime is intentionally quarantined to this reviewed set of
// integration seams. New business packages should depend on Novera-owned
// adapters instead of importing the runtime directly.
func TestWailsImportBoundary(t *testing.T) {
	t.Parallel()

	want := []string{
		"build/android/main_android.go",
		"build/ios/app_options_default.go",
		"build/ios/app_options_ios.go",
		"internal/agent/agent.go",
		"internal/artifacts/artifacts.go",
		"internal/bigfile/fileservice.go",
		"internal/bigfile/fileservice_csv.go",
		"internal/bigfile/fileservice_edit.go",
		"internal/bigfile/jobs.go",
		"internal/jobs/jobs.go",
		"internal/llm/llm.go",
		"internal/terminal/terminal.go",
		"internal/watcher/watcher.go",
		"main.go",
		"shell_service.go",
	}

	var got []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			// Windows CI keeps GOTMPDIR inside the checkout. Go may remove a
			// package's temporary directory while this parallel test is walking
			// the tree, so never descend into any of those transient roots.
			if strings.HasPrefix(entry.Name(), ".gotmp") {
				return filepath.SkipDir
			}
			switch entry.Name() {
			case ".git", "bin", "col-review", "dist", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "bridge_contract_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(source), `"github.com/wailsapp/wails/v3/`) {
			got = append(got, filepath.ToSlash(strings.TrimPrefix(path, `.`+string(filepath.Separator))))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan Wails imports: %v", err)
	}

	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("Wails import boundary changed; route new runtime use through a reviewed adapter: got %v, want %v", got, want)
	}
}
