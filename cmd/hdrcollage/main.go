// Command hdrcollage builds a single Ultra HDR image whose tiles each show the real HDR effect
// at a different threshold, so the settings can be compared on an actual HDR display.
//
// usage: hdrcollage [flags] <in.ARW> <in.JPG> <out.jpg>
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"strconv"
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

// minimal 5x7 bitmap font (low 5 bits per row) for digits and a few label chars.
var glyphs = map[rune][7]uint8{
	'0': {0x0E, 0x11, 0x13, 0x15, 0x19, 0x11, 0x0E},
	'1': {0x04, 0x0C, 0x04, 0x04, 0x04, 0x04, 0x0E},
	'2': {0x0E, 0x11, 0x01, 0x02, 0x04, 0x08, 0x1F},
	'3': {0x1F, 0x02, 0x04, 0x02, 0x01, 0x11, 0x0E},
	'4': {0x02, 0x06, 0x0A, 0x12, 0x1F, 0x02, 0x02},
	'5': {0x1F, 0x10, 0x1E, 0x01, 0x01, 0x11, 0x0E},
	'6': {0x06, 0x08, 0x10, 0x1E, 0x11, 0x11, 0x0E},
	'7': {0x1F, 0x01, 0x02, 0x04, 0x08, 0x08, 0x08},
	'8': {0x0E, 0x11, 0x11, 0x0E, 0x11, 0x11, 0x0E},
	'9': {0x0E, 0x11, 0x11, 0x0F, 0x01, 0x02, 0x0C},
	'.': {0, 0, 0, 0, 0, 0x0C, 0x0C},
	' ': {0, 0, 0, 0, 0, 0, 0},
	't': {0x08, 0x08, 0x1C, 0x08, 0x08, 0x09, 0x06},
	'h': {0x10, 0x10, 0x16, 0x19, 0x11, 0x11, 0x11},
	'r': {0, 0, 0x16, 0x19, 0x10, 0x10, 0x10},
	'=': {0, 0, 0x1F, 0, 0x1F, 0, 0},
	's': {0, 0x0E, 0x10, 0x0E, 0x01, 0x1E, 0},
}

func drawText(im *imaging.Image, s string, x, y, scale int, r, g, b float32) {
	cx := x
	for _, ch := range s {
		gl, ok := glyphs[ch]
		if !ok {
			gl = glyphs[' ']
		}
		for row := 0; row < 7; row++ {
			bits := gl[row]
			for col := 0; col < 5; col++ {
				if bits&(1<<uint(4-col)) != 0 {
					for dy := 0; dy < scale; dy++ {
						for dx := 0; dx < scale; dx++ {
							im.Set(cx+col*scale+dx, y+row*scale+dy, r, g, b)
						}
					}
				}
			}
		}
		cx += 6 * scale
	}
}

func linearize(im *imaging.Image) *imaging.Image {
	out := imaging.New(im.W, im.H)
	for i := range im.Pix {
		out.Pix[i] = color.SRGBDecode(im.Pix[i])
	}
	return out
}

func blit(dst, src *imaging.Image, x0, y0 int) {
	for y := 0; y < src.H; y++ {
		for x := 0; x < src.W; x++ {
			i := (y*src.W + x) * 3
			dst.Set(x0+x, y0+y, src.Pix[i], src.Pix[i+1], src.Pix[i+2])
		}
	}
}

func jpegBytes(im *imaging.Image, q int) []byte {
	out := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
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
		out.Pix[p*4] = clamp(im.Pix[p*3])
		out.Pix[p*4+1] = clamp(im.Pix[p*3+1])
		out.Pix[p*4+2] = clamp(im.Pix[p*3+2])
		out.Pix[p*4+3] = 255
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, out, &jpeg.Options{Quality: q})
	return buf.Bytes()
}

func main() {
	var strength float64 = 2.0
	thrSpec := "0.65,0.45,0.30,0.15"
	tilesSpec := "" // explicit tiles as "thr:str,thr:str,..." (overrides -thresholds/-strength)
	tileW := 1000
	args := []string{}
	for i := 1; i < len(os.Args); i++ {
		a := os.Args[i]
		switch {
		case a == "-strength" && i+1 < len(os.Args):
			i++
			strength, _ = strconv.ParseFloat(os.Args[i], 64)
		case a == "-thresholds" && i+1 < len(os.Args):
			i++
			thrSpec = os.Args[i]
		case a == "-tiles" && i+1 < len(os.Args):
			i++
			tilesSpec = os.Args[i]
		case a == "-tile" && i+1 < len(os.Args):
			i++
			tileW, _ = strconv.Atoi(os.Args[i])
		default:
			args = append(args, a)
		}
	}
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: hdrcollage [-strength S] [-thresholds a,b,c,d] <in.ARW> <in.JPG> <out.jpg>")
		os.Exit(2)
	}
	arw, jpgPath, outPath := args[0], args[1], args[2]

	o := raw.DefaultOpts()
	o.Linear = true
	o.Highlight = 2 // blend: max recovery range; SDR-guided filter gates its artifacts
	img, _, err := raw.Decode(arw, o)
	must(err, "decode")
	cp, err := sonylens.ReadARW(arw)
	must(err, "params")
	warped := sonylens.Warp(img, cp, sonylens.DefaultWarp())
	jpgIm, err := imaging.LoadImage(jpgPath)
	must(err, "load jpeg")
	sdrLinFull := linearize(jpgIm)
	rawAligned := warped.Resize(jpgIm.W, jpgIm.H)
	aff := register.Estimate(jpgIm, rawAligned, register.Options{})
	rawAligned = aff.Apply(rawAligned, jpgIm.W, jpgIm.H)

	tileH := tileW * jpgIm.H / jpgIm.W
	sdrT := sdrLinFull.Resize(tileW, tileH)
	rawT := rawAligned.Resize(tileW, tileH)
	dispT := jpgIm.Resize(tileW, tileH)

	// tiles: (threshold, strength) per tile
	type tile struct{ thr, str float64 }
	var tiles []tile
	if tilesSpec != "" {
		for _, tok := range strings.Split(tilesSpec, ",") {
			parts := strings.SplitN(strings.TrimSpace(tok), ":", 2)
			t, _ := strconv.ParseFloat(parts[0], 64)
			s := strength
			if len(parts) == 2 {
				s, _ = strconv.ParseFloat(parts[1], 64)
			}
			tiles = append(tiles, tile{t, s})
		}
	} else {
		for _, t := range strings.Split(thrSpec, ",") {
			v, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
			tiles = append(tiles, tile{v, strength})
		}
	}
	thrs := make([]float64, len(tiles))
	for i, t := range tiles {
		thrs[i] = t.thr
	}
	cols := 2
	rows := (len(thrs) + cols - 1) / cols
	m := 34
	lab := 52 // label strip height above each tile
	GW := cols*tileW + (cols+1)*m
	GH := rows*(tileH+lab) + (rows+1)*m

	baseDisp := imaging.New(GW, GH)
	for i, tl := range tiles {
		r, c := i/cols, i%cols
		x0 := m + c*(tileW+m)
		y0 := m + r*(tileH+lab+m) + lab
		blit(baseDisp, dispT, x0, y0)
		drawText(baseDisp, fmt.Sprintf("thr %.2f  str %.1f", tl.thr, tl.str), x0+8, y0-lab+8, 5, 1, 0.92, 0.4)
	}
	// sdr grid (linear) exactly matches the base we will JPEG-encode.
	sdrGrid := linearize(baseDisp)
	// hdr grid starts equal to sdr (so margins + labels have gain 0), tiles overwritten.
	hdrGrid := imaging.New(GW, GH)
	copy(hdrGrid.Pix, sdrGrid.Pix)
	for i, tl := range tiles {
		b := hdrbuild.DefaultOptions()
		b.Strength = tl.str
		b.Threshold = tl.thr
		hdr, _ := hdrbuild.Build(sdrT, rawT, b)
		r, c := i/cols, i%cols
		x0 := m + c*(tileW+m)
		y0 := m + r*(tileH+lab+m) + lab
		blit(hdrGrid, hdr, x0, y0)
	}

	gmOpt := gainmap.DefaultOptions()
	gmOpt.ScaleFactor = 2
	gm, meta, err := gainmap.Compute(sdrGrid, hdrGrid, gmOpt)
	must(err, "gainmap")
	base := jpegBytes(baseDisp, 92)
	uhdr, err := ultrahdr.Encode(base, gm, meta, ultrahdr.DefaultOptions())
	must(err, "encode")
	must(os.WriteFile(outPath, uhdr, 0644), "write")
	fmt.Printf("wrote %s  %dx%d  %d tiles  maxBoost=%.2f stops  %d KB\n",
		outPath, GW, GH, len(tiles), meta.MaxLog2[0], len(uhdr)/1024)
}

func must(err error, ctx string) {
	if err != nil {
		fmt.Fprintln(os.Stderr, ctx+":", err)
		os.Exit(1)
	}
}
