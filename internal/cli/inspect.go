package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/invis/arw2uhdr/internal/sonylens"
)

// RunInspect implements `arw2uhdr inspect`: dump the embedded Sony lens profile.
func RunInspect(args []string) error {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "emit the parsed correction parameters as JSON")
	if err := fs.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage}
	}
	if fs.NArg() < 1 {
		return usageErr("inspect: missing <file.arw>")
	}
	path := fs.Arg(0)
	cp, err := sonylens.ReadARW(path)
	if err != nil {
		return &ExitError{Code: ExitLens, Message: fmt.Sprintf("cannot read lens metadata from %s: %v", path, err)}
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cp)
	}
	fmt.Print(cp.Summary())
	return nil
}
