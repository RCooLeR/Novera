// Package jsonsafe centralizes strict decoding for persisted, security-sensitive
// JSON. Go 1.27's encoding/json/v2 rejects invalid UTF-8 and duplicate object
// names by default; persisted Novera formats additionally reject unknown fields.
package jsonsafe

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
)

// Unmarshal decodes exactly one JSON value using case-sensitive field matching.
// Duplicate names, invalid UTF-8, unknown struct fields, and trailing values are
// rejected. Legacy merge semantics preserve existing persisted-format behavior
// when callers decode partial documents over non-zero defaults. Callers remain
// responsible for bounding the input before decoding.
func Unmarshal(data []byte, out any) error {
	return jsonv2.Unmarshal(
		data,
		out,
		jsonv2.RejectUnknownMembers(true),
		jsonv1.MergeWithLegacySemantics(true),
	)
}
