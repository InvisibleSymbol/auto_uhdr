// Command dbgregion dumps diagnostics for a region: per-channel stats of the anchored RAW vs the
// SDR, plus viewable crops. Used to debug recovered-highlight color casts.
// usage: dbgregion <in.ARW> <in.JPG> <cx> <cy> <w> <h> <outPrefix>
package main

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
)

func main() {
	a := os.Args
	if len(a) < 8 {
		fmt.Fprintln(os.Stderr, "usage: dbgregion <arw> <jpg> <cx> <cy> <w> <h> <outPrefix>")
		os.Exit(2)
	}
	cx, _ := strconv.Atoi(a[3])
	cy, _ := strconv.Atoi(a[4])
	cw, _ := strconv.Atoi(a[5])
	ch, _ := strconv.Atoi(a[6])
	prefix := a[7]

	o := raw.DefaultOpts()
	o.Linear = true
	o.Highlight = highlightMode()
	img, _, err := raw.Decode(a[1], o)
	must(err)
	cp, err := sonylens.ReadARW(a[1])
	must(err)
	warped := sonylens.Warp(img, cp, sonylens.DefaultWarp())
	jpg, err := imaging.LoadImage(a[2])
	must(err)
	sdrLin := imaging.New(jpg.W, jpg.H)
	for i := range jpg.Pix {
		sdrLin.Pix[i] = color.SRGBDecode(jpg.Pix[i])
	}
	rawAligned := warped.Resize(jpg.W, jpg.H)
	aff := register.Estimate(jpg, rawAligned, register.Options{})
	rawAligned = aff.Apply(rawAligned, jpg.W, jpg.H)

	// anchor gains (same as hdrbuild)
	k := anchor(sdrLin, rawAligned)
	fmt.Printf("anchor k = [%.3f %.3f %.3f]\n", k[0], k[1], k[2])

	// region stats
	var stats [3]struct{ rawMax, rawMean float64 }
	var n float64
	// raw pre-anchor histogram of per-channel max in region
	clipCount := [3]int{}
	pix := 0
	for y := cy; y < cy+ch; y++ {
		for x := cx; x < cx+cw; x++ {
			i := (y*jpg.W + x) * 3
			for c := 0; c < 3; c++ {
				rv := float64(rawAligned.Pix[i+c])
				stats[c].rawMean += rv
				if rv > stats[c].rawMax {
					stats[c].rawMax = rv
				}
			}
			pix++
		}
	}
	n = float64(pix)
	// estimate per-channel raw ceiling over the WHOLE image (99.99th percentile)
	var ceil [3]float64
	for c := 0; c < 3; c++ {
		vals := make([]float64, 0, jpg.W*jpg.H/64)
		for p := c; p < len(rawAligned.Pix); p += 3 * 64 {
			vals = append(vals, float64(rawAligned.Pix[p]))
		}
		sort.Float64s(vals)
		ceil[c] = vals[len(vals)-1-len(vals)/10000]
	}
	fmt.Printf("global raw ceiling (p99.99): R=%.4f G=%.4f B=%.4f\n", ceil[0], ceil[1], ceil[2])
	for c, nme := range []string{"R", "G", "B"} {
		// count region pixels within 2% of that channel's ceiling
		for y := cy; y < cy+ch; y++ {
			for x := cx; x < cx+cw; x++ {
				i := (y*jpg.W+x)*3 + c
				if float64(rawAligned.Pix[i]) > 0.98*ceil[c] {
					clipCount[c]++
				}
			}
		}
		fmt.Printf("region %s: mean=%.4f max=%.4f  atCeil=%.1f%%  (anchored max=%.2f)\n",
			nme, stats[c].rawMean/n, stats[c].rawMax, 100*float64(clipCount[c])/n, stats[c].rawMax*k[c])
	}

	// save crops: SDR, anchored raw at 2 exposures
	saveCrop := func(src *imaging.Image, mul [3]float64, exp float32, path string) {
		out := imaging.New(cw, ch)
		for y := 0; y < ch; y++ {
			for x := 0; x < cw; x++ {
				si := ((cy+y)*jpg.W + cx + x) * 3
				di := (y*cw + x) * 3
				for c := 0; c < 3; c++ {
					v := float32(float64(src.Pix[si+c])*mul[c]) * exp
					out.Pix[di+c] = color.SRGBEncode(v)
				}
			}
		}
		must(out.SavePNG8(path))
	}
	one := [3]float64{1, 1, 1}
	saveCrop(sdrLin, one, 1.0, prefix+"_sdr.png")
	saveCrop(rawAligned, k, 1.0, prefix+"_rawk.png")
	saveCrop(rawAligned, k, 0.35, prefix+"_rawk_dim.png")
	fmt.Println("wrote crops:", prefix+"_{sdr,rawk,rawk_dim}.png")
}

func anchor(sdr, raw *imaging.Image) [3]float64 {
	W, H := sdr.W, sdr.H
	step := int(math.Sqrt(float64(W*H)/400000.0)) + 1
	var ratios [3][]float64
	for y := 0; y < H; y += step {
		for x := 0; x < W; x += step {
			i := (y*W + x) * 3
			ys := 0.2126*float64(sdr.Pix[i]) + 0.7152*float64(sdr.Pix[i+1]) + 0.0722*float64(sdr.Pix[i+2])
			if ys < 0.12 || ys > 0.90 {
				continue
			}
			for c := 0; c < 3; c++ {
				rv := float64(raw.Pix[i+c])
				sv := float64(sdr.Pix[i+c])
				if rv > 1e-4 && sv > 1e-4 {
					ratios[c] = append(ratios[c], sv/rv)
				}
			}
		}
	}
	var k [3]float64
	for c := 0; c < 3; c++ {
		sort.Float64s(ratios[c])
		k[c] = ratios[c][len(ratios[c])/2]
	}
	return k
}

func highlightMode() int {
	if v := os.Getenv("DBG_HIGHLIGHT"); v != "" {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return 2
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
