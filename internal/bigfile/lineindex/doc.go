// Package lineindex builds sparse line-to-byte indexes.
//
// A full line index for a huge file can be too large, so Quarry stores anchor
// points every N lines and uses nearby scans to answer exact questions. This
// gives fast navigation while keeping memory bounded for very large inputs.
package lineindex
