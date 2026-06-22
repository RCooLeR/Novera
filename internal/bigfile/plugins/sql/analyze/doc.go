// Package analyze scans SQL dumps for table and schema metadata.
//
// The analyzer looks for meaningful SQL structures such as CREATE TABLE and
// INSERT ranges without loading an entire dump into memory. UI tools use this
// metadata for navigation and split/extract previews.
package analyze
