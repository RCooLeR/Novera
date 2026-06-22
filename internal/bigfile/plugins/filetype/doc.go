// Package filetype detects file types from names, extensions, and content hints.
//
// Quarry uses file type results to choose syntax rules, tool panels, and safe
// defaults. Keeping detection here avoids scattering extension checks through
// editor and UI code.
package filetype
