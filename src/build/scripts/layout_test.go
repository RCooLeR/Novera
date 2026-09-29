package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteOutputPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, before, after string
		pairs               [][2]string
	}{
		{"nfpm", `src: "./bin/Novera"`, `src: "../bin/Novera"`, [][2]string{{`src: "./bin/`, `src: "../bin/`}}},
		{"ios", `path = "../../../bin/Novera.a"; -o \"bin/Novera.a\"`, `path = "../../../../bin/Novera.a"; -o \"../bin/Novera.a\"`, iosArchivePaths},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "asset")
			if err := os.WriteFile(path, []byte(tc.before), 0o600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := rewriteOutputPaths(path, tc.pairs); err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(path)
				if err != nil || string(got) != tc.after {
					t.Fatalf("got %q, %v; want %q", got, err, tc.after)
				}
			}
			for _, invalid := range []string{"", tc.before + tc.before, tc.before + tc.after} {
				if err := os.WriteFile(path, []byte(invalid), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := rewriteOutputPaths(path, tc.pairs); err == nil {
					t.Fatal("accepted missing or ambiguous output path")
				}
				got, _ := os.ReadFile(path)
				if string(got) != invalid {
					t.Fatal("mutated an unrecognized template")
				}
			}
		})
	}
}

func TestReconcileBuildAssets(t *testing.T) {
	fixture := t.TempDir()
	assets := map[string]string{
		"darwin/Info.plist":     "<key>LSMinimumSystemVersion</key><string>12.0.0</string>",
		"darwin/Info.dev.plist": "<key>LSMinimumSystemVersion</key><string>12.0.0</string>",
		"linux/nfpm/nfpm.yaml":  `contents: [{src: "./bin/Novera"}]`,
		"ios/project.pbxproj":   `path = "../../../bin/Novera.a"; -o \"bin/Novera.a\"`,
	}
	for path, data := range assets {
		path = filepath.Join(fixture, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(fixture)
	for range 2 {
		if err := reconcileBuildAssets(); err != nil {
			t.Fatal(err)
		}
	}
	for path, want := range map[string]string{
		"darwin/Info.plist":     "<string>13.0.0</string>",
		"darwin/Info.dev.plist": "<string>13.0.0</string>",
		"linux/nfpm/nfpm.yaml":  `src: "../bin/Novera"`,
		"ios/project.pbxproj":   `-o \"../bin/Novera.a\"`,
	} {
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s: got %q, %v; want %q", path, data, err, want)
		}
	}
}

func TestDockerOutputIsSeparateFromSource(t *testing.T) {
	t.Parallel()
	for _, platform := range []string{"windows", "darwin", "linux"} {
		data, err := os.ReadFile(filepath.Join("..", platform, "Taskfile.yml"))
		if err != nil {
			t.Fatal(err)
		}
		body := string(data)
		if !strings.Contains(body, `-v "{{.SOURCE_DIR}}:/app" -v "{{.BIN_DIR}}:/out"`) || strings.Contains(body, "/app/bin") {
			t.Fatalf("%s must mount root bin separately from source", platform)
		}
	}
	data, err := os.ReadFile("../docker/Dockerfile.cross")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`OUTPUT="/out/${APP}`, "CC_TARGET=$($CC -dumpmachine", "output architecture verification failed"} {
		if !strings.Contains(string(data), required) {
			t.Fatalf("Dockerfile missing %q", required)
		}
	}
}

// Exercise the actual nested Task includes in an isolated copy. The probe only
// prints paths; no real frontend, compiler, packaging or Docker commands run.
func TestTaskEntrypointLayout(t *testing.T) {
	task, err := exec.LookPath("task")
	if err != nil {
		t.Skip("Task executable is not installed")
	}
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(t.TempDir(), "repo with spaces")
	for _, relative := range []string{
		"Taskfile.yml", "src/Taskfile.yml", "src/build/Taskfile.yml",
		"src/build/windows/Taskfile.yml", "src/build/darwin/Taskfile.yml",
		"src/build/linux/Taskfile.yml", "src/build/android/Taskfile.yml", "src/build/ios/Taskfile.yml",
	} {
		data, err := os.ReadFile(filepath.Join(repo, relative))
		if err != nil {
			t.Fatal(err)
		}
		if relative == "src/build/Taskfile.yml" {
			data = append(data, []byte("\n  layout:probe:\n    dir: frontend\n    cmds:\n      - pwd\n      - echo \"{{.SOURCE_DIR}}\"\n      - echo \"{{.BIN_DIR}}\"\n      - echo \"{{.LAYOUT_VALUE}}\"\n")...)
		}
		path := filepath.Join(fixture, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(fixture, "src/frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		filepath.ToSlash(filepath.Join(fixture, "src/frontend")),
		filepath.ToSlash(filepath.Join(fixture, "src")),
		filepath.ToSlash(filepath.Join(fixture, "bin")),
		"LayoutProbe",
	}, "\n")
	for _, dir := range []string{fixture, filepath.Join(fixture, "src")} {
		for _, name := range []string{"common:layout:probe", "windows:common:layout:probe"} {
			cmd := exec.Command(task, "--silent", name, "LAYOUT_VALUE=LayoutProbe")
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%s in %s: %v\n%s", name, dir, err, output)
			}
			got := filepath.ToSlash(strings.TrimSpace(strings.ReplaceAll(string(output), "\r\n", "\n")))
			if got != want {
				t.Fatalf("%s in %s: got %q; want %q", name, dir, got, want)
			}
		}
	}
}
