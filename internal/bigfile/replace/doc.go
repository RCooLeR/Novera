// Package replace streams search-and-replace operations to safe outputs.
//
// Replacements never patch the source file in place while scanning. The package
// writes a new file, tracks lifecycle evidence in manifests, supports recovery,
// and verifies the source has not changed before any optional final swap.
//
// The implementation is chunked because files may be tens or hundreds of
// gigabytes. Boundary carry buffers let matches cross chunk edges without
// loading the whole source into memory.
package replace
