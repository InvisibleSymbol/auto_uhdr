package sonylens

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// samplePath resolves a sample ARW relative to the repo; tests skip if unavailable.
func samplePath(name string) string {
	return filepath.Join("..", "..", "..", "..", "samples", name)
}

func TestReadARW_DSC01617(t *testing.T) {
	p := samplePath("DSC01617.ARW")
	if _, err := os.Stat(p); err != nil {
		t.Skipf("sample %s not present", p)
	}
	cp, err := ReadARW(p)
	if err != nil {
		t.Fatalf("ReadARW: %v", err)
	}
	if cp.Model != "DSC-RX100M7A" {
		t.Errorf("model=%q, want DSC-RX100M7A", cp.Model)
	}
	if cp.Orientation != 1 {
		t.Errorf("orientation=%d, want 1", cp.Orientation)
	}
	// Values below are exiftool-verified ground truth (9mm capture).
	wantDist := []int16{1136, 1066, 901, 641, 311, -69, -485, -922, -1364, -1771, -2134}
	if cp.DistortionN != 11 || !reflect.DeepEqual(cp.Distortion, wantDist) {
		t.Errorf("distortion n=%d %v,\n want 11 %v", cp.DistortionN, cp.Distortion, wantDist)
	}
	wantVig := []int16{0, 0, 0, 0, 0, 0, 0, 0, 201, 567, 1083, 1989, 3473, 5538, 6994, 6994}
	if cp.VignettingN != 16 || !reflect.DeepEqual(cp.Vignetting, wantVig) {
		t.Errorf("vignetting n=%d %v,\n want 16 %v", cp.VignettingN, cp.Vignetting, wantVig)
	}
	wantRed := []int16{0, 512, 768, 896, 896, 1024, 1024, 896, 768, 1024, 1408}
	wantBlue := []int16{0, 1024, 896, 896, 1152, 1152, 1152, 1152, 1152, 0, -1664}
	if cp.CATotal != 22 || !reflect.DeepEqual(cp.CARed, wantRed) || !reflect.DeepEqual(cp.CABlue, wantBlue) {
		t.Errorf("CA total=%d\n red=%v\n blue=%v", cp.CATotal, cp.CARed, cp.CABlue)
	}
}

func TestReadARW_Orientation(t *testing.T) {
	cases := map[string]int{
		"DSC01063.ARW": 6, // rotate 90 CW (portrait)
		"DSC01885.ARW": 8, // rotate 270 CW (portrait)
		"DSC02028.ARW": 1, // landscape
	}
	for name, want := range cases {
		p := samplePath(name)
		if _, err := os.Stat(p); err != nil {
			t.Skipf("sample %s not present", p)
		}
		cp, err := ReadARW(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if cp.Orientation != want {
			t.Errorf("%s orientation=%d, want %d", name, cp.Orientation, want)
		}
	}
}

func TestSetCA_Split(t *testing.T) {
	// count-prefixed: total=4 -> 2 red + 2 blue
	c := &CorrParams{}
	c.setCA([]int16{4, 10, 20, 30, 40, 0, 0})
	if c.CATotal != 4 || !reflect.DeepEqual(c.CARed, []int16{10, 20}) || !reflect.DeepEqual(c.CABlue, []int16{30, 40}) {
		t.Errorf("bad CA split: total=%d red=%v blue=%v", c.CATotal, c.CARed, c.CABlue)
	}
}
