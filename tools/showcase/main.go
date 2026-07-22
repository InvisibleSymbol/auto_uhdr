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
//	       the recovered highlights get an aggressive log shoulder into [white,1],
//	       so they read as a separated band — the recovered detail viewers without
//	       an HDR screen would otherwise miss
//
// The output is one Ultra HDR JPEG whose gain map is zero everywhere except the
// row-3 tiles. Processing is at reduced resolution (half-size decode) for speed.
//
// usage: showcase [-peak p] <dir> <out.jpg>
package main

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

const (
	boxW, boxH = 360, 260
	gap        = 6
	gutter     = 190 // left margin for row labels
)

func main() {
	peakPct := flag.Float64("peak", 0.995, "row-4 highlight-peak percentile (hot-pixel-robust)")
	white := flag.Float64("white", 0.75, "row-4 SDR white point: display level SDR-white squishes to, reserving [white,1] for HDR")
	agg := flag.Float64("agg", 8, "row-4 HDR-shoulder aggressiveness (higher = steeper log rolloff)")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: showcase [-peak p] [-white w] [-agg a] <dir> <out.jpg>")
		os.Exit(2)
	}
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
		t := process(pr, tone)
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
	base := encodeJPEG(sdr, 92)
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

func process(pr pair, tone toneParams) tiles {
	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2
	ro.HalfSize = true
	img, _, err := raw.Decode(pr.arw, ro)
	must(err)
	cp, err := sonylens.ReadARW(pr.arw)
	must(err)
	warped := sonylens.Warp(img, cp, sonylens.DefaultWarp())

	jpg, err := imaging.LoadImage(pr.jpg)
	must(err)
	if jpg.W > 1400 {
		jpg = jpg.Resize(1400, jpg.H*1400/jpg.W)
	}
	sdrLin := imaging.New(jpg.W, jpg.H)
	for i := range sdrLin.Pix {
		sdrLin.Pix[i] = color.SRGBDecode(jpg.Pix[i])
	}
	rawA := warped.Resize(jpg.W, jpg.H)
	aff := register.Estimate(jpg, rawA, register.Options{})
	rawA = aff.Apply(rawA, jpg.W, jpg.H)

	ho := hdrbuild.DefaultOptions()
	ho.Mode = hdrbuild.ModeRawBoost
	ho.Strength, ho.Threshold, ho.ChromaStrength = 1, 0.5, 0.3
	hdr, _ := hdrbuild.Build(sdrLin, rawA, ho)

	gmo := gainmap.DefaultOptions()
	gmo.MultiChannel = true
	gmo.ScaleFactor = 1
	gmo.NeutralizeHighlights = false
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
// (linear 1..peak) then switches to an aggressive log shoulder that packs the
// recovered highlights into the reserved [white, 1] band, so they read as a
// distinct, separated zone instead of dissolving into the squish (gamma below the
// SDR white, log above — the HLG structure, compressed to fit SDR).
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
	idx := int(float64(n-1) * min(max(tone.peakPct, 0), 1))
	peak := max(maxes[idx], 1)

	// Reserve headroom only in proportion to how far the tile actually exceeds SDR
	// (full squish once the peak is ≥ ~0.6 stop over white); an all-SDR tile keeps
	// white = 1 and renders as the plain JPEG.
	hdrAmt := min(max((peak-1)/0.5, 0), 1)
	white := 1 - (1-tone.white)*hdrAmt

	logK := math.Log(1 + tone.agg)
	span := max(peak-1, 1e-6) // guard the all-SDR tile (peak≈1)
	curve := func(v float64) float32 {
		if v <= 1 {
			return float32(white * float64(color.SRGBEncode(float32(v))))
		}
		t := min((v-1)/span, 1)
		return float32(white + (1-white)*math.Log(1+tone.agg*t)/logK)
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
	labels := []string{"SDR", "GAIN MAP", "ULTRA HDR", "SDR PREVIEW"}
	for r, s := range labels {
		y := gap + r*(boxH+gap) + boxH/2 - 11
		drawText(im, s, 10, y, 3, 0.95, 0.95, 0.95)
	}
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
