// Command showcase renders a 4-row grid over every ARW+JPEG pair in a directory:
//
//	row 1  SDR (the camera JPEG)
//	row 2  the RGB gain map (default settings)
//	row 3  the Ultra HDR result, tonemapped to SDR for preview (ACES)
//	row 4  the Ultra HDR result exposed down (−EV) to reveal recovered highlights
//
// It visualizes what the tool does with a real reconstructed preview. Processing
// is at reduced resolution (half-size decode) for speed.
//
// usage: showcase [-ev f] <dir> <out.png>
package main

import (
	"flag"
	"fmt"
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
)

const (
	boxW, boxH = 360, 260
	gap        = 6
	gutter     = 150 // left margin for row labels (added later)
)

func main() {
	ev := flag.Float64("ev", 2.0, "stops to expose down for row 4")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: showcase [-ev f] <dir> <out.png>")
		os.Exit(2)
	}
	dir, out := flag.Arg(0), flag.Arg(1)

	pairs := discover(dir)
	if len(pairs) == 0 {
		fmt.Fprintln(os.Stderr, "no ARW+JPEG pairs found in", dir)
		os.Exit(1)
	}
	n := len(pairs)
	grid := imaging.New(gutter+n*boxW+(n+1)*gap, 4*boxH+5*gap)
	for col, pr := range pairs {
		fmt.Printf("[%d/%d] %s\n", col+1, n, filepath.Base(pr.arw))
		tiles := process(pr, *ev)
		for row := range 4 {
			x := gutter + gap + col*(boxW+gap)
			y := gap + row*(boxH+gap)
			blit(grid, tiles[row], x, y)
		}
	}
	must(grid.SavePNG8(out))
	fmt.Println("wrote", out)
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

// process returns the four display-space tiles for one pair.
func process(pr pair, ev float64) [4]*imaging.Image {
	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2
	ro.HalfSize = true // speed: half-res decode is plenty for small tiles
	img, _, err := raw.Decode(pr.arw, ro)
	must(err)
	cp, err := sonylens.ReadARW(pr.arw)
	must(err)
	warped := sonylens.Warp(img, cp, sonylens.DefaultWarp())

	jpg, err := imaging.LoadImage(pr.jpg)
	must(err)
	if jpg.W > 1400 { // work small for speed
		jpg = jpg.Resize(1400, jpg.H*1400/jpg.W)
	}
	sdrLin := imaging.New(jpg.W, jpg.H)
	for i := range sdrLin.Pix {
		sdrLin.Pix[i] = color.SRGBDecode(jpg.Pix[i])
	}
	rawA := warped.Resize(jpg.W, jpg.H)
	aff := register.Estimate(jpg, rawA, register.Options{})
	rawA = aff.Apply(rawA, jpg.W, jpg.H)

	// default rendering: raw mode, chroma 0.3, RGB gain map
	ho := hdrbuild.DefaultOptions()
	ho.Mode = hdrbuild.ModeRawBoost
	ho.Strength, ho.Threshold, ho.ChromaStrength = 1, 0.5, 0.3
	hdr, _ := hdrbuild.Build(sdrLin, rawA, ho)

	gmo := gainmap.DefaultOptions()
	gmo.MultiChannel = true
	gmo.ScaleFactor = 1
	gmo.NeutralizeHighlights = false
	gm, meta, err := gainmap.Compute(sdrLin, hdr, gmo)
	must(err)
	hdrR := gainmap.Reconstruct(sdrLin, gm, meta, 1.0) // linear HDR, the true round-trip

	o := cp.Orientation
	t1 := fit(orient(jpg, o))                     // SDR (already display)
	t2 := fit(orient(gammaLift(gm.ToImage()), o)) // gain map (brightened for visibility)
	t3 := fit(orient(tonemapHighlights(hdrR), o)) // HDR preview (display)
	t4 := fit(orient(exposeSRGB(hdrR, ev), o))    // exposed for highlights (display)
	return [4]*imaging.Image{t1, t2, t3, t4}
}

// tonemapHighlights keeps midtones/shadows exactly as the SDR (identity below a
// knee) and rolls off the extended-range highlights into the remaining headroom,
// so the recovered highlight detail becomes visible without darkening the image.
func tonemapHighlights(hdr *imaging.Image) *imaging.Image {
	out := imaging.New(hdr.W, hdr.H)
	const t = 0.8
	f := func(v float64) float64 {
		if v <= t {
			return v
		}
		return t + (1-t)*(1-math.Exp(-(v-t)/(1-t)))
	}
	for i := range hdr.Pix {
		out.Pix[i] = color.SRGBEncode(float32(f(float64(hdr.Pix[i]))))
	}
	return out
}

// gammaLift brightens a display-space image (for the mostly-dark gain map).
func gammaLift(im *imaging.Image) *imaging.Image {
	out := imaging.New(im.W, im.H)
	for i := range im.Pix {
		out.Pix[i] = float32(math.Pow(float64(im.Pix[i]), 0.55))
	}
	return out
}

// exposeSRGB multiplies linear HDR by 2^-ev, clamps, and sRGB-encodes for display.
func exposeSRGB(hdr *imaging.Image, ev float64) *imaging.Image {
	out := imaging.New(hdr.W, hdr.H)
	scale := float32(math.Exp2(-ev))
	for i := range hdr.Pix {
		out.Pix[i] = color.SRGBEncode(hdr.Pix[i] * scale)
	}
	return out
}

// fit scales im to fit within the tile box, centered on black.
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

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
