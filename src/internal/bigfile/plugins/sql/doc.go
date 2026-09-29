// Package sql provides SQL language support and dump-oriented tooling.
//
// It includes syntax highlighting, schema/table analysis, reshape, fixture,
// schema-diff, and extraction helpers. Operations publish individual processing
// and memory models rather than one plugin-wide huge-file-safety promise.
// Cleanup preset builders remain for compatibility, but the service does not
// advertise or execute them until structural SQL safety is proven.
package sql
