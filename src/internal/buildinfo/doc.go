// Package buildinfo exposes the single canonical Novera build identity.
//
// Release builds inject version, commit, channel, and build-date values through
// linker flags. Development builds retain explicit, honest fallback values and
// supplement them with Go's VCS build settings when those are available.
package buildinfo
