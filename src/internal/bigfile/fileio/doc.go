// Package fileio contains filesystem helpers with explicit safety semantics.
//
// The package owns atomic writes, exclusive output creation, path comparisons,
// and overwrite recovery. Callers use these helpers instead of open-coded file
// writes so source files are not accidentally overwritten or partially updated.
package fileio
