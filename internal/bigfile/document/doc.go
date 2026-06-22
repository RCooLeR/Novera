// Package document is Quarry's bounded file-reading model.
//
// A FileDocument represents a source file without loading it all into memory.
// Callers ask for byte ranges, line starts, decoded previews, and sparse index
// information. The package owns the rules that keep huge-file access bounded
// and byte-offset-safe.
//
// The important idea for beginners: the document is a window into a file, not a
// string copy of the whole file. Most functions therefore accept byte limits or
// return offsets that other packages can use for streaming operations.
package document
