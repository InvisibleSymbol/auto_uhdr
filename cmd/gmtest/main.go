// Command gmtest runs the HDR half of the pipeline on a real ARW+JPEG pair and writes
// visualization images plus real Ultra HDR files. Supports an HDR-strength sweep.
//
// usage: gmtest [flags] <in.ARW> <in.JPG> <outPrefix>
package main

import (
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

func main() {
	strength := flag.Float64("strength", 1.0, "HDR display-boost strength (extra stops at white)")
	threshold := flag.Float64("threshold", 0.5, "HDR boost threshold (lower reaches more of the image)")
	maxboost := flag.Float64("maxboost", 3.0, "hard ceiling on boost (stops)")
	gmscale := flag.Int("gmscale", 4, "gain map downsample factor (1 = full res)")
	gmquality := flag.Int("gmquality", 90, "gain map JPEG quality")
	blurfrac := flag.Float64("blur", -1, "boost blur frac (-1 = default; 0 = pointwise)")
	reclo := flag.Float64("reclo", -1, "recovery gate low (-1 = default)")
	sweep := flag.String("sweep", "", "strength sweep e.g. '0,1,2,3' -> gain maps")
	tsweep := flag.String("tsweep", "", "threshold sweep e.g. '0.2,0.35,0.5,0.65' at fixed -strength")
	flag.Parse()
	if flag.NArg() < 3 {
		fmt.Fprintln(os.Stderr, "usage: gmtest [flags] <in.ARW> <in.JPG> <outPrefix>")
		os.Exit(2)
	}
	arw, jpgPath, prefix := flag.Arg(0), flag.Arg(1), flag.Arg(2)

	// --- pipeline up to the aligned linear RAW (done once) ---
	o := raw.DefaultOpts()
	o.Linear = true
	o.Highlight = 2 // blend: max recovery range; SDR-guided filter gates its artifacts
	img, meta, err := raw.Decode(arw, o)
	must(err, "decode")
	cp, err := sonylens.ReadARW(arw)
	must(err, "params")
	warped := sonylens.Warp(img, cp, sonylens.DefaultWarp())
	jpg, err := imaging.LoadImage(jpgPath)
	must(err, "load jpeg")
	sdrLin := linearize(jpg)
	rawAligned := warped.Resize(jpg.W, jpg.H)
	aff := register.Estimate(jpg, rawAligned, register.Options{})
	rawAligned = aff.Apply(rawAligned, jpg.W, jpg.H)
	fmt.Printf("decoded %dx%d (%s); registration Sx=%.4f Sy=%.4f\n",
		meta.Width, meta.Height, meta.Model, aff.Sx, aff.Sy)

	buildOpts := func(s float64) hdrbuild.Options {
		b := hdrbuild.DefaultOptions()
		b.Strength = s
		b.Threshold = *threshold
		b.MaxBoostStops = *maxboost
		if *blurfrac >= 0 {
			b.BlurFrac = *blurfrac
		}
		if *reclo >= 0 {
			b.RecoverLo = *reclo
		}
		return b
	}

	if *sweep != "" {
		for _, s := range parseFloats(*sweep) {
			hdr, _ := hdrbuild.Build(sdrLin, rawAligned, buildOpts(s))
			gm, m, err := gainmap.Compute(sdrLin, hdr, single(false))
			must(err, "gm")
			save(gm.ToImage(), fmt.Sprintf("%s_gm_s%.1f.png", prefix, s))
			fmt.Printf("strength=%.1f  coverage=%.1f%%  maxBoost=%.2f stops\n", s, coverage(gm, m), m.MaxLog2[0])
		}
		return
	}
	if *tsweep != "" {
		for _, t := range parseFloats(*tsweep) {
			b := buildOpts(*strength)
			b.Threshold = t
			hdr, _ := hdrbuild.Build(sdrLin, rawAligned, b)
			gm, m, err := gainmap.Compute(sdrLin, hdr, single(false))
			must(err, "gm")
			save(gm.ToImage(), fmt.Sprintf("%s_gm_t%.2f.png", prefix, t))
			fmt.Printf("threshold=%.2f (strength=%.1f)  coverage=%.1f%%  maxBoost=%.2f stops\n",
				t, *strength, coverage(gm, m), m.MaxLog2[0])
		}
		return
	}

	// --- single setting: viz + real Ultra HDR files ---
	hdr, k := hdrbuild.Build(sdrLin, rawAligned, buildOpts(*strength))
	fmt.Printf("anchor k=[%.2f %.2f %.2f]  strength=%.1f threshold=%.2f\n", k[0], k[1], k[2], *strength, *threshold)

	gmS, metaS, err := gainmap.Compute(sdrLin, hdr, single(false))
	must(err, "gm single")
	gmR, _, err := gainmap.Compute(sdrLin, hdr, single(true))
	must(err, "gm rgb")
	fmt.Printf("coverage(gain>0.1 stop) = %.1f%%  maxBoost=%.2f stops\n", coverage(gmS, metaS), metaS.MaxLog2[0])
	save(jpg, prefix+"_sdr.png")
	save(gmS.ToImage(), prefix+"_gm_single.png")
	save(gmR.ToImage(), prefix+"_gm_rgb.png")
	// reconstruct the HDR and expose down to reveal whether highlight detail survives.
	rec := gainmap.Reconstruct(sdrLin, gmS, metaS, 1.0)
	for i := range rec.Pix {
		rec.Pix[i] = color.SRGBEncode(rec.Pix[i] * 0.35)
	}
	save(rec, prefix+"_hdrdown.png")

	baseBytes, err := os.ReadFile(jpgPath)
	must(err, "read base jpeg")
	writeUHDR := func(multi bool, suffix string) {
		go4 := single(multi)
		go4.ScaleFactor = *gmscale
		g4, m4, err := gainmap.Compute(sdrLin, hdr, go4)
		must(err, "gm encode")
		out, err := ultrahdr.Encode(baseBytes, g4, m4, ultrahdr.Options{GainMapQuality: *gmquality})
		must(err, "encode")
		must(os.WriteFile(prefix+"_ultrahdr_"+suffix+".jpg", out, 0644), "write")
		fmt.Printf("wrote %s_ultrahdr_%s.jpg (%d KB, gmscale=%d q=%d)\n", prefix, suffix, len(out)/1024, *gmscale, *gmquality)
	}
	writeUHDR(false, "single")
	writeUHDR(true, "rgb")
}

func parseFloats(spec string) []float64 {
	var out []float64
	for _, tok := range splitCommas(spec) {
		var v float64
		fmt.Sscanf(tok, "%g", &v)
		out = append(out, v)
	}
	return out
}

// coverage: percent of gain-map pixels encoding more than ~0.1 stop of boost.
func coverage(gm *gainmap.GainMap, m gainmap.Metadata) float64 {
	if m.MaxLog2[0] <= 0 {
		return 0
	}
	thr := 0.1 / m.MaxLog2[0] * 255 // encoded value for 0.1 stop
	var n, tot int
	stepC := gm.Channels
	for p := 0; p < gm.W*gm.H; p++ {
		if float64(gm.Pix[p*stepC]) > thr {
			n++
		}
		tot++
	}
	return 100 * float64(n) / float64(tot)
}

func single(multi bool) gainmap.Options {
	o := gainmap.DefaultOptions()
	o.MultiChannel = multi
	return o
}

func splitCommas(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
		} else {
			cur += string(r)
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func linearize(im *imaging.Image) *imaging.Image {
	out := imaging.New(im.W, im.H)
	for i := range im.Pix {
		out.Pix[i] = color.SRGBDecode(im.Pix[i])
	}
	return out
}

func save(im *imaging.Image, path string) {
	must(im.SavePNG8(path), "save "+path)
}

func must(err error, ctx string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, ctx+":", err)
		os.Exit(1)
	}
}

var _ = math.Exp2
