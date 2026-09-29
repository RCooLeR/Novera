//go:build windows

package fileio

import (
	"path/filepath"
	"strings"
)

// validatePlatformExactPath rejects Windows spellings that name a device,
// alternate data stream, or a component Win32 would silently rewrite.
func validatePlatformExactPath(path string, clean string) error {
	volume := filepath.VolumeName(path)
	if volume != "" && !filepath.IsAbs(path) {
		return invalidWindowsExactPath(path, clean, "drive-relative paths are not supported")
	}
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, `\\.\`) || strings.HasPrefix(lower, `\??\`) {
		return invalidWindowsExactPath(path, clean, "device-namespace paths are not supported")
	}
	if strings.HasPrefix(lower, `\\?\`) {
		return invalidWindowsExactPath(path, clean, "extended device paths are not supported by this publication primitive")
	}

	remainder := path[len(volume):]
	if strings.Contains(remainder, ":") {
		return invalidWindowsExactPath(path, clean, "alternate-data-stream paths are not supported")
	}
	for _, component := range strings.FieldsFunc(remainder, func(r rune) bool {
		return r == '\\' || r == '/'
	}) {
		if strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
			return invalidWindowsExactPath(path, clean, "components ending in a dot or space are not exact")
		}
		if strings.ContainsAny(component, `<>"|?*`) || containsWindowsControlCharacter(component) {
			return invalidWindowsExactPath(path, clean, "path contains a Win32-reserved character")
		}
		if isWindowsReservedComponent(component) {
			return invalidWindowsExactPath(path, clean, "reserved DOS device-name components are not supported")
		}
	}
	return nil
}

func invalidWindowsExactPath(path string, clean string, reason string) error {
	return &InvalidExactPathError{Path: path, CleanPath: clean, Reason: reason}
}

func containsWindowsControlCharacter(component string) bool {
	for _, value := range component {
		if value >= 0 && value < 32 {
			return true
		}
	}
	return false
}

func isWindowsReservedComponent(component string) bool {
	stem := component
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " "))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CLOCK$", "CONIN$", "CONOUT$":
		return true
	}
	if !strings.HasPrefix(stem, "COM") && !strings.HasPrefix(stem, "LPT") {
		return false
	}
	switch stem[3:] {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "\u00b9", "\u00b2", "\u00b3":
		return true
	default:
		return false
	}
}
