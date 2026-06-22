//go:build !windows && !linux

package document

import "os"

// applyReadHint is a no-op on platforms without a portable sequential-read hint.
func applyReadHint(f *os.File, _ string) *os.File { return f }
