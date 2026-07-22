// Command gaincompare renders the RGB gain map for three highlight-colour
// settings side by side: neutralize ON, neutralize OFF, and preserve-chroma.
//
// usage: gaincompare <in.ARW> <in.JPG> <out.png>
package main

import (
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
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: gaincompare <in.ARW> <in.JPG> <out.png>")
		os.Exit(2)
	}
	arw, jpgPath, out := os.Args[1], os.Args[2], os.Args[3]

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

	// raw-boost defaults (match the CLI): strength 1, threshold 0.5; sweep the Chroma dial.
	base := hdrbuild.DefaultOptions()
	base.Mode = hdrbuild.ModeRawBoost
	base.Strength, base.Threshold = 1, 0.5

	gmOpt := gainmap.DefaultOptions()
	gmOpt.MultiChannel = true
	gmOpt.ScaleFactor = 1
	gmOpt.NeutralizeHighlights = false

	mk := func(chroma float64) *imaging.Image {
		o := base
		o.ChromaStrength = chroma
		hdr, _ := hdrbuild.Build(sdrLin, rawA, o)
		gm, _, _ := gainmap.Compute(sdrLin, hdr, gmOpt)
		return enc(orient(gm.ToImage(), cp.Orientation))
	}
	panel := hstack3(mk(0.0), mk(0.3), mk(1.0))
	if panel.W > 1800 {
		s := 1800.0 / float64(panel.W)
		panel = panel.Resize(int(float64(panel.W)*s), int(float64(panel.H)*s))
	}
	must(panel.SavePNG8(out))
	fmt.Println("wrote", out, "(RGB gain map at chroma  left: 0.0   middle: 0.3   right: 1.0)")
}

func enc(im *imaging.Image) *imaging.Image {
	for i := range im.Pix {
		im.Pix[i] = color.SRGBEncode(im.Pix[i])
	}
	return im
}

func hstack3(a, b, c *imaging.Image) *imaging.Image {
	H := a.H
	gap := 6
	dst := imaging.New(a.W+b.W+c.W+2*gap, H)
	blit := func(src *imaging.Image, x0 int) {
		for y := 0; y < src.H && y < H; y++ {
			for x := 0; x < src.W; x++ {
				si := (y*src.W + x) * 3
				di := (y*dst.W + x0 + x) * 3
				dst.Pix[di], dst.Pix[di+1], dst.Pix[di+2] = src.Pix[si], src.Pix[si+1], src.Pix[si+2]
			}
		}
	}
	blit(a, 0)
	blit(b, a.W+gap)
	blit(c, a.W+b.W+2*gap)
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
