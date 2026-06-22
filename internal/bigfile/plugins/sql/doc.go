// Package sql provides SQL language support and dump-oriented tooling.
//
// It includes syntax highlighting, schema/table analysis, presets, and
// extraction helpers. The tools are designed for large database dumps, so they
// prefer streaming scans and metadata summaries over whole-file parsing.
package sql
