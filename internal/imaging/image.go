// Package imaging provides a small float32 RGB image type plus load/save,
// bilinear sampling, and a separable box blur, used by the geometry (warp),
// registration, and HDR stages.
package imaging

import (
	"image"
	_ "image/jpeg" // register the JPEG decoder for LoadImage
	"image/png"
	"os"

	"github.com/invis/arw2uhdr/internal/xmath"
)

// Image is an interleaved RGB float32 buffer. Values are conventionally in [0,1]
// but may exceed 1.0 for the extended-range HDR stage.
type Image struct {
	W, H int
	Pix  []float32 // len == W*H*3, row-major, RGB
}

// New allocates a zeroed W×H image.
func New(w, h int) *Image {
	return &Image{W: w, H: h, Pix: make([]float32, w*h*3)}
}

// Clone returns a deep copy of im.
func (im *Image) Clone() *Image {
	cp := New(im.W, im.H)
	copy(cp.Pix, im.Pix)
	return cp
}

func (im *Image) idx(x, y int) int { return (y*im.W + x) * 3 }

// Set writes the RGB triple at (x,y). Out-of-range coordinates are ignored.
func (im *Image) Set(x, y int, r, g, b float32) {
	if x < 0 || y < 0 || x >= im.W || y >= im.H {
		return
	}
	i := im.idx(x, y)
	im.Pix[i], im.Pix[i+1], im.Pix[i+2] = r, g, b
}

// At returns the RGB triple at (x,y), clamping the coordinates to the border.
func (im *Image) At(x, y int) (r, g, b float32) {
	x = xmath.Clamp(x, 0, im.W-1)
	y = xmath.Clamp(y, 0, im.H-1)
	i := im.idx(x, y)
	return im.Pix[i], im.Pix[i+1], im.Pix[i+2]
}

// SampleBilinear samples with border clamping. fx,fy are in pixel coordinates.
func (im *Image) SampleBilinear(fx, fy float64) (r, g, b float32) {
	fx = xmath.Clamp(fx, 0, float64(im.W-1))
	fy = xmath.Clamp(fy, 0, float64(im.H-1))
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, im.W-1), min(y0+1, im.H-1)
	tx := float32(fx - float64(x0))
	ty := float32(fy - float64(y0))
	r00, g00, b00 := im.At(x0, y0)
	r10, g10, b10 := im.At(x1, y0)
	r01, g01, b01 := im.At(x0, y1)
	r11, g11, b11 := im.At(x1, y1)
	r = xmath.Lerp(xmath.Lerp(r00, r10, tx), xmath.Lerp(r01, r11, tx), ty)
	g = xmath.Lerp(xmath.Lerp(g00, g10, tx), xmath.Lerp(g01, g11, tx), ty)
	b = xmath.Lerp(xmath.Lerp(b00, b10, tx), xmath.Lerp(b01, b11, tx), ty)
	return
}

// Resize returns a bilinearly-resampled copy at (nw,nh).
func (im *Image) Resize(nw, nh int) *Image {
	dst := New(nw, nh)
	sx := float64(im.W) / float64(nw)
	sy := float64(im.H) / float64(nh)
	ParallelRows(nh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			fy := (float64(y)+0.5)*sy - 0.5
			for x := range nw {
				fx := (float64(x)+0.5)*sx - 0.5
				r, g, b := im.SampleBilinear(fx, fy)
				dst.Set(x, y, r, g, b)
			}
		}
	})
	return dst
}

// BoxBlurF applies an iterated separable box blur to a single-channel float
// plane (an iter-fold box ≈ Gaussian). Edges clamp. The input is not modified;
// a new slice is returned. Both passes run across cores.
func BoxBlurF(src []float64, w, h, radius, iter int) []float64 {
	buf := make([]float64, len(src))
	copy(buf, src)
	if radius < 1 || iter < 1 {
		return buf
	}
	tmp := make([]float64, w*h)
	win := float64(2*radius + 1)
	for range iter {
		// horizontal pass: rows are independent
		ParallelRows(h, func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				row := y * w
				var sum float64
				for x := -radius; x <= radius; x++ {
					sum += buf[row+xmath.Clamp(x, 0, w-1)]
				}
				for x := range w {
					tmp[row+x] = sum / win
					sum += buf[row+xmath.Clamp(x+radius+1, 0, w-1)] - buf[row+xmath.Clamp(x-radius, 0, w-1)]
				}
			}
		})
		// vertical pass: columns are independent
		ParallelBands(w, func(x0, x1 int) {
			for x := x0; x < x1; x++ {
				var sum float64
				for y := -radius; y <= radius; y++ {
					sum += tmp[xmath.Clamp(y, 0, h-1)*w+x]
				}
				for y := range h {
					buf[y*w+x] = sum / win
					sum += tmp[xmath.Clamp(y+radius+1, 0, h-1)*w+x] - tmp[xmath.Clamp(y-radius, 0, h-1)*w+x]
				}
			}
		})
	}
	return buf
}

// LoadImage decodes any Go-supported image (PNG/JPEG) into a float32 RGB Image,
// reading 8- or 16-bit sources via the image.Image interface (normalized to [0,1]).
func LoadImage(path string) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	im := New(w, h)
	const inv = 1.0 / 65535.0
	for y := range h {
		for x := range w {
			r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA() // 16-bit, alpha-premultiplied
			i := im.idx(x, y)
			im.Pix[i] = float32(float64(r) * inv)
			im.Pix[i+1] = float32(float64(g) * inv)
			im.Pix[i+2] = float32(float64(bl) * inv)
		}
	}
	return im, nil
}

// SavePNG8 writes an 8-bit PNG (values clamped to [0,1]).
func (im *Image) SavePNG8(path string) error {
	out := image.NewRGBA(image.Rect(0, 0, im.W, im.H))
	to8 := func(v float32) uint8 { return uint8(xmath.Clamp01(v)*255 + 0.5) }
	for y := range im.H {
		for x := range im.W {
			i := im.idx(x, y)
			o := (y*im.W + x) * 4
			out.Pix[o] = to8(im.Pix[i])
			out.Pix[o+1] = to8(im.Pix[i+1])
			out.Pix[o+2] = to8(im.Pix[i+2])
			out.Pix[o+3] = 255
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}
