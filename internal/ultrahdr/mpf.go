package ultrahdr

import "encoding/binary"

// mpfFields records byte offsets (within the MPF APP2 segment) of the MP Endian field and the
// per-image size/offset fields that must be patched once final lengths are known.
type mpfFields struct {
	endianOff                          int
	size0Off, off0Off, size1Off, off1Off int
}

// buildMPF constructs a two-image MPF (Multi-Picture Format) APP2 segment with placeholder
// sizes/offsets, big-endian, per CIPA DC-007. Layout (byte offsets within the segment):
//
//	0   FFE2                marker
//	2   length (=88)
//	4   "MPF\0"
//	8   MP Endian field: "MM" 002A  IFD-offset=8     <- offsets are relative to here
//	16  MP Index IFD: count=3
//	18  entry MPFVersion   (0xB000)
//	30  entry NumberImages (0xB001)=2
//	42  entry MPEntry      (0xB002) -> value offset 50
//	54  next-IFD = 0
//	58  MPEntry[0]: attr,size,offset,dep(2+2)
//	74  MPEntry[1]: attr,size,offset,dep(2+2)
func buildMPF() ([]byte, mpfFields) {
	const (
		endianOff  = 8
		ifdOff     = 16
		mpEntryOff = 58
	)
	seg := make([]byte, 90)
	seg[0], seg[1] = 0xFF, 0xE2
	binary.BigEndian.PutUint16(seg[2:], 88) // length = 90 - 2
	copy(seg[4:], "MPF\x00")

	// MP Endian + TIFF header (big-endian).
	copy(seg[8:], []byte{'M', 'M'})
	binary.BigEndian.PutUint16(seg[10:], 0x002A)
	binary.BigEndian.PutUint32(seg[12:], 8) // first IFD at endian+8

	// MP Index IFD.
	binary.BigEndian.PutUint16(seg[ifdOff:], 3) // 3 entries
	// entry MPFVersion: tag 0xB000 type UNDEFINED(7) count 4 value "0100"
	e := ifdOff + 2
	binary.BigEndian.PutUint16(seg[e:], 0xB000)
	binary.BigEndian.PutUint16(seg[e+2:], 7)
	binary.BigEndian.PutUint32(seg[e+4:], 4)
	copy(seg[e+8:], "0100")
	// entry NumberOfImages: tag 0xB001 type LONG(4) count 1 value 2
	e += 12
	binary.BigEndian.PutUint16(seg[e:], 0xB001)
	binary.BigEndian.PutUint16(seg[e+2:], 4)
	binary.BigEndian.PutUint32(seg[e+4:], 1)
	binary.BigEndian.PutUint32(seg[e+8:], 2)
	// entry MPEntry: tag 0xB002 type UNDEFINED(7) count 32 value=offset(50 rel to endian)
	e += 12
	binary.BigEndian.PutUint16(seg[e:], 0xB002)
	binary.BigEndian.PutUint16(seg[e+2:], 7)
	binary.BigEndian.PutUint32(seg[e+4:], 32)
	binary.BigEndian.PutUint32(seg[e+8:], uint32(mpEntryOff-endianOff)) // = 50
	// next-IFD offset = 0
	binary.BigEndian.PutUint32(seg[54:], 0)

	// MPEntry[0] (primary): representative + MP type "Baseline MP Primary Image".
	binary.BigEndian.PutUint32(seg[mpEntryOff+0:], 0x20030000)
	// size/offset patched later.
	// dependent entries = 0 (already zero).
	// MPEntry[1] (gain map): type undefined.
	binary.BigEndian.PutUint32(seg[mpEntryOff+16+0:], 0x00000000)

	f := mpfFields{
		endianOff: endianOff,
		size0Off:  mpEntryOff + 4,
		off0Off:   mpEntryOff + 8,
		size1Off:  mpEntryOff + 16 + 4,
		off1Off:   mpEntryOff + 16 + 8,
	}
	return seg, f
}
