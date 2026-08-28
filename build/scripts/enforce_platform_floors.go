package main

import (
	"bytes"
	"fmt"
	"os"
)

const macOSMinimumVersion = "13.0.0"

func main() {
	for _, path := range []string{"darwin/Info.plist", "darwin/Info.dev.plist"} {
		if err := setPlistString(path, "LSMinimumSystemVersion", macOSMinimumVersion); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
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
