package wholeindex

import (
	"encoding/binary"
	"math"
)

// ScaleHeaderBytes is the size of the little-endian float32 scale prefix that
// precedes the int8 payload of every quantized block.
const ScaleHeaderBytes = 4

// QuantizeF32 symmetrically quantizes a dense float32 vector to int8 bytes.
//
// Layout: [4-byte little-endian float32 scale][len(v) int8 values].
// scale = 127/max|v|; each element is round(x*scale) clamped to [-127,127].
// For an all-zero (or non-finite) input the scale is 0, every byte is 0 and
// DequantizeI8 returns zeros without producing NaN or Inf. An empty vector
// encodes to just the 4-byte scale header.
func QuantizeF32(v []float32) []byte {
	scale := quantScale(v)
	out := make([]byte, ScaleHeaderBytes+len(v))
	binary.LittleEndian.PutUint32(out[:ScaleHeaderBytes], math.Float32bits(scale))
	fs := float64(scale)
	for i, x := range v {
		if !finite32(x) {
			continue // non-finite input quantizes to 0
		}
		q := int32(math.Round(float64(x) * fs))
		if q > 127 {
			q = 127
		} else if q < -127 {
			q = -127
		}
		out[ScaleHeaderBytes+i] = byte(int8(q))
	}
	return out
}

// DequantizeI8 reconstructs the float32 vector of dimension dim from a
// QuantizeF32 payload. It returns nil when the payload length does not match
// 4+dim. A zero scale (all-zero input) reconstructs an all-zero vector.
func DequantizeI8(q []byte, dim int) []float32 {
	if dim < 0 || len(q) != ScaleHeaderBytes+dim {
		return nil
	}
	scale := math.Float32frombits(binary.LittleEndian.Uint32(q[:ScaleHeaderBytes]))
	out := make([]float32, dim)
	if scale == 0 {
		return out
	}
	for i := range out {
		out[i] = float32(int8(q[ScaleHeaderBytes+i])) / scale
	}
	return out
}

// QuantizeMulti quantizes a multi-token matrix held as row-major []float32
// with rows of width dim, as one whole block: a single scale = 127/max|mat|
// covers every element and the layout matches QuantizeF32. It returns nil
// when dim <= 0 or len(mat) is not a multiple of dim.
func QuantizeMulti(mat []float32, dim int) []byte {
	if dim <= 0 || len(mat)%dim != 0 {
		return nil
	}
	return QuantizeF32(mat)
}

// DequantizeMulti reconstructs a rows*dim row-major matrix from a
// QuantizeMulti block. It returns nil when the payload length does not match
// 4+rows*dim.
func DequantizeMulti(q []byte, rows, dim int) []float32 {
	if rows < 0 || dim <= 0 || len(q) < ScaleHeaderBytes {
		return nil
	}
	payload := len(q) - ScaleHeaderBytes
	if payload%dim != 0 || payload/dim != rows {
		return nil
	}
	return DequantizeI8(q, payload)
}

// RoundTripMaxErr returns the maximum absolute reconstruction error of v over
// a QuantizeF32/DequantizeI8 round trip. For finite inputs it is bounded by
// max|v|/254 (half a quantization step) plus float32 division rounding; the
// contract bound is max|v|/127+1e-6.
func RoundTripMaxErr(v []float32) float64 {
	r := DequantizeI8(QuantizeF32(v), len(v))
	var maxErr float64
	for i, x := range v {
		if e := math.Abs(float64(x) - float64(r[i])); e > maxErr {
			maxErr = e
		}
	}
	return maxErr
}

// quantScale computes the symmetric quantization scale 127/max|v|. Zero,
// subnormal-overflow and non-finite inputs degrade to a clamped or zero scale
// so the payload stays finite: when 127/maxAbs exceeds the float32 range the
// scale is pinned to MaxFloat32 (values quantize to 0 instead of overflowing).
func quantScale(v []float32) float32 {
	var maxAbs float64
	for _, x := range v {
		if !finite32(x) {
			continue
		}
		if a := math.Abs(float64(x)); a > maxAbs {
			maxAbs = a
		}
	}
	if !(maxAbs > 0) {
		return 0
	}
	s := 127.0 / maxAbs
	if s > math.MaxFloat32 {
		return math.MaxFloat32
	}
	return float32(s)
}

func finite32(x float32) bool {
	return !math.IsNaN(float64(x)) && !math.IsInf(float64(x), 0)
}
