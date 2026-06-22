// Package buildinfo exposes compile-time build metadata.
//
// Release builds may inject version, commit, or date values through linker
// flags. The rest of the app reads them from here instead of duplicating build
// variable names across packages.
package buildinfo
