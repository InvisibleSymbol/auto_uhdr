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

// A flat image with positive vignetting knots (increasing toward the corner)
// must be brightened more at the corners than at the center.
func TestVignettingBrightensCorners(t *testing.T) {
	W, H := 80, 60
	im := imaging.New(W, H)
	for i := range im.Pix {
		im.Pix[i] = 0.5
	}
	// zero at center, rising to the corner
	vign := make([]int16, 11)
	for i := range vign {
		vign[i] = int16(i * 400)
	}
	cp := &CorrParams{
		HasDistortion: true, DistortionN: 11, Distortion: make([]int16, 11),
		HasVignetting: true, VignettingN: len(vign), Vignetting: vign,
	}
	out := Warp(im, cp, WarpConfig{CropScale: 1, ApplyVignetting: true})
	center := out.Pix[(H/2*W+W/2)*3]
	corner := out.Pix[0]
	if !(corner > center+1e-3) {
		t.Errorf("corner (%v) not brighter than center (%v)", corner, center)
	}
	if math.Abs(float64(center)-0.5) > 5e-3 {
		t.Errorf("center gain should be ~1 (0.5), got %v", center)
	}
}

// Vignetting disabled must leave brightness untouched even when knots exist.
func TestVignettingDisabledIsPhotometricIdentity(t *testing.T) {
	im := imaging.New(40, 30)
	for i := range im.Pix {
		im.Pix[i] = 0.5
	}
	cp := &CorrParams{
		HasDistortion: true, DistortionN: 11, Distortion: make([]int16, 11),
		HasVignetting: true, VignettingN: 11, Vignetting: []int16{0, 100, 200, 400, 800, 1600, 2400, 3200, 4000, 5000, 6000},
	}
	out := Warp(im, cp, WarpConfig{CropScale: 1, ApplyVignetting: false})
	for i := range out.Pix {
		if math.Abs(float64(out.Pix[i])-0.5) > 1e-3 {
			t.Fatalf("disabled vignetting changed pixel %d: %v", i, out.Pix[i])
		}
	}
}

func sonyWarpForTest(im *imaging.Image, cp *CorrParams) *imaging.Image {
	return Warp(im, cp, WarpConfig{CropScale: 1.0, ApplyCA: false})
}
