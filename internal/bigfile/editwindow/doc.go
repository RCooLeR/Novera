// Package editwindow manages editable slices of huge files.
//
// Huge-file editing works by loading a bounded byte window, staging edits for
// that window, and writing replacements to a new output path. The package keeps
// that logic separate from widgets so tests can verify slice boundaries and
// edit safety directly.
package editwindow
