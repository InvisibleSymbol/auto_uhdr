// Package ultrahdr assembles an Ultra HDR (JPEG_R) file: a base SDR JPEG with a GContainer +
// hdrgm XMP and an MPF index, followed by an appended gain-map JPEG carrying its hdrgm metadata.
//
// This is a pure-Go implementation of the Android Ultra HDR v1.1 / Adobe hdrgm (XMP) variant.
// The package boundary matches a future libultrahdr cgo backend: Encode(baseJPEG, gm, meta, opts).
package ultrahdr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/jpeg"

	"github.com/invis/arw2uhdr/internal/gainmap"
)

// Options controls encoding.
type Options struct {
	GainMapQuality int // JPEG quality for the gain map (default 90)
}

// DefaultOptions returns spec-reasonable defaults.
func DefaultOptions() Options { return Options{GainMapQuality: 90} }

// Encode produces the Ultra HDR file bytes from a base SDR JPEG and a computed gain map.
func Encode(baseJPEG []byte, gm *gainmap.GainMap, meta gainmap.Metadata, o Options) ([]byte, error) {
	if o.GainMapQuality <= 0 {
		o.GainMapQuality = 90
	}
	if len(baseJPEG) < 2 || baseJPEG[0] != 0xFF || baseJPEG[1] != 0xD8 {
		return nil, fmt.Errorf("base is not a JPEG")
	}

	// 1) encode the gain map as a JPEG and splice in its hdrgm XMP.
	gmRaw, err := encodeGainMapJPEG(gm, o.GainMapQuality)
	if err != nil {
		return nil, fmt.Errorf("gainmap jpeg: %w", err)
	}
	gmXMP := app1XMP(gainmapHdrgmXMP(meta))
	gmFinal, _, err := insertSegments(gmRaw, [][]byte{gmXMP})
	if err != nil {
		return nil, fmt.Errorf("gainmap splice: %w", err)
	}

	// 2) build the primary's GContainer XMP (needs the final gain-map length) and MPF index.
	primXMP := app1XMP(primaryGContainerXMP(len(gmFinal)))
	mpf, f := buildMPF()

	// 3) splice both into the base JPEG; remember where the MPF landed.
	primary, insAbs, err := insertSegments(baseJPEG, [][]byte{primXMP, mpf})
	if err != nil {
		return nil, fmt.Errorf("base splice: %w", err)
	}
	mpfAbs := insAbs[1]

	// 4) patch the MPF offsets/sizes now that lengths are known.
	primaryLen := len(primary)
	endianAbs := mpfAbs + f.endianOff
	putU32BE(primary, mpfAbs+f.size0Off, uint32(primaryLen))
	putU32BE(primary, mpfAbs+f.off0Off, 0)
	putU32BE(primary, mpfAbs+f.size1Off, uint32(len(gmFinal)))
	putU32BE(primary, mpfAbs+f.off1Off, uint32(primaryLen-endianAbs))

	// 5) concatenate.
	out := make([]byte, 0, primaryLen+len(gmFinal))
	out = append(out, primary...)
	out = append(out, gmFinal...)
	return out, nil
}

func encodeGainMapJPEG(gm *gainmap.GainMap, quality int) ([]byte, error) {
	var img image.Image
	if gm.Channels == 1 {
		gr := image.NewGray(image.Rect(0, 0, gm.W, gm.H))
		copy(gr.Pix, gm.Pix)
		img = gr
	} else {
		rgba := image.NewRGBA(image.Rect(0, 0, gm.W, gm.H))
		for p := 0; p < gm.W*gm.H; p++ {
			rgba.Pix[p*4] = gm.Pix[p*3]
			rgba.Pix[p*4+1] = gm.Pix[p*3+1]
			rgba.Pix[p*4+2] = gm.Pix[p*3+2]
			rgba.Pix[p*4+3] = 255
		}
		img = rgba
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// insertSegments returns a copy of base with the given marker segments inserted after the leading
// APPn block (before the first non-APPn marker), dropping any existing XMP (APP1) or MPF (APP2)
// segments. It also returns the absolute start offset of each inserted segment.
func insertSegments(base []byte, segs [][]byte) ([]byte, []int, error) {
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		return nil, nil, fmt.Errorf("not a JPEG")
	}
	out := make([]byte, 0, len(base)+segLen(segs))
	out = append(out, 0xFF, 0xD8)
	abs := make([]int, 0, len(segs))
	inserted := false
	doInsert := func() {
		for _, s := range segs {
			abs = append(abs, len(out))
			out = append(out, s...)
		}
		inserted = true
	}

	i := 2
	for i+1 < len(base) {
		if base[i] != 0xFF {
			break // unexpected; copy remainder below
		}
		marker := base[i+1]
		// Standalone markers without length.
		if marker == 0xD9 { // EOI
			break
		}
		isAPPn := marker >= 0xE0 && marker <= 0xEF
		if !isAPPn && !inserted {
			doInsert() // first non-APPn (DQT/DHT/SOF/SOS) => insert before it
		}
		if marker == 0xDA { // SOS: copy everything from here verbatim
			out = append(out, base[i:]...)
			return out, abs, nil
		}
		if i+4 > len(base) {
			break
		}
		segLength := int(base[i+2])<<8 | int(base[i+3])
		end := i + 2 + segLength
		if end > len(base) {
			return nil, nil, fmt.Errorf("truncated segment at %d", i)
		}
		seg := base[i:end]
		if isAPPn && (isXMPSeg(seg, marker) || isMPFSeg(seg, marker)) {
			// drop existing XMP/MPF
		} else {
			out = append(out, seg...)
		}
		i = end
	}
	if !inserted {
		doInsert()
	}
	out = append(out, base[i:]...)
	return out, abs, nil
}

func isXMPSeg(seg []byte, marker byte) bool {
	return marker == 0xE1 && bytes.Contains(seg, []byte("http://ns.adobe.com/xap/1.0/"))
}
func isMPFSeg(seg []byte, marker byte) bool {
	return marker == 0xE2 && len(seg) >= 8 && bytes.Equal(seg[4:8], []byte("MPF\x00"))
}

func segLen(segs [][]byte) (n int) {
	for _, s := range segs {
		n += len(s)
	}
	return
}

func putU32BE(b []byte, off int, v uint32) {
	binary.BigEndian.PutUint32(b[off:off+4], v)
}
