package ultrahdr

import (
	"encoding/binary"
	"strings"
	"testing"

	"github.com/invis/arw2uhdr/internal/gainmap"
)

func TestSingleChannelXMPIsScalar(t *testing.T) {
	m := gainmap.Metadata{MaxLog2: [3]float64{1.5, 1.5, 1.5}, Gamma: [3]float64{1, 1, 1}, CapacityMax: 1.5}
	xmp := gainmapHdrgmXMP(m)
	if !strings.Contains(xmp, `hdrgm:GainMapMax="1.5"`) {
		t.Errorf("single-channel XMP missing scalar GainMapMax:\n%s", xmp)
	}
	if strings.Contains(xmp, "<rdf:Seq>") {
		t.Error("single-channel XMP should not contain rdf:Seq lists")
	}
}

func TestMultiChannelXMPUsesSeq(t *testing.T) {
	m := gainmap.Metadata{
		MultiChannel: true,
		MaxLog2:      [3]float64{1, 2, 3}, Gamma: [3]float64{1, 1, 1}, CapacityMax: 3,
	}
	xmp := gainmapHdrgmXMP(m)
	if !strings.Contains(xmp, "<hdrgm:GainMapMax><rdf:Seq>") {
		t.Errorf("multi-channel XMP missing GainMapMax rdf:Seq:\n%s", xmp)
	}
	for _, v := range []string{"<rdf:li>1</rdf:li>", "<rdf:li>2</rdf:li>", "<rdf:li>3</rdf:li>"} {
		if !strings.Contains(xmp, v) {
			t.Errorf("multi-channel XMP missing per-channel value %q", v)
		}
	}
}

func TestApp1XMPMarkerAndLength(t *testing.T) {
	seg := app1XMP("<x/>")
	if seg[0] != 0xFF || seg[1] != 0xE1 {
		t.Fatal("APP1 segment must start with FFE1")
	}
	got := binary.BigEndian.Uint16(seg[2:4])
	// length field counts everything after the marker (length bytes + payload)
	if int(got) != len(seg)-2 {
		t.Errorf("APP1 length field = %d, want %d", got, len(seg)-2)
	}
	if !strings.Contains(string(seg), "http://ns.adobe.com/xap/1.0/") {
		t.Error("APP1 payload missing the XMP namespace header")
	}
}

func TestPrimaryXMPCarriesGainMapLength(t *testing.T) {
	xmp := primaryGContainerXMP(4242)
	if !strings.Contains(xmp, `Item:Length="4242"`) {
		t.Errorf("primary XMP missing gain-map length:\n%s", xmp)
	}
	if !strings.Contains(xmp, `Item:Semantic="Primary"`) || !strings.Contains(xmp, `Item:Semantic="GainMap"`) {
		t.Error("primary XMP missing Primary/GainMap container items")
	}
}
