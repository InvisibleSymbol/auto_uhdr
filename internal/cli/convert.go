package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/invis/arw2uhdr"
	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// RunConvert implements `arw2uhdr convert`.
func RunConvert(args []string) error {
	fs := flag.NewFlagSet("convert", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf convertFlags
	cf.register(fs)
	output := fs.String("o", "", "output path (default: <jpg-base>_uhdr.jpg)")
	skip := fs.Bool("skip-existing", false, "exit 0 without work if the output exists")
	verify := fs.Bool("verify", false, "verify the written file's Ultra HDR structure")
	jsonOut := fs.Bool("json", false, "emit a machine-readable JSON result on stdout")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: arw2uhdr convert [flags] <input.arw> [input.jpg]\n\nflags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage}
	}

	pos := fs.Args()
	if len(pos) < 1 {
		fs.Usage()
		return usageErr("convert: missing <input.arw>")
	}
	arw := pos[0]
	jpg := ""
	if len(pos) > 1 {
		jpg = pos[1]
	} else if jpg = inferJPEG(arw); jpg == "" {
		return inputErr("no paired JPEG found for %s (tried .JPG/.jpg/.JPEG/.jpeg)", arw)
	}
	for _, p := range []string{arw, jpg} {
		if _, err := os.Stat(p); err != nil {
			return inputErr("cannot read %s", p)
		}
	}

	out := *output
	if out == "" {
		out = deriveOutput(jpg)
	}
	if *skip {
		if _, err := os.Stat(out); err == nil {
			if *jsonOut {
				_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "skipped", "output": out})
			} else {
				fmt.Println("exists, skipping:", out)
			}
			return nil
		}
	}

	opts, err := cf.options()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	res, err := arw2uhdr.Convert(ctx, arw2uhdr.Input{ARW: arw, JPEG: jpg, Output: out}, opts)
	if err != nil {
		return convertExit(err)
	}
	if *verify {
		data, rerr := os.ReadFile(out)
		if rerr == nil {
			rerr = ultrahdr.Verify(data)
		}
		if rerr != nil {
			return &ExitError{Code: ExitEncode, Message: "verify failed: " + rerr.Error()}
		}
	}

	if *jsonOut {
		printJSON(map[string]any{"status": "ok", "input_arw": arw, "input_jpg": jpg}, res)
	} else {
		fmt.Printf("%s  (%dx%d, %d-ch gain map, max boost %.2f stops, %d KB, %.1fs)\n",
			res.Output, res.Width, res.Height, res.GainMapChannels, res.MaxBoostStops,
			res.OutputBytes/1024, float64(res.ElapsedMs)/1000)
	}
	return nil
}

// printJSON merges a Result into an envelope map and writes it as one JSON line.
func printJSON(envelope map[string]any, res *arw2uhdr.Result) {
	b, _ := json.Marshal(res)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, v := range m {
		envelope[k] = v
	}
	_ = json.NewEncoder(os.Stdout).Encode(envelope)
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

func deriveOutput(jpgPath string) string {
	return strings.TrimSuffix(jpgPath, filepath.Ext(jpgPath)) + "_uhdr.jpg"
}
