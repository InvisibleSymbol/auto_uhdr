package ultrahdr

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
)

// locateGainMap returns the byte range [start, start+size) of the appended gain-map
// JPEG inside an Ultra HDR container, read from the MPF second-image entry. Offsets
// in the MPF index are relative to the start of the MP Endian field (see buildMPF).
func locateGainMap(data []byte) (start, size int, err error) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, errors.New("not a JPEG")
	}
	mid := bytes.Index(data, []byte("MPF\x00"))
	if mid < 0 {
		return 0, 0, errors.New("missing MPF APP2 segment")
	}
	endian := mid + 4
	if endian+50+16+12 > len(data) {
		return 0, 0, errors.New("truncated MPF")
	}
	size = int(binary.BigEndian.Uint32(data[endian+50+16+4:]))
	start = endian + int(binary.BigEndian.Uint32(data[endian+50+16+8:]))
	if start+2 > len(data) || start+size > len(data) || size < 2 ||
		data[start] != 0xFF || data[start+1] != 0xD8 {
		return 0, 0, errors.New("MPF image-2 does not point at a valid appended JPEG")
	}
	return start, size, nil
}

// ExtractGainMap returns a copy of the appended gain-map JPEG from an Ultra HDR file.
// The returned bytes are a standalone JPEG; its pixel values are the hdrgm-encoded
// gain (concentrated near zero, so it renders very dark without a brightening pass).
func ExtractGainMap(data []byte) ([]byte, error) {
	start, size, err := locateGainMap(data)
	if err != nil {
		return nil, fmt.Errorf("extract gain map: %w", err)
	}
	return bytes.Clone(data[start : start+size]), nil
}
