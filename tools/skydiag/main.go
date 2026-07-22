// Command skydiag visualizes the RGB gain map and recovery behaviour for one
// ARW+JPEG pair, to debug spatial discontinuities (e.g. a blown sky that only
// partly brightens). Writes a side-by-side PNG and prints region stats.
//
// usage: skydiag <in.ARW> <in.JPG> <out.png>
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
		fmt.Fprintln(os.Stderr, "usage: skydiag <in.ARW> <in.JPG> <out.png>")
		os.Exit(2)
	}
	arw, jpgPath, out := os.Args[1], os.Args[2], os.Args[3]

	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2
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
	rawAligned := warped.Resize(jpg.W, jpg.H)
	aff := register.Estimate(jpg, rawAligned, register.Options{})
	rawAligned = aff.Apply(rawAligned, jpg.W, jpg.H)
	fmt.Printf("model=%s orient=%d reg Sx=%.4f Sy=%.4f\n", meta.Model, cp.Orientation, aff.Sx, aff.Sy)

	hdr, k := hdrbuild.Build(sdrLin, rawAligned, hdrbuild.DefaultOptions())
	fmt.Printf("anchor k=[%.2f %.2f %.2f]\n", k[0], k[1], k[2])

	gmo := gainmap.DefaultOptions()
	gmo.MultiChannel = true
	gmo.ScaleFactor = 4
	gm, gmeta, err := gainmap.Compute(sdrLin, hdr, gmo)
	must(err)
	fmt.Printf("gainmap %dx%d ch=%d  maxLog2=[%.3f %.3f %.3f]\n",
		gm.W, gm.H, gm.Channels, gmeta.MaxLog2[0], gmeta.MaxLog2[1], gmeta.MaxLog2[2])

	// Stats: among strongly-clipped SDR pixels (luma>0.95), how do RAW luma and the
	// encoded gain behave? A bimodal gain there is what reads as "half the sky lit".
	W, H := jpg.W, jpg.H
	var nClip, nRawHead, nRawSat int
	for p := 0; p < W*H; p++ {
		i := p * 3
		ly := 0.2126*float64(sdrLin.Pix[i]) + 0.7152*float64(sdrLin.Pix[i+1]) + 0.0722*float64(sdrLin.Pix[i+2])
		if ly < 0.95 {
			continue
		}
		nClip++
		ry := (0.2126*float64(rawAligned.Pix[i])*k[0] + 0.7152*float64(rawAligned.Pix[i+1])*k[1] + 0.0722*float64(rawAligned.Pix[i+2])*k[2])
		if ry > ly*1.05 {
			nRawHead++ // RAW still has headroom -> gets recovered/boosted
		} else {
			nRawSat++ // RAW also saturated -> only the display boost
		}
	}
	fmt.Printf("clipped(luma>0.95): %d px; RAW-has-headroom: %d (%.0f%%); RAW-also-saturated: %d (%.0f%%)\n",
		nClip, nRawHead, pct(nRawHead, nClip), nRawSat, pct(nRawSat, nClip))

	// Visualization: SDR | gain map (upsampled), in display orientation.
	gmImg := gm.ToImage().Resize(W, H)
	panel := sideBySide(sdrLin, gmImg) // sdrLin is linear; encode for viewing below
	panel = orient(panel, cp.Orientation)
	// tone: sdr side is linear, so sRGB-encode the whole panel for display
	for i := range panel.Pix {
		panel.Pix[i] = color.SRGBEncode(panel.Pix[i])
	}
	// downscale for delivery
	scale := 1600.0 / float64(panel.W)
	if scale < 1 {
		panel = panel.Resize(int(float64(panel.W)*scale), int(float64(panel.H)*scale))
	}
	must(panel.SavePNG8(out))
	fmt.Println("wrote", out)
}

func sideBySide(linSDR, gm *imaging.Image) *imaging.Image {
	W, H := linSDR.W, linSDR.H
	gap := 8
	dst := imaging.New(W*2+gap, H)
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			i := (y*W + x) * 3
			o := (y*dst.W + x) * 3
			dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2] = linSDR.Pix[i], linSDR.Pix[i+1], linSDR.Pix[i+2]
			// gain map is already in [0,1] display space; put it in linear so the
			// final sRGB encode roughly preserves its look (good enough for a map).
			o2 := (y*dst.W + (x + W + gap)) * 3
			dst.Pix[o2] = color.SRGBDecode(gm.Pix[i])
			dst.Pix[o2+1] = color.SRGBDecode(gm.Pix[i+1])
			dst.Pix[o2+2] = color.SRGBDecode(gm.Pix[i+2])
		}
	}
	return dst
}

// orient rotates a stored-space image to display orientation (EXIF 6/8 only).
func orient(im *imaging.Image, o int) *imaging.Image {
	switch o {
	case 6: // rotate 90 CW
		dst := imaging.New(im.H, im.W)
		for y := 0; y < im.H; y++ {
			for x := 0; x < im.W; x++ {
				r, g, b := im.At(x, y)
				dst.Set(im.H-1-y, x, r, g, b)
			}
		}
		return dst
	case 8: // rotate 90 CCW
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

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
