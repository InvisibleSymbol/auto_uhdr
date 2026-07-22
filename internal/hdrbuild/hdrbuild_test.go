package hdrbuild

import (
	"math"
	"testing"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// TestGoldenCurveV10 locks the tuned default rendition ("v10"). The expected
// outputs were captured from the validated build; a change here means the default
// HDR look moved, which must be a deliberate, reviewed decision — not a silent
// side effect of a refactor. Regenerate intentionally if the curve is retuned.
func TestGoldenCurveV10(t *testing.T) {
	const W, H = 256, 4
	sdr := imaging.New(W, H)
	raw := imaging.New(W, H)
	for y := range H {
		for x := range W {
			v := float32(x) / float32(W-1)
			i := (y*W + x) * 3
			for c := range 3 {
				sdr.Pix[i+c] = v
				raw.Pix[i+c] = v
			}
		}
	}
	hdr, k := Build(sdr, raw, DefaultOptions())
	if k != [3]float64{1, 1, 1} {
		t.Fatalf("anchor gains drifted for raw==sdr: %v", k)
	}
	golden := []struct {
		x    int
		want float32
	}{
		{25, 0.098039}, {76, 0.298039}, {128, 0.951051}, {166, 1.841251},
		{217, 2.406936}, {238, 2.639865}, {253, 2.806243},
	}
	for _, g := range golden {
		got := hdr.Pix[g.x*3]
		if math.Abs(float64(got-g.want)) > 1e-4 {
			t.Errorf("curve moved at x=%d: got %.6f want %.6f", g.x, got, g.want)
		}
	}
}

// fill makes a uniform SDR and a matching RAW (so anchor k≈1), at a given luma.
func pair(luma float32) (*imaging.Image, *imaging.Image) {
	W, H := 32, 32
	sdr := imaging.New(W, H)
	raw := imaging.New(W, H)
	for i := 0; i < W*H*3; i++ {
		sdr.Pix[i] = luma
		raw.Pix[i] = luma
	}
	return sdr, raw
}

func TestRawBoostGatesShadowsLiftsHighlights(t *testing.T) {
	const W, H = 8, 6
	sdr := imaging.New(W, H)
	raw := imaging.New(W, H)
	set := func(im *imaging.Image, x, y int, v float32) {
		i := (y*W + x) * 3
		im.Pix[i], im.Pix[i+1], im.Pix[i+2] = v, v, v
	}
	for y := range H { // midtones: raw==sdr so the anchor k≈1
		for x := range W {
			set(sdr, x, y, 0.4)
			set(raw, x, y, 0.4)
		}
	}
	set(sdr, 0, 0, 0.95) // clipped highlight, RAW has headroom
	set(raw, 0, 0, 2.0)
	set(sdr, 1, 0, 0.20) // shadow with a bright/noisy RAW value
	set(raw, 1, 0, 1.5)

	o := DefaultOptions()
	o.Mode = ModeRawBoost
	o.Strength = 1
	o.Threshold = 0.5
	hdr, k := Build(sdr, raw, o)
	if math.Abs(k[0]-1) > 0.05 {
		t.Fatalf("anchor k=%v, want ~1", k)
	}
	if hdr.Pix[0] < 1.5 { // highlight lifted toward the RAW (~2.0)
		t.Errorf("clipped highlight not lifted toward RAW: %v", hdr.Pix[0])
	}
	if s := hdr.Pix[(0*W+1)*3]; s > 0.25 { // shadow masked despite bright RAW
		t.Errorf("shadow got boosted (noise would leak): %v", s)
	}
	if m := hdr.Pix[(3*W+3)*3]; math.Abs(float64(m)-0.4) > 0.02 { // midtone untouched
		t.Errorf("midtone changed: %v", m)
	}
}

func TestStrengthZeroIsTameInMidtones(t *testing.T) {
	sdr, raw := pair(0.6) // an upper-midtone, below the recovery gate
	o := DefaultOptions()
	o.Strength = 0
	hdr, _ := Build(sdr, raw, o)
	// with strength 0 and raw==sdr, HDR should equal SDR (no boost)
	if d := hdr.Pix[0] - sdr.Pix[0]; d > 1e-3 || d < -1e-3 {
		t.Errorf("strength 0 boosted a midtone: hdr=%.3f sdr=%.3f", hdr.Pix[0], sdr.Pix[0])
	}
}

func TestLowerThresholdBoostsMoreOfTheRange(t *testing.T) {
	o := DefaultOptions()
	o.Strength = 2
	// Probe a low-midtone inside the boost ramp (below RecoverLo): boosted when threshold=0.2
	// but not when threshold=0.9.
	const probe = 0.38
	sdr, raw := pair(probe)
	o.Threshold = 0.9
	hi, _ := Build(sdr, raw, o)
	o.Threshold = 0.2
	lo, _ := Build(sdr, raw, o)
	if !(lo.Pix[0] > hi.Pix[0]+1e-3) {
		t.Errorf("lowering threshold did not increase boost: lo=%.3f hi=%.3f", lo.Pix[0], hi.Pix[0])
	}
	if hi.Pix[0] > probe+1e-3 {
		t.Errorf("high threshold should leave a %.2f midtone unboosted, got %.3f", probe, hi.Pix[0])
	}
}

func TestStrengthIntensifiesBrights(t *testing.T) {
	o := DefaultOptions()
	o.Threshold = 0.3
	sdr, raw := pair(0.85)
	o.Strength = 0.5
	weak, _ := Build(sdr, raw, o)
	o.Strength = 3.0
	strong, _ := Build(sdr, raw, o)
	if !(strong.Pix[0] > weak.Pix[0]+1e-2) {
		t.Errorf("higher strength did not intensify: strong=%.3f weak=%.3f", strong.Pix[0], weak.Pix[0])
	}
}

func TestLogBoostCurveShape(t *testing.T) {
	const maxStops = 3.0
	// Endpoints are fixed for any b.
	for _, b := range []float64{0, 2, 6} {
		if got := logBoostCurve(0, maxStops, b); got != 0 {
			t.Errorf("b=%v: logBoostCurve(0)=%v, want 0", b, got)
		}
		if got := logBoostCurve(maxStops, maxStops, b); math.Abs(got-maxStops) > 1e-9 {
			t.Errorf("b=%v: logBoostCurve(max)=%v, want %v", b, got, maxStops)
		}
	}
	// b=0 is the identity.
	if got := logBoostCurve(1.5, maxStops, 0); got != 1.5 {
		t.Errorf("b=0 not identity: %v", got)
	}
	// b>0 lifts a mid gain, and lifts more as b grows (concave, monotonic in b).
	lin := 1.5
	low := logBoostCurve(1.5, maxStops, 2)
	high := logBoostCurve(1.5, maxStops, 6)
	if !(low > lin && high > low) {
		t.Errorf("expected mid lift increasing with b: lin=%v b2=%v b6=%v", lin, low, high)
	}
}
