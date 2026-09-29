package csv

import "fmt"

const (
	// MaxCSVFieldsPerRecord is the application-wide ceiling for fields in one
	// logical CSV record. The raw-byte ceiling alone is insufficient: a record
	// containing only delimiters can otherwise make encoding/csv allocate an
	// attacker-controlled number of field descriptors and can fan that width
	// out through bridge payloads and frontend state.
	MaxCSVFieldsPerRecord = 1024

	// MaxTransformColumnMappings bounds caller-controlled projection, redaction,
	// and CSV-to-SQL column maps. At this limit, a projected record retains at
	// most 16 KiB of string headers on 64-bit platforms before field data; the
	// 16 MiB logical-record ceiling remains the separate data-byte bound.
	MaxTransformColumnMappings = MaxCSVFieldsPerRecord

	// MaxTransformConfigStringBytes bounds the aggregate byte length of strings
	// retained or indexed while normalizing one transform configuration. It is
	// intentionally shared by projection, redaction, and CSV-to-SQL so direct
	// plugin callers cannot bypass the bridge boundary policy.
	MaxTransformConfigStringBytes = 1 << 20
)

func validateTransformColumnMappingCount(label string, count int, require bool) error {
	if require && count == 0 {
		return fmt.Errorf("%s requires at least one column", label)
	}
	if count > MaxTransformColumnMappings {
		return fmt.Errorf("%s has %d column mappings; maximum is %d", label, count, MaxTransformColumnMappings)
	}
	return nil
}

// addTransformConfigString accounts for a string without allowing the running
// total to overflow. Callers invoke it before allocating normalized slices or
// maps from the configuration.
func addTransformConfigString(total *int, label, value string) error {
	remaining := MaxTransformConfigStringBytes - *total
	if len(value) > remaining {
		return fmt.Errorf("%s configuration strings exceed %d-byte aggregate limit", label, MaxTransformConfigStringBytes)
	}
	*total += len(value)
	return nil
}

func validateDistinctNonNegativeIndexes(label string, indexes []int) error {
	for i, index := range indexes {
		if index < 0 {
			return fmt.Errorf("%s index %d at position %d is negative", label, index, i)
		}
		// Keep validation allocation-free. The shared 1,024-entry ceiling makes
		// this bounded quadratic pass cheaper and safer than sizing a map from an
		// unchecked bridge-controlled collection.
		for previous := 0; previous < i; previous++ {
			if indexes[previous] == index {
				return fmt.Errorf("%s index %d duplicates position %d at position %d", label, index, previous, i)
			}
		}
	}
	return nil
}
