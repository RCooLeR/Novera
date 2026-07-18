// Package encodingx wraps text encoding and decoding helpers.
//
// Novera must preserve byte offsets while still showing text to users. This
// package centralizes BOM handling, decoder readers, encoder writers, and
// encoding names so export and document code agree on how bytes become text.
package encodingx
