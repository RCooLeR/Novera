// Package exportx streams selected text or file ranges to new output files.
//
// Export operations never overwrite the source file. They copy bounded byte or
// line ranges through optional decoders/encoders, can compute SHA-256 evidence,
// and write manifests that describe exactly what was produced.
package exportx
