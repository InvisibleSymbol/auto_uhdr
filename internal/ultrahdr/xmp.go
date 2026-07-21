package ultrahdr

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/invis/arw2uhdr/internal/gainmap"
)

const (
	xmpHeader = "http://ns.adobe.com/xap/1.0/\x00"
	// xpacket begin carries the UTF-8 BOM (U+FEFF), written via escape to keep it out of the source.
	xpacketHi = "<?xpacket begin=\"\uFEFF\" id=\"W5M0MpCehiHzreSzNTczkc9d\"?>"
	xpacketLo = `<?xpacket end="w"?>`
)

func g(v float64) string {
	return strconv.FormatFloat(v, 'g', 6, 64)
}

// primaryGContainerXMP builds the primary image's XMP: a GContainer directory declaring the
// Primary + GainMap items (with the gain map's byte length) and hdrgm:Version.
func primaryGContainerXMP(gainMapLen int) string {
	var b strings.Builder
	b.WriteString(xpacketHi)
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="arw2uhdr">`)
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" `)
	b.WriteString(`xmlns:Container="http://ns.google.com/photos/1.0/container/" `)
	b.WriteString(`xmlns:Item="http://ns.google.com/photos/1.0/container/item/" `)
	b.WriteString(`xmlns:hdrgm="http://ns.adobe.com/hdr-gain-map/1.0/" `)
	b.WriteString(`hdrgm:Version="1.0">`)
	b.WriteString(`<Container:Directory><rdf:Seq>`)
	b.WriteString(`<rdf:li rdf:parseType="Resource"><Container:Item Item:Semantic="Primary" Item:Mime="image/jpeg"/></rdf:li>`)
	b.WriteString(`<rdf:li rdf:parseType="Resource"><Container:Item Item:Semantic="GainMap" Item:Mime="image/jpeg" Item:Length="`)
	b.WriteString(strconv.Itoa(gainMapLen))
	b.WriteString(`"/></rdf:li>`)
	b.WriteString(`</rdf:Seq></Container:Directory>`)
	b.WriteString(`</rdf:Description></rdf:RDF></x:xmpmeta>`)
	b.WriteString(xpacketLo)
	return b.String()
}

// gainmapHdrgmXMP builds the gain map image's hdrgm metadata XMP.
// Single-channel uses scalar attributes; multi-channel uses rdf:Seq element lists.
func gainmapHdrgmXMP(m gainmap.Metadata) string {
	var b strings.Builder
	b.WriteString(xpacketHi)
	b.WriteString(`<x:xmpmeta xmlns:x="adobe:ns:meta/" x:xmptk="arw2uhdr">`)
	b.WriteString(`<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#">`)
	b.WriteString(`<rdf:Description rdf:about="" xmlns:hdrgm="http://ns.adobe.com/hdr-gain-map/1.0/" `)
	b.WriteString(`hdrgm:Version="1.0" `)
	baseHDR := "False"
	if m.BaseIsHDR {
		baseHDR = "True"
	}
	b.WriteString(`hdrgm:BaseRenditionIsHDR="` + baseHDR + `" `)

	if !m.MultiChannel {
		// scalar attributes
		fmt.Fprintf(&b, `hdrgm:GainMapMin="%s" `, g(m.MinLog2[0]))
		fmt.Fprintf(&b, `hdrgm:GainMapMax="%s" `, g(m.MaxLog2[0]))
		fmt.Fprintf(&b, `hdrgm:Gamma="%s" `, g(m.Gamma[0]))
		fmt.Fprintf(&b, `hdrgm:OffsetSDR="%s" `, g(m.OffsetSDR[0]))
		fmt.Fprintf(&b, `hdrgm:OffsetHDR="%s" `, g(m.OffsetHDR[0]))
		fmt.Fprintf(&b, `hdrgm:HDRCapacityMin="%s" `, g(m.CapacityMin))
		fmt.Fprintf(&b, `hdrgm:HDRCapacityMax="%s"`, g(m.CapacityMax))
		b.WriteString(`/>`)
	} else {
		// capacity is scalar even in multichannel
		fmt.Fprintf(&b, `hdrgm:HDRCapacityMin="%s" `, g(m.CapacityMin))
		fmt.Fprintf(&b, `hdrgm:HDRCapacityMax="%s">`, g(m.CapacityMax))
		seq := func(name string, vals [3]float64) {
			b.WriteString(`<hdrgm:` + name + `><rdf:Seq>`)
			for c := range 3 {
				b.WriteString(`<rdf:li>` + g(vals[c]) + `</rdf:li>`)
			}
			b.WriteString(`</rdf:Seq></hdrgm:` + name + `>`)
		}
		seq("GainMapMin", m.MinLog2)
		seq("GainMapMax", m.MaxLog2)
		seq("Gamma", m.Gamma)
		seq("OffsetSDR", m.OffsetSDR)
		seq("OffsetHDR", m.OffsetHDR)
		b.WriteString(`</rdf:Description>`)
	}
	b.WriteString(`</rdf:RDF></x:xmpmeta>`)
	b.WriteString(xpacketLo)
	return b.String()
}

// app1XMP wraps an XMP packet in an APP1 marker segment.
func app1XMP(xmp string) []byte {
	payload := xmpHeader + xmp
	total := len(payload) + 2 // + length field
	seg := make([]byte, 0, total+2)
	seg = append(seg, 0xFF, 0xE1)
	seg = append(seg, byte(total>>8), byte(total))
	seg = append(seg, payload...)
	return seg
}
