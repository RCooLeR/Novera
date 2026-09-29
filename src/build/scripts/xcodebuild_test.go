package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestXcodebuildFailureStopsDevicePipeline(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if runtime.GOOS == "windows" {
		// The Windows system bash.exe is a WSL launcher. Use Git's bundled
		// shell to exercise the portable failure contract without Xcode.
		git, gitErr := exec.LookPath("git")
		if gitErr != nil {
			t.Skip("Git Bash is not installed")
		}
		bash = filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe")
		if _, statErr := os.Stat(bash); statErr != nil {
			t.Skip("Git Bash is not installed")
		}
		err = nil
	}
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../ios/xcodebuild.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                                        string
		formatter                                   bool
		compilerStatus, formatterStatus, wantStatus int
	}{
		{"plain success", false, 0, 0, 0},
		{"plain compiler failure", false, 17, 0, 17},
		{"formatted success", true, 0, 0, 0},
		{"formatted compiler failure", true, 17, 0, 17},
		{"formatter failure", true, 0, 23, 23},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name, body string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			write("xcodebuild", "printf '%s\\n' \"$@\" >> \"$TEST_ARGS\"\nprintf 'build output\\n'\nexit \"$COMPILER_STATUS\"\n")
			if tc.formatter {
				write("xcpretty", "while IFS= read -r line; do printf '%s\\n' \"$line\"; done\nexit \"$FORMATTER_STATUS\"\n")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			// Invoke the real helper with a PATH containing only fixture tools;
			// installed xcpretty/Xcode versions cannot influence the outcome.
			cmd := exec.CommandContext(ctx, bash, "-c", `"$BASH" "$TEST_SCRIPT" -project "project with spaces.xcodeproj" build && printf installed`)
			cmd.Env = append(os.Environ(), "PATH="+filepath.ToSlash(dir), "TEST_SCRIPT="+filepath.ToSlash(script), "TEST_ARGS="+filepath.ToSlash(filepath.Join(dir, "args")),
				"COMPILER_STATUS="+strconv.Itoa(tc.compilerStatus), "FORMATTER_STATUS="+strconv.Itoa(tc.formatterStatus))
			output, err := cmd.CombinedOutput()
			status := 0
			if err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				status = exit.ExitCode()
			}
			if status != tc.wantStatus || strings.Contains(string(output), "installed") != (status == 0) {
				t.Fatalf("status %d, want %d; output %q", status, tc.wantStatus, output)
			}
			args, err := os.ReadFile(filepath.Join(dir, "args"))
			if err != nil || string(args) != "-project\nproject with spaces.xcodeproj\nbuild\n" {
				t.Fatalf("compiler must run once with intact arguments: %q, %v", args, err)
			}
		})
	}
}
