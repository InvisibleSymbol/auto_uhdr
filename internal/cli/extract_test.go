package cli

import (
	"image"
	"image/color"
	"testing"
)

func TestTiffOrientation(t *testing.T) {
	// Little-endian TIFF, IFD at offset 8, one entry: Orientation(0x0112) SHORT = 6.
	tiff := []byte{
		'I', 'I', 0x2A, 0x00, 0x08, 0, 0, 0,
		0x01, 0x00, // 1 entry
		0x12, 0x01, 0x03, 0x00, 0x01, 0, 0, 0, 0x06, 0x00, 0, 0,
		0, 0, 0, 0, // next IFD
	}
	if o, ok := tiffOrientation(tiff); !ok || o != 6 {
		t.Errorf("tiffOrientation = %d,%v; want 6,true", o, ok)
	}
	if _, ok := tiffOrientation([]byte("not tiff")); ok {
		t.Error("expected failure on non-TIFF")
	}
}

func TestOrientImage(t *testing.T) {
	// 2x1: left red, right blue.
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	src.Set(1, 0, color.NRGBA{0, 0, 255, 255})

	if got := orientImage(src, 1); got != image.Image(src) {
		t.Error("orientation 1 should be identity")
	}

	// Orientation 6 = rotate 90° CW: dims swap to 1x2, left→top, right→bottom.
	out := orientImage(src, 6)
	if out.Bounds().Dx() != 1 || out.Bounds().Dy() != 2 {
		t.Fatalf("dims after o=6: %v, want 1x2", out.Bounds())
	}
	top := color.NRGBAModel.Convert(out.At(0, 0)).(color.NRGBA)
	bot := color.NRGBAModel.Convert(out.At(0, 1)).(color.NRGBA)
	if top.R != 255 || bot.B != 255 {
		t.Errorf("o=6 mapping wrong: top=%v bot=%v", top, bot)
	}
}
