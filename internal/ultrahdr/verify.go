package ultrahdr

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Verify checks the structural validity of an Ultra HDR file: a valid primary JPEG carrying a
// GContainer XMP and an MPF index whose second image points at an appended gain-map JPEG with
// hdrgm metadata. Returns nil if the file looks like a well-formed Ultra HDR.
func Verify(data []byte) error {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return fmt.Errorf("not a JPEG")
	}
	// GContainer XMP with GainMap item on the primary
	if !bytes.Contains(data, []byte("http://ns.google.com/photos/1.0/container/")) {
		return fmt.Errorf("missing GContainer XMP")
	}
	if !bytes.Contains(data, []byte(`Item:Semantic="GainMap"`)) {
		return fmt.Errorf("missing GainMap container item")
	}
	// MPF index
	mid := bytes.Index(data, []byte("MPF\x00"))
	if mid < 0 {
		return fmt.Errorf("missing MPF APP2 segment")
	}
	endian := mid + 4
	if endian+60 > len(data) {
		return fmt.Errorf("truncated MPF")
	}
	// MP entries at endian+50 (writer layout); image 2 size/offset
	size1 := binary.BigEndian.Uint32(data[endian+50+16+4:])
	off1 := binary.BigEndian.Uint32(data[endian+50+16+8:])
	gmAbs := endian + int(off1)
	if gmAbs+2 > len(data) || data[gmAbs] != 0xFF || data[gmAbs+1] != 0xD8 {
		return fmt.Errorf("MPF image-2 offset %d does not point at a JPEG SOI", gmAbs)
	}
	if gmAbs+int(size1) != len(data) {
		return fmt.Errorf("MPF image-2 size mismatch: %d+%d != %d", gmAbs, size1, len(data))
	}
	// gain map must carry hdrgm metadata
	gm := data[gmAbs:]
	for _, want := range []string{"hdrgm:Version", "GainMapMax", "HDRCapacityMax"} {
		if !bytes.Contains(gm, []byte(want)) {
			return fmt.Errorf("gain map missing %s metadata", want)
		}
	}
	return nil
}
