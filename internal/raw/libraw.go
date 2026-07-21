// Package raw decodes Sony ARW (and other) raw files to a float image via LibRaw (cgo).
package raw

/*
#cgo pkg-config: libraw_r
#include <libraw/libraw.h>
#include <stdlib.h>
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// Demosaic algorithms (LibRaw user_qual).
const (
	DemosaicLinear = 0
	DemosaicVNG    = 1
	DemosaicPPG    = 2
	DemosaicAHD    = 3
	DemosaicDCB    = 4
	DemosaicDHT    = 11
)

// Output color spaces (LibRaw output_color).
const (
	ColorRaw    = 0
	ColorSRGB   = 1
	ColorAdobe  = 2
	ColorWide   = 3
	ColorProPho = 4
	ColorXYZ    = 5
)

// Opts controls the raw develop.
type Opts struct {
	Linear      bool // true: gamma 1.0 (scene/display-linear); false: default BT.709-ish gamma
	UseCameraWB bool // match the camera's as-shot white balance
	OutputColor int  // ColorSRGB, ...
	Demosaic    int  // DemosaicAHD, ...
	Highlight   int  // 0=clip,1=unclip,2=blend,3-9=reconstruct
	HalfSize    bool // half-resolution decode (fast)
	NoAutoScale bool // keep raw scaling (advanced)
}

// DefaultOpts returns sensible defaults for matching a camera JPEG.
func DefaultOpts() Opts {
	return Opts{Linear: false, UseCameraWB: true, OutputColor: ColorSRGB, Demosaic: DemosaicAHD, Highlight: 0}
}

// Meta carries basic decode metadata.
type Meta struct {
	Make, Model   string
	Width, Height int
	Bits, Colors  int
}

// Decode develops the raw at path into a float32 RGB image.
//
// With Opts.Linear the pixels are scene-linear (needed for HDR/gain-map math);
// otherwise they carry LibRaw's default display gamma (handy for previews/geometry
// checks). The heavy LibRaw calls are not interruptible; cancellation is handled by
// the orchestrator at stage boundaries.
func Decode(path string, o Opts) (*imaging.Image, *Meta, error) {
	lr := C.libraw_init(0)
	if lr == nil {
		return nil, nil, fmt.Errorf("libraw_init failed")
	}
	defer C.libraw_close(lr)

	lr.params.output_bps = 16
	lr.params.no_auto_bright = 1
	if o.Linear {
		lr.params.gamm[0] = C.double(1.0)
		lr.params.gamm[1] = C.double(1.0)
	}
	if o.UseCameraWB {
		lr.params.use_camera_wb = 1
	}
	lr.params.output_color = C.int(o.OutputColor)
	lr.params.user_qual = C.int(o.Demosaic)
	lr.params.highlight = C.int(o.Highlight)
	if o.HalfSize {
		lr.params.half_size = 1
	}
	if o.NoAutoScale {
		lr.params.no_auto_scale = 1
	}

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))

	if rc := C.libraw_open_file(lr, cpath); rc != 0 {
		return nil, nil, fmt.Errorf("open_file: %s", strerr(rc))
	}
	if rc := C.libraw_unpack(lr); rc != 0 {
		return nil, nil, fmt.Errorf("unpack: %s", strerr(rc))
	}
	if rc := C.libraw_dcraw_process(lr); rc != 0 {
		return nil, nil, fmt.Errorf("dcraw_process: %s", strerr(rc))
	}

	var errc C.int
	pi := C.libraw_dcraw_make_mem_image(lr, &errc)
	if pi == nil {
		return nil, nil, fmt.Errorf("make_mem_image: %s", strerr(errc))
	}
	defer C.libraw_dcraw_clear_mem(pi)

	w, h := int(pi.width), int(pi.height)
	colors, bits, n := int(pi.colors), int(pi.bits), int(pi.data_size)
	if colors != 3 {
		return nil, nil, fmt.Errorf("unexpected colors=%d (want 3)", colors)
	}
	// Copy the LibRaw-owned buffer into Go memory before we release it.
	buf := C.GoBytes(unsafe.Pointer(&pi.data[0]), C.int(n))

	im := imaging.New(w, h)
	switch bits {
	case 16:
		const inv = 1.0 / 65535.0
		// interleaved RGB, host byte order (little-endian on amd64)
		for i := range w * h * 3 {
			im.Pix[i] = float32(float64(uint16(buf[2*i])|uint16(buf[2*i+1])<<8) * inv)
		}
	case 8:
		const inv = 1.0 / 255.0
		for i := range w * h * 3 {
			im.Pix[i] = float32(float64(buf[i]) * inv)
		}
	default:
		return nil, nil, fmt.Errorf("unexpected bits=%d", bits)
	}

	meta := &Meta{
		Make:  C.GoString(&lr.idata.make[0]),
		Model: C.GoString(&lr.idata.model[0]),
		Width: w, Height: h, Bits: bits, Colors: colors,
	}
	return im, meta, nil
}

func strerr(rc C.int) string {
	return C.GoString(C.libraw_strerror(rc))
}
