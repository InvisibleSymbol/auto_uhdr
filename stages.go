package arw2uhdr

import (
	"context"
	"fmt"

	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/sonylens"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// Image is a scene/display-linear float32 RGB image passed between stages.
type Image = imaging.Image

// RawMeta carries basic metadata from a decoded raw file.
type RawMeta = raw.Meta

// The pipeline is composed of four swappable stages. A caller can replace any of
// them on a [Converter] to plug in, e.g., custom raw handling or an alternative
// Ultra HDR generator, while reusing the rest of the pipeline.

// RawDecoder develops a camera raw file into a scene-linear RGB image.
type RawDecoder interface {
	Decode(ctx context.Context, arwPath string) (*Image, *RawMeta, error)
}

// LensCorrector geometrically (and optionally photometrically) corrects a decoded
// raw so it aligns to the in-camera-corrected JPEG. It may read the raw file again
// for embedded correction metadata.
type LensCorrector interface {
	Correct(ctx context.Context, img *Image, arwPath string) (*Image, error)
}

// HDRRenderer builds the HDR-linear rendition from the linear SDR base and the
// lens-corrected, registered linear RAW (same dimensions).
type HDRRenderer interface {
	Render(ctx context.Context, sdrLinear, rawLinear *Image) (*Image, error)
}

// EncodeResult is what an [UltraHDREncoder] returns.
type EncodeResult struct {
	Data            []byte
	GainMapChannels int
	MaxBoostStops   float64
}

// UltraHDREncoder turns the SDR base JPEG plus the linear SDR/HDR pair into a
// finished Ultra HDR file. Owning both gain-map derivation and container assembly
// lets an alternative backend (e.g. libultrahdr) do it its own way.
type UltraHDREncoder interface {
	Encode(ctx context.Context, baseJPEG []byte, sdrLinear, hdrLinear *Image) (EncodeResult, error)
}

// --- default implementations (the current pure-Go / LibRaw pipeline) ---

// NewLibRawDecoder returns the default LibRaw-backed decoder.
func NewLibRawDecoder(o Options) RawDecoder { return libRawDecoder{opts: o.rawOpts()} }

type libRawDecoder struct{ opts raw.Opts }

func (d libRawDecoder) Decode(ctx context.Context, arwPath string) (*Image, *RawMeta, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return raw.Decode(arwPath, d.opts)
}

// NewEmbeddedLensCorrector returns the default corrector using Sony's embedded profile.
func NewEmbeddedLensCorrector(o Options) LensCorrector {
	return embeddedLensCorrector{enabled: o.Lens != LensOff, warp: o.warpConfig()}
}

type embeddedLensCorrector struct {
	enabled bool
	warp    sonylens.WarpConfig
}

func (c embeddedLensCorrector) Correct(ctx context.Context, img *Image, arwPath string) (*Image, error) {
	if !c.enabled {
		return img, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cp, err := sonylens.ReadARW(arwPath)
	if err != nil || !cp.HasDistortion {
		return nil, fmt.Errorf("embedded lens params unavailable: %w", err)
	}
	return sonylens.Warp(img, cp, c.warp), nil
}

// NewHighlightRenderer returns the default highlight-recovery HDR renderer.
func NewHighlightRenderer(o Options) HDRRenderer { return highlightRenderer{opts: o.hdrOptions()} }

type highlightRenderer struct{ opts hdrbuild.Options }

func (r highlightRenderer) Render(ctx context.Context, sdrLinear, rawLinear *Image) (*Image, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hdr, _ := hdrbuild.Build(sdrLinear, rawLinear, r.opts)
	return hdr, nil
}

// NewGoEncoder returns the default pure-Go gain-map + JPEG_R encoder.
func NewGoEncoder(o Options) UltraHDREncoder {
	return goEncoder{gm: o.gainmapOptions(), enc: o.encodeOptions()}
}

type goEncoder struct {
	gm  gainmap.Options
	enc ultrahdr.Options
}

func (e goEncoder) Encode(ctx context.Context, baseJPEG []byte, sdrLinear, hdrLinear *Image) (EncodeResult, error) {
	if err := ctx.Err(); err != nil {
		return EncodeResult{}, err
	}
	gm, meta, err := gainmap.Compute(sdrLinear, hdrLinear, e.gm)
	if err != nil {
		return EncodeResult{}, err
	}
	data, err := ultrahdr.Encode(baseJPEG, gm, meta, e.enc)
	if err != nil {
		return EncodeResult{}, err
	}
	return EncodeResult{
		Data:            data,
		GainMapChannels: gm.Channels,
		MaxBoostStops:   max(meta.MaxLog2[0], meta.MaxLog2[1], meta.MaxLog2[2]),
	}, nil
}
