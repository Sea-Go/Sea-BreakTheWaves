package artifact

import (
	"math"
	"reflect"
	"testing"
)

func TestImpactRoundTrip(t *testing.T) {
	terms := []Term{
		{TermID: 0, Weight: 0},
		{TermID: 1, Weight: 1},
		{TermID: 127, Weight: 128},
		{TermID: 4294967295, Weight: 255},
		{TermID: 42, Weight: 77},
	}
	b := EncodeImpact(terms)
	if len(b) != impactRecordBytes*len(terms) {
		t.Fatalf("encoded length = %d, want %d", len(b), impactRecordBytes*len(terms))
	}
	back, err := DecodeImpact(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(back, terms) {
		t.Fatalf("round trip mismatch: %+v vs %+v", back, terms)
	}

	empty := EncodeImpact(nil)
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty encode must be non-nil and empty, got %#v", empty)
	}
	back, err = DecodeImpact(empty)
	if err != nil || back == nil || len(back) != 0 {
		t.Fatalf("empty decode = (%#v, %v), want empty non-nil", back, err)
	}
}

func TestImpactWireLayout(t *testing.T) {
	b := EncodeImpact([]Term{{TermID: 0x04030201, Weight: 0xAB}})
	want := []byte{0x01, 0x02, 0x03, 0x04, 0xAB} // little-endian term id, then weight
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("wire layout % x, want % x", b, want)
	}
}

func TestDecodeImpactMalformed(t *testing.T) {
	for _, n := range []int{1, 3, 7, 9} {
		if _, err := DecodeImpact(make([]byte, n)); err == nil {
			t.Fatalf("length %d must fail", n)
		}
	}
}

func TestImpactWeight(t *testing.T) {
	cases := []struct {
		w, maxW float32
		want    uint8
	}{
		{0, 1, 0},
		{1, 1, 255},
		{0.5, 1, 128}, // round(127.5) away from zero
		{0.25, 1, 64},
		{-1, 1, 0},
		{1.5, 1, 255}, // clamped
		{0.5, 2, 64},  // round(63.75)
		{1, 0, 0},     // degenerate max
		{-1, -1, 0},   // negative max
		{float32(math.NaN()), 1, 0},
		{1, float32(math.NaN()), 0},
		{float32(math.Inf(1)), float32(math.Inf(1)), 0},
	}
	for _, c := range cases {
		if got := ImpactWeight(c.w, c.maxW); got != c.want {
			t.Errorf("ImpactWeight(%v, %v) = %d, want %d", c.w, c.maxW, got, c.want)
		}
	}
}
