package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestGoVersionAlignment(t *testing.T) {
	t.Parallel()
	version := func(path, pattern string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		matches := regexp.MustCompile(pattern).FindAllSubmatch(data, -1)
		if len(matches) != 1 {
			t.Fatalf("%s: expected one Go version pin, got %d", path, len(matches))
		}
		return string(matches[0][1])
	}
	want := version("src/go.mod", `(?m)^go ([0-9]+\.[0-9]+\.[0-9]+)\s*$`)
	for _, workflow := range []string{"ci.yml", "release.yml", "security.yml", "performance.yml"} {
		got := version(".github/workflows/"+workflow, `(?m)^\s*GO_VERSION:\s*["']?([0-9]+\.[0-9]+\.[0-9]+)["']?\s*(?:#.*)?$`)
		if got != want {
			t.Errorf("%s uses Go %s; go.mod requires %s (update the module, workflows and Docker image together)", workflow, got, want)
		}
	}
	got := version("src/build/docker/Dockerfile.cross", `(?m)^FROM\s+[^\r\n]*golang:([0-9]+\.[0-9]+\.[0-9]+)-bookworm@sha256:[a-f0-9]{64}\s*$`)
	if got != want {
		t.Errorf("Docker image uses Go %s; go.mod requires %s", got, want)
	}
}

// Dependabot proposes the Go and npm updates independently, but Wails' CLI,
// generator, Go runtime and renderer must be validated at the same version.
func TestWailsVersionAlignment(t *testing.T) {
	t.Parallel()
	read := func(path string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("../../..", path))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	goPins := regexp.MustCompile(`(?m)^\s*github\.com/wailsapp/wails/v3\s+(v\S+)\s*(?://.*)?$`).FindAllSubmatch(read("src/go.mod"), -1)
	if len(goPins) != 1 {
		t.Fatalf("expected one Wails Go module pin, got %d", len(goPins))
	}
	want := strings.TrimPrefix(string(goPins[0][1]), "v")
	check := func(name, got string) {
		t.Helper()
		if got != want {
			t.Errorf("%s pins %q; Wails Go module requires %q (update Go, npm and CLI together)", name, got, want)
		}
	}
	type npmPackage struct {
		Version      string            `json:"version"`
		Dependencies map[string]string `json:"dependencies"`
	}
	var manifest npmPackage
	if err := json.Unmarshal(read("src/frontend/package.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	check("package.json runtime", manifest.Dependencies["@wailsio/runtime"])
	var lock struct {
		Packages map[string]npmPackage `json:"packages"`
	}
	if err := json.Unmarshal(read("src/frontend/package-lock.json"), &lock); err != nil {
		t.Fatal(err)
	}
	check("package-lock.json root runtime", lock.Packages[""].Dependencies["@wailsio/runtime"])
	check("package-lock.json installed runtime", lock.Packages["node_modules/@wailsio/runtime"].Version)
	for _, workflow := range []string{"ci.yml", "release.yml"} {
		pins := regexp.MustCompile(`(?m)^\s*WAILS_VERSION:\s*["']?v([^\s"']+)["']?\s*(?:#.*)?$`).FindAllSubmatch(read(".github/workflows/"+workflow), -1)
		if len(pins) != 1 {
			t.Errorf("%s: expected one WAILS_VERSION pin, got %d", workflow, len(pins))
			continue
		}
		check(workflow+" CLI", string(pins[0][1]))
	}
}
