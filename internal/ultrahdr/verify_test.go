package ultrahdr

import (
	"testing"

	"github.com/invis/arw2uhdr/internal/gainmap"
)

func sampleUltraHDR(t *testing.T) []byte {
	t.Helper()
	base := tinyJPEG(t, 48, 32)
	gm := &gainmap.GainMap{W: 12, H: 8, Channels: 1, Pix: make([]uint8, 12*8)}
	for i := range gm.Pix {
		gm.Pix[i] = uint8(i * 3)
	}
	meta := gainmap.Metadata{
		MaxLog2: [3]float64{1.5, 1.5, 1.5}, Gamma: [3]float64{1, 1, 1},
		OffsetSDR: [3]float64{0.015625, 0.015625, 0.015625},
		OffsetHDR: [3]float64{0.015625, 0.015625, 0.015625}, CapacityMax: 1.5,
	}
	out, err := Encode(base, gm, meta, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestVerifyAcceptsEncoded(t *testing.T) {
	if err := Verify(sampleUltraHDR(t)); err != nil {
		t.Errorf("Verify rejected a freshly encoded Ultra HDR: %v", err)
	}
}

func TestVerifyRejectsPlainJPEG(t *testing.T) {
	if err := Verify(tinyJPEG(t, 16, 16)); err == nil {
		t.Error("Verify accepted a plain JPEG with no gain map")
	}
}

func TestVerifyRejectsGarbage(t *testing.T) {
	if err := Verify([]byte{0x00, 0x01, 0x02}); err == nil {
		t.Error("Verify accepted non-JPEG bytes")
	}
}

// Re-encoding an already-Ultra-HDR file must drop the old XMP/MPF and still verify
// (exercises the marker walker's existing-segment removal).
func TestReEncodeIsIdempotentlyValid(t *testing.T) {
	once := sampleUltraHDR(t)
	gm := &gainmap.GainMap{W: 8, H: 8, Channels: 1, Pix: make([]uint8, 64)}
	meta := gainmap.Metadata{MaxLog2: [3]float64{1, 1, 1}, Gamma: [3]float64{1, 1, 1}, CapacityMax: 1}
	twice, err := Encode(once, gm, meta, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(twice); err != nil {
		t.Errorf("re-encoded file failed verify: %v", err)
	}
}
