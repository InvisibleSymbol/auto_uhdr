package gainmap

import (
	"math"
	"testing"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// A uniform 2× HDR boost should reconstruct closely at full weight.
func TestRoundTripUniformBoost(t *testing.T) {
	W, H := 16, 16
	sdr := imaging.New(W, H)
	hdr := imaging.New(W, H)
	for i := 0; i < W*H*3; i++ {
		sdr.Pix[i] = 0.4
		hdr.Pix[i] = 0.8 // +1 stop
	}
	o := DefaultOptions()
	o.MaxBoostStops = 3
	gm, meta, err := Compute(sdr, hdr, o)
	if err != nil {
		t.Fatal(err)
	}
	rec := Reconstruct(sdr, gm, meta, 1.0)
	var maxErr float64
	for i := 0; i < W*H*3; i++ {
		d := math.Abs(float64(rec.Pix[i]) - float64(hdr.Pix[i]))
		if d > maxErr {
			maxErr = d
		}
	}
	if maxErr > 0.02 {
		t.Errorf("round-trip maxErr=%.4f (want <0.02)", maxErr)
	}
	// max content boost should be ~1 stop
	if math.Abs(meta.MaxLog2[0]-1.0) > 0.05 {
		t.Errorf("MaxLog2=%.3f, want ~1.0", meta.MaxLog2[0])
	}
}

// Where HDR==SDR the gain must be ~0 (no spurious boost).
func TestNoBoostWhereEqual(t *testing.T) {
	W, H := 8, 8
	sdr := imaging.New(W, H)
	hdr := imaging.New(W, H)
	for i := 0; i < W*H*3; i++ {
		sdr.Pix[i] = 0.3
		hdr.Pix[i] = 0.3
	}
	// add one boosted pixel so range is non-degenerate
	hdr.Pix[0] = 0.6
	gm, meta, err := Compute(sdr, hdr, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	rec := Reconstruct(sdr, gm, meta, 1.0)
	// pixel 1 (equal) should reconstruct ~0.3
	if d := math.Abs(float64(rec.Pix[3]) - 0.3); d > 0.02 {
		t.Errorf("equal pixel reconstructed %.3f, want ~0.3", rec.Pix[3])
	}
}
