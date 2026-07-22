// Command aligndiag renders the linear SDR beside the aligned, highlight-recovered
// RAW (raw*k), exposure pulled down so blown highlights reveal their structure.
// Used to check whether content in a bright region matches between JPEG and RAW.
//
// usage: aligndiag [-exp f] [-crop x0,y0,x1,y1] <in.ARW> <in.JPG> <out.png>
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
)

func main() {
	exp := flag.Float64("exp", 0.35, "exposure multiplier before sRGB encode (reveal highlights)")
	crop := flag.String("crop", "", "x0,y0,x1,y1 crop in STORED (landscape) pixels")
	hl := flag.Int("hl", 2, "libraw highlight mode (0 clip, 1 unclip, 2 blend)")
	noreg := flag.Bool("noreg", false, "skip registration")
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Fprintln(os.Stderr, "usage: aligndiag [-exp f] [-crop x0,y0,x1,y1] <in.ARW> <in.JPG> <out.png>")
		os.Exit(2)
	}
	arw, jpgPath, out := flag.Arg(0), flag.Arg(1), flag.Arg(2)

	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = *hl
	img, meta, err := raw.Decode(arw, ro)
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
	if !*noreg {
		aff := register.Estimate(jpg, rawA, register.Options{})
		rawA = aff.Apply(rawA, jpg.W, jpg.H)
		fmt.Printf("reg Sx=%.4f Sy=%.4f\n", aff.Sx, aff.Sy)
	}
	k := anchor(sdrLin, rawA)
	fmt.Printf("decoded %dx%d (%s) orient=%d hl=%d k=[%.1f %.1f %.1f]\n",
		meta.Width, meta.Height, meta.Model, cp.Orientation, *hl, k[0], k[1], k[2])

	// optional crop
	x0, y0, x1, y1 := 0, 0, jpg.W, jpg.H
	if *crop != "" {
		fmt.Sscanf(strings.ReplaceAll(*crop, ",", " "), "%d %d %d %d", &x0, &y0, &x1, &y1)
	}
	cw, ch := x1-x0, y1-y0
	gap := 6
	panel := imaging.New(cw*2+gap, ch)
	for y := 0; y < ch; y++ {
		for x := 0; x < cw; x++ {
			si := ((y0+y)*jpg.W + (x0 + x)) * 3
			// left: SDR
			putTM(panel, x, y, sdrLin.Pix[si], sdrLin.Pix[si+1], sdrLin.Pix[si+2], *exp)
			// right: raw*k
			putTM(panel, x+cw+gap, y,
				rawA.Pix[si]*float32(k[0]), rawA.Pix[si+1]*float32(k[1]), rawA.Pix[si+2]*float32(k[2]), *exp)
		}
	}
	// downscale wide panels for delivery
	if panel.W > 1800 {
		s := 1800.0 / float64(panel.W)
		panel = panel.Resize(int(float64(panel.W)*s), int(float64(panel.H)*s))
	}
	must(panel.SavePNG8(out))
	fmt.Println("wrote", out, "(left=SDR, right=RAW*k)")
}

func putTM(dst *imaging.Image, x, y int, r, g, b float32, exp float64) {
	e := float32(exp)
	dst.Set(x, y, color.SRGBEncode(r*e), color.SRGBEncode(g*e), color.SRGBEncode(b*e))
}

func anchor(sdr, raw *imaging.Image) [3]float64 {
	var k [3]float64
	for c := range 3 {
		var num, den float64
		for p := 0; p < sdr.W*sdr.H; p += 37 {
			i := p*3 + c
			s, r := float64(sdr.Pix[i]), float64(raw.Pix[i])
			if s > 0.1 && s < 0.8 && r > 1e-4 {
				num += s
				den += r
			}
		}
		if den > 0 {
			k[c] = num / den
		} else {
			k[c] = 1
		}
	}
	return k
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
