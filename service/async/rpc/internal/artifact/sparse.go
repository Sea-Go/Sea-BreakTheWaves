package artifact

import (
	"encoding/binary"
	"fmt"
	"math"
)

// impactRecordBytes is the fixed on-wire size of one term: a 4-byte
// little-endian TermID followed by a 1-byte weight.
const impactRecordBytes = 5

// Term is the sparse-lane impact representation: a term ID with a linear
// 0-255 weight.
type Term struct {
	TermID uint32
	Weight uint8
}

// ImpactWeight linearly maps a float32 weight to a uint8 impact:
// round(255*w/maxW), with w clamped to [0,maxW]. A non-positive maxW or a
// non-finite input returns 0 so the mapping never yields NaN.
func ImpactWeight(w, maxW float32) uint8 {
	if !(maxW > 0) || !finite32(w) || !finite32(maxW) {
		return 0
	}
	if w <= 0 {
		return 0
	}
	if w >= maxW {
		return 255
	}
	return uint8(math.Round(float64(w) * 255.0 / float64(maxW)))
}

// EncodeImpact encodes terms as fixed-width 5-byte records (TermID
// little-endian, then Weight), preserving order. An empty list encodes to an
// empty (non-nil) byte slice.
func EncodeImpact(terms []Term) []byte {
	out := make([]byte, 0, impactRecordBytes*len(terms))
	var rec [impactRecordBytes]byte
	for _, t := range terms {
		binary.LittleEndian.PutUint32(rec[:4], t.TermID)
		rec[4] = t.Weight
		out = append(out, rec[:]...)
	}
	return out
}

// DecodeImpact decodes an EncodeImpact payload, preserving order. It fails
// when the payload length is not a multiple of 5.
func DecodeImpact(b []byte) ([]Term, error) {
	if len(b)%impactRecordBytes != 0 {
		return nil, fmt.Errorf("impact payload length %d is not a multiple of %d", len(b), impactRecordBytes)
	}
	terms := make([]Term, 0, len(b)/impactRecordBytes)
	for i := 0; i < len(b); i += impactRecordBytes {
		terms = append(terms, Term{
			TermID: binary.LittleEndian.Uint32(b[i : i+4]),
			Weight: b[i+4],
		})
	}
	return terms, nil
}
