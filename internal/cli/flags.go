package cli

import (
	"flag"
	"log/slog"
	"os"

	"github.com/invis/arw2uhdr"
)

// convertFlags holds the conversion knobs shared by the convert and batch commands.
type convertFlags struct {
	hdrMode   string
	strength  float64
	threshold float64
	rampWidth float64
	maxBoost  float64

	gainmap    string
	gmQuality  int
	gmScale    int
	neutralize bool

	demosaic   string
	lens       string
	vignetting bool
	noRegister bool
	chroma     float64

	verbose bool
}

func (c *convertFlags) register(fs *flag.FlagSet) {
	d := arw2uhdr.DefaultOptions()
	fs.StringVar(&c.hdrMode, "hdr-mode", "raw", "raw | highlight | develop")
	fs.Float64Var(&c.strength, "strength", d.Strength, "display boost in stops at the plateau (0 = pure recovery)")
	fs.Float64Var(&c.threshold, "threshold", d.Threshold, "SDR luma where the boost ramp begins (lower = more of the image)")
	fs.Float64Var(&c.rampWidth, "ramp-width", d.RampWidth, "luma span over which the boost reaches full strength")
	fs.Float64Var(&c.maxBoost, "max-boost", d.MaxBoost, "ceiling on total boost, stops (soft shoulder)")
	fs.StringVar(&c.gainmap, "gainmap", "single", "single | rgb (per-channel colour gain map)")
	fs.IntVar(&c.gmQuality, "gainmap-quality", d.GainMapQuality, "gain map JPEG quality 1-100")
	fs.IntVar(&c.gmScale, "gainmap-scale", d.GainMapScale, "gain map downsample factor per dimension (1 = full res)")
	fs.BoolVar(&c.neutralize, "neutralize", false, "neutralize per-channel colour in clipped highlights (RGB gain map)")
	fs.StringVar(&c.demosaic, "demosaic", "ahd", "ahd | dcb | dht | vng | ppg | linear")
	fs.StringVar(&c.lens, "lens", "distortion+ca", "distortion+ca | distortion | off")
	fs.BoolVar(&c.vignetting, "vignetting", false, "apply experimental radial vignetting correction")
	fs.BoolVar(&c.noRegister, "no-register", false, "skip residual registration (debug)")
	fs.Float64Var(&c.chroma, "chroma", d.Chroma, "raw mode RGB gain saturation 0..1 (0 = neutral, 1 = full per-channel)")
	fs.BoolVar(&c.verbose, "v", false, "verbose progress to stderr")
}

func (c *convertFlags) options() (arw2uhdr.Options, error) {
	o := arw2uhdr.DefaultOptions()

	switch c.hdrMode {
	case "raw":
		o.Mode = arw2uhdr.HDRRaw
	case "highlight":
		o.Mode = arw2uhdr.HDRHighlight
	case "develop":
		o.Mode = arw2uhdr.HDRDevelop
	default:
		return o, usageErr("unknown --hdr-mode %q (raw|highlight|develop)", c.hdrMode)
	}
	o.Strength, o.Threshold, o.RampWidth, o.MaxBoost = c.strength, c.threshold, c.rampWidth, c.maxBoost

	switch c.gainmap {
	case "single":
		o.GainMap = arw2uhdr.GainMapLuminance
	case "rgb":
		o.GainMap = arw2uhdr.GainMapRGB
	default:
		return o, usageErr("unknown --gainmap %q (single|rgb)", c.gainmap)
	}
	o.GainMapQuality, o.GainMapScale = c.gmQuality, c.gmScale

	switch c.demosaic {
	case "ahd":
		o.Demosaic = arw2uhdr.DemosaicAHD
	case "dcb":
		o.Demosaic = arw2uhdr.DemosaicDCB
	case "dht":
		o.Demosaic = arw2uhdr.DemosaicDHT
	case "vng":
		o.Demosaic = arw2uhdr.DemosaicVNG
	case "ppg":
		o.Demosaic = arw2uhdr.DemosaicPPG
	case "linear":
		o.Demosaic = arw2uhdr.DemosaicLinear
	default:
		return o, usageErr("unknown --demosaic %q", c.demosaic)
	}

	switch c.lens {
	case "distortion+ca":
		o.Lens = arw2uhdr.LensDistortionCA
	case "distortion":
		o.Lens = arw2uhdr.LensDistortion
	case "off":
		o.Lens = arw2uhdr.LensOff
	default:
		return o, usageErr("unknown --lens %q (distortion+ca|distortion|off)", c.lens)
	}

	o.Vignetting = c.vignetting
	o.Register = !c.noRegister
	o.NoNeutralize = !c.neutralize
	o.Chroma = c.chroma
	if c.verbose {
		o.Logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return o, nil
}
