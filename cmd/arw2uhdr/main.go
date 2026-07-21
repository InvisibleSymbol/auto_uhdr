// Command arw2uhdr converts Sony ARW + camera JPEG pairs into Ultra HDR (JPEG_R)
// images, and inspects/verifies them.
//
// Usage:
//
//	arw2uhdr convert [flags] <input.arw> [input.jpg]   # default command
//	arw2uhdr batch   [flags] <dir|file>...
//	arw2uhdr verify   <file.jpg>
//	arw2uhdr inspect  <file.arw>
//	arw2uhdr version
//
// If the first argument is not a known command it is treated as convert, so
// `arw2uhdr photo.arw` still works. Exit codes are a stable contract for scripts:
// 0 ok, 2 usage, 3 input, 4 raw decode, 5 lens metadata, 6 render, 7 encode, 8 write.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/invis/arw2uhdr/internal/cli"
)

const version = "1.0.0"

func main() {
	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		cmd = args[0]
	}

	var err error
	switch cmd {
	case "convert":
		err = cli.RunConvert(args[1:])
	case "batch":
		err = cli.RunBatch(args[1:])
	case "verify":
		err = cli.RunVerify(args[1:])
	case "inspect":
		err = cli.RunInspect(args[1:])
	case "version", "--version", "-version":
		fmt.Println("arw2uhdr", version)
		return
	case "help", "-h", "--help", "":
		usage()
		if cmd == "" {
			os.Exit(int(cli.ExitUsage))
		}
		return
	default:
		// Unknown command: treat the whole arg list as a convert invocation so
		// `arw2uhdr photo.arw` and `arw2uhdr -o x.jpg photo.arw` keep working.
		err = cli.RunConvert(args)
	}

	if err != nil {
		var ce *cli.ExitError
		if errors.As(err, &ce) {
			if ce.Message != "" {
				fmt.Fprintln(os.Stderr, "arw2uhdr:", ce.Message)
			}
			os.Exit(int(ce.Code))
		}
		fmt.Fprintln(os.Stderr, "arw2uhdr:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `arw2uhdr %s — Sony ARW + JPEG → Ultra HDR (JPEG_R)

usage:
  arw2uhdr convert [flags] <input.arw> [input.jpg]   convert a pair (default command)
  arw2uhdr batch   [flags] <dir|file>...             convert every paired ARW under paths
  arw2uhdr verify  <file.jpg>                         check a file's Ultra HDR structure
  arw2uhdr inspect <file.arw>                         print the embedded Sony lens profile
  arw2uhdr version                                    print version

Run "arw2uhdr convert -h" or "arw2uhdr batch -h" for command flags.
`, version)
}
