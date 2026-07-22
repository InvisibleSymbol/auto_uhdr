// Command modecompare renders the default JPEG-anchored HDR mode beside the
// raw-luminance-driven, JPEG-gated mode for one pair: top row = gain maps,
// bottom row = tonemapped HDR. Full-resolution gain maps (no downscale) so the
// detail difference is visible.
//
// usage: modecompare [-threshold f] [-strength f] <in.ARW> <in.JPG> <out.png>
package main

import (
	"flag"
	"fmt"

	"os"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
)

func main() {
	threshold := flag.Float64("threshold", 0.5, "raw-boost mode JPEG gate threshold")
	strength := flag.Float64("strength", 1.0, "raw-boost mode gain multiplier")
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Fprintln(os.Stderr, "usage: modecompare [-threshold f] [-strength f] <in.ARW> <in.JPG> <out.png>")
		os.Exit(2)
	}
	arw, jpgPath, out := flag.Arg(0), flag.Arg(1), flag.Arg(2)

	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2
	img, _, err := raw.Decode(arw, ro)
	must(err)
	cp, err := sonylens.ReadARW(arw)
	must(err)
	warped := sonylens.Warp(img, cp, sonylens.DefaultWarp())
	jpg, err := imaging.LoadImage(jpgPath)
	must(err)
	sdrLin := imaging.New(jpg.W, jpg.H)
	for i := range sdrLin.Pix {
		sdrLin.Pix[i] = color.SRGBDecode(jpg.Pix[i])
	}
	rawA := warped.Resize(jpg.W, jpg.H)
	aff := register.Estimate(jpg, rawA, register.Options{})
	rawA = aff.Apply(rawA, jpg.W, jpg.H)

	// current default (JPEG-anchored highlight + boost)
	hdrH, _ := hdrbuild.Build(sdrLin, rawA, hdrbuild.DefaultOptions())
	// raw-luminance-driven, JPEG-gated
	oR := hdrbuild.DefaultOptions()
	oR.Mode = hdrbuild.ModeRawBoost
	oR.Threshold = *threshold
	oR.Strength = *strength
	hdrR, _ := hdrbuild.Build(sdrLin, rawA, oR)

	gmOpt := gainmap.DefaultOptions()
	gmOpt.ScaleFactor = 1 // full-res: show the true detail
	gmH, _, _ := gainmap.Compute(sdrLin, hdrH, gmOpt)
	gmR, _, _ := gainmap.Compute(sdrLin, hdrR, gmOpt)
	fmt.Printf("orient=%d reg Sx=%.4f Sy=%.4f\n", cp.Orientation, aff.Sx, aff.Sy)

	// Orient each panel individually, then 2x2 grid:
	//   [gain current | gain raw-driven]
	//   [HDR  current | HDR  raw-driven]
	enc := func(im *imaging.Image) *imaging.Image {
		for i := range im.Pix {
			im.Pix[i] = color.SRGBEncode(im.Pix[i])
		}
		return im
	}
	gmHo := enc(orient(gmH.ToImage(), cp.Orientation))
	gmRo := enc(orient(gmR.ToImage(), cp.Orientation))
	tmHo := enc(orient(tonemap(hdrH), cp.Orientation))
	tmRo := enc(orient(tonemap(hdrR), cp.Orientation))
	panel := stack(sideBySide(gmHo, gmRo), sideBySide(tmHo, tmRo))
	if panel.W > 1700 {
		s := 1700.0 / float64(panel.W)
		panel = panel.Resize(int(float64(panel.W)*s), int(float64(panel.H)*s))
	}
	must(panel.SavePNG8(out))
	fmt.Println("wrote", out, "(top: gain maps  bottom: tonemapped HDR   left=current  right=raw-driven)")
}

// tonemap compresses linear HDR into a viewable [0,1] plane (Reinhard) so highlight
// structure is visible.
func tonemap(hdr *imaging.Image) *imaging.Image {
	out := imaging.New(hdr.W, hdr.H)
	for i := range hdr.Pix {
		v := float64(hdr.Pix[i])
		out.Pix[i] = float32(v / (1 + v))
	}
	return out
}

func sideBySide(a, b *imaging.Image) *imaging.Image {
	W, H := a.W, a.H
	gap := 6
	dst := imaging.New(W*2+gap, H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			ai := (y*W + x) * 3
			di := (y*dst.W + x) * 3
			dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2] = a.Pix[ai], a.Pix[ai+1], a.Pix[ai+2]
			d2 := (y*dst.W + (x + W + gap)) * 3
			dst.Pix[d2], dst.Pix[d2+1], dst.Pix[d2+2] = b.Pix[ai], b.Pix[ai+1], b.Pix[ai+2]
		}
	}
	return dst
}

func stack(a, b *imaging.Image) *imaging.Image {
	W := max(a.W, b.W)
	gap := 6
	dst := imaging.New(W, a.H+b.H+gap)
	blit := func(src *imaging.Image, y0 int) {
		for y := 0; y < src.H; y++ {
			for x := 0; x < src.W; x++ {
				si := (y*src.W + x) * 3
				di := ((y+y0)*dst.W + x) * 3
				dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2] = src.Pix[si], src.Pix[si+1], src.Pix[si+2]
			}
		}
	}
	blit(a, 0)
	blit(b, a.H+gap)
	return dst
}

func orient(im *imaging.Image, o int) *imaging.Image {
	switch o {
	case 6:
		dst := imaging.New(im.H, im.W)
		for y := 0; y < im.H; y++ {
			for x := 0; x < im.W; x++ {
				r, g, b := im.At(x, y)
				dst.Set(im.H-1-y, x, r, g, b)
			}
		}
		return dst
	case 8:
		dst := imaging.New(im.H, im.W)
		for y := 0; y < im.H; y++ {
			for x := 0; x < im.W; x++ {
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
