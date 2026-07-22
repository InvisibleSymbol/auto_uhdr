package ultrahdr

import (
	"bytes"
	"testing"

	"github.com/invis/arw2uhdr/internal/gainmap"
)

func TestExtractGainMap(t *testing.T) {
	base := tinyJPEG(t, 64, 48)
	gm := &gainmap.GainMap{W: 16, H: 12, Channels: 1, Pix: make([]uint8, 16*12)}
	for i := range gm.Pix {
		gm.Pix[i] = uint8(i % 256)
	}
	meta := gainmap.Metadata{
		MaxLog2: [3]float64{1.5, 1.5, 1.5}, Gamma: [3]float64{1, 1, 1},
		OffsetSDR: [3]float64{0.015625, 0.015625, 0.015625},
		OffsetHDR: [3]float64{0.015625, 0.015625, 0.015625}, CapacityMax: 1.5,
	}
	uhdr, err := Encode(base, gm, meta, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}

	got, err := ExtractGainMap(uhdr)
	if err != nil {
		t.Fatalf("ExtractGainMap: %v", err)
	}
	if len(got) < 2 || got[0] != 0xFF || got[1] != 0xD8 {
		t.Fatalf("extracted gain map is not a JPEG (starts %x)", got[:min(2, len(got))])
	}
	if !bytes.Contains(got, []byte("hdrgm:Version")) {
		t.Error("extracted gain map missing hdrgm metadata")
	}
	// The extracted bytes must be exactly the tail the container appended.
	if !bytes.HasSuffix(uhdr, got) {
		t.Error("extracted gain map is not the container's appended image")
	}

	// A plain JPEG (no MPF) should be rejected, not misparsed.
	if _, err := ExtractGainMap(base); err == nil {
		t.Error("expected error extracting from a non-Ultra-HDR JPEG")
	}
}
