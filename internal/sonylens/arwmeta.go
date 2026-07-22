// Package sonylens reads Sony ARW embedded lens-correction parameters (distortion,
// chromatic aberration, vignetting) directly from the plaintext Exif SubIFD.
//
// Verified on Sony DSC-RX100M7A files: the correction arrays live un-encrypted in the
// SubIFD referenced by IFD0 tag 0x014a (SubIFDs), at tags:
//
//	0x7037 DistortionCorrParams          int16[1+16]  (data[0]=knot count, then 16 slots)
//	0x7035 ChromaticAberrationCorrParams int16[1+32]  (data[0]=total, red||blue, then pad)
//	0x7032 VignettingCorrParams          int16[1+16]  (data[0]=knot count, then 16 slots)
//
// The leading element of each array is a count. For this body: distortion=11 knots,
// vignetting=16 knots, CA total=22 (11 red + 11 blue). The reader is count-driven, so it
// adapts to other bodies/knot counts rather than hard-coding 11/16.
//
// An encrypted SR2SubIFD copy (tags 0x7982/0x7980/0x797d) also exists but is not needed.
package sonylens

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// Tag IDs in the Exif SubIFD (plaintext).
const (
	tagOrientation = 0x0112 // in IFD0
	tagSubIFDs     = 0x014a // in IFD0 -> offsets to sub-IFDs
	tagImageWidth  = 0x0100
	tagImageHeight = 0x0101
	tagCropTopLeft = 0x74c7 // Sony crop origin (int32u[2]: top,left)  -- appears in SubIFD
	tagCropSize    = 0x74c8 // Sony crop size   (int32u[2]: h,w?)      -- appears in SubIFD
	tagVignetting  = 0x7032
	tagChromaticAb = 0x7035
	tagDistortion  = 0x7037
)

// CorrParams holds the parsed embedded correction data for one ARW.
type CorrParams struct {
	Make, Model string

	Orientation int // EXIF orientation (1=normal, 6=90CW, 8=270CW, ...)

	HasDistortion bool
	DistortionN   int     // number of valid knots
	Distortion    []int16 // len==DistortionN, radial, center->corner

	HasVignetting bool
	VignettingN   int
	Vignetting    []int16 // len==VignettingN

	HasCA   bool
	CATotal int     // red+blue
	CARed   []int16 // len==CATotal/2
	CABlue  []int16 // len==CATotal/2

	// Sony crop region (present in SubIFD on some bodies); zero if absent.
	CropTop, CropLeft int
	CropW, CropH      int
}

type tiff struct {
	b  []byte
	bo binary.ByteOrder
}

func (t *tiff) u16(off int) (uint16, error) {
	if off < 0 || off+2 > len(t.b) {
		return 0, fmt.Errorf("u16 out of range at %d", off)
	}
	return t.bo.Uint16(t.b[off:]), nil
}

func (t *tiff) u32(off int) (uint32, error) {
	if off < 0 || off+4 > len(t.b) {
		return 0, fmt.Errorf("u32 out of range at %d", off)
	}
	return t.bo.Uint32(t.b[off:]), nil
}

// TIFF type sizes (bytes) for the types we care about.
var typeSize = map[uint16]int{
	1: 1, 2: 1, 3: 2, 4: 4, 5: 8, 6: 1, 7: 1, 8: 2, 9: 4, 10: 8, 11: 4, 12: 8,
}

type entry struct {
	tag, typ  uint16
	count     uint32
	valOff    int // absolute file offset of the value bytes (inline resolved)
	inlineOff int // offset of the 4-byte value field (for small values)
}

// dataOffset returns the absolute offset of an entry's data, resolving inline vs pointer.
func (t *tiff) dataOffset(e entry) int {
	sz := typeSize[e.typ]
	if sz == 0 {
		sz = 1
	}
	if int(e.count)*sz <= 4 {
		return e.inlineOff
	}
	v, _ := t.u32(e.inlineOff)
	return int(v)
}

func (t *tiff) readInt16s(e entry) ([]int16, error) {
	off := t.dataOffset(e)
	n := int(e.count)
	if off < 0 || off+2*n > len(t.b) {
		return nil, fmt.Errorf("int16 array tag 0x%04x out of range", e.tag)
	}
	out := make([]int16, n)
	for i := range n {
		out[i] = int16(t.bo.Uint16(t.b[off+2*i:]))
	}
	return out, nil
}

func (t *tiff) readU32s(e entry) ([]uint32, error) {
	off := t.dataOffset(e)
	n := int(e.count)
	if off < 0 || off+4*n > len(t.b) {
		return nil, fmt.Errorf("u32 array tag 0x%04x out of range", e.tag)
	}
	out := make([]uint32, n)
	for i := range n {
		out[i] = t.bo.Uint32(t.b[off+4*i:])
	}
	return out, nil
}

// readIFD parses the IFD at absolute offset off, returning its entries and the next-IFD offset.
func (t *tiff) readIFD(off int) (map[uint16]entry, int, error) {
	nEntries, err := t.u16(off)
	if err != nil {
		return nil, 0, err
	}
	m := make(map[uint16]entry, nEntries)
	p := off + 2
	for i := range int(nEntries) {
		if p+12 > len(t.b) {
			return nil, 0, fmt.Errorf("IFD entry %d out of range", i)
		}
		tag := t.bo.Uint16(t.b[p:])
		typ := t.bo.Uint16(t.b[p+2:])
		count := t.bo.Uint32(t.b[p+4:])
		m[tag] = entry{tag: tag, typ: typ, count: count, inlineOff: p + 8}
		p += 12
	}
	next, _ := t.u32(p)
	return m, int(next), nil
}

// ReadARW parses the embedded correction parameters from an ARW file.
func ReadARW(path string) (*CorrParams, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseARW(b)
}

func parseARW(b []byte) (*CorrParams, error) {
	if len(b) < 8 {
		return nil, errors.New("sonylens: file too small")
	}
	t := &tiff{b: b}
	switch {
	case b[0] == 'I' && b[1] == 'I':
		t.bo = binary.LittleEndian
	case b[0] == 'M' && b[1] == 'M':
		t.bo = binary.BigEndian
	default:
		return nil, errors.New("sonylens: not a TIFF/ARW (bad byte order marker)")
	}
	if magic, _ := t.u16(2); magic != 42 {
		return nil, fmt.Errorf("sonylens: not a TIFF/ARW (bad magic %d)", magic)
	}
	ifd0Off, err := t.u32(4)
	if err != nil {
		return nil, err
	}

	ifd0, _, err := t.readIFD(int(ifd0Off))
	if err != nil {
		return nil, fmt.Errorf("read IFD0: %w", err)
	}

	cp := &CorrParams{}
	if e, ok := ifd0[tagOrientation]; ok {
		if v, err := t.u16(t.dataOffset(e)); err == nil {
			cp.Orientation = int(v)
		}
	}
	cp.Make = asciiTag(t, ifd0, 0x010f)
	cp.Model = asciiTag(t, ifd0, 0x0110)

	// Collect candidate sub-IFD offsets: the SubIFDs tag (0x014a) may list several.
	var subOffs []int
	if e, ok := ifd0[tagSubIFDs]; ok {
		if offs, err := t.readU32s(e); err == nil {
			for _, o := range offs {
				subOffs = append(subOffs, int(o))
			}
		}
	}
	if len(subOffs) == 0 {
		return nil, errors.New("sonylens: no SubIFDs (0x014a) in IFD0")
	}

	// Scan each sub-IFD for the correction tags; take the first that carries them.
	for _, so := range subOffs {
		sub, _, err := t.readIFD(so)
		if err != nil {
			continue
		}
		if e, ok := sub[tagDistortion]; ok && !cp.HasDistortion {
			if arr, err := t.readInt16s(e); err == nil {
				cp.setDistortion(arr)
			}
		}
		if e, ok := sub[tagVignetting]; ok && !cp.HasVignetting {
			if arr, err := t.readInt16s(e); err == nil {
				cp.setVignetting(arr)
			}
		}
		if e, ok := sub[tagChromaticAb]; ok && !cp.HasCA {
			if arr, err := t.readInt16s(e); err == nil {
				cp.setCA(arr)
			}
		}
		if e, ok := sub[tagCropTopLeft]; ok {
			if v, err := t.readU32s(e); err == nil && len(v) == 2 {
				cp.CropTop, cp.CropLeft = int(v[0]), int(v[1])
			}
		}
		if e, ok := sub[tagCropSize]; ok {
			if v, err := t.readU32s(e); err == nil && len(v) == 2 {
				cp.CropW, cp.CropH = int(v[0]), int(v[1])
			}
		}
	}
	return cp, nil
}

func asciiTag(t *tiff, ifd map[uint16]entry, tag uint16) string {
	e, ok := ifd[tag]
	if !ok {
		return ""
	}
	off := t.dataOffset(e)
	n := int(e.count)
	if off < 0 || off+n > len(t.b) {
		return ""
	}
	s := t.b[off : off+n]
	for len(s) > 0 && s[len(s)-1] == 0 {
		s = s[:len(s)-1]
	}
	return string(s)
}

// setDistortion interprets a count-prefixed distortion array: data[0]=knot count.
func (c *CorrParams) setDistortion(a []int16) {
	if len(a) < 1 {
		return
	}
	n := int(a[0])
	if n <= 0 || 1+n > len(a) {
		return
	}
	c.HasDistortion = true
	c.DistortionN = n
	c.Distortion = slices.Clone(a[1 : 1+n])
}

func (c *CorrParams) setVignetting(a []int16) {
	if len(a) < 1 {
		return
	}
	n := int(a[0])
	if n <= 0 || 1+n > len(a) {
		return
	}
	c.HasVignetting = true
	c.VignettingN = n
	c.Vignetting = slices.Clone(a[1 : 1+n])
}

// setCA interprets a count-prefixed CA array: data[0]=total (red+blue), red first then blue.
func (c *CorrParams) setCA(a []int16) {
	if len(a) < 1 {
		return
	}
	total := int(a[0])
	if total <= 0 || total%2 != 0 || 1+total > len(a) {
		return
	}
	half := total / 2
	c.HasCA = true
	c.CATotal = total
	c.CARed = slices.Clone(a[1 : 1+half])
	c.CABlue = slices.Clone(a[1+half : 1+total])
}

// Summary renders the parsed correction parameters as a human-readable report,
// used by the `inspect` command.
func (c *CorrParams) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Camera:       %s %s\n", c.Make, c.Model)
	fmt.Fprintf(&b, "Orientation:  %d\n", c.Orientation)
	if c.CropW > 0 || c.CropH > 0 {
		fmt.Fprintf(&b, "Crop:         %dx%d at (%d,%d)\n", c.CropW, c.CropH, c.CropLeft, c.CropTop)
	}
	fmt.Fprintf(&b, "Distortion:   %s\n", knotLine(c.HasDistortion, c.DistortionN, c.Distortion))
	fmt.Fprintf(&b, "Vignetting:   %s\n", knotLine(c.HasVignetting, c.VignettingN, c.Vignetting))
	if c.HasCA {
		fmt.Fprintf(&b, "CA (red):     %d knots %v\n", len(c.CARed), c.CARed)
		fmt.Fprintf(&b, "CA (blue):    %d knots %v\n", len(c.CABlue), c.CABlue)
	} else {
		b.WriteString("CA:           (absent)\n")
	}
	return b.String()
}

func knotLine(has bool, n int, v []int16) string {
	if !has {
		return "(absent)"
	}
	return fmt.Sprintf("%d knots %v", n, v)
}
