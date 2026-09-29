package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"novera/internal/datatools"
)

const transformTestCSV = "id,name\n1,Ada\n2,Bob\n"

const transformTestMySQLDump = "CREATE TABLE users (id INT, name TEXT);\n" +
	"INSERT INTO users VALUES (1, 'Ada');\n"

const transformTestPGDump = "CREATE TABLE public.users (id integer, name text);\n" +
	"COPY public.users (id, name) FROM stdin;\n" +
	"1\tAda\n" +
	"2\tBob\n" +
	"\\.\n"

func TestLegacyTransformFacadesRejectInputAsOutput(t *testing.T) {
	type transformCase struct {
		name    string
		rel     string
		content string
		run     func(*Service) error
	}
	cases := []transformCase{
		{
			name:    "ConvertCsvToSql",
			rel:     "data.csv",
			content: transformTestCSV,
			run: func(s *Service) error {
				_, err := s.ConvertCsvToSql("data.csv", "data.csv", "users", true)
				return err
			},
		},
		{
			name:    "ExtractDumpTable",
			rel:     "users.sql",
			content: transformTestMySQLDump,
			run: func(s *Service) error {
				_, err := s.ExtractDumpTable("users.sql", "users", "users.sql")
				return err
			},
		},
		{
			name:    "SplitDump",
			rel:     "users.sql",
			content: transformTestMySQLDump,
			run: func(s *Service) error {
				// Splitting table users into the workspace root would generate
				// users.sql, which is the input itself.
				_, err := s.SplitDump("users.sql", ".")
				return err
			},
		},
		{
			name:    "TransformDump",
			rel:     "dump.sql",
			content: transformTestMySQLDump,
			run: func(s *Service) error {
				_, err := s.TransformDump("dump.sql", "dump.sql", datatools.DumpTransform{RemoveDefiner: true})
				return err
			},
		},
		{
			name:    "ProjectCsv",
			rel:     "data.csv",
			content: transformTestCSV,
			run: func(s *Service) error {
				_, err := s.ProjectCsv("data.csv", "data.csv", []string{"name"})
				return err
			},
		},
		{
			name:    "AddCsvColumn",
			rel:     "data.csv",
			content: transformTestCSV,
			run: func(s *Service) error {
				_, err := s.AddCsvColumn("data.csv", "data.csv", "active", "true")
				return err
			},
		},
		{
			name:    "DumpTableToCsv",
			rel:     "dump.sql",
			content: transformTestPGDump,
			run: func(s *Service) error {
				_, err := s.DumpTableToCsv("dump.sql", "users", "dump.sql")
				return err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			inputPath := filepath.Join(root, tc.rel)
			if err := os.WriteFile(inputPath, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			s := openTransformTestWorkspace(t, root)

			err := tc.run(s)
			if !errors.Is(err, ErrTransformOutputAliasesInput) {
				t.Fatalf("error = %v, want ErrTransformOutputAliasesInput", err)
			}
			assertTransformFileContent(t, inputPath, tc.content)
			assertNoStagedTransformFiles(t, root)
		})
	}
}

func TestTransformOutputRejectsCanonicalAndFilesystemAliases(t *testing.T) {
	t.Run("canonical relative alias", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
			t.Fatal(err)
		}
		inputPath := filepath.Join(root, "nested", "data.csv")
		if err := os.WriteFile(inputPath, []byte(transformTestCSV), 0o644); err != nil {
			t.Fatal(err)
		}
		s := openTransformTestWorkspace(t, root)

		_, err := s.ProjectCsv("nested/data.csv", "nested/../nested/data.csv", []string{"name"})
		if !errors.Is(err, ErrTransformOutputAliasesInput) {
			t.Fatalf("error = %v, want ErrTransformOutputAliasesInput", err)
		}
		assertTransformFileContent(t, inputPath, transformTestCSV)
	})

	t.Run("symlink alias", func(t *testing.T) {
		root := t.TempDir()
		inputPath := filepath.Join(root, "data.csv")
		if err := os.WriteFile(inputPath, []byte(transformTestCSV), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("data.csv", filepath.Join(root, "alias.csv")); err != nil {
			t.Skipf("symlinks unavailable on this host: %v", err)
		}
		s := openTransformTestWorkspace(t, root)

		_, err := s.ProjectCsv("data.csv", "alias.csv", []string{"name"})
		if !errors.Is(err, ErrTransformOutputAliasesInput) {
			t.Fatalf("error = %v, want ErrTransformOutputAliasesInput", err)
		}
		assertTransformFileContent(t, inputPath, transformTestCSV)
	})

	t.Run("hard-link alias", func(t *testing.T) {
		root := t.TempDir()
		inputPath := filepath.Join(root, "data.csv")
		aliasPath := filepath.Join(root, "alias.csv")
		if err := os.WriteFile(inputPath, []byte(transformTestCSV), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(inputPath, aliasPath); err != nil {
			t.Skipf("hard links unavailable on this host: %v", err)
		}
		s := openTransformTestWorkspace(t, root)

		_, err := s.ProjectCsv("data.csv", "alias.csv", []string{"name"})
		if !errors.Is(err, ErrTransformOutputAliasesInput) {
			t.Fatalf("error = %v, want ErrTransformOutputAliasesInput", err)
		}
		assertTransformFileContent(t, inputPath, transformTestCSV)
		assertTransformFileContent(t, aliasPath, transformTestCSV)
	})

	t.Run("Windows case alias", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("case-alias regression is Windows-specific")
		}
		root := t.TempDir()
		inputPath := filepath.Join(root, "Data.csv")
		if err := os.WriteFile(inputPath, []byte(transformTestCSV), 0o644); err != nil {
			t.Fatal(err)
		}
		s := openTransformTestWorkspace(t, root)

		_, err := s.ProjectCsv("Data.csv", "data.csv", []string{"name"})
		if !errors.Is(err, ErrTransformOutputAliasesInput) {
			t.Fatalf("error = %v, want ErrTransformOutputAliasesInput", err)
		}
		assertTransformFileContent(t, inputPath, transformTestCSV)
	})
}

func TestTransformOutputPublishesOnlyAfterSuccess(t *testing.T) {
	root := t.TempDir()
	inputPath := filepath.Join(root, "data.csv")
	outputPath := filepath.Join(root, "projected.csv")
	if err := os.WriteFile(inputPath, []byte(transformTestCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outputPath, []byte("existing output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTransformTestWorkspace(t, root)

	if _, err := s.ProjectCsv("data.csv", "projected.csv", []string{"missing"}); err == nil {
		t.Fatal("ProjectCsv succeeded with a missing column")
	}
	assertTransformFileContent(t, outputPath, "existing output\n")
	assertNoStagedTransformFiles(t, root)

	rows, err := s.ProjectCsv("data.csv", "projected.csv", []string{"name"})
	if err != nil {
		t.Fatalf("successful ProjectCsv: %v", err)
	}
	if rows != 2 {
		t.Fatalf("rows = %d, want 2", rows)
	}
	assertTransformFileContent(t, outputPath, "name\nAda\nBob\n")
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose POSIX permission bits through Chmod/Stat.
	if got := info.Mode().Perm(); runtime.GOOS != "windows" && got != 0o600 {
		t.Fatalf("output permissions = %o, want existing mode 600", got)
	}
	assertNoStagedTransformFiles(t, root)
}

func TestExtractDumpTableCollectsSeparatedDDLAndData(t *testing.T) {
	root := t.TempDir()
	dump := "CREATE TABLE public.users (id integer);\n" +
		"CREATE TABLE public.orders (id integer);\n" +
		"COPY public.orders (id) FROM stdin;\n9\n\\.\n" +
		"COPY public.users (id) FROM stdin;\n1\n\\.\n" +
		"INSERT INTO public.users VALUES (2);\n"
	if err := os.WriteFile(filepath.Join(root, "dump.sql"), []byte(dump), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTransformTestWorkspace(t, root)
	result, err := s.ExtractDumpTable("dump.sql", "public.users", "users.sql")
	if err != nil {
		t.Fatal(err)
	}
	if result.Tables != 1 || result.Bytes <= 0 {
		t.Fatalf("result = %+v", result)
	}
	got, err := os.ReadFile(filepath.Join(root, "users.sql"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{"CREATE TABLE public.users", "COPY public.users", "\n1\n\\.\n", "INSERT INTO public.users VALUES (2)"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in extracted output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "public.orders") || strings.Contains(text, "\n9\n") {
		t.Fatalf("extracted output contains unrelated table data:\n%s", text)
	}
}

func TestExtractDumpTablePreservesDestinationOnTruncatedBlock(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "dump.sql"), []byte("COPY public.users (id) FROM stdin;\n1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "users.sql")
	if err := os.WriteFile(output, []byte("existing output\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTransformTestWorkspace(t, root)
	if _, err := s.ExtractDumpTable("dump.sql", "public.users", "users.sql"); err == nil || !strings.Contains(err.Error(), "truncated COPY") {
		t.Fatalf("error = %v, want truncated COPY refusal", err)
	}
	assertTransformFileContent(t, output, "existing output\n")
	assertNoStagedTransformFiles(t, root)
}

func TestSplitDumpUsesStatementOwnershipForEveryTable(t *testing.T) {
	root := t.TempDir()
	dump := "CREATE TABLE public.users (id integer);\n" +
		"CREATE TABLE public.orders (id integer);\n" +
		"INSERT INTO public.orders VALUES (9);\n" +
		"INSERT INTO public.users VALUES (1);\n"
	if err := os.WriteFile(filepath.Join(root, "dump.sql"), []byte(dump), 0o600); err != nil {
		t.Fatal(err)
	}
	s := openTransformTestWorkspace(t, root)
	result, err := s.SplitDump("dump.sql", "split")
	if err != nil {
		t.Fatal(err)
	}
	if result.Tables != 2 || len(result.Outputs) != 2 {
		t.Fatalf("result = %+v, want two table outputs", result)
	}
	for _, output := range result.Outputs {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(output)))
		if err != nil {
			t.Fatal(err)
		}
		text := string(content)
		switch {
		case strings.Contains(output, "users"):
			if !strings.Contains(text, "public.users") || strings.Contains(text, "public.orders") {
				t.Fatalf("users split has wrong ownership:\n%s", text)
			}
		case strings.Contains(output, "orders"):
			if !strings.Contains(text, "public.orders") || strings.Contains(text, "public.users") {
				t.Fatalf("orders split has wrong ownership:\n%s", text)
			}
		default:
			t.Fatalf("unexpected split output %q", output)
		}
	}
}

func TestTransformOutputUsesPrivateModeForNewFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits through Chmod/Stat")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.csv"), []byte(transformTestCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	s := openTransformTestWorkspace(t, root)
	if _, err := s.ProjectCsv("data.csv", "private.csv", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "private.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got&0o077 != 0 {
		t.Fatalf("new transform output mode = %o, must not grant group/other access", got)
	}
}

func TestTransformOutputPreservesExistingZeroMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose POSIX permission bits through Chmod/Stat")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.csv"), []byte(transformTestCSV), 0o644); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "locked.csv")
	if err := os.WriteFile(outputPath, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(outputPath, 0); err != nil {
		t.Fatal(err)
	}
	s := openTransformTestWorkspace(t, root)
	if _, err := s.ProjectCsv("data.csv", "locked.csv", []string{"name"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0 {
		t.Fatalf("replacement output mode = %o, want existing mode 0", got)
	}
}

func openTransformTestWorkspace(t *testing.T, root string) *Service {
	t.Helper()
	s := New()
	if _, err := s.Open(root); err != nil {
		t.Fatalf("open workspace: %v", err)
	}
	return s
}

func assertTransformFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s content = %q, want %q", path, got, want)
	}
}

func assertNoStagedTransformFiles(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".novera-transform-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staged transform files were not cleaned up: %v", matches)
	}
}
