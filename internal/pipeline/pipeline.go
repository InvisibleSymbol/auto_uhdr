// Package pipeline orchestrates the full ARW+JPEG → Ultra HDR conversion:
//
//	decode ARW (LibRaw, scene-linear, camera WB)
//	→ Sony embedded lens correction (distortion + CA warp)
//	→ residual registration to the JPEG grid (anisotropic scale + shift)
//	→ HDR-linear rendition (clip-gated RAW recovery + monotonic display boost)
//	→ gain map (single or RGB channel)
//	→ Ultra HDR (JPEG_R) container assembly
package pipeline

import (
	"fmt"
	"os"
	"time"

	"github.com/invis/arw2uhdr/internal/color"
	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/register"
	"github.com/invis/arw2uhdr/internal/sonylens"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// Opts controls the full conversion.
type Opts struct {
	HDR      hdrbuild.Options
	GainMap  gainmap.Options
	Encode   ultrahdr.Options
	Demosaic int  // raw.DemosaicAHD etc.
	LensCorr bool // apply Sony embedded distortion+CA (default true)
	ApplyCA  bool // chromatic aberration correction (default true)
	Register bool // residual registration to the JPEG grid (default true)
	Verbose  bool
}

// DefaultOpts returns the settled defaults.
func DefaultOpts() Opts {
	return Opts{
		HDR:      hdrbuild.DefaultOptions(),
		GainMap:  gainmap.DefaultOptions(),
		Encode:   ultrahdr.DefaultOptions(),
		Demosaic: raw.DemosaicAHD,
		LensCorr: true,
		ApplyCA:  true,
		Register: true,
	}
}

// Result reports what was done.
type Result struct {
	Output          string     `json:"output"`
	Width           int        `json:"width"`
	Height          int        `json:"height"`
	Model           string     `json:"model"`
	AnchorGains     [3]float64 `json:"anchor_gains"`
	RegistrationSx  float64    `json:"registration_sx"`
	RegistrationSy  float64    `json:"registration_sy"`
	MaxBoostStops   float64    `json:"max_boost_stops"`
	GainMapChannels int        `json:"gainmap_channels"`
	OutputBytes     int        `json:"output_bytes"`
	ElapsedMs       int64      `json:"elapsed_ms"`
}

func logf(o Opts, format string, args ...interface{}) {
	if o.Verbose {
		fmt.Fprintf(os.Stderr, "arw2uhdr: "+format+"\n", args...)
	}
}

// Process converts one ARW+JPEG pair into an Ultra HDR file at outPath.
func Process(arwPath, jpgPath, outPath string, o Opts) (*Result, error) {
	t0 := time.Now()

	// 1) RAW decode: scene-linear, camera WB, blend highlights (max recovery range).
	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2
	ro.Demosaic = o.Demosaic
	img, meta, err := raw.Decode(arwPath, ro)
	if err != nil {
		return nil, &StageError{Stage: "decode", Err: err}
	}
	logf(o, "decoded %dx%d (%s)", meta.Width, meta.Height, meta.Model)

	// 2) lens correction from the embedded Sony profile.
	if o.LensCorr {
		cp, err := sonylens.ReadARW(arwPath)
		if err != nil || !cp.HasDistortion {
			return nil, &StageError{Stage: "lensmeta", Err: fmt.Errorf("embedded lens params unavailable: %v", err)}
		}
		wc := sonylens.DefaultWarp()
		wc.ApplyCA = o.ApplyCA
		img = sonylens.Warp(img, cp, wc)
		logf(o, "lens correction applied (distN=%d ca=%v)", cp.DistortionN, wc.ApplyCA)
	}

	// 3) SDR base + linearize; align RAW to the JPEG grid.
	jpg, err := imaging.LoadImage(jpgPath)
	if err != nil {
		return nil, &StageError{Stage: "sdr", Err: err}
	}
	sdrLin := imaging.New(jpg.W, jpg.H)
	imaging.ParallelRows(jpg.H, func(y0, y1 int) {
		for i := y0 * jpg.W * 3; i < y1*jpg.W*3; i++ {
			sdrLin.Pix[i] = color.SRGBDecode(jpg.Pix[i])
		}
	})
	rawAligned := img.Resize(jpg.W, jpg.H)
	aff := register.Identity()
	if o.Register {
		aff = register.Estimate(jpg, rawAligned, register.Options{})
		rawAligned = aff.Apply(rawAligned, jpg.W, jpg.H)
		logf(o, "registration Sx=%.4f Sy=%.4f", aff.Sx, aff.Sy)
	}

	// 4) HDR-linear rendition.
	hdr, k := hdrbuild.Build(sdrLin, rawAligned, o.HDR)
	logf(o, "hdr built (anchor k=[%.2f %.2f %.2f], strength=%.1f threshold=%.2f)",
		k[0], k[1], k[2], o.HDR.Strength, o.HDR.Threshold)

	// 5) gain map.
	gm, gmeta, err := gainmap.Compute(sdrLin, hdr, o.GainMap)
	if err != nil {
		return nil, &StageError{Stage: "gainmap", Err: err}
	}

	// 6) container assembly with the ORIGINAL JPEG bytes as the untouched SDR base.
	baseBytes, err := os.ReadFile(jpgPath)
	if err != nil {
		return nil, &StageError{Stage: "sdr", Err: err}
	}
	out, err := ultrahdr.Encode(baseBytes, gm, gmeta, o.Encode)
	if err != nil {
		return nil, &StageError{Stage: "encode", Err: err}
	}
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return nil, &StageError{Stage: "write", Err: err}
	}

	maxB := gmeta.MaxLog2[0]
	for c := 1; c < 3; c++ {
		if gmeta.MaxLog2[c] > maxB {
			maxB = gmeta.MaxLog2[c]
		}
	}
	return &Result{
		Output: outPath, Width: jpg.W, Height: jpg.H, Model: meta.Model,
		AnchorGains: k, RegistrationSx: aff.Sx, RegistrationSy: aff.Sy,
		MaxBoostStops: maxB, GainMapChannels: gm.Channels,
		OutputBytes: len(out), ElapsedMs: time.Since(t0).Milliseconds(),
	}, nil
}

// StageError wraps an error with the pipeline stage that produced it (drives CLI exit codes).
type StageError struct {
	Stage string
	Err   error
}

func (e *StageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *StageError) Unwrap() error { return e.Err }
