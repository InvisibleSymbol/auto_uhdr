// Package gainmap computes an Ultra HDR gain map from an SDR-linear and an HDR-linear image,
// per the Android Ultra HDR / Adobe hdrgm math, plus reconstruction for validation.
//
// Encode (per pixel, linear inputs on a common scale):
//
//	pixelGainLog2 = log2((HDR + offHdr) / (SDR + offSdr))
//	logRecovery   = (pixelGainLog2 - minLog2) / (maxLog2 - minLog2)
//	encoded       = clamp(logRecovery, 0, 1)^gamma * 255
//
// Decode (what a player does at full headroom, weight=1):
//
//	logRecovery = (encoded/255)^(1/gamma)
//	logBoost    = minLog2*(1-logRecovery) + maxLog2*logRecovery
//	HDR         = (SDR + offSdr) * 2^(logBoost*weight) - offHdr
package gainmap

import (
	"errors"
	"math"
	"sync"

	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/xmath"
)

// sRGB/BT.709 luminance weights.
const (
	lumR = 0.2126
	lumG = 0.7152
	lumB = 0.0722
)

// ErrDimMismatch is returned when the SDR and HDR images differ in size.
var ErrDimMismatch = errors.New("gainmap: sdr and hdr dimensions differ")

// Options controls gain-map computation.
type Options struct {
	MultiChannel  bool    // true: 3-channel (RGB) gain map; false: single-channel (luminance)
	MaxBoostStops float64 // cap on max content boost, in stops (log2). 0 => derive from data
	Gamma         float64 // map gamma (>0), default 1.0
	OffsetSDR     float64 // default 1/64
	OffsetHDR     float64 // default 1/64
	ScaleFactor   int     // downsample factor per dimension (>=1), default 1
	// SmoothFrac low-pass filters the gain map by ~this fraction of the gain-map width. A gain map
	// must be low-frequency (a smooth boost; the SDR carries the detail) or per-pixel gain
	// discontinuities at edges produce halo/ringing artifacts when boosted. Default ~0.0025.
	SmoothFrac float64
}

// DefaultOptions returns spec-recommended defaults. SmoothFrac defaults to 0: gain-map smoothing
// is handled upstream by smoothing the display boost in hdrbuild (which preserves sharp recovery
// detail). A small SmoothFrac remains available as a safety net if needed.
func DefaultOptions() Options {
	return Options{MultiChannel: false, MaxBoostStops: 3.0, Gamma: 1.0,
		OffsetSDR: 1.0 / 64.0, OffsetHDR: 1.0 / 64.0, ScaleFactor: 1, SmoothFrac: 0}
}

// Metadata is the hdrgm gain-map metadata (log2 domain).
type Metadata struct {
	MultiChannel bool
	MinLog2      [3]float64
	MaxLog2      [3]float64
	Gamma        [3]float64
	OffsetSDR    [3]float64
	OffsetHDR    [3]float64
	CapacityMin  float64 // log2(min display boost)
	CapacityMax  float64 // log2(max display boost) = max of MaxLog2
	BaseIsHDR    bool
}

// GainMap holds the quantized gain-map image.
type GainMap struct {
	W, H     int
	Channels int     // 1 or 3
	Pix      []uint8 // len == W*H*Channels
}

func lum(r, g, b float32) float64 {
	return lumR*float64(r) + lumG*float64(g) + lumB*float64(b)
}

// Compute builds the gain map + metadata from SDR-linear and HDR-linear images (same dimensions).
func Compute(sdr, hdr *imaging.Image, o Options) (*GainMap, Metadata, error) {
	if sdr.W != hdr.W || sdr.H != hdr.H {
		return nil, Metadata{}, ErrDimMismatch
	}
	if o.Gamma <= 0 {
		o.Gamma = 1.0
	}
	if o.ScaleFactor < 1 {
		o.ScaleFactor = 1
	}
	W, H := sdr.W, sdr.H
	nCh := 1
	if o.MultiChannel {
		nCh = 3
	}

	// Pass 1: find the max observed gain (per channel) to set maxLog2. Rows split
	// across cores; each band folds into a local max merged once at the end.
	var maxObs [3]float64
	var mu sync.Mutex
	imaging.ParallelRows(H, func(y0, y1 int) {
		var localMax [3]float64
		for p := y0 * W; p < y1*W; p++ {
			i := p * 3
			if o.MultiChannel {
				for c := range 3 {
					g := math.Log2((float64(hdr.Pix[i+c]) + o.OffsetHDR) / (float64(sdr.Pix[i+c]) + o.OffsetSDR))
					localMax[c] = max(localMax[c], g)
				}
			} else {
				g := math.Log2((lum(hdr.Pix[i], hdr.Pix[i+1], hdr.Pix[i+2]) + o.OffsetHDR) /
					(lum(sdr.Pix[i], sdr.Pix[i+1], sdr.Pix[i+2]) + o.OffsetSDR))
				localMax[0] = max(localMax[0], g)
			}
		}
		mu.Lock()
		for c := range 3 {
			maxObs[c] = max(maxObs[c], localMax[c])
		}
		mu.Unlock()
	})

	// minLog2 = 0 (HDR >= SDR in highlight-recovery); maxLog2 = min(observed, cap).
	boostCap := o.MaxBoostStops
	var meta Metadata
	meta.MultiChannel = o.MultiChannel
	for c := range 3 {
		mx := maxObs[c]
		if boostCap > 0 {
			mx = min(mx, boostCap)
		}
		mx = max(mx, 1e-6) // avoid a zero range
		meta.MinLog2[c] = 0
		meta.MaxLog2[c] = mx
		meta.Gamma[c] = o.Gamma
		meta.OffsetSDR[c] = o.OffsetSDR
		meta.OffsetHDR[c] = o.OffsetHDR
	}
	meta.CapacityMin = 0
	meta.CapacityMax = max(meta.MaxLog2[0], meta.MaxLog2[1], meta.MaxLog2[2])

	// Pass 2: encode (with optional integer box downsample). Output rows are disjoint.
	sf := o.ScaleFactor
	gw := (W + sf - 1) / sf
	gh := (H + sf - 1) / sf
	gm := &GainMap{W: gw, H: gh, Channels: nCh, Pix: make([]uint8, gw*gh*nCh)}

	encodeChan := func(s, h float64, c int) float64 {
		g := math.Log2((h + o.OffsetHDR) / (s + o.OffsetSDR))
		lr := (g - meta.MinLog2[c]) / (meta.MaxLog2[c] - meta.MinLog2[c])
		return math.Pow(xmath.Clamp01(lr), o.Gamma) * 255
	}

	imaging.ParallelRows(gh, func(gy0, gy1 int) {
		for gy := gy0; gy < gy1; gy++ {
			for gx := range gw {
				var acc [3]float64
				var cnt float64
				for dy := range sf {
					sy := gy*sf + dy
					if sy >= H {
						break
					}
					for dx := range sf {
						sx := gx*sf + dx
						if sx >= W {
							break
						}
						i := (sy*W + sx) * 3
						if o.MultiChannel {
							for c := range 3 {
								acc[c] += encodeChan(float64(sdr.Pix[i+c]), float64(hdr.Pix[i+c]), c)
							}
						} else {
							acc[0] += encodeChan(
								lum(sdr.Pix[i], sdr.Pix[i+1], sdr.Pix[i+2]),
								lum(hdr.Pix[i], hdr.Pix[i+1], hdr.Pix[i+2]), 0)
						}
						cnt++
					}
				}
				gi := (gy*gw + gx) * nCh
				for c := range nCh {
					gm.Pix[gi+c] = uint8(acc[c]/cnt + 0.5)
				}
			}
		}
	})

	// Low-pass the gain map so it stays a smooth boost (removes edge halos/ringing).
	if o.SmoothFrac > 0 {
		blurGainMap(gm, int(o.SmoothFrac*float64(gw)+0.5))
	}
	return gm, meta, nil
}

// blurGainMap applies a 3-fold separable box blur (≈ Gaussian) to each channel in place,
// reusing the shared imaging.BoxBlurF.
func blurGainMap(gm *GainMap, radius int) {
	if radius < 1 {
		return
	}
	w, h, ch := gm.W, gm.H, gm.Channels
	plane := make([]float64, w*h)
	for c := range ch {
		for i := range w * h {
			plane[i] = float64(gm.Pix[i*ch+c])
		}
		blurred := imaging.BoxBlurF(plane, w, h, radius, 3)
		for i := range w * h {
			gm.Pix[i*ch+c] = uint8(xmath.Clamp(blurred[i]+0.5, 0, 255))
		}
	}
}

// Reconstruct rebuilds an HDR-linear image from an SDR-linear image + gain map + metadata,
// at display weight (0..1). Used to validate the round-trip. The gain map is upsampled
// (nearest) to the SDR resolution.
func Reconstruct(sdr *imaging.Image, gm *GainMap, meta Metadata, weight float64) *imaging.Image {
	W, H := sdr.W, sdr.H
	out := imaging.New(W, H)
	imaging.ParallelRows(H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			gy := y * gm.H / H
			for x := range W {
				gx := x * gm.W / W
				i := (y*W + x) * 3
				gi := (gy*gm.W + gx) * gm.Channels
				for c := range 3 {
					ci, gj := c, gi+c
					if gm.Channels == 1 {
						ci, gj = 0, gi
					}
					enc := float64(gm.Pix[gj]) / 255.0
					lr := math.Pow(enc, 1.0/meta.Gamma[ci])
					logBoost := meta.MinLog2[ci]*(1-lr) + meta.MaxLog2[ci]*lr
					h := (float64(sdr.Pix[i+c])+meta.OffsetSDR[ci])*math.Exp2(logBoost*weight) - meta.OffsetHDR[ci]
					out.Pix[i+c] = float32(max(h, 0))
				}
			}
		}
	})
	return out
}

// ToImage renders the gain map as a viewable image (single-channel replicated to gray).
func (gm *GainMap) ToImage() *imaging.Image {
	im := imaging.New(gm.W, gm.H)
	for p := range gm.W * gm.H {
		if gm.Channels == 3 {
			im.Pix[p*3] = float32(gm.Pix[p*3]) / 255
			im.Pix[p*3+1] = float32(gm.Pix[p*3+1]) / 255
			im.Pix[p*3+2] = float32(gm.Pix[p*3+2]) / 255
		} else {
			v := float32(gm.Pix[p]) / 255
			im.Pix[p*3], im.Pix[p*3+1], im.Pix[p*3+2] = v, v, v
		}
	}
	return im
}
