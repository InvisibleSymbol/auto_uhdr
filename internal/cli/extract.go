package cli

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// RunExtractGainMap implements `arw2uhdr extract-gainmap`: it pulls the appended
// gain-map JPEG out of an Ultra HDR file. With -lift it decodes and gamma-brightens
// the map (whose values sit near zero and would otherwise render nearly black) and
// writes a PNG for viewing.
func RunExtractGainMap(args []string) error {
	fs := flag.NewFlagSet("extract-gainmap", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	out := fs.String("o", "", "output path (default: <input>_gainmap.jpg, or .png with -lift)")
	lift := fs.Bool("lift", false, "gamma-brighten for visibility and write a PNG (not colorimetric)")
	if err := fs.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage}
	}
	if fs.NArg() < 1 {
		return usageErr("extract-gainmap: missing <file.jpg>")
	}
	in := fs.Arg(0)

	data, err := os.ReadFile(in)
	if err != nil {
		return inputErr("cannot read %s", in)
	}
	gm, err := ultrahdr.ExtractGainMap(data)
	if err != nil {
		return inputErr("%s: %v", in, err)
	}

	dst := *out
	if !*lift {
		if dst == "" {
			dst = gainmapPath(in, ".jpg")
		}
		if err := os.WriteFile(dst, gm, 0o644); err != nil {
			return &ExitError{Code: ExitWrite, Message: err.Error()}
		}
		fmt.Println("wrote", dst)
		return nil
	}

	if dst == "" {
		dst = gainmapPath(in, ".png")
	}
	lifted, err := liftGainMap(gm)
	if err != nil {
		return &ExitError{Code: ExitInput, Message: fmt.Sprintf("%s: %v", in, err)}
	}
	f, err := os.Create(dst)
	if err != nil {
		return &ExitError{Code: ExitWrite, Message: err.Error()}
	}
	defer f.Close()
	if err := png.Encode(f, lifted); err != nil {
		return &ExitError{Code: ExitWrite, Message: err.Error()}
	}
	fmt.Println("wrote", dst, "(brightened for visibility)")
	return nil
}

// liftGainMap decodes the gain-map JPEG and applies a fixed brightening gamma so the
// near-zero gains become visible. The result is illustrative, not colorimetric.
func liftGainMap(jpegBytes []byte) (image.Image, error) {
	src, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return nil, fmt.Errorf("decode gain map: %w", err)
	}
	const gamma = 0.35
	var lut [256]uint8
	for i := range lut {
		lut[i] = uint8(math.Round(255 * math.Pow(float64(i)/255, gamma)))
	}
	b := src.Bounds()
	dst := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := src.At(x, y).RGBA() // 16-bit per channel
			i := (y-b.Min.Y)*dst.Stride + (x-b.Min.X)*4
			dst.Pix[i] = lut[r>>8]
			dst.Pix[i+1] = lut[g>>8]
			dst.Pix[i+2] = lut[bl>>8]
			dst.Pix[i+3] = 255
		}
	}
	return dst, nil
}

func gainmapPath(in, ext string) string {
	return strings.TrimSuffix(in, filepath.Ext(in)) + "_gainmap" + ext
}
