// Command warptest applies the Sony embedded distortion+CA correction (pure Go) to a
// decoded RAW image and writes the corrected result. Used to validate the warp against
// the camera JPEG.
//
// usage: warptest <decoded_raw.png> <file.ARW> <out_corrected.png> [cropScale] [ca=1|0]
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/invis/arw2uhdr/internal/imaging"
	"github.com/invis/arw2uhdr/internal/sonylens"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: warptest <raw.png> <file.ARW> <out.png> [cropScale] [ca=1|0]")
		os.Exit(2)
	}
	rawPNG, arw, out := os.Args[1], os.Args[2], os.Args[3]
	cfg := sonylens.DefaultWarp()
	if len(os.Args) > 4 {
		if v, err := strconv.ParseFloat(os.Args[4], 64); err == nil {
			cfg.CropScale = v
		}
	}
	if len(os.Args) > 5 {
		cfg.ApplyCA = os.Args[5] == "1" || os.Args[5] == "ca=1"
	}

	cp, err := sonylens.ReadARW(arw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read params:", err)
		os.Exit(1)
	}
	src, err := imaging.LoadImage(rawPNG)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load raw:", err)
		os.Exit(1)
	}
	t0 := time.Now()
	dst := sonylens.Warp(src, cp, cfg)
	dt := time.Since(t0)
	if err := dst.SavePNG8(out); err != nil {
		fmt.Fprintln(os.Stderr, "save:", err)
		os.Exit(1)
	}
	fmt.Printf("warp %dx%d  cropScale=%.3f CA=%v  distN=%d caTotal=%d  %.0fms -> %s\n",
		dst.W, dst.H, cfg.CropScale, cfg.ApplyCA, cp.DistortionN, cp.CATotal,
		float64(dt.Milliseconds()), out)
}
