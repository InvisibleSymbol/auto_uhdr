package hdrbuild

import (
	"testing"

	"github.com/invis/arw2uhdr/internal/imaging"
)

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
