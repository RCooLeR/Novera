package lineindex

const (
	defaultEveryLines       int64 = 4096
	MaxIndexEntries               = 250_000
	maxPriorityIndexEntries       = 4096
)

// EveryLinesForSize returns a sparse-index stride sized to keep anchor counts
// predictable for huge short-line files while preserving the historical default
// for ordinary files.
func EveryLinesForSize(sizeBytes int64) int64 {
	if sizeBytes <= 0 {
		return defaultEveryLines
	}
	// A file made entirely of one-byte LF records is the actual worst case.
	// Leave room for the mandatory line-one anchor.
	anchorBudget := int64(MaxIndexEntries - 1)
	minEvery := sizeBytes / anchorBudget
	if sizeBytes%anchorBudget != 0 {
		minEvery++
	}
	if minEvery <= defaultEveryLines {
		return defaultEveryLines
	}
	return nextPowerOfTwo(minEvery)
}

func nextPowerOfTwo(value int64) int64 {
	if value <= 1 {
		return 1
	}
	pow := int64(1)
	for pow < value && pow > 0 {
		pow <<= 1
	}
	if pow <= 0 {
		return value
	}
	return pow
}
