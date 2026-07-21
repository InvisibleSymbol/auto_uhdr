// Package color provides transfer-function and (later) primaries helpers.
package color

import "math"

// SRGBEncode applies the sRGB OETF (linear -> non-linear display), input clamped to [0,1].
func SRGBEncode(v float32) float32 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 1
	}
	x := float64(v)
	if x <= 0.0031308 {
		return float32(12.92 * x)
	}
	return float32(1.055*math.Pow(x, 1.0/2.4) - 0.055)
}

// SRGBDecode applies the sRGB EOTF (non-linear display -> linear).
func SRGBDecode(v float32) float32 {
	x := float64(v)
	if x <= 0.04045 {
		return float32(x / 12.92)
	}
	return float32(math.Pow((x+0.055)/1.055, 2.4))
}
