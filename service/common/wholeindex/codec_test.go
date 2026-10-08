package wholeindex

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

func TestQuantizeF32LayoutAndKnownValues(t *testing.T) {
	v := []float32{1, -0.5, 0.25}
	q := QuantizeF32(v)
	if len(q) != ScaleHeaderBytes+len(v) {
		t.Fatalf("payload length = %d, want %d", len(q), ScaleHeaderBytes+len(v))
	}
	if got := binary.LittleEndian.Uint32(q[:4]); got != math.Float32bits(127) {
		t.Fatalf("scale header = %08x, want float32(127) = %08x", got, math.Float32bits(127))
	}
	want := []int8{127, -64, 32} // round(x*127): 63.5 rounds away from zero
	for i, w := range want {
		if got := int8(q[ScaleHeaderBytes+i]); got != w {
			t.Fatalf("payload[%d] = %d, want %d", i, got, w)
		}
	}
}

func TestQuantizeF32Empty(t *testing.T) {
	q := QuantizeF32(nil)
	if len(q) != ScaleHeaderBytes {
		t.Fatalf("empty vector must encode to the bare scale header, got %d bytes", len(q))
	}
	if r := DequantizeI8(q, 0); len(r) != 0 {
		t.Fatalf("empty round trip returned %d elements", len(r))
	}
	if e := RoundTripMaxErr(nil); e != 0 {
		t.Fatalf("empty round-trip error = %v, want 0", e)
	}
}

func TestQuantizeF32RoundTrip1024(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	v := make([]float32, 1024)
	maxAbs := float32(0)
	for i := range v {
		v[i] = float32(rng.NormFloat64() * 3)
		if a := float32(math.Abs(float64(v[i]))); a > maxAbs {
			maxAbs = a
		}
	}
	if err := RoundTripMaxErr(v); err > float64(maxAbs)/127+1e-6 {
		t.Fatalf("round-trip max error %v exceeds bound %v", err, float64(maxAbs)/127+1e-6)
	}
	// Elementwise check on the same vector.
	r := DequantizeI8(QuantizeF32(v), len(v))
	for i := range v {
		if d := math.Abs(float64(v[i] - r[i])); d > float64(maxAbs)/127+1e-6 {
			t.Fatalf("v[%d] error %v exceeds bound", i, d)
		}
	}
}

func TestQuantizeF32RoundTripExtremeMagnitudes(t *testing.T) {
	for _, scale := range []float64{1e-30, 1e-12, 1, 1e12, 1e30} {
		v := []float32{float32(scale), float32(-0.37 * scale), float32(0.11 * scale), 0}
		maxAbs := scale
		if e := RoundTripMaxErr(v); e > maxAbs/127+1e-6 {
			t.Fatalf("scale %g: round-trip max error %v exceeds bound %v", scale, e, maxAbs/127+1e-6)
		}
	}
}

func TestQuantizeF32AllZeroNoNaN(t *testing.T) {
	for _, v := range [][]float32{make([]float32, 16), {0, -0, 0}, {math.SmallestNonzeroFloat32}} {
		r := DequantizeI8(QuantizeF32(v), len(v))
		for i, x := range r {
			if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
				t.Fatalf("zero-ish input reconstructed NaN/Inf at %d: %v", i, x)
			}
		}
	}
}

func TestQuantizeF32NonFiniteInputStaysFinite(t *testing.T) {
	v := []float32{1, float32(math.NaN()), float32(math.Inf(1)), -0.5}
	r := DequantizeI8(QuantizeF32(v), len(v))
	if r == nil {
		t.Fatal("non-finite input must still decode")
	}
	for i, x := range r {
		if math.IsNaN(float64(x)) || math.IsInf(float64(x), 0) {
			t.Fatalf("non-finite input produced NaN/Inf at %d: %v", i, x)
		}
	}
	// Inf input reconstructs to 0, so the reconstruction error is honestly +Inf;
	// NaN inputs are skipped by max-err comparisons and stay finite.
	if e := RoundTripMaxErr(v); !math.IsInf(e, 1) {
		t.Fatalf("RoundTripMaxErr with Inf input = %v, want +Inf", e)
	}
	if e := RoundTripMaxErr([]float32{1, float32(math.NaN()), -0.5}); math.IsNaN(e) || math.IsInf(e, 0) {
		t.Fatalf("RoundTripMaxErr with NaN input = %v, want finite", e)
	}
}

func TestDequantizeI8Malformed(t *testing.T) {
	q := QuantizeF32([]float32{1, 2, 3})
	if r := DequantizeI8(q, 2); r != nil {
		t.Fatal("dim mismatch must return nil")
	}
	if r := DequantizeI8(q[:4], 1); r != nil {
		t.Fatal("truncated payload must return nil")
	}
	if r := DequantizeI8(nil, -1); r != nil {
		t.Fatal("negative dim must return nil")
	}
}

func TestQuantizeMultiWholeBlock(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	const rows, dim = 6, 17
	mat := make([]float32, 0, rows*dim)
	maxAbs := float64(0)
	for i := 0; i < rows*dim; i++ {
		x := float32(rng.NormFloat64() * 2)
		if a := math.Abs(float64(x)); a > maxAbs {
			maxAbs = a
		}
		mat = append(mat, x)
	}
	q := QuantizeMulti(mat, dim)
	if q == nil {
		t.Fatal("valid matrix must encode")
	}
	// Whole-block quantization shares one scale: identical to flat QuantizeF32.
	if string(q) != string(QuantizeF32(mat)) {
		t.Fatal("QuantizeMulti must quantize the whole block with a single scale")
	}
	back := DequantizeMulti(q, rows, dim)
	if back == nil {
		t.Fatal("valid block must decode")
	}
	for i := range mat {
		if d := math.Abs(float64(mat[i] - back[i])); d > maxAbs/127+1e-6 {
			t.Fatalf("mat[%d] error %v exceeds bound %v", i, d, maxAbs/127+1e-6)
		}
	}
}

func TestQuantizeMultiMalformed(t *testing.T) {
	if q := QuantizeMulti([]float32{1, 2, 3}, 0); q != nil {
		t.Fatal("dim 0 must return nil")
	}
	if q := QuantizeMulti([]float32{1, 2, 3}, 2); q != nil {
		t.Fatal("non-multiple length must return nil")
	}
	q := QuantizeMulti([]float32{1, 2, 3, 4}, 2)
	if r := DequantizeMulti(q, 1, 2); r != nil {
		t.Fatal("row count mismatch must return nil")
	}
	if r := DequantizeMulti(q, 2, 3); r != nil {
		t.Fatal("dim mismatch must return nil")
	}
	if r := DequantizeMulti(nil, 0, 1); r != nil {
		t.Fatal("empty payload with rows must return nil")
	}
}
