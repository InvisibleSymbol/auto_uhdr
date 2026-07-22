// Package hdrbuild constructs the HDR-linear rendition used to compute the gain map.
//
// The governing rule (set with the user after iterating on real files): RAW content is spliced in
// ONLY where the SDR is clipped/near-clipped — that is where the JPEG has no valid data and any
// recovered detail is a win. Everywhere else the JPEG already has valid data, so the HDR is just
// the SDR times a tone-dependent display boost; no RAW blending there (blending RAW into bright
// but unclipped areas smears partial-clip mottling over clean JPEG content).
//
//   - Recovery gate: RecoverLo..RecoverHi sits at the SDR clip point (~0.90..0.985 luma).
//   - Display boost: dials Threshold (how far down the tonal range it reaches; lower = more of
//     the image) and Strength (stops of lift). It tapers out through the recovery range so it
//     never pushes recovered highlights into the ceiling.
//   - A soft shoulder near the max-boost ceiling compresses instead of hard-clipping.
//
// Strength 0 = pure recovery.
package hdrbuild

import (
	"math"
	"slices"

	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/xmath"
)

// Mode selects the HDR-derivation strategy.
type Mode int

const (
	ModeHighlight Mode = iota // JPEG-anchored highlight recovery + optional display boost (default)
	ModeDevelop               // full RAW linear as HDR (creative)
	ModeRawBoost              // JPEG-gated, RAW-luminance-driven boost (physical; recovers clipped detail for free)
)

// Options controls HDR construction.
type Options struct {
	Mode Mode

	// Recovery gate: RAW recovery ramps in over this SDR-luma range. This sits at the SDR clip
	// point so RAW content only ever replaces clipped/near-clipped JPEG data.
	RecoverLo float64 // default 0.90
	RecoverHi float64 // default 0.97

	// Display boost (the "dials"). The boost is MONOTONIC in SDR luma: it ramps from Threshold and
	// plateaus RampWidth above it — it never falls back toward the highlights. (An earlier "taper"
	// that zeroed the boost before the recovery zone created a gain valley around every bright
	// object, rendering as gray outlines. The ceiling is handled by the soft shoulder instead.)
	//
	// The ramp is FIXED-WIDTH so the threshold dial is responsive: lowering it slides the whole
	// ramp down and brings midtones to FULL strength (a threshold→white ramp would merely stretch,
	// giving midtones only a sliver of boost and making the dial look inert).
	Threshold float64 // SDR luma where the boost ramp begins; lower reaches more of the image (default 0.3)
	RampWidth float64 // luma span over which the boost reaches full strength (default 0.35)
	Strength  float64 // display boost in stops at the plateau (default 1.5; 0 = pure recovery)

	// GuideRadiusFrac optionally smooths the recovery gain with an SDR-guided filter (fraction of
	// image width). Default 0 (off): with the clip-gated recovery it is unnecessary and it can
	// soften genuine recovered detail. Kept as an expert knob.
	GuideRadiusFrac float64
	// GuideEps is the guided-filter regularizer (used only if GuideRadiusFrac > 0).
	GuideEps float64

	// BlurFrac optionally low-passes the boost ramp's luma (fraction of width; default 0).
	BlurFrac float64

	// PreserveChroma (ModeRawBoost only): apply the RAW-driven boost as a single neutral
	// luminance multiplier instead of per channel, so the SDR's exact hue/saturation is kept
	// (the camera JPEG's colour is brightened, not blended toward the flatter RAW colour). Fixes
	// mid-highlight desaturation at the cost of per-channel clipped-channel recovery.
	PreserveChroma bool

	MaxBoostStops float64 // ceiling on total boost, in stops (default 3.0)
}

// DefaultOptions returns balanced defaults.
func DefaultOptions() Options {
	return Options{Mode: ModeHighlight, RecoverLo: 0.90, RecoverHi: 0.97,
		Threshold: 0.3, RampWidth: 0.35, Strength: 1.5, GuideRadiusFrac: 0, GuideEps: 1e-4,
		BlurFrac: 0, MaxBoostStops: 3.0}
}

// anchorGains returns per-channel multipliers k such that raw*k matches sdr in the midtones.
func anchorGains(sdr, raw *imaging.Image) [3]float64 {
	const loY, hiY = 0.12, 0.90
	W, H := sdr.W, sdr.H
	step := 1
	if W*H > 400000 {
		step = int(math.Sqrt(float64(W*H) / 400000.0))
	}
	var ratios [3][]float64
	for y := 0; y < H; y += step {
		for x := 0; x < W; x += step {
			i := (y*W + x) * 3
			ys := 0.2126*float64(sdr.Pix[i]) + 0.7152*float64(sdr.Pix[i+1]) + 0.0722*float64(sdr.Pix[i+2])
			if ys < loY || ys > hiY {
				continue
			}
			for c := range 3 {
				rv := float64(raw.Pix[i+c])
				sv := float64(sdr.Pix[i+c])
				if rv > 1e-4 && sv > 1e-4 {
					ratios[c] = append(ratios[c], sv/rv)
				}
			}
		}
	}
	var k [3]float64
	for c := range 3 {
		if len(ratios[c]) == 0 {
			k[c] = 1
			continue
		}
		slices.Sort(ratios[c])
		k[c] = ratios[c][len(ratios[c])/2]
	}
	return k
}

// softShoulder maps v through a smooth compressive shoulder that asymptotes to vmax (in the log2
// domain), so values near the ceiling compress rather than hard-clip — highlight detail survives.
func softShoulder(v, maxStops float64) float64 {
	if v <= 0 {
		return 0
	}
	lv := math.Log2(v)
	knee := maxStops - 0.7
	if lv <= knee {
		return v
	}
	span := maxStops - knee
	lp := knee + span*math.Tanh((lv-knee)/span)
	return math.Exp2(lp)
}

// guidedFilter runs a standard (box-window) guided filter of p with guide I.
// Output structure follows the guide: where the guide is flat, p collapses to its window mean.
func guidedFilter(I, p []float64, w, h, r int, eps float64) []float64 {
	if r < 1 {
		out := make([]float64, len(p))
		copy(out, p)
		return out
	}
	box := func(v []float64) []float64 { return imaging.BoxBlurF(v, w, h, r, 1) }
	n := len(p)
	Ip := make([]float64, n)
	II := make([]float64, n)
	for i := range n {
		Ip[i] = I[i] * p[i]
		II[i] = I[i] * I[i]
	}
	mI := box(I)
	mp := box(p)
	cIp := box(Ip)
	cII := box(II)
	a := make([]float64, n)
	b := make([]float64, n)
	for i := range n {
		va := (cIp[i] - mI[i]*mp[i]) / (cII[i] - mI[i]*mI[i] + eps)
		a[i] = va
		b[i] = mp[i] - va*mI[i]
	}
	ma := box(a)
	mb := box(b)
	out := make([]float64, n)
	for i := range n {
		out[i] = ma[i]*I[i] + mb[i]
	}
	return out
}

// Build produces the HDR-linear image. sdrLin and rawLin must be the same dimensions
// (rawLin already lens-corrected and registered to the JPEG grid), both scene/display-linear.
func Build(sdrLin, rawLin *imaging.Image, o Options) (*imaging.Image, [3]float64) {
	W, H := sdrLin.W, sdrLin.H
	k := anchorGains(sdrLin, rawLin)
	out := imaging.New(W, H)

	if o.Mode == ModeDevelop {
		for i := range W * H * 3 {
			v := float64(rawLin.Pix[i]) * k[i%3]
			out.Pix[i] = float32(softShoulder(v, o.MaxBoostStops))
		}
		return out, k
	}

	if o.Mode == ModeRawBoost {
		// Gate from the JPEG, magnitude from the RAW. gate = smoothstep(threshold..) on JPEG
		// luma masks the shadows (no dark->bright glow, no raw shadow noise). Per channel,
		// rawGain = log2(raw·k / sdr) and out = sdr·2^(strength·gate·rawGain): bright surfaces
		// lift by how bright the RAW says they are, and in clipped regions the flat-white JPEG
		// makes this reconstruct the raw value — real detail, sharp, for free. Per-channel keeps
		// coloured (unclipped) highlights saturated; clipped-highlight colour is cleaned by the
		// gain map's highlight neutralization.
		const eps = 1e-4
		rw := o.RampWidth
		if rw <= 0 {
			rw = 0.35
		}
		rampHi := math.Min(o.Threshold+rw, 1.0)
		strength := o.Strength
		if strength <= 0 {
			strength = 1.0
		}
		imaging.ParallelRows(H, func(py0, py1 int) {
			for p := py0 * W; p < py1*W; p++ {
				i := p * 3
				lS := 0.2126*float64(sdrLin.Pix[i]) + 0.7152*float64(sdrLin.Pix[i+1]) + 0.0722*float64(sdrLin.Pix[i+2])
				gate := xmath.Smoothstep(o.Threshold, rampHi, lS)
				if o.PreserveChroma {
					// One neutral multiplier from the RAW luminance ratio: keeps the SDR's exact
					// colour, brightens by how much brighter the scene really is.
					lR := 0.2126*float64(rawLin.Pix[i])*k[0] + 0.7152*float64(rawLin.Pix[i+1])*k[1] + 0.0722*float64(rawLin.Pix[i+2])*k[2]
					rg := xmath.Clamp(math.Log2((lR+eps)/(lS+eps)), 0, o.MaxBoostStops)
					boost := math.Exp2(rg * gate * strength)
					for c := range 3 {
						out.Pix[i+c] = float32(softShoulder(float64(sdrLin.Pix[i+c])*boost, o.MaxBoostStops))
					}
					continue
				}
				for c := range 3 {
					s := float64(sdrLin.Pix[i+c])
					rv := float64(rawLin.Pix[i+c]) * k[c]
					rg := xmath.Clamp(math.Log2((rv+eps)/(s+eps)), 0, o.MaxBoostStops)
					out.Pix[i+c] = float32(softShoulder(s*math.Exp2(rg*gate*strength), o.MaxBoostStops))
				}
			}
		})
		return out, k
	}

	// SDR luma drives the recovery gate and boost ramp.
	luma := make([]float64, W*H)
	imaging.ParallelRows(H, func(py0, py1 int) {
		for p := py0 * W; p < py1*W; p++ {
			luma[p] = 0.2126*float64(sdrLin.Pix[p*3]) + 0.7152*float64(sdrLin.Pix[p*3+1]) + 0.0722*float64(sdrLin.Pix[p*3+2])
		}
	})
	lumaBoost := luma
	if o.BlurFrac > 0 {
		lumaBoost = imaging.BoxBlurF(luma, W, H, int(o.BlurFrac*float64(W)+0.5), 3)
	}

	// Optional expert knob: SDR-guided smoothing of a luminance-only recovery gain.
	// Default path (GuideRadiusFrac == 0) is the direct per-channel splice below.
	var gRec []float64
	if o.GuideRadiusFrac > 0 {
		const eps = 1e-6
		gRec = make([]float64, W*H)
		for p := range W * H {
			i := p * 3
			rY := 0.2126*float64(rawLin.Pix[i])*k[0] + 0.7152*float64(rawLin.Pix[i+1])*k[1] + 0.0722*float64(rawLin.Pix[i+2])*k[2]
			g := xmath.Clamp(math.Log2(math.Max(rY, eps)/math.Max(luma[p], eps)), 0, o.MaxBoostStops)
			gRec[p] = g * xmath.Smoothstep(o.RecoverLo, o.RecoverHi, luma[p])
		}
		r := int(o.GuideRadiusFrac*float64(W) + 0.5)
		ge := o.GuideEps
		if ge <= 0 {
			ge = 1e-4
		}
		gRec = guidedFilter(luma, gRec, W, H, r, ge)
	}

	rampW := o.RampWidth
	if rampW <= 0 {
		rampW = 0.35
	}
	rampHi := math.Min(o.Threshold+rampW, 1.0)
	imaging.ParallelRows(H, func(py0, py1 int) {
		for p := py0 * W; p < py1*W; p++ {
			i := p * 3
			// display boost: monotonic fixed-width ramp from the threshold, plateauing at full
			// strength. Never decreases with luminance, so no gain valley (= no gray outline).
			yb := lumaBoost[p]
			liftW := xmath.Smoothstep(o.Threshold, rampHi, yb)
			boost := math.Exp2(o.Strength * liftW)

			if gRec != nil {
				// guided luminance-gain path (expert knob)
				gain := math.Exp2(gRec[p]) * boost
				for c := range 3 {
					v := float64(sdrLin.Pix[i+c]) * gain
					out.Pix[i+c] = float32(softShoulder(v, o.MaxBoostStops))
				}
				continue
			}

			// Default: clip-gated per-channel splice. wRec is ~0 below the SDR clip point, so RAW
			// content only ever replaces clipped/near-clipped JPEG data.
			wRec := xmath.Smoothstep(o.RecoverLo, o.RecoverHi, luma[p])
			for c := range 3 {
				s := float64(sdrLin.Pix[i+c])
				rv := float64(rawLin.Pix[i+c]) * k[c]
				recovered := s*(1-wRec) + math.Max(s, rv)*wRec
				out.Pix[i+c] = float32(softShoulder(recovered*boost, o.MaxBoostStops))
			}
		}
	})
	return out, k
}
