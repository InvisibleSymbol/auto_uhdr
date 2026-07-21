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
	"math"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// sRGB/BT.709 luminance weights.
const (
	lumR = 0.2126
	lumG = 0.7152
	lumB = 0.0722
)

// Options controls gain-map computation.
type Options struct {
	MultiChannel bool    // true: 3-channel (RGB) gain map; false: single-channel (luminance)
	MaxBoostStops float64 // cap on max content boost, in stops (log2). 0 => derive from data
	Gamma        float64 // map gamma (>0), default 1.0
	OffsetSDR    float64 // default 1/64
	OffsetHDR    float64 // default 1/64
	ScaleFactor  int     // downsample factor per dimension (>=1), default 1
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

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// Compute builds the gain map + metadata from SDR-linear and HDR-linear images (same dimensions).
func Compute(sdr, hdr *imaging.Image, o Options) (*GainMap, Metadata, error) {
	if sdr.W != hdr.W || sdr.H != hdr.H {
		return nil, Metadata{}, errDim
	}
	if o.Gamma <= 0 {
		o.Gamma = 1.0
	}
	if o.ScaleFactor < 1 {
		o.ScaleFactor = 1
	}
	W, H := sdr.W, sdr.H

	// Pass 1: find max observed gain (per channel) to set maxLog2.
	var maxObs [3]float64
	sample := func(fn func(sg, hg float64, c int)) {
		for y := 0; y < H; y++ {
			for x := 0; x < W; x++ {
				i := (y*W + x) * 3
				if o.MultiChannel {
					for c := 0; c < 3; c++ {
						s := float64(sdr.Pix[i+c])
						h := float64(hdr.Pix[i+c])
						fn(s, h, c)
					}
				} else {
					s := lum(sdr.Pix[i], sdr.Pix[i+1], sdr.Pix[i+2])
					h := lum(hdr.Pix[i], hdr.Pix[i+1], hdr.Pix[i+2])
					fn(s, h, 0)
				}
			}
		}
	}
	sample(func(s, h float64, c int) {
		g := math.Log2((h + o.OffsetHDR) / (s + o.OffsetSDR))
		if g > maxObs[c] {
			maxObs[c] = g
		}
	})

	// minLog2 = 0 (HDR >= SDR in highlight-recovery); maxLog2 = min(observed, cap).
	cap := o.MaxBoostStops
	var meta Metadata
	meta.MultiChannel = o.MultiChannel
	nCh := 1
	if o.MultiChannel {
		nCh = 3
	}
	for c := 0; c < 3; c++ {
		mx := maxObs[c]
		if cap > 0 && mx > cap {
			mx = cap
		}
		if mx < 1e-6 {
			mx = 1e-6 // avoid zero range
		}
		meta.MinLog2[c] = 0
		meta.MaxLog2[c] = mx
		meta.Gamma[c] = o.Gamma
		meta.OffsetSDR[c] = o.OffsetSDR
		meta.OffsetHDR[c] = o.OffsetHDR
	}
	meta.CapacityMin = 0
	meta.CapacityMax = math.Max(meta.MaxLog2[0], math.Max(meta.MaxLog2[1], meta.MaxLog2[2]))
	meta.BaseIsHDR = false

	// Pass 2: encode (with optional integer box downsample).
	sf := o.ScaleFactor
	gw := (W + sf - 1) / sf
	gh := (H + sf - 1) / sf
	gm := &GainMap{W: gw, H: gh, Channels: nCh, Pix: make([]uint8, gw*gh*nCh)}

	encodeChan := func(s, h float64, c int) uint8 {
		g := math.Log2((h + o.OffsetHDR) / (s + o.OffsetSDR))
		rng := meta.MaxLog2[c] - meta.MinLog2[c]
		lr := (g - meta.MinLog2[c]) / rng
		v := math.Pow(clamp01(lr), o.Gamma)
		return uint8(v*255 + 0.5)
	}

	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			// average the sf×sf source block
			var acc [3]float64
			var cnt float64
			for dy := 0; dy < sf; dy++ {
				sy := gy*sf + dy
				if sy >= H {
					break
				}
				for dx := 0; dx < sf; dx++ {
					sx := gx*sf + dx
					if sx >= W {
						break
					}
					i := (sy*W + sx) * 3
					if o.MultiChannel {
						for c := 0; c < 3; c++ {
							acc[c] += float64(encodeChan(float64(sdr.Pix[i+c]), float64(hdr.Pix[i+c]), c))
						}
					} else {
						s := lum(sdr.Pix[i], sdr.Pix[i+1], sdr.Pix[i+2])
						hh := lum(hdr.Pix[i], hdr.Pix[i+1], hdr.Pix[i+2])
						acc[0] += float64(encodeChan(s, hh, 0))
					}
					cnt++
				}
			}
			gi := (gy*gw + gx) * nCh
			for c := 0; c < nCh; c++ {
				gm.Pix[gi+c] = uint8(acc[c]/cnt + 0.5)
			}
		}
	}

	// Low-pass the gain map so it is a smooth boost (removes edge halos/ringing).
	if o.SmoothFrac > 0 {
		radius := int(o.SmoothFrac*float64(gw) + 0.5)
		blurGainMap(gm, radius)
	}
	return gm, meta, nil
}

// blurGainMap applies a 3-pass separable box blur (≈ Gaussian) to each channel in place.
func blurGainMap(gm *GainMap, radius int) {
	if radius < 1 {
		return
	}
	w, h, ch := gm.W, gm.H, gm.Channels
	buf := make([]float64, w*h)
	for c := 0; c < ch; c++ {
		for i := 0; i < w*h; i++ {
			buf[i] = float64(gm.Pix[i*ch+c])
		}
		for it := 0; it < 3; it++ {
			buf = boxBlurH(buf, w, h, radius)
			buf = boxBlurV(buf, w, h, radius)
		}
		for i := 0; i < w*h; i++ {
			v := buf[i] + 0.5
			if v < 0 {
				v = 0
			} else if v > 255 {
				v = 255
			}
			gm.Pix[i*ch+c] = uint8(v)
		}
	}
}

func clampi(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func boxBlurH(src []float64, w, h, r int) []float64 {
	dst := make([]float64, w*h)
	win := float64(2*r + 1)
	for y := 0; y < h; y++ {
		row := y * w
		var sum float64
		for x := -r; x <= r; x++ {
			sum += src[row+clampi(x, 0, w-1)]
		}
		for x := 0; x < w; x++ {
			dst[row+x] = sum / win
			sum += src[row+clampi(x+r+1, 0, w-1)] - src[row+clampi(x-r, 0, w-1)]
		}
	}
	return dst
}

func boxBlurV(src []float64, w, h, r int) []float64 {
	dst := make([]float64, w*h)
	win := float64(2*r + 1)
	for x := 0; x < w; x++ {
		var sum float64
		for y := -r; y <= r; y++ {
			sum += src[clampi(y, 0, h-1)*w+x]
		}
		for y := 0; y < h; y++ {
			dst[y*w+x] = sum / win
			sum += src[clampi(y+r+1, 0, h-1)*w+x] - src[clampi(y-r, 0, h-1)*w+x]
		}
	}
	return dst
}

// Reconstruct rebuilds an HDR-linear image from an SDR-linear image + gain map + metadata,
// at display weight (0..1). Used to validate the round-trip. The gain map is upsampled
// (nearest) to the SDR resolution.
func Reconstruct(sdr *imaging.Image, gm *GainMap, meta Metadata, weight float64) *imaging.Image {
	W, H := sdr.W, sdr.H
	out := imaging.New(W, H)
	for y := 0; y < H; y++ {
		gy := y * gm.H / H
		for x := 0; x < W; x++ {
			gx := x * gm.W / W
			i := (y*W + x) * 3
			gi := (gy*gm.W + gx) * gm.Channels
			for c := 0; c < 3; c++ {
				var enc float64
				if gm.Channels == 3 {
					enc = float64(gm.Pix[gi+c]) / 255.0
				} else {
					enc = float64(gm.Pix[gi]) / 255.0
				}
				ci := c
				if gm.Channels == 1 {
					ci = 0
				}
				lr := math.Pow(enc, 1.0/meta.Gamma[ci])
				logBoost := meta.MinLog2[ci]*(1-lr) + meta.MaxLog2[ci]*lr
				s := float64(sdr.Pix[i+c])
				h := (s+meta.OffsetSDR[ci])*math.Exp2(logBoost*weight) - meta.OffsetHDR[ci]
				if h < 0 {
					h = 0
				}
				out.Pix[i+c] = float32(h)
			}
		}
	}
	return out
}

// ToImage renders the gain map as a viewable image (single-channel replicated to gray).
func (gm *GainMap) ToImage() *imaging.Image {
	im := imaging.New(gm.W, gm.H)
	for p := 0; p < gm.W*gm.H; p++ {
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

var errDim = dimErr("sdr and hdr dimensions differ")

type dimErr string

func (e dimErr) Error() string { return string(e) }
