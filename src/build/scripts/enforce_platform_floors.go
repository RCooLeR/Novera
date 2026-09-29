package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
)

const macOSMinimumVersion = "13.0.0"

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = reconcileBuildAssets()
	case len(os.Args) == 3 && os.Args[1] == "-ios-project":
		err = rewriteOutputPaths(os.Args[2], iosArchivePaths)
	default:
		err = fmt.Errorf("usage: enforce_platform_floors [-ios-project project.pbxproj]")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// Wails templates assume bin lives beside go.mod. Preserve the repository's
// shared root bin as well as the newer OS floor after regenerating assets.
func reconcileBuildAssets() error {
	for _, path := range []string{"darwin/Info.plist", "darwin/Info.dev.plist"} {
		if err := setPlistString(path, "LSMinimumSystemVersion", macOSMinimumVersion); err != nil {
			return err
		}
	}
	if err := rewriteOutputPaths("linux/nfpm/nfpm.yaml", [][2]string{{`src: "./bin/`, `src: "../bin/`}}); err != nil {
		return err
	}
	return rewriteOutputPaths("ios/project.pbxproj", iosArchivePaths)
}

var iosArchivePaths = [][2]string{
	{`path = "../../../bin/`, `path = "../../../../bin/`},
	{`-o \"bin/`, `-o \"../bin/`},
}

func rewriteOutputPaths(path string, replacements [][2]string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated := string(data)
	for _, pair := range replacements {
		if strings.Count(updated, pair[0])+strings.Count(updated, pair[1]) != 1 {
			return fmt.Errorf("%s must contain exactly one output path %q or %q", path, pair[0], pair[1])
		}
		updated = strings.ReplaceAll(updated, pair[0], pair[1])
	}
	if updated == string(data) {
		return nil
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func setPlistString(path, key, value string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	prefix := []byte("<key>" + key + "</key>")
	keyOffset := bytes.Index(data, prefix)
	if keyOffset < 0 || bytes.Index(data[keyOffset+len(prefix):], prefix) >= 0 {
		return fmt.Errorf("%s must contain exactly one %s key", path, key)
	}

	valueToken := keyOffset + len(prefix)
	for valueToken < len(data) && isXMLSpace(data[valueToken]) {
		valueToken++
	}
	const stringOpen = "<string>"
	if !bytes.HasPrefix(data[valueToken:], []byte(stringOpen)) {
		return fmt.Errorf("%s must contain a string immediately after %s", path, key)
	}
	valueStart := valueToken + len(stringOpen)
	valueEnd := bytes.Index(data[valueStart:], []byte("</string>"))
	if valueEnd < 0 {
		return fmt.Errorf("%s has an unterminated string value for %s", path, key)
	}
	if nextKey := bytes.Index(data[valueStart:], []byte("<key>")); nextKey >= 0 && nextKey < valueEnd {
		return fmt.Errorf("%s has an intervening key before the value for %s ends", path, key)
	}
	valueEnd += valueStart

	if string(data[valueStart:valueEnd]) == value {
		return nil
	}
	updated := make([]byte, 0, len(data)-valueEnd+valueStart+len(value))
	updated = append(updated, data[:valueStart]...)
	updated = append(updated, value...)
	updated = append(updated, data[valueEnd:]...)
	if err := os.WriteFile(path, updated, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

func isXMLSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}
