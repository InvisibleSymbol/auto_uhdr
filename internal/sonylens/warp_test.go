package sonylens

import (
	"math"
	"testing"

	"github.com/invis/arw2uhdr/internal/imaging"
)

func TestSplineInterpolatesKnots(t *testing.T) {
	xs := []float64{0, 0.25, 0.5, 0.75, 1.0}
	ys := []float64{1.0, 1.02, 0.99, 0.95, 0.90}
	s := newCubicSpline(xs, ys)
	for i := range xs {
		if got := s.eval(xs[i]); math.Abs(got-ys[i]) > 1e-9 {
			t.Errorf("spline at knot %v = %v, want %v", xs[i], got, ys[i])
		}
	}
	// clamps outside range
	if got := s.eval(-1); got != ys[0] {
		t.Errorf("left clamp = %v, want %v", got, ys[0])
	}
	if got := s.eval(2); got != ys[len(ys)-1] {
		t.Errorf("right clamp = %v, want %v", got, ys[len(ys)-1])
	}
}

// With all-zero distortion knots and no CA, the warp is the identity map.
func TestWarpIdentityOnZeroKnots(t *testing.T) {
	im := imaging.New(64, 48)
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			im.Set(x, y, float32(x)/64, float32(y)/48, 0.5)
		}
	}
	cp := &CorrParams{HasDistortion: true, DistortionN: 11, Distortion: make([]int16, 11)}
	out := sonyWarpForTest(im, cp)
	var maxDiff float32
	for i := range im.Pix {
		d := float32(math.Abs(float64(out.Pix[i] - im.Pix[i])))
		if d > maxDiff {
			maxDiff = d
		}
	}
	if maxDiff > 1e-3 {
		t.Errorf("zero-knot warp not identity: maxDiff=%v", maxDiff)
	}
}

func sonyWarpForTest(im *imaging.Image, cp *CorrParams) *imaging.Image {
	return Warp(im, cp, WarpConfig{CropScale: 1.0, ApplyCA: false})
}
