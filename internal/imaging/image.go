// Package imaging provides a small float32 RGB image type plus load/save and
// bilinear sampling, used by the geometry (warp) and HDR stages.
package imaging

import (
	"image"
	_ "image/jpeg" // register JPEG decoder for LoadImage
	"image/png"
	"os"
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

func (im *Image) idx(x, y int) int { return (y*im.W + x) * 3 }

// Set writes the RGB triple at (x,y). Out-of-range coordinates are ignored.
func (im *Image) Set(x, y int, r, g, b float32) {
	if x < 0 || y < 0 || x >= im.W || y >= im.H {
		return
	}
	i := im.idx(x, y)
	im.Pix[i], im.Pix[i+1], im.Pix[i+2] = r, g, b
}

// At returns the RGB triple at (x,y) (clamped to the border).
func (im *Image) At(x, y int) (r, g, b float32) {
	if x < 0 {
		x = 0
	} else if x >= im.W {
		x = im.W - 1
	}
	if y < 0 {
		y = 0
	} else if y >= im.H {
		y = im.H - 1
	}
	i := im.idx(x, y)
	return im.Pix[i], im.Pix[i+1], im.Pix[i+2]
}

// SampleBilinear samples with border clamping. fx,fy in pixel coordinates.
func (im *Image) SampleBilinear(fx, fy float64) (r, g, b float32) {
	if fx < 0 {
		fx = 0
	} else if fx > float64(im.W-1) {
		fx = float64(im.W - 1)
	}
	if fy < 0 {
		fy = 0
	} else if fy > float64(im.H-1) {
		fy = float64(im.H - 1)
	}
	x0, y0 := int(fx), int(fy)
	x1, y1 := x0+1, y0+1
	if x1 >= im.W {
		x1 = im.W - 1
	}
	if y1 >= im.H {
		y1 = im.H - 1
	}
	tx := float32(fx - float64(x0))
	ty := float32(fy - float64(y0))
	r00, g00, b00 := im.At(x0, y0)
	r10, g10, b10 := im.At(x1, y0)
	r01, g01, b01 := im.At(x0, y1)
	r11, g11, b11 := im.At(x1, y1)
	lerp := func(a, b, t float32) float32 { return a + (b-a)*t }
	r = lerp(lerp(r00, r10, tx), lerp(r01, r11, tx), ty)
	g = lerp(lerp(g00, g10, tx), lerp(g01, g11, tx), ty)
	b = lerp(lerp(b00, b10, tx), lerp(b01, b11, tx), ty)
	return
}

// BoxBlurF applies an iterated separable box blur to a float plane (≈ Gaussian). Edges clamp.
func BoxBlurF(src []float64, w, h, radius, iter int) []float64 {
	if radius < 1 || iter < 1 {
		out := make([]float64, len(src))
		copy(out, src)
		return out
	}
	clamp := func(v, hi int) int {
		if v < 0 {
			return 0
		}
		if v > hi {
			return hi
		}
		return v
	}
	buf := make([]float64, w*h)
	copy(buf, src)
	tmp := make([]float64, w*h)
	win := float64(2*radius + 1)
	for it := 0; it < iter; it++ {
		// horizontal
		for y := 0; y < h; y++ {
			row := y * w
			var sum float64
			for x := -radius; x <= radius; x++ {
				sum += buf[row+clamp(x, w-1)]
			}
			for x := 0; x < w; x++ {
				tmp[row+x] = sum / win
				sum += buf[row+clamp(x+radius+1, w-1)] - buf[row+clamp(x-radius, w-1)]
			}
		}
		// vertical
		for x := 0; x < w; x++ {
			var sum float64
			for y := -radius; y <= radius; y++ {
				sum += tmp[clamp(y, h-1)*w+x]
			}
			for y := 0; y < h; y++ {
				buf[y*w+x] = sum / win
				sum += tmp[clamp(y+radius+1, h-1)*w+x] - tmp[clamp(y-radius, h-1)*w+x]
			}
		}
	}
	return buf
}

// Resize returns a bilinearly-resampled copy at (nw,nh).
func (im *Image) Resize(nw, nh int) *Image {
	dst := New(nw, nh)
	sx := float64(im.W) / float64(nw)
	sy := float64(im.H) / float64(nh)
	ParallelRows(nh, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			fy := (float64(y)+0.5)*sy - 0.5
			for x := 0; x < nw; x++ {
				fx := (float64(x)+0.5)*sx - 0.5
				r, g, b := im.SampleBilinear(fx, fy)
				dst.Set(x, y, r, g, b)
			}
		}
	})
	return dst
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
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
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
	clamp := func(v float32) uint8 {
		if v <= 0 {
			return 0
		}
		if v >= 1 {
			return 255
		}
		return uint8(v*255 + 0.5)
	}
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := im.idx(x, y)
			o := (y*im.W + x) * 4
			out.Pix[o] = clamp(im.Pix[i])
			out.Pix[o+1] = clamp(im.Pix[i+1])
			out.Pix[o+2] = clamp(im.Pix[i+2])
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
