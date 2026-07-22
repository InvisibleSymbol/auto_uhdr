package arw2uhdr

import (
	"log/slog"

	"github.com/invis/arw2uhdr/internal/gainmap"
	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/sonylens"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// HDRMode selects the HDR-derivation strategy.
type HDRMode int

const (
	// HDRRaw is the default: a JPEG-gated, RAW-luminance-driven boost. The JPEG masks
	// shadows; the boost magnitude comes from how much brighter the RAW is than the
	// JPEG, so bright surfaces lift by their real scene luminance and clipped regions
	// reconstruct genuine RAW detail. Faithful/photographic rather than synthetic pop.
	HDRRaw HDRMode = iota
	// HDRHighlight is the older JPEG-anchored highlight recovery plus a synthetic
	// display boost (punchier, less faithful).
	HDRHighlight
	// HDRDevelop treats the full RAW linear as the HDR image (creative).
	HDRDevelop
)

// LensMode selects how much of the embedded Sony lens profile to apply.
type LensMode int

const (
	LensOff          LensMode = iota // no geometric correction
	LensDistortion                   // distortion only
	LensDistortionCA                 // distortion + lateral chromatic aberration (default)
)

// GainMapMode selects a luminance or per-channel gain map.
type GainMapMode int

const (
	GainMapLuminance GainMapMode = iota // single-channel (default)
	GainMapRGB                          // per-channel colour gain map
)

// Demosaic selects the LibRaw demosaic algorithm for the default decoder.
type Demosaic int

const (
	DemosaicAHD Demosaic = iota // adaptive homogeneity-directed (default)
	DemosaicDCB
	DemosaicDHT
	DemosaicVNG
	DemosaicPPG
	DemosaicLinear
)

func (d Demosaic) libraw() int {
	switch d {
	case DemosaicDCB:
		return raw.DemosaicDCB
	case DemosaicDHT:
		return raw.DemosaicDHT
	case DemosaicVNG:
		return raw.DemosaicVNG
	case DemosaicPPG:
		return raw.DemosaicPPG
	case DemosaicLinear:
		return raw.DemosaicLinear
	default:
		return raw.DemosaicAHD
	}
}

// Options configures the default pipeline stages. It is the knob surface the CLI
// exposes; a caller wiring custom stages via [Converter] can ignore the fields a
// replaced stage owns. The zero value is not valid — start from [DefaultOptions].
type Options struct {
	// Decode
	Demosaic Demosaic

	// Lens correction
	Lens       LensMode
	Vignetting bool // experimental radial brightness correction (unvalidated scale)
	Register   bool // residual registration of the RAW to the JPEG grid

	// HDR rendition
	Mode      HDRMode
	Strength  float64 // display boost in stops at the plateau (0 = pure recovery)
	Threshold float64 // SDR luma where the boost ramp begins (lower reaches more of the image)
	RampWidth float64 // luma span over which the boost reaches full strength
	MaxBoost  float64 // ceiling on total boost, in stops (soft shoulder)
	// Chroma (raw mode) dials the RGB gain-map saturation in [0,1]: 0 = neutral boost that keeps
	// the JPEG's exact colour (RGB map ≡ single-channel), 1 = full per-channel recovery. ~0.3 adds
	// some real colour without a jarring mid-highlight transition.
	Chroma float64
	// ChromaTrack scales Chroma by JPEG brightness so per-channel colour recovery ramps in with
	// clipping (neutral midtones, full Chroma in the blown highlights). Chroma becomes the peak.
	ChromaTrack bool
	// BoostCurve (raw mode) reshapes the RAW-derived boost from linear (0) toward logarithmic (>0),
	// lifting partially-clipped mid-highlights up toward the recovery ceiling. 0 = linear (default).
	BoostCurve float64

	// Gain map + container
	GainMap        GainMapMode
	GainMapScale   int  // downsample factor per dimension (1 = full res)
	GainMapQuality int  // gain-map JPEG quality, 1..100
	NoNeutralize   bool // disable neutralizing per-channel colour in clipped highlights (RGB maps)

	// Logger receives structured progress at Debug level. Defaults to a discard
	// logger when nil.
	Logger *slog.Logger
}

// DefaultOptions returns the validated defaults.
func DefaultOptions() Options {
	return Options{
		Demosaic:       DemosaicAHD,
		Lens:           LensDistortionCA,
		Vignetting:     false,
		Register:       true,
		Mode:           HDRRaw,
		Strength:       1.0, // raw-boost: multiplier on the physical RAW gain (1 = as measured)
		Threshold:      0.5, // JPEG-luma gate; masks shadows (RAW gain is ~0 in midtones anyway)
		RampWidth:      0.35,
		MaxBoost:       3.0,
		Chroma:         0.3,        // gentle per-channel colour; 0 = neutral, 1 = full recovery
		BoostCurve:     0,          // linear recovery; raise for a log-like lift of the mid-highlights
		GainMap:        GainMapRGB, // so the Chroma dial takes effect by default
		GainMapScale:   1,          // full-res: raw-boost carries real recovered detail in the map
		GainMapQuality: ultrahdr.DefaultOptions().GainMapQuality,
		NoNeutralize:   true, // the Chroma dial governs colour now; neutralization is opt-in
	}
}

// The methods below resolve the public Options into the internal per-stage
// option structs the default stage implementations consume.

func (o Options) rawOpts() raw.Opts {
	ro := raw.DefaultOpts()
	ro.Linear = true
	ro.Highlight = 2 // blend highlights for maximum recovery range
	ro.Demosaic = o.Demosaic.libraw()
	return ro
}

func (o Options) warpConfig() sonylens.WarpConfig {
	return sonylens.WarpConfig{
		CropScale:       1.0,
		ApplyCA:         o.Lens == LensDistortionCA,
		ApplyVignetting: o.Vignetting,
	}
}

func (o Options) hdrOptions() hdrbuild.Options {
	ho := hdrbuild.DefaultOptions()
	switch o.Mode {
	case HDRRaw:
		ho.Mode = hdrbuild.ModeRawBoost
	case HDRDevelop:
		ho.Mode = hdrbuild.ModeDevelop
	default:
		ho.Mode = hdrbuild.ModeHighlight
	}
	ho.Strength = o.Strength
	ho.Threshold = o.Threshold
	ho.RampWidth = o.RampWidth
	ho.MaxBoostStops = o.MaxBoost
	ho.ChromaStrength = o.Chroma
	ho.ChromaTrack = o.ChromaTrack
	ho.BoostCurve = o.BoostCurve
	return ho
}

func (o Options) gainmapOptions() gainmap.Options {
	gmo := gainmap.DefaultOptions()
	gmo.MultiChannel = o.GainMap == GainMapRGB
	gmo.MaxBoostStops = o.MaxBoost
	gmo.ScaleFactor = o.GainMapScale
	gmo.NeutralizeHighlights = !o.NoNeutralize
	return gmo
}

func (o Options) encodeOptions() ultrahdr.Options {
	eo := ultrahdr.DefaultOptions()
	if o.GainMapQuality > 0 {
		eo.GainMapQuality = o.GainMapQuality
	}
	return eo
}

func (o Options) logger() *slog.Logger {
	if o.Logger != nil {
		return o.Logger
	}
	return slog.New(slog.DiscardHandler)
}
