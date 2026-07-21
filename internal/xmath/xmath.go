// Package xmath holds the small numeric helpers shared across the pipeline
// (clamping, smoothstep, linear interpolation). Keeping them in one place avoids
// the near-duplicate copies that otherwise accrete in each image-processing stage.
package xmath

import "cmp"

// Real is any real-number type we interpolate or clamp.
type Real interface{ ~float32 | ~float64 }

// Clamp constrains v to [lo, hi]. lo must not exceed hi.
func Clamp[T cmp.Ordered](v, lo, hi T) T { return min(max(v, lo), hi) }

// Clamp01 constrains v to [0, 1].
func Clamp01[T Real](v T) T { return min(max(v, 0), 1) }

// Lerp linearly interpolates from a to b by t (t is not clamped).
func Lerp[T Real](a, b, t T) T { return a + (b-a)*t }

// Smoothstep returns the Hermite S-curve of x mapped onto [a, b], clamped to
// [0, 1]. When b <= a it degenerates to a step at a.
func Smoothstep(a, b, x float64) float64 {
	if b <= a {
		if x < a {
			return 0
		}
		return 1
	}
	t := Clamp01((x - a) / (b - a))
	return t * t * (3 - 2*t)
}
