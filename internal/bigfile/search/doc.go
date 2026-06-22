// Package search streams bounded searches over large files.
//
// Search readers process chunks and carry just enough overlap to catch matches
// that cross chunk boundaries. Results use byte offsets so navigation and
// replacement can jump directly to file positions.
package search
