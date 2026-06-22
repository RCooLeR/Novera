package lineindex

const (
	defaultEveryLines     int64 = 4096
	targetMaxIndexAnchors       = 1_000_000
	minTextBytesPerLine   int64 = 10
)

// EveryLinesForSize returns a sparse-index stride sized to keep anchor counts
// predictable for huge short-line files while preserving the historical default
// for ordinary files.
func EveryLinesForSize(sizeBytes int64) int64 {
	if sizeBytes <= 0 {
		return defaultEveryLines
	}
	minEvery := sizeBytes / (minTextBytesPerLine * targetMaxIndexAnchors)
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
