package encodingx

import (
	"encoding/binary"
	"math"
	"unicode"
	"unicode/utf16"
)

const ambiguousUTF16Name = "Ambiguous UTF-16"

type utf16CandidateScore struct {
	name           string
	score          float64
	plausible      bool
	scalars        int
	surrogatePairs int
}

// detectBOMlessUTF16 scores both byte orders conservatively. When both
// decodings look plausible and endian evidence is weak, callers must request
// explicit confirmation instead of silently selecting a corrupting guess.
func detectBOMlessUTF16(sample []byte, allowIncompleteUnit bool) (Info, bool) {
	if len(sample) < 8 {
		return Info{}, false
	}
	if len(sample)%2 != 0 {
		if !allowIncompleteUnit {
			return Info{}, false
		}
		sample = sample[:len(sample)-1]
	}
	if len(sample) < 8 {
		return Info{}, false
	}

	le := scoreUTF16Candidate(sample, binary.LittleEndian, "UTF-16LE")
	be := scoreUTF16Candidate(sample, binary.BigEndian, "UTF-16BE")
	best, other := le, be
	if be.score > le.score {
		best, other = be, le
	}

	const (
		minimumPlausible = 0.62
		minimumCertain   = 0.70
		minimumLead      = 0.12
	)
	if best.plausible && best.score >= minimumCertain {
		if !other.plausible || other.score < minimumPlausible || best.score-other.score >= minimumLead {
			confidence := 0.72 + math.Min(0.23, math.Max(0, best.score-other.score)*0.8)
			return Info{Name: best.name, Confidence: confidence}, true
		}
		return Info{
			Name:                 ambiguousUTF16Name,
			Confidence:           math.Min(best.score, 0.69),
			RequiresConfirmation: true,
		}, true
	}
	return Info{}, false
}

func scoreUTF16Candidate(sample []byte, order binary.ByteOrder, name string) utf16CandidateScore {
	result := utf16CandidateScore{name: name}
	var graphic, textLike, commonEvidence, swappedASCII, bad int

	for i := 0; i+1 < len(sample); {
		unit := order.Uint16(sample[i : i+2])
		i += 2
		var r rune
		switch {
		case unit >= 0xD800 && unit <= 0xDBFF:
			if i+1 >= len(sample) {
				bad++
				continue
			}
			next := order.Uint16(sample[i : i+2])
			if next < 0xDC00 || next > 0xDFFF {
				bad++
				continue
			}
			i += 2
			r = utf16.DecodeRune(rune(unit), rune(next))
			result.surrogatePairs++
		case unit >= 0xDC00 && unit <= 0xDFFF:
			bad++
			continue
		default:
			r = rune(unit)
		}

		result.scalars++
		if r == 0 || isUnicodeNoncharacter(r) || (unicode.IsControl(r) && r != '\t' && r != '\r' && r != '\n') {
			bad++
			continue
		}
		if unicode.IsGraphic(r) || r == '\t' || r == '\r' || r == '\n' {
			graphic++
		}
		if unicode.IsLetter(r) || unicode.IsNumber(r) || unicode.IsMark(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) || unicode.IsSpace(r) {
			textLike++
		}
		switch {
		case r == ' ' || r == '\t' || r == '\r' || r == '\n':
			commonEvidence += 4
		case r >= 0x21 && r <= 0x7E:
			commonEvidence += 2
		}
		if r&0xFF == 0 {
			high := r >> 8
			if high >= 0x20 && high <= 0x7E {
				swappedASCII++
			}
		}
	}

	if result.scalars < 4 || bad > 0 {
		return result
	}
	total := float64(result.scalars)
	graphicRatio := float64(graphic) / total
	textRatio := float64(textLike) / total
	commonBonus := math.Min(0.25, float64(commonEvidence)/total*0.15)
	pairBonus := math.Min(0.25, float64(result.surrogatePairs)/total*0.75)
	swappedPenalty := math.Min(0.70, float64(swappedASCII)/total*0.85)
	result.score = 0.45*graphicRatio + 0.25*textRatio + commonBonus + pairBonus - swappedPenalty
	result.plausible = graphicRatio >= 0.80 && textRatio >= 0.65
	return result
}

func isUnicodeNoncharacter(r rune) bool {
	return (r >= 0xFDD0 && r <= 0xFDEF) || (r >= 0 && r <= unicode.MaxRune && r&0xFFFE == 0xFFFE)
}
