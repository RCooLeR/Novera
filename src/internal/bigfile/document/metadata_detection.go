package document

import (
	"bytes"
	"strings"
	"unicode/utf8"

	"novera/internal/bigfile/encodingx"
	"novera/internal/bigfile/plugins/filetype"
)

func detectMetadata(path string, size int64, sample []byte) Metadata {
	truncatedSample := int64(len(sample)) < size
	enc := encodingx.DetectSample(sample)
	if truncatedSample {
		enc = encodingx.DetectPrefixSample(sample)
	}
	binary, binaryConfidence := detectBinary(sample, truncatedSample)
	if enc.RequiresConfirmation || strings.HasPrefix(enc.Name, "UTF-16") || strings.HasPrefix(enc.Name, "UTF-32") {
		binary = false
		binaryConfidence = enc.Confidence
	}
	decodedSample := ""
	if !enc.RequiresConfirmation {
		decodedSample = encodingx.DecodeBytesBestEffort(enc.Name, sample)
	}

	return Metadata{
		Path:                         path,
		Size:                         size,
		Encoding:                     enc.Name,
		EncodingConfidence:           enc.Confidence,
		EncodingRequiresConfirmation: enc.RequiresConfirmation,
		HasBOM:                       enc.HasBOM,
		LineEnding:                   detectLineEnding(decodedSample),
		FileType:                     detectFileType(path, decodedSample, binary),
		Binary:                       binary,
		BinaryConfidence:             binaryConfidence,
	}
}

func detectBinary(sample []byte, allowIncompleteUTF8Tail bool) (bool, float64) {
	if len(sample) == 0 {
		return false, 0
	}
	if bytes.IndexByte(sample, 0) >= 0 {
		return true, 0.95
	}
	if utf8.Valid(sample) || (allowIncompleteUTF8Tail && encodingx.ValidUTF8Prefix(sample)) {
		return false, 0.8
	}

	control := 0
	for _, b := range sample {
		if b < 0x09 || (b > 0x0D && b < 0x20) {
			control++
		}
	}
	ratio := float64(control) / float64(len(sample))
	if ratio > 0.03 {
		return true, ratio
	}
	return false, 0.5
}

func detectLineEnding(sample string) string {
	if len(sample) == 0 {
		return "unknown"
	}

	crlf := strings.Count(sample, "\r\n")
	lf := strings.Count(sample, "\n") - crlf
	cr := strings.Count(sample, "\r") - crlf

	types := 0
	if crlf > 0 {
		types++
	}
	if lf > 0 {
		types++
	}
	if cr > 0 {
		types++
	}
	if types > 1 {
		return "mixed"
	}
	switch {
	case crlf > 0:
		return "CRLF"
	case lf > 0:
		return "LF"
	case cr > 0:
		return "CR"
	default:
		return "none"
	}
}

func detectFileType(path string, sample string, binary bool) string {
	return filetype.Detect(path, sample, binary)
}
