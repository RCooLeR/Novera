// Package cachepath creates deterministic cache file paths.
//
// Cache files are derived from source paths and feature-specific suffixes. The
// helpers in this package keep those names stable, filesystem-safe, and grouped
// by purpose so cached indexes or metadata do not collide.
package cachepath
