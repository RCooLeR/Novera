// Package asciifold provides byte-stable ASCII case folding.
//
// Quarry works with byte offsets so huge files can be searched, replaced, and
// sliced without loading the whole file. Full Unicode case folding can change
// byte length, so this package only folds ASCII A-Z to a-z and leaves every
// other byte unchanged.
package asciifold
