package sonylens

import (
	"math"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// Correction scale factors (reverse-engineered, validated against RX100M7 JPEGs).
const (
	distScale = 1.0 / 16384.0   // 2^-14, distortion knots
	caScale   = 1.0 / 2097152.0 // 2^-21, chromatic-aberration knots

	// vignScale converts vignetting knots to a brightness-gain delta. Unlike the
	// distortion/CA scales this one is NOT yet validated against camera output, so
	// vignetting correction is off by default (see WarpConfig.ApplyVignetting).
	vignScale = 1.0 / 16384.0
)

// WarpConfig controls the geometric (and optional photometric) correction.
type WarpConfig struct {
	// CropScale multiplies the radial displacement field. 1.0 reproduces the
	// RX100M7 camera JPEG (validated crop factor ≈ 0.995–1.0; the camera applies
	// the distortion curve with essentially no extra autoscale).
	CropScale float64
	// ApplyCA enables per-channel lateral chromatic-aberration correction.
	ApplyCA bool
	// ApplyVignetting enables radial brightness (vignetting) correction. EXPERIMENTAL:
	// the knot scale is not yet validated against camera JPEGs, so this defaults to
	// false and the default pipeline never sets it. Enable only for experiments.
	ApplyVignetting bool
}

// DefaultWarp returns the validated defaults: distortion + CA, no vignetting.
func DefaultWarp() WarpConfig { return WarpConfig{CropScale: 1.0, ApplyCA: true} }

// factorSpline builds a spline of (1 + knot*scale) over normalized radius [0,1],
// with knots equi-spaced from center (r=0) to corner (r=1). Returns nil for no knots.
func factorSpline(knots []int16, scale float64) *cubicSpline {
	n := len(knots)
	if n == 0 {
		return nil
	}
	xs := make([]float64, n)
	ys := make([]float64, n)
	for i := range n {
		if n > 1 {
			xs[i] = float64(i) / float64(n-1)
		}
		ys[i] = 1.0 + float64(knots[i])*scale
	}
	return newCubicSpline(xs, ys)
}

// Warp applies the Sony embedded distortion (and optional lateral-CA and
// vignetting) correction to src, producing an image geometrically aligned to the
// camera JPEG. The output has the same dimensions as src. Radius is normalized to
// the half-diagonal.
func Warp(src *imaging.Image, cp *CorrParams, cfg WarpConfig) *imaging.Image {
	W, H := src.W, src.H
	cx := float64(W-1) / 2.0
	cy := float64(H-1) / 2.0
	rmax := math.Hypot(float64(W)/2.0, float64(H)/2.0)
	cs := cfg.CropScale
	if cs == 0 {
		cs = 1.0
	}

	dsp := factorSpline(cp.Distortion, distScale)
	var rsp, bsp *cubicSpline
	useCA := cfg.ApplyCA && cp.HasCA && len(cp.CARed) == len(cp.CABlue)
	if useCA {
		rsp = factorSpline(cp.CARed, caScale)
		bsp = factorSpline(cp.CABlue, caScale)
	}
	var vsp *cubicSpline
	useVign := cfg.ApplyVignetting && cp.HasVignetting
	if useVign {
		vsp = factorSpline(cp.Vignetting, vignScale)
	}

	dst := imaging.New(W, H)
	imaging.ParallelRows(H, func(yy0, yy1 int) {
		for y := yy0; y < yy1; y++ {
			ddy := float64(y) - cy
			for x := range W {
				ddx := float64(x) - cx
				r := min(math.Hypot(ddx, ddy)/rmax, 1)
				gd := cs
				if dsp != nil {
					gd *= dsp.eval(r)
				}

				var rr, g, bb float32
				if useCA {
					// Green: distortion only. Red/Blue: distortion × per-channel CA factor.
					_, g, _ = src.SampleBilinear(cx+ddx*gd, cy+ddy*gd)
					fr := gd * rsp.eval(r)
					fb := gd * bsp.eval(r)
					rr, _, _ = src.SampleBilinear(cx+ddx*fr, cy+ddy*fr)
					_, _, bb = src.SampleBilinear(cx+ddx*fb, cy+ddy*fb)
				} else {
					rr, g, bb = src.SampleBilinear(cx+ddx*gd, cy+ddy*gd)
				}

				if useVign {
					vg := float32(vsp.eval(r))
					rr, g, bb = rr*vg, g*vg, bb*vg
				}
				dst.Set(x, y, rr, g, bb)
			}
		}
	})
	return dst
}
