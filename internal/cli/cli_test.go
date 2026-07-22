package cli

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/invis/arw2uhdr"
)

func parseOpts(t *testing.T, args ...string) (arw2uhdr.Options, error) {
	t.Helper()
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	var cf convertFlags
	cf.register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cf.options()
}

func TestFlagDefaults(t *testing.T) {
	o, err := parseOpts(t)
	if err != nil {
		t.Fatal(err)
	}
	def := arw2uhdr.DefaultOptions()
	if o.Strength != def.Strength || o.Threshold != def.Threshold || o.MaxBoost != def.MaxBoost {
		t.Errorf("flag defaults diverge from DefaultOptions: %+v", o)
	}
	if o.Lens != arw2uhdr.LensDistortionCA || o.GainMap != arw2uhdr.GainMapRGB || !o.Register {
		t.Errorf("unexpected default modes: %+v", o)
	}
}

func TestFlagMapping(t *testing.T) {
	o, err := parseOpts(t, "-gainmap", "rgb", "-lens", "off", "-no-register", "-hdr-mode", "develop", "-strength", "2.5")
	if err != nil {
		t.Fatal(err)
	}
	if o.GainMap != arw2uhdr.GainMapRGB {
		t.Error("gainmap rgb not mapped")
	}
	if o.Lens != arw2uhdr.LensOff {
		t.Error("lens off not mapped")
	}
	if o.Register {
		t.Error("-no-register should disable registration")
	}
	if o.Mode != arw2uhdr.HDRDevelop {
		t.Error("hdr-mode develop not mapped")
	}
	if o.Strength != 2.5 {
		t.Errorf("strength = %v, want 2.5", o.Strength)
	}
}

func TestFlagRejectsUnknownEnum(t *testing.T) {
	if _, err := parseOpts(t, "-gainmap", "bogus"); err == nil {
		t.Error("expected error for unknown --gainmap value")
	}
	var ee *ExitError
	_, err := parseOpts(t, "-demosaic", "nope")
	if !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Errorf("bad --demosaic should be a usage ExitError, got %v", err)
	}
}

func TestInferAndDeriveOutput(t *testing.T) {
	dir := t.TempDir()
	arw := filepath.Join(dir, "DSC1.ARW")
	jpg := filepath.Join(dir, "DSC1.JPG")
	if err := os.WriteFile(jpg, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := inferJPEG(arw); got != jpg {
		t.Errorf("inferJPEG=%q want %q", got, jpg)
	}
	if got := deriveOutput(jpg); got != filepath.Join(dir, "DSC1_uhdr.jpg") {
		t.Errorf("deriveOutput=%q", got)
	}
	// no sibling JPEG
	if got := inferJPEG(filepath.Join(dir, "MISSING.ARW")); got != "" {
		t.Errorf("expected empty for missing pair, got %q", got)
	}
}

func TestConvertUsageErrors(t *testing.T) {
	var ee *ExitError
	if err := RunConvert(nil); !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Errorf("no args should be a usage error, got %v", err)
	}
	if err := RunVerify(nil); !errors.As(err, &ee) || ee.Code != ExitUsage {
		t.Errorf("verify without args should be a usage error, got %v", err)
	}
}

func TestRegisterConvertFlags(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	resolve := RegisterConvertFlags(fs)
	if err := fs.Parse([]string{"-raw-lift", "0.7", "-threshold", "0.5", "-boost-curve", "3"}); err != nil {
		t.Fatalf("parse: %v", err)
	}
	o, err := resolve()
	if err != nil {
		t.Fatal(err)
	}
	if o.RawLift != 0.7 || o.Threshold != 0.5 || o.BoostCurve != 3 {
		t.Errorf("shared convert flags not resolved: %+v", o)
	}
	// Defaults still match DefaultOptions when nothing is passed.
	fs2 := flag.NewFlagSet("t2", flag.ContinueOnError)
	r2 := RegisterConvertFlags(fs2)
	_ = fs2.Parse(nil)
	o2, _ := r2()
	if o2.Strength != arw2uhdr.DefaultOptions().Strength {
		t.Errorf("defaults diverge: %+v", o2)
	}
}
