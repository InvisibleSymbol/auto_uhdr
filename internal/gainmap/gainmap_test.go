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

// Highlight neutralization must remove per-channel colour from the gain inside
// clipped highlights (a blown white region stays neutral) while preserving it for
// unclipped colour (a dark, coloured region keeps its per-channel boost).
func TestHighlightNeutralization(t *testing.T) {
	// px0: reference that maxes every channel (sets a generous maxLog2).
	// px1: clipped near-white with divergent per-channel HDR -> should neutralize.
	// px2: dark neutral SDR with divergent HDR -> below the gate -> should stay coloured.
	sdr := imaging.New(3, 1)
	hdr := imaging.New(3, 1)
	set := func(im *imaging.Image, p int, r, g, b float32) {
		im.Pix[p*3], im.Pix[p*3+1], im.Pix[p*3+2] = r, g, b
	}
	set(sdr, 0, 0.5, 0.5, 0.5)
	set(hdr, 0, 4.0, 4.0, 4.0) // +3 stops, all channels
	set(sdr, 1, 0.98, 0.98, 0.98)
	set(hdr, 1, 0.98*3.5, 0.98*2.5, 0.98*1.3) // strongly coloured recovery
	set(sdr, 2, 0.30, 0.30, 0.30)
	set(hdr, 2, 0.90, 0.60, 0.45) // coloured, but dark (unclipped)

	o := DefaultOptions()
	o.MultiChannel = true
	o.MaxBoostStops = 3

	withNeutral, meta, err := Compute(sdr, hdr, o)
	if err != nil {
		t.Fatal(err)
	}
	rec := Reconstruct(sdr, withNeutral, meta, 1.0)
	ratio := func(p int) float64 { return float64(rec.Pix[p*3]) / float64(rec.Pix[p*3+2]) } // R/B
	if r := ratio(1); r > 1.08 {
		t.Errorf("clipped highlight not neutralized: R/B=%.3f (want ~1)", r)
	}
	if r := ratio(2); r < 1.4 {
		t.Errorf("dark coloured region lost its colour: R/B=%.3f (want >1.4)", r)
	}

	// With neutralization off, the clipped highlight stays coloured.
	o.NeutralizeHighlights = false
	off, meta2, err := Compute(sdr, hdr, o)
	if err != nil {
		t.Fatal(err)
	}
	rec2 := Reconstruct(sdr, off, meta2, 1.0)
	if r := float64(rec2.Pix[3]) / float64(rec2.Pix[5]); r < 1.8 {
		t.Errorf("without neutralization the highlight should stay coloured, R/B=%.3f", r)
	}
}

// A per-channel (RGB) gain map must reconstruct distinct per-channel boosts.
func TestMultiChannelRoundTrip(t *testing.T) {
	const W, H = 12, 12
	sdr := imaging.New(W, H)
	hdr := imaging.New(W, H)
	// SDR mid-gray; HDR pushes R +1 stop, G +0.5, B +2 stops.
	boosts := [3]float64{2.0, math.Sqrt2, 4.0}
	for p := range W * H {
		i := p * 3
		for c := range 3 {
			sdr.Pix[i+c] = 0.25
			hdr.Pix[i+c] = float32(0.25 * boosts[c])
		}
	}
	o := DefaultOptions()
	o.MultiChannel = true
	o.MaxBoostStops = 3 // caps blue's +2 comfortably
	gm, meta, err := Compute(sdr, hdr, o)
	if err != nil {
		t.Fatal(err)
	}
	if gm.Channels != 3 || !meta.MultiChannel {
		t.Fatalf("expected 3-channel map, got %d", gm.Channels)
	}
	rec := Reconstruct(sdr, gm, meta, 1.0)
	for c := range 3 {
		want := float32(0.25 * boosts[c])
		if d := math.Abs(float64(rec.Pix[c] - want)); d > 0.02 {
			t.Errorf("channel %d reconstructed %.3f, want %.3f", c, rec.Pix[c], want)
		}
	}
}
