// Package manualedit implements bounded manual replacement sessions.
//
// It uses a piece-table style model: original file ranges and inserted text are
// tracked separately, then streamed to a new output when saved. This avoids
// rewriting huge in-memory strings. Each session has explicit per-edit,
// cumulative inserted-text, history, piece-count, and transient rebuild limits.
package manualedit
