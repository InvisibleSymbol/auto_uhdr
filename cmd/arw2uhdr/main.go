// Command arw2uhdr converts a Sony ARW + camera JPEG pair into an Ultra HDR (JPEG_R) image.
//
// The camera JPEG is preserved byte-for-byte as the SDR base; the RAW supplies lens-corrected,
// registered highlight recovery and drives the gain map. See --help for the dials.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/invis/arw2uhdr/internal/hdrbuild"
	"github.com/invis/arw2uhdr/internal/pipeline"
	"github.com/invis/arw2uhdr/internal/raw"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

const version = "0.9.0"

// exit codes (stable contract for batch scripts)
const (
	exitOK       = 0
	exitUsage    = 2
	exitInput    = 3
	exitDecode   = 4
	exitLensMeta = 5
	exitEncode   = 7
	exitWrite    = 8
)

func main() {
	fs := flag.NewFlagSet("arw2uhdr", flag.ExitOnError)
	fs.Usage = usage(fs)

	output := fs.String("o", "", "output path (default: <jpg-base>_uhdr.jpg)")
	skipExisting := fs.Bool("skip-existing", false, "exit 0 without work if output exists")

	hdrMode := fs.String("hdr-mode", "highlight", "highlight | develop")
	strength := fs.Float64("strength", 1.5, "display boost in stops at the plateau (0 = pure recovery)")
	threshold := fs.Float64("threshold", 0.3, "SDR luma where the boost ramp begins (lower = more of the image)")
	rampWidth := fs.Float64("ramp-width", 0.35, "luma span over which the boost reaches full strength")
	maxBoost := fs.Float64("max-boost", 3.0, "ceiling on total boost, stops (soft shoulder)")

	gainmapKind := fs.String("gainmap", "single", "single | rgb (per-channel color gain map)")
	gmQuality := fs.Int("gainmap-quality", 90, "gain map JPEG quality 1-100")
	gmScale := fs.Int("gainmap-scale", 4, "gain map downsample factor per dimension (1 = full res)")

	demosaic := fs.String("demosaic", "ahd", "ahd | dcb | dht | vng | ppg | linear")
	lens := fs.String("lens-correction", "distortion+ca", "distortion+ca | distortion | off")
	noRegister := fs.Bool("no-register", false, "skip residual registration (debug)")

	verify := fs.Bool("verify", false, "verify the written file's Ultra HDR structure")
	jsonOut := fs.Bool("json", false, "emit a machine-readable JSON result on stdout")
	verbose := fs.Bool("v", false, "verbose progress to stderr")
	showVersion := fs.Bool("version", false, "print version")

	fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("arw2uhdr", version)
		return
	}
	args := fs.Args()
	if len(args) < 1 {
		fs.Usage()
		os.Exit(exitUsage)
	}

	arwPath := args[0]
	jpgPath := ""
	if len(args) > 1 {
		jpgPath = args[1]
	} else {
		jpgPath = inferJPEG(arwPath)
		if jpgPath == "" {
			fail(*jsonOut, exitInput, "no paired JPEG found for %s (tried .JPG/.jpg/.JPEG/.jpeg)", arwPath)
		}
	}
	for _, p := range []string{arwPath, jpgPath} {
		if _, err := os.Stat(p); err != nil {
			fail(*jsonOut, exitInput, "cannot read %s", p)
		}
	}

	outPath := *output
	if outPath == "" {
		base := strings.TrimSuffix(jpgPath, filepath.Ext(jpgPath))
		outPath = base + "_uhdr.jpg"
	}
	if *skipExisting {
		if _, err := os.Stat(outPath); err == nil {
			if *jsonOut {
				json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "skipped", "output": outPath})
			} else {
				fmt.Println("exists, skipping:", outPath)
			}
			return
		}
	}

	// assemble options
	o := pipeline.DefaultOpts()
	o.Verbose = *verbose
	switch *hdrMode {
	case "highlight":
		o.HDR.Mode = hdrbuild.ModeHighlight
	case "develop":
		o.HDR.Mode = hdrbuild.ModeDevelop
	default:
		fail(*jsonOut, exitUsage, "unknown --hdr-mode %q", *hdrMode)
	}
	o.HDR.Strength = *strength
	o.HDR.Threshold = *threshold
	o.HDR.RampWidth = *rampWidth
	o.HDR.MaxBoostStops = *maxBoost
	o.GainMap.MaxBoostStops = *maxBoost
	switch *gainmapKind {
	case "single":
		o.GainMap.MultiChannel = false
	case "rgb":
		o.GainMap.MultiChannel = true
	default:
		fail(*jsonOut, exitUsage, "unknown --gainmap %q (single|rgb)", *gainmapKind)
	}
	o.GainMap.ScaleFactor = *gmScale
	o.Encode.GainMapQuality = *gmQuality
	switch *demosaic {
	case "ahd":
		o.Demosaic = raw.DemosaicAHD
	case "dcb":
		o.Demosaic = raw.DemosaicDCB
	case "dht":
		o.Demosaic = raw.DemosaicDHT
	case "vng":
		o.Demosaic = raw.DemosaicVNG
	case "ppg":
		o.Demosaic = raw.DemosaicPPG
	case "linear":
		o.Demosaic = raw.DemosaicLinear
	default:
		fail(*jsonOut, exitUsage, "unknown --demosaic %q", *demosaic)
	}
	switch *lens {
	case "distortion+ca":
		o.LensCorr, o.ApplyCA = true, true
	case "distortion":
		o.LensCorr, o.ApplyCA = true, false
	case "off":
		o.LensCorr = false
	default:
		fail(*jsonOut, exitUsage, "unknown --lens-correction %q", *lens)
	}
	o.Register = !*noRegister

	res, err := pipeline.Process(arwPath, jpgPath, outPath, o)
	if err != nil {
		code := exitEncode
		var se *pipeline.StageError
		if errors.As(err, &se) {
			switch se.Stage {
			case "decode":
				code = exitDecode
			case "lensmeta":
				code = exitLensMeta
			case "sdr":
				code = exitInput
			case "write":
				code = exitWrite
			}
		}
		fail(*jsonOut, code, "%v", err)
	}

	if *verify {
		data, err := os.ReadFile(outPath)
		if err == nil {
			err = ultrahdr.Verify(data)
		}
		if err != nil {
			fail(*jsonOut, exitEncode, "verify failed: %v", err)
		}
		if *verbose {
			fmt.Fprintln(os.Stderr, "arw2uhdr: verify OK")
		}
	}

	if *jsonOut {
		out := map[string]any{"status": "ok", "input_arw": arwPath, "input_jpg": jpgPath}
		b, _ := json.Marshal(res)
		var m map[string]any
		json.Unmarshal(b, &m)
		for k, v := range m {
			out[k] = v
		}
		json.NewEncoder(os.Stdout).Encode(out)
	} else {
		fmt.Printf("%s  (%dx%d, %d-ch gain map, max boost %.2f stops, %d KB, %.1fs)\n",
			outPath, res.Width, res.Height, res.GainMapChannels, res.MaxBoostStops,
			res.OutputBytes/1024, float64(res.ElapsedMs)/1000)
	}
}

func inferJPEG(arwPath string) string {
	base := strings.TrimSuffix(arwPath, filepath.Ext(arwPath))
	for _, ext := range []string{".JPG", ".jpg", ".JPEG", ".jpeg"} {
		if _, err := os.Stat(base + ext); err == nil {
			return base + ext
		}
	}
	return ""
}

func fail(jsonOut bool, code int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	if jsonOut {
		json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "error", "code": code, "message": msg})
	}
	fmt.Fprintln(os.Stderr, "arw2uhdr:", msg)
	os.Exit(code)
}

func usage(fs *flag.FlagSet) func() {
	return func() {
		fmt.Fprintf(os.Stderr, `arw2uhdr %s — Sony ARW + JPEG → Ultra HDR (JPEG_R)

usage: arw2uhdr [flags] <input.arw> [input.jpg]

The camera JPEG is kept byte-for-byte as the SDR base (it must be the full-res JPEG shot
alongside the RAW). If input.jpg is omitted it is inferred from the ARW basename.

flags:
`, version)
		fs.PrintDefaults()
		fmt.Fprintf(os.Stderr, `
exit codes: 0 ok, 2 usage, 3 input, 4 raw decode, 5 lens metadata, 7 encode, 8 write
`)
	}
}
