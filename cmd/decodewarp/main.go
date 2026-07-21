// Command decodewarp runs the Go pipeline so far: LibRaw decode -> Sony distortion+CA warp.
// usage: decodewarp [flags] <in.ARW> <out.png>
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/sonylens"
)

func main() {
	linear := flag.Bool("linear", false, "decode scene-linear (else display gamma)")
	ca := flag.Bool("ca", true, "apply chromatic-aberration correction")
	crop := flag.Float64("crop", 1.0, "warp crop scale")
	half := flag.Bool("half", false, "half-resolution decode (fast)")
	nowarp := flag.Bool("nowarp", false, "skip the warp (decode only)")
	flag.Parse()
	if flag.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: decodewarp [flags] <in.ARW> <out.png>")
		os.Exit(2)
	}
	in, out := flag.Arg(0), flag.Arg(1)

	t0 := time.Now()
	o := raw.DefaultOpts()
	o.Linear = *linear
	o.HalfSize = *half
	img, meta, err := raw.Decode(in, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		os.Exit(1)
	}
	tDec := time.Since(t0)

	if !*nowarp {
		cp, err := sonylens.ReadARW(in)
		if err != nil {
			fmt.Fprintln(os.Stderr, "params:", err)
			os.Exit(1)
		}
		t1 := time.Now()
		img = sonylens.Warp(img, cp, sonylens.WarpConfig{CropScale: *crop, ApplyCA: *ca})
		fmt.Printf("decode %dx%d (%s) %.0fms | warp distN=%d ca=%v %.0fms\n",
			meta.Width, meta.Height, meta.Model, float64(tDec.Milliseconds()),
			cp.DistortionN, *ca, float64(time.Since(t1).Milliseconds()))
	}

	// If linear, encode to sRGB for a viewable PNG.
	if *linear {
		encodeSRGBInPlace(img)
	}
	if err := img.SavePNG8(out); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", out)
}

func encodeSRGBInPlace(im *imaging.Image) {
	for i := range im.Pix {
		im.Pix[i] = color.SRGBEncode(im.Pix[i])
	}
}
