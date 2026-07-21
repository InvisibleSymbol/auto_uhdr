// Command arwmeta dumps the embedded Sony lens-correction parameters from ARW files.
// Debug/validation tool: compare its output against exiftool to confirm the pure-Go reader.
package main

import (
	"fmt"
	"os"

	"github.com/invis/arw2uhdr/internal/sonylens"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: arwmeta <file.ARW> [more.ARW ...]")
		os.Exit(2)
	}
	for _, p := range os.Args[1:] {
		cp, err := sonylens.ReadARW(p)
		if err != nil {
			fmt.Printf("%s: ERROR %v\n", p, err)
			continue
		}
		fmt.Printf("== %s ==\n", p)
		fmt.Printf("  model=%q orientation=%d crop=(t%d,l%d %dx%d)\n",
			cp.Model, cp.Orientation, cp.CropTop, cp.CropLeft, cp.CropW, cp.CropH)
		fmt.Printf("  distortion(n=%d): %v\n", cp.DistortionN, cp.Distortion)
		fmt.Printf("  vignetting(n=%d): %v\n", cp.VignettingN, cp.Vignetting)
		fmt.Printf("  ca(total=%d) red:  %v\n", cp.CATotal, cp.CARed)
		fmt.Printf("            blue: %v\n", cp.CABlue)
	}
}
