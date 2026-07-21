// Package arw2uhdr converts a Sony ARW + camera JPEG pair into an Ultra HDR
// (JPEG_R) image: the camera JPEG stays byte-for-byte as the SDR base, the RAW
// supplies lens-corrected highlight recovery, and a gain map makes highlights pop
// on HDR displays while SDR viewers see the untouched JPEG.
//
// The conversion is a pipeline of four swappable stages — [RawDecoder],
// [LensCorrector], [HDRRenderer] and [UltraHDREncoder]. [New] wires the default
// LibRaw + pure-Go implementations from [Options]; replace any field on the
// returned [Converter] to plug in custom behaviour (e.g. a different raw handler
// or Ultra HDR backend).
package arw2uhdr

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/register"
)

// Input names the files for one conversion.
type Input struct {
	ARW    string // source raw
	JPEG   string // full-resolution camera JPEG (the SDR base)
	Output string // destination Ultra HDR path
}

// Result reports what a conversion did.
type Result struct {
	Output          string  `json:"output"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	Model           string  `json:"model"`
	Registered      bool    `json:"registered"`
	RegistrationSx  float64 `json:"registration_sx"`
	RegistrationSy  float64 `json:"registration_sy"`
	GainMapChannels int     `json:"gainmap_channels"`
	MaxBoostStops   float64 `json:"max_boost_stops"`
	OutputBytes     int     `json:"output_bytes"`
	ElapsedMs       int64   `json:"elapsed_ms"`
}

// Converter composes the four pipeline stages. Construct it with [New] and,
// optionally, override any stage before calling [Converter.Convert].
type Converter struct {
	Decoder   RawDecoder
	Corrector LensCorrector
	Renderer  HDRRenderer
	Encoder   UltraHDREncoder

	Register bool // residual registration of the RAW to the JPEG grid
	Log      *slog.Logger
}

// New builds a Converter with the default stages configured from o.
func New(o Options) *Converter {
	return &Converter{
		Decoder:   NewLibRawDecoder(o),
		Corrector: NewEmbeddedLensCorrector(o),
		Renderer:  NewHighlightRenderer(o),
		Encoder:   NewGoEncoder(o),
		Register:  o.Register,
		Log:       o.logger(),
	}
}

// Convert is a one-shot convenience: New(o).Convert(ctx, in).
func Convert(ctx context.Context, in Input, o Options) (*Result, error) {
	return New(o).Convert(ctx, in)
}

// Convert runs the pipeline for one input pair and writes in.Output. ctx is
// honoured at every stage boundary.
func (c *Converter) Convert(ctx context.Context, in Input) (*Result, error) {
	t0 := time.Now()
	log := c.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	img, meta, err := c.Decoder.Decode(ctx, in.ARW)
	if err != nil {
		return nil, stageErr(StageDecode, err)
	}
	log.Debug("decoded", "w", meta.Width, "h", meta.Height, "model", meta.Model)

	img, err = c.Corrector.Correct(ctx, img, in.ARW)
	if err != nil {
		return nil, stageErr(StageLens, err)
	}

	jpg, err := imaging.LoadImage(in.JPEG)
	if err != nil {
		return nil, stageErr(StageInput, err)
	}
	sdrLin := linearizeSRGB(jpg)

	rawAligned := img.Resize(jpg.W, jpg.H)
	res := &Result{RegistrationSx: 1, RegistrationSy: 1}
	if c.Register {
		aff := register.Estimate(jpg, rawAligned, register.Options{})
		rawAligned = aff.Apply(rawAligned, jpg.W, jpg.H)
		res.Registered = true
		res.RegistrationSx, res.RegistrationSy = aff.Sx, aff.Sy
		log.Debug("registered", "sx", aff.Sx, "sy", aff.Sy)
	}

	hdr, err := c.Renderer.Render(ctx, sdrLin, rawAligned)
	if err != nil {
		return nil, stageErr(StageRender, err)
	}

	base, err := os.ReadFile(in.JPEG)
	if err != nil {
		return nil, stageErr(StageInput, err)
	}
	enc, err := c.Encoder.Encode(ctx, base, sdrLin, hdr)
	if err != nil {
		return nil, stageErr(StageEncode, err)
	}
	if err := os.WriteFile(in.Output, enc.Data, 0o644); err != nil {
		return nil, stageErr(StageWrite, err)
	}

	res.Output = in.Output
	res.Width, res.Height = jpg.W, jpg.H
	res.Model = meta.Model
	res.GainMapChannels = enc.GainMapChannels
	res.MaxBoostStops = enc.MaxBoostStops
	res.OutputBytes = len(enc.Data)
	res.ElapsedMs = time.Since(t0).Milliseconds()
	log.Debug("done", "bytes", res.OutputBytes, "elapsed_ms", res.ElapsedMs)
	return res, nil
}

// linearizeSRGB converts a gamma-sRGB image to scene-linear.
func linearizeSRGB(jpg *imaging.Image) *imaging.Image {
	out := imaging.New(jpg.W, jpg.H)
	imaging.ParallelRows(jpg.H, func(y0, y1 int) {
		for i := y0 * jpg.W * 3; i < y1*jpg.W*3; i++ {
			out.Pix[i] = color.SRGBDecode(jpg.Pix[i])
		}
	})
	return out
}
