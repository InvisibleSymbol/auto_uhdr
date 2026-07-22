package ultrahdr

import (
	"bytes"
	"errors"
	"fmt"
)

// Verify checks the structural validity of an Ultra HDR file: a valid primary JPEG carrying a
// GContainer XMP and an MPF index whose second image points at an appended gain-map JPEG with
// hdrgm metadata. Returns nil if the file looks like a well-formed Ultra HDR.
func Verify(data []byte) error {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return errors.New("not a JPEG")
	}
	// GContainer XMP with GainMap item on the primary
	if !bytes.Contains(data, []byte("http://ns.google.com/photos/1.0/container/")) {
		return errors.New("missing GContainer XMP")
	}
	if !bytes.Contains(data, []byte(`Item:Semantic="GainMap"`)) {
		return errors.New("missing GainMap container item")
	}
	// MPF index → appended gain-map JPEG bounds
	gmAbs, size1, err := locateGainMap(data)
	if err != nil {
		return err
	}
	if gmAbs+size1 != len(data) {
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
