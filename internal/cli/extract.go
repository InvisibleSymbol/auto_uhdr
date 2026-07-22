package cli

import (
	"bytes"
	"encoding/binary"
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
	// The gain map is stored in the base's sensor orientation; the base's EXIF
	// Orientation is what viewers apply, so bake it in to keep the output upright.
	orient := exifOrientation(data)
	note := ""
	if orient != 1 {
		note = fmt.Sprintf(" (rotated to EXIF orientation %d)", orient)
	}

	dst := *out
	if !*lift {
		if dst == "" {
			dst = gainmapPath(in, ".jpg")
		}
		outBytes := gm // byte-exact when there is no rotation to apply
		if orient != 1 {
			src, derr := jpeg.Decode(bytes.NewReader(gm))
			if derr != nil {
				return inputErr("%s: decode gain map: %v", in, derr)
			}
			var buf bytes.Buffer
			if err := jpeg.Encode(&buf, orientImage(src, orient), &jpeg.Options{Quality: 95}); err != nil {
				return &ExitError{Code: ExitWrite, Message: err.Error()}
			}
			outBytes = buf.Bytes()
		}
		if err := os.WriteFile(dst, outBytes, 0o644); err != nil {
			return &ExitError{Code: ExitWrite, Message: err.Error()}
		}
		fmt.Println("wrote", dst+note)
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
	if err := png.Encode(f, orientImage(lifted, orient)); err != nil {
		return &ExitError{Code: ExitWrite, Message: err.Error()}
	}
	liftMsg := "(brightened for visibility)"
	if orient != 1 {
		liftMsg = fmt.Sprintf("(brightened for visibility, rotated to EXIF orientation %d)", orient)
	}
	fmt.Println("wrote", dst, liftMsg)
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

// exifOrientation reads the EXIF Orientation (1..8) from a JPEG's APP1 segment,
// walking the marker segments up to the start of scan. Returns 1 (normal) if absent.
func exifOrientation(data []byte) int {
	for i := 2; i+4 <= len(data) && data[i] == 0xFF; {
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 { // SOS / EOI: header segments are done
			break
		}
		segLen := int(data[i+2])<<8 | int(data[i+3])
		if segLen < 2 || i+2+segLen > len(data) {
			break
		}
		seg := data[i+4 : i+2+segLen]
		if marker == 0xE1 && len(seg) >= 6 && string(seg[:6]) == "Exif\x00\x00" {
			if o, ok := tiffOrientation(seg[6:]); ok {
				return o
			}
		}
		i += 2 + segLen
	}
	return 1
}

// tiffOrientation parses the Orientation tag (0x0112) from an EXIF TIFF block.
func tiffOrientation(tiff []byte) (int, bool) {
	if len(tiff) < 8 {
		return 0, false
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 0, false
	}
	ifd := int(bo.Uint32(tiff[4:8]))
	if ifd+2 > len(tiff) {
		return 0, false
	}
	n := int(bo.Uint16(tiff[ifd:]))
	for e := ifd + 2; e+12 <= len(tiff) && n > 0; e, n = e+12, n-1 {
		if bo.Uint16(tiff[e:]) == 0x0112 { // Orientation, type SHORT: value in-place
			if v := int(bo.Uint16(tiff[e+8:])); v >= 1 && v <= 8 {
				return v, true
			}
			return 0, false
		}
	}
	return 0, false
}

// orientImage applies an EXIF orientation (1..8) to src, returning an upright image.
func orientImage(src image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 { // 5..8 transpose the axes
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := range h {
		for x := range w {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}
