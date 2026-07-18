package bigfile

const (
	defaultSearchAllHits        = 1000
	maxSearchAllHits            = 10_000
	maxWindowRequestBytes       = 8 << 20
	maxCSVGridBytes             = 1 << 20
	maxEditWindowBytes          = 4 << 20
	maxHexWindowBytes           = 256 << 10
	defaultCSVPreviewRows       = 50
	maxCSVPreviewRows           = 1_000
	maxCSVColumns               = 10_000
	defaultSQLFixtureRows       = 100
	maxSQLFixtureRows           = 10_000
	defaultSQLBatchRows         = 100
	maxSQLBatchRows             = 10_000
	maxInt64Value         int64 = 1<<63 - 1
)

// clampRequestInt treats non-positive caller values as "use the default" and
// caps positive values at an immutable backend maximum before any conversion,
// arithmetic, allocation, scan, or bridge serialization occurs.
func clampRequestInt(value, fallback, maximum int) int {
	if fallback <= 0 || maximum < fallback {
		panic("bigfile: invalid request budget")
	}
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

// boundedReadEnd adds a previously clamped byte budget without overflowing an
// int64 near the end of a very large or sparse file.
func boundedReadEnd(start, size int64, budget int) int64 {
	if start < 0 {
		start = 0
	}
	if start >= size || budget <= 0 {
		return size
	}
	span := int64(budget)
	if span >= size-start {
		return size
	}
	return start + span
}

func checkedRangeEnd(start, length int64) (int64, bool) {
	if start < 0 || length < 0 || start > maxInt64Value-length {
		return 0, false
	}
	return start + length, true
}
