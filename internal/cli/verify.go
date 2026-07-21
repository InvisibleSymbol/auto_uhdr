package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/invis/arw2uhdr/internal/ultrahdr"
)

// RunVerify implements `arw2uhdr verify`.
func RunVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOut := fs.Bool("json", false, "emit a machine-readable JSON result")
	if err := fs.Parse(args); err != nil {
		return &ExitError{Code: ExitUsage}
	}
	if fs.NArg() < 1 {
		return usageErr("verify: missing <file.jpg>")
	}
	path := fs.Arg(0)
	data, err := os.ReadFile(path)
	if err != nil {
		return inputErr("cannot read %s", path)
	}

	verr := ultrahdr.Verify(data)
	if *jsonOut {
		res := map[string]any{"file": path, "valid": verr == nil}
		if verr != nil {
			res["error"] = verr.Error()
		}
		_ = json.NewEncoder(os.Stdout).Encode(res)
	}
	if verr != nil {
		return &ExitError{Code: ExitEncode, Message: fmt.Sprintf("%s: not a valid Ultra HDR: %v", path, verr)}
	}
	if !*jsonOut {
		fmt.Println("OK:", path)
	}
	return nil
}
