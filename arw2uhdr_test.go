package arw2uhdr_test

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/invis/arw2uhdr"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// fakeDecoder returns a synthetic scene-linear image, standing in for LibRaw so the
// orchestration can be tested without a real ARW / cgo.
type fakeDecoder struct{ w, h int }

func (d fakeDecoder) Decode(_ context.Context, _ string) (*arw2uhdr.Image, *arw2uhdr.RawMeta, error) {
	im := &arw2uhdr.Image{W: d.w, H: d.h, Pix: make([]float32, d.w*d.h*3)}
	for i := range im.Pix {
		im.Pix[i] = 0.6
	}
	return im, &arw2uhdr.RawMeta{Model: "FAKE", Width: d.w, Height: d.h}, nil
}

// identityCorrector passes the image through untouched (no ARW read).
type identityCorrector struct{}

func (identityCorrector) Correct(_ context.Context, img *arw2uhdr.Image, _ string) (*arw2uhdr.Image, error) {
	return img, nil
}

// failingDecoder always errors, to exercise stage-error wrapping.
type failingDecoder struct{}

func (failingDecoder) Decode(context.Context, string) (*arw2uhdr.Image, *arw2uhdr.RawMeta, error) {
	return nil, nil, errors.New("boom")
}

func writeTinyJPEG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x * 8 % 256), uint8(y * 8 % 256), 200, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatal(err)
	}
}

func TestConverterWithFakeStagesProducesValidUltraHDR(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "in.jpg")
	out := filepath.Join(dir, "out.jpg")
	writeTinyJPEG(t, jpg, 64, 48)

	opts := arw2uhdr.DefaultOptions()
	opts.Register = false // skip registration on the synthetic pair
	conv := arw2uhdr.New(opts)
	conv.Decoder = fakeDecoder{w: 40, h: 30} // resized to the JPEG grid internally
	conv.Corrector = identityCorrector{}     // no ARW metadata needed

	res, err := conv.Convert(context.Background(), arw2uhdr.Input{ARW: "unused.arw", JPEG: jpg, Output: out})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if res.Width != 64 || res.Height != 48 {
		t.Errorf("result dims %dx%d, want 64x48", res.Width, res.Height)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := ultrahdr.Verify(data); err != nil {
		t.Errorf("output is not a valid Ultra HDR: %v", err)
	}
}

func TestConvertWrapsStageError(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "in.jpg")
	writeTinyJPEG(t, jpg, 16, 16)

	conv := arw2uhdr.New(arw2uhdr.DefaultOptions())
	conv.Decoder = failingDecoder{}
	_, err := conv.Convert(context.Background(), arw2uhdr.Input{ARW: "x.arw", JPEG: jpg, Output: filepath.Join(dir, "o.jpg")})

	var se *arw2uhdr.StageError
	if !errors.As(err, &se) {
		t.Fatalf("expected a StageError, got %T (%v)", err, err)
	}
	if se.Stage != arw2uhdr.StageDecode {
		t.Errorf("stage = %q, want decode", se.Stage)
	}
}

func TestConvertHonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "in.jpg")
	writeTinyJPEG(t, jpg, 16, 16)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	conv := arw2uhdr.New(arw2uhdr.DefaultOptions())
	conv.Decoder = fakeDecoder{w: 16, h: 16}
	conv.Corrector = identityCorrector{}
	_, err := conv.Convert(ctx, arw2uhdr.Input{ARW: "x.arw", JPEG: jpg, Output: filepath.Join(dir, "o.jpg")})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
