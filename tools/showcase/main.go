// Command showcase renders a 4-row grid over every ARW+JPEG pair in a directory
// and writes it as a single real Ultra HDR (JPEG_R) file:
//
//	row 1  SDR (the camera JPEG)
//	row 2  the RGB gain map (what the tool adds), gamma-lifted for visibility
//	row 3  the Ultra HDR result — this row carries a real gain map, so on an HDR
//	       display it actually renders brighter; on SDR it matches row 1
//	row 4  an SDR preview of the HDR rendition via a compressed, HLG-like curve:
//	       the SDR range keeps the sRGB gamma shape (squished into [0,white], so
//	       un-boosted content stays faithful to row 1, just a touch darker) while
//	       the recovered highlights get an S-shaped shoulder into [white,1] that joins
//	       the SDR curve tangentially (no kink) and eases to a flat max-out at the
//	       peak, so they read as a separated band — the recovered detail viewers
//	       without an HDR screen would otherwise miss
//
// The output is one Ultra HDR JPEG whose gain map is zero everywhere except the
// row-3 tiles. Processing is at reduced resolution (half-size decode) for speed.
//
// It accepts the full set of convert/batch flags (e.g. --hdr-mode, --strength,
// --threshold, --chroma, --boost-curve, --raw-lift, --lens) so rows 3 and 4 reflect
// the exact rendition the CLI would produce, plus its own row-4/layout flags below.
//
// usage: showcase [convert flags] [-scale s] [-peak p] [-white w] [-agg a] <dir> <out.jpg>
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/invis/arw2uhdr"
	"github.com/invis/arw2uhdr/internal/cli"
	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// Layout dimensions, scaled from -scale in main. rowLabels drives both the drawn
// labels and the gutter width, so the gutter always fits the longest label.
var (
	boxW, boxH = 360, 260
	gap        = 6
	gutter     = 190 // left margin for row labels; recomputed from the labels
	textScale  = 3   // row-label glyph scale
	rowLabels  = []string{"SDR", "GAIN MAP", "ULTRA HDR", "SDR PREVIEW"}
)

const labelPad = 10 // margin left of the label and between label and first tile

func main() {
	// Accept the full convert/batch flag set so the rows reflect the same rendition the CLI produces.
	resolveConvert := cli.RegisterConvertFlags(flag.CommandLine)
	peakPct := flag.Float64("peak", 0.995, "row-4 highlight-peak percentile (hot-pixel-robust)")
	white := flag.Float64("white", 0.75, "row-4 SDR white point: display level SDR-white squishes to, reserving [white,1] for HDR")
	agg := flag.Float64("agg", 4, "row-4 HDR-shoulder S steepness (higher = quicker mid-transition; smooth join + smooth max-out)")
	quality := flag.Int("q", 92, "base JPEG quality (lower shrinks the file)")
	scale := flag.Float64("scale", 1, "output resolution multiplier (2 = double-size tiles, crisper)")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: showcase [convert flags] [-scale s] [-peak p] [-white w] [-agg a] <dir> <out.jpg>")
		os.Exit(2)
	}
	opts, err := resolveConvert()
	must(err)
	applyScale(*scale)
	tone := toneParams{peakPct: *peakPct, white: *white, agg: *agg}
	dir, out := flag.Arg(0), flag.Arg(1)

	pairs := discover(dir)
	if len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "no ARW+JPEG pairs found in", dir)
		os.Exit(1)
	}
	n := len(pairs)

	gridW := gutter + n*boxW + (n+1)*gap
	gridH := 4*boxH + 5*gap
	sdr := imaging.New(gridW, gridH)    // display SDR base grid
	hdrLin := imaging.New(gridW, gridH) // linear HDR grid (row 3 lifted)

	rowY := func(r int) int { return gap + r*(boxH+gap) }
	colX := func(c int) int { return gutter + gap + c*(boxW+gap) }

	all := make([]tiles, n)
	for col, pr := range pairs {
		fmt.Printf("[%d/%d] %s\n", col+1, n, filepath.Base(pr.arw))
		t := process(pr, tone, opts)
		all[col] = t
		x := colX(col)
		// SDR base: row1 SDR, row2 gain map, row3 SDR (same base), row4 exposed.
		blit(sdr, t.sdrDisp, x, rowY(0))
		blit(sdr, t.gainViz, x, rowY(1))
		blit(sdr, t.sdrDisp, x, rowY(2))
		blit(sdr, t.exposed, x, rowY(3))
	}

	drawLabels(sdr)

	// HDR grid = the linearized SDR everywhere (gain 0), with the real HDR spliced
	// into the row-3 tiles so only that row lifts on an HDR display.
	for i := range hdrLin.Pix {
		hdrLin.Pix[i] = color.SRGBDecode(sdr.Pix[i])
	}
	for col := range pairs {
		blit(hdrLin, all[col].hdrLin, colX(col), rowY(2))
	}

	// Encode the SDR grid as the base JPEG, compute the gain map from the grids.
	base := encodeJPEG(sdr, *quality)
	sdrGridLin := imaging.New(gridW, gridH)
	for i := range sdrGridLin.Pix {
		sdrGridLin.Pix[i] = color.SRGBDecode(sdr.Pix[i])
	}
	gmo := gainmap.DefaultOptions()
	gmo.MultiChannel = true
	gmo.ScaleFactor = 1
	gmo.NeutralizeHighlights = false
	gm, meta, err := gainmap.Compute(sdrGridLin, hdrLin, gmo)
	must(err)
	uhdr, err := ultrahdr.Encode(base, gm, meta, ultrahdr.DefaultOptions())
	must(err)
	must(os.WriteFile(out, uhdr, 0o644))
	if err := ultrahdr.Verify(uhdr); err != nil {
		fmt.Fprintln(os.Stderr, "warning: verify:", err)
	}
	fmt.Printf("wrote %s (%d KB, Ultra HDR — row 3 renders as HDR)\n", out, len(uhdr)/1024)
}

type tiles struct {
	sdrDisp, gainViz, exposed, hdrLin *imaging.Image
}

type toneParams struct{ peakPct, white, agg float64 }

func process(pr pair, tone toneParams, opts arw2uhdr.Options) tiles {
	ctx := context.Background()
	conv := arw2uhdr.New(opts) // configured stages: corrector (lens), renderer (all HDR dials)

	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2
	ro.HalfSize = true // reduced-resolution decode for showcase speed
	ro.Demosaic = librawDemosaic(opts.Demosaic)
	img, _, err := raw.Decode(pr.arw, ro)
	must(err)
	cp, err := sonylens.ReadARW(pr.arw)
	must(err)
	warped, err := conv.Corrector.Correct(ctx, img, pr.arw) // honours --lens / --vignetting
	must(err)

	jpg, err := imaging.LoadImage(pr.jpg)
	must(err)
	// Work at ~2x the tile width (min 1400) so downscaling into the box stays crisp;
	// the half-size RAW decode (~2700px) keeps up through scale ~3.
	workCap := max(1400, boxW*2)
	if jpg.W > workCap {
		jpg = jpg.Resize(workCap, jpg.H*workCap/jpg.W)
	}
	sdrLin := imaging.New(jpg.W, jpg.H)
	for i := range sdrLin.Pix {
		sdrLin.Pix[i] = color.SRGBDecode(jpg.Pix[i])
	}
	rawA := warped.Resize(jpg.W, jpg.H)
	if opts.Register {
		aff := register.Estimate(jpg, rawA, register.Options{})
		rawA = aff.Apply(rawA, jpg.W, jpg.H)
	}

	hdr, err := conv.Renderer.Render(ctx, sdrLin, rawA) // --hdr-mode, --strength, --raw-lift, …
	must(err)

	gmo := gainmap.DefaultOptions()
	gmo.MultiChannel = opts.GainMap == arw2uhdr.GainMapRGB
	gmo.ScaleFactor = 1 // full-res for the viz row
	gmo.NeutralizeHighlights = !opts.NoNeutralize
	gmo.MaxBoostStops = opts.MaxBoost
	gmViz, _, err := gainmap.Compute(sdrLin, hdr, gmo)
	must(err)

	o := cp.Orientation
	sdrLinTile := fit(orient(sdrLin, o)) // linear
	return tiles{
		sdrDisp: srgb(sdrLinTile), // display base (rows 1 & 3)
		gainViz: fit(orient(gammaLift(gmViz.ToImage()), o)),
		exposed: fit(orient(tonemapInvHLG(hdr, tone), o)),
		hdrLin:  fit(orient(hdr, o)), // linear HDR (row 3 lift)
	}
}

// librawDemosaic maps the public --demosaic choice to the LibRaw algorithm id used by the
// reduced-resolution showcase decode (which builds raw.Opts directly rather than via the library).
func librawDemosaic(d arw2uhdr.Demosaic) int {
	switch d {
	case arw2uhdr.DemosaicDCB:
		return raw.DemosaicDCB
	case arw2uhdr.DemosaicDHT:
		return raw.DemosaicDHT
	case arw2uhdr.DemosaicVNG:
		return raw.DemosaicVNG
	case arw2uhdr.DemosaicPPG:
		return raw.DemosaicPPG
	case arw2uhdr.DemosaicLinear:
		return raw.DemosaicLinear
	default:
		return raw.DemosaicAHD
	}
}

type pair struct{ arw, jpg string }

func discover(dir string) []pair {
	entries, _ := os.ReadDir(dir)
	var ps []pair
	for _, e := range entries {
		if strings.EqualFold(filepath.Ext(e.Name()), ".arw") {
			base := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
			for _, ext := range []string{".JPG", ".jpg", ".JPEG", ".jpeg"} {
				j := filepath.Join(dir, base+ext)
				if _, err := os.Stat(j); err == nil {
					ps = append(ps, pair{filepath.Join(dir, e.Name()), j})
					break
				}
			}
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].arw < ps[j].arw })
	return ps
}

func srgb(lin *imaging.Image) *imaging.Image {
	out := imaging.New(lin.W, lin.H)
	for i := range lin.Pix {
		out.Pix[i] = color.SRGBEncode(lin.Pix[i])
	}
	return out
}

// tonemapInvHLG maps the linear HDR tile to SDR with a compressed, HLG-like split
// curve. The SDR range (linear ≤ 1) keeps the ordinary sRGB gamma *shape*, just
// scaled into [0, white], so un-boosted content stays faithful to the JPEG — same
// tonality, uniformly a touch darker, never higher-contrast. The HDR range
// (linear 1..peak) is a smooth shoulder that packs the recovered highlights into
// the reserved [white, 1] band so they read as a distinct, separated zone.
//
// The shoulder is an S-curve: C¹-continuous with the SDR curve at the split (starts
// tangent to the shallow SDR slope — no kink), then transitions quickly through the
// middle and flattens to zero slope at the peak (a smooth max-out into white). agg
// sets how quick the middle transition is.
//
// The squish is adaptive: a tile whose highlights barely exceed SDR needs little
// headroom, so its white point relaxes toward 1 and it is left essentially as the
// JPEG; only tiles with real recovered highlights are pulled down to make room.
func tonemapInvHLG(hdr *imaging.Image, tone toneParams) *imaging.Image {
	n := hdr.W * hdr.H
	maxes := make([]float64, n)
	for p := range n {
		i := p * 3
		maxes[p] = max(float64(hdr.Pix[i]), float64(hdr.Pix[i+1]), float64(hdr.Pix[i+2]))
	}
	sort.Float64s(maxes)
	pct := func(p float64) float64 { return maxes[int(float64(n-1)*min(max(p, 0), 1))] }

	// Brightness/squish is driven by the peakPct percentile, so a low-HDR tile stays
	// bright; reserve headroom only in proportion to how far it exceeds SDR (full
	// squish once ≥ ~0.6 stop over white). An all-SDR tile keeps white = 1.
	whitePeak := max(pct(tone.peakPct), 1)
	hdrAmt := min(max((whitePeak-1)/0.5, 0), 1)
	white := 1 - (1-tone.white)*hdrAmt

	// The span (what reaches display white) is driven by a near-max percentile, so a
	// small but real bright region compresses into the band instead of hard-clipping
	// to white; only the top ~0.1% (genuine speculars) clip.
	const srgbSlope1 = 1.055 / 2.4 // d/dv sRGBEncode at v=1 ≈ 0.4396
	spanPeak := max(pct(0.999), whitePeak)
	span := max(spanPeak-1, 1e-6)

	// HDR shoulder: an S-curve joining the SDR curve tangentially at the split (start
	// slope m0 in t-space → C¹, no kink) and flattening to zero slope at the peak
	// (smooth max-out), transitioning quickly through the middle. agg sets how quick.
	m0 := min(white*srgbSlope1*span/(1-white), 0.95)
	steep := min(max(tone.agg/(tone.agg+4), 0), 0.98)
	shoulder := buildShoulder(m0, steep)
	curve := func(v float64) float32 {
		if v <= 1 {
			return float32(white * float64(color.SRGBEncode(float32(v))))
		}
		if white >= 1 {
			return 1
		}
		return float32(white + (1-white)*shoulder(min((v-1)/span, 1)))
	}

	out := imaging.New(hdr.W, hdr.H)
	for p := range n {
		i := p * 3
		out.Pix[i] = curve(float64(hdr.Pix[i]))
		out.Pix[i+1] = curve(float64(hdr.Pix[i+1]))
		out.Pix[i+2] = curve(float64(hdr.Pix[i+2]))
	}
	return out
}

// buildShoulder returns an S-curve s:[0,1]→[0,1] for the HDR shoulder, sampled into
// a lookup for speed. It is a cubic Bézier from (0,0) to (1,1) whose start tangent
// has slope m0 (so the shoulder joins the SDR curve tangentially — C¹, no kink) and
// whose end tangent is flat (slope 0 — a smooth max-out at the peak). steep in [0,1)
// pulls the interior control points toward the centre, so the curve hugs each end
// then transitions quickly through the middle; higher = quicker. Monotonic for
// steep < 1.
func buildShoulder(m0, steep float64) func(float64) float64 {
	beta := 0.5 * min(max(steep, 0), 0.98)
	bx := func(u float64) float64 {
		return 3*(1-u)*(1-u)*u*beta + 3*(1-u)*u*u*(1-beta) + u*u*u
	}
	by := func(u float64) float64 {
		return 3*(1-u)*(1-u)*u*(beta*m0) + 3*(1-u)*u*u + u*u*u
	}
	const grid = 1024
	lut := make([]float64, grid+1)
	// Walk the Bézier parameter and resample y onto a uniform x (=t) grid; x(u) is
	// monotonic for beta < 0.5, so each grid point is filled in order.
	j := 0
	px, py := 0.0, 0.0
	const steps = 8192
	for s := 1; s <= steps; s++ {
		u := float64(s) / steps
		x, y := bx(u), by(u)
		for j <= grid && float64(j)/grid <= x {
			tt := float64(j) / grid
			if x > px {
				lut[j] = py + (y-py)*(tt-px)/(x-px)
			} else {
				lut[j] = y
			}
			j++
		}
		px, py = x, y
	}
	for ; j <= grid; j++ {
		lut[j] = 1
	}
	return func(t float64) float64 {
		if t <= 0 {
			return 0
		}
		if t >= 1 {
			return 1
		}
		f := t * grid
		i := int(f)
		return lut[i] + (lut[i+1]-lut[i])*(f-float64(i))
	}
}

func gammaLift(im *imaging.Image) *imaging.Image {
	out := imaging.New(im.W, im.H)
	for i := range im.Pix {
		out.Pix[i] = float32(math.Pow(float64(im.Pix[i]), 0.55))
	}
	return out
}

func fit(im *imaging.Image) *imaging.Image {
	s := math.Min(float64(boxW)/float64(im.W), float64(boxH)/float64(im.H))
	nw, nh := max(1, int(float64(im.W)*s)), max(1, int(float64(im.H)*s))
	r := im.Resize(nw, nh)
	dst := imaging.New(boxW, boxH)
	blit(dst, r, (boxW-nw)/2, (boxH-nh)/2)
	return dst
}

func blit(dst, src *imaging.Image, x0, y0 int) {
	for y := range src.H {
		dy := y0 + y
		if dy < 0 || dy >= dst.H {
			continue
		}
		for x := range src.W {
			dx := x0 + x
			if dx < 0 || dx >= dst.W {
				continue
			}
			si, di := (y*src.W+x)*3, (dy*dst.W+dx)*3
			dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2] = src.Pix[si], src.Pix[si+1], src.Pix[si+2]
		}
	}
}

func orient(im *imaging.Image, o int) *imaging.Image {
	switch o {
	case 6:
		dst := imaging.New(im.H, im.W)
		for y := range im.H {
			for x := range im.W {
				r, g, b := im.At(x, y)
				dst.Set(im.H-1-y, x, r, g, b)
			}
		}
		return dst
	case 8:
		dst := imaging.New(im.H, im.W)
		for y := range im.H {
			for x := range im.W {
				r, g, b := im.At(x, y)
				dst.Set(y, im.W-1-x, r, g, b)
			}
		}
		return dst
	}
	return im
}

// minimal 5x7 uppercase bitmap font (low 5 bits per row).
var glyphs = map[rune][7]uint8{
	'A': {0x0E, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11}, 'B': {0x1E, 0x11, 0x11, 0x1E, 0x11, 0x11, 0x1E},
	'C': {0x0E, 0x11, 0x10, 0x10, 0x10, 0x11, 0x0E}, 'D': {0x1E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x1E},
	'E': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x1F}, 'F': {0x1F, 0x10, 0x10, 0x1E, 0x10, 0x10, 0x10},
	'G': {0x0E, 0x11, 0x10, 0x17, 0x11, 0x11, 0x0F}, 'H': {0x11, 0x11, 0x11, 0x1F, 0x11, 0x11, 0x11},
	'I': {0x0E, 0x04, 0x04, 0x04, 0x04, 0x04, 0x0E}, 'J': {0x07, 0x02, 0x02, 0x02, 0x02, 0x12, 0x0C},
	'K': {0x11, 0x12, 0x14, 0x18, 0x14, 0x12, 0x11}, 'L': {0x10, 0x10, 0x10, 0x10, 0x10, 0x10, 0x1F},
	'M': {0x11, 0x1B, 0x15, 0x15, 0x11, 0x11, 0x11}, 'N': {0x11, 0x19, 0x15, 0x13, 0x11, 0x11, 0x11},
	'O': {0x0E, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E}, 'P': {0x1E, 0x11, 0x11, 0x1E, 0x10, 0x10, 0x10},
	'Q': {0x0E, 0x11, 0x11, 0x11, 0x15, 0x12, 0x0D}, 'R': {0x1E, 0x11, 0x11, 0x1E, 0x14, 0x12, 0x11},
	'S': {0x0F, 0x10, 0x10, 0x0E, 0x01, 0x01, 0x1E}, 'T': {0x1F, 0x04, 0x04, 0x04, 0x04, 0x04, 0x04},
	'U': {0x11, 0x11, 0x11, 0x11, 0x11, 0x11, 0x0E}, 'V': {0x11, 0x11, 0x11, 0x11, 0x11, 0x0A, 0x04},
	'W': {0x11, 0x11, 0x11, 0x15, 0x15, 0x1B, 0x11}, 'X': {0x11, 0x11, 0x0A, 0x04, 0x0A, 0x11, 0x11},
	'Y': {0x11, 0x11, 0x0A, 0x04, 0x04, 0x04, 0x04}, 'Z': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x10, 0x1F},
	' ': {0, 0, 0, 0, 0, 0, 0}, '-': {0, 0, 0, 0x1F, 0, 0, 0},
}

func drawText(im *imaging.Image, s string, x, y, scale int, r, g, b float32) {
	cx := x
	for _, ch := range s {
		gl, ok := glyphs[ch]
		if !ok {
			gl = glyphs[' ']
		}
		for row := range 7 {
			bits := gl[row]
			for col := range 5 {
				if bits&(1<<uint(4-col)) != 0 {
					for dy := range scale {
						for dx := range scale {
							im.Set(cx+col*scale+dx, y+row*scale+dy, r, g, b)
						}
					}
				}
			}
		}
		cx += 6 * scale
	}
}

func drawLabels(im *imaging.Image) {
	pad := textScale * labelPad / 3
	for r, s := range rowLabels {
		y := gap + r*(boxH+gap) + boxH/2 - 7*textScale/2
		drawText(im, s, pad, y, textScale, 0.95, 0.95, 0.95)
	}
}

// applyScale sizes the layout from the resolution multiplier and widens the gutter
// so it always fits the longest row label (no text bleeding into the tiles).
func applyScale(s float64) {
	if s < 1 {
		s = 1
	}
	boxW = int(360 * s)
	boxH = int(260 * s)
	gap = int(6 * s)
	textScale = max(3, int(3*s+0.5))
	pad := textScale * labelPad / 3
	longest := 0
	for _, l := range rowLabels {
		longest = max(longest, len(l))
	}
	gutter = pad + longest*6*textScale + pad
}

func encodeJPEG(im *imaging.Image, q int) []byte {
	rgba := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
	clamp := func(v float32) uint8 {
		if v <= 0 {
			return 0
		}
		if v >= 1 {
			return 255
		}
		return uint8(v*255 + 0.5)
	}
	for p := 0; p < im.W*im.H; p++ {
		rgba.Pix[p*4] = clamp(im.Pix[p*3])
		rgba.Pix[p*4+1] = clamp(im.Pix[p*3+1])
		rgba.Pix[p*4+2] = clamp(im.Pix[p*3+2])
		rgba.Pix[p*4+3] = 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, rgba, &jpeg.Options{Quality: q}); err != nil {
		must(err)
	}
	return buf.Bytes()
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
