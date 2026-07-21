package ultrahdr

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/invis/arw2uhdr/internal/gainmap"
)

func tinyJPEG(t *testing.T, w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEncodeStructure(t *testing.T) {
	base := tinyJPEG(t, 64, 48)
	gm := &gainmap.GainMap{W: 16, H: 12, Channels: 1, Pix: make([]uint8, 16*12)}
	for i := range gm.Pix {
		gm.Pix[i] = uint8(i % 256)
	}
	meta := gainmap.Metadata{
		MinLog2: [3]float64{0, 0, 0}, MaxLog2: [3]float64{1.5, 1.5, 1.5},
		Gamma: [3]float64{1, 1, 1}, OffsetSDR: [3]float64{0.015625, 0.015625, 0.015625},
		OffsetHDR: [3]float64{0.015625, 0.015625, 0.015625}, CapacityMax: 1.5,
	}
	out, err := Encode(base, gm, meta, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if out[0] != 0xFF || out[1] != 0xD8 {
		t.Fatal("output not a JPEG")
	}
	// must contain our GContainer XMP and MPF, and the hdrgm metadata.
	for _, want := range [][]byte{
		[]byte("http://ns.google.com/photos/1.0/container/"),
		[]byte("hdrgm:Version"),
		[]byte("MPF\x00"),
		[]byte("GainMapMax"),
	} {
		if !bytes.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	// Parse the MPF and assert image-2 offset points at the appended gain map's SOI.
	mid := bytes.Index(out, []byte("MPF\x00"))
	endian := mid + 4
	// MP entries live at endian+50 (see buildMPF layout); image1 offset field at +50+16+8.
	off1 := binary.BigEndian.Uint32(out[endian+50+16+8:])
	size1 := binary.BigEndian.Uint32(out[endian+50+16+4:])
	gmAbs := endian + int(off1)
	if gmAbs >= len(out) || out[gmAbs] != 0xFF || out[gmAbs+1] != 0xD8 {
		t.Fatalf("MPF image-2 offset %d does not point at a SOI", gmAbs)
	}
	if gmAbs+int(size1) != len(out) {
		t.Errorf("MPF image-2 size %d + offset != file end (%d vs %d)", size1, gmAbs+int(size1), len(out))
	}
	// image-1 size must equal the primary length (offset of the gain map).
	size0 := binary.BigEndian.Uint32(out[endian+50+4:])
	if int(size0) != gmAbs {
		t.Errorf("MPF image-1 size %d != primary length %d", size0, gmAbs)
	}
}
