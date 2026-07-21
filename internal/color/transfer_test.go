package color

import (
	"math"
	"testing"
)

func TestSRGBAnchors(t *testing.T) {
	if SRGBEncode(0) != 0 || SRGBEncode(1) != 1 {
		t.Error("encode endpoints must map 0->0, 1->1")
	}
	if SRGBDecode(0) != 0 {
		t.Error("decode(0) must be 0")
	}
	// linear segment slope near black
	if got := SRGBEncode(0.001); math.Abs(float64(got)-12.92*0.001) > 1e-6 {
		t.Errorf("low-end slope wrong: %v", got)
	}
	// mid-gray sanity: 0.5 linear encodes to ~0.735
	if got := SRGBEncode(0.5); math.Abs(float64(got)-0.735) > 2e-3 {
		t.Errorf("encode(0.5)=%v want ~0.735", got)
	}
}

func TestSRGBRoundTrip(t *testing.T) {
	var maxErr float64
	for i := range 1001 {
		v := float32(i) / 1000
		back := SRGBDecode(SRGBEncode(v))
		if d := math.Abs(float64(back - v)); d > maxErr {
			maxErr = d
		}
	}
	if maxErr > 1e-4 {
		t.Errorf("sRGB round-trip maxErr=%v", maxErr)
	}
}

func BenchmarkSRGBDecode(b *testing.B) {
	var acc float32
	for i := range b.N {
		acc += SRGBDecode(float32(i%1000) / 1000)
	}
	_ = acc
}
