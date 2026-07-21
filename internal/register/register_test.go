package register

import (
	"math"
	"testing"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// synthetic textured image
func checker(W, H int) *imaging.Image {
	im := imaging.New(W, H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			v := float32(0.2)
			if (x/17+y/17)%2 == 0 {
				v = 0.8
			}
			// add fine detail so gradients are rich
			if (x/3+y/5)%2 == 0 {
				v += 0.1
			}
			im.Set(x, y, v, v, v)
		}
	}
	return im
}

func TestEstimateRecoversScaleShift(t *testing.T) {
	W, H := 1200, 800
	ref := checker(W, H)
	// moving = ref sampled with a known affine: mov(x,y) = ref(1.006*x - 3, 1.008*y + 2)
	trueSx, trueSy := 1.006, 1.008
	trueOx, trueOy := -3.0, 2.0
	mov := imaging.New(W, H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			r, g, b := ref.SampleBilinear(trueSx*float64(x)+trueOx, trueSy*float64(y)+trueOy)
			mov.Set(x, y, r, g, b)
		}
	}
	// Estimate should recover an affine that maps ref-coords to the same mov sample.
	a := Estimate(ref, mov, Options{})
	// Apply(mov) should reproduce ref closely.
	rec := a.Apply(mov, W, H)
	var se, n float64
	for y := 100; y < H-100; y += 7 {
		for x := 100; x < W-100; x += 7 {
			i := (y*W + x) * 3
			d := float64(rec.Pix[i] - ref.Pix[i])
			se += d * d
			n++
		}
	}
	rmse := math.Sqrt(se / n)
	if rmse > 0.06 {
		t.Errorf("registration RMSE=%.4f too high (Sx=%.4f Sy=%.4f Ox=%.4f Oy=%.4f)",
			rmse, a.Sx, a.Sy, a.OxFrac*float64(W), a.OyFrac*float64(H))
	}
}
