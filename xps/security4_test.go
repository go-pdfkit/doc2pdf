package xps

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

// Fourth review: MaxMemory has to be the sum of what is LIVE, not of what is
// charged once. The inputs are the reviewer's; each asserts the 400 MiB gate
// at the default limits (peak3), and what the conversion did is logged.

// JPEG parts sized so the library's emit buffer doubles on the last image.
func TestSecPeakJPEGDoubling(t *testing.T) {
	const mib = 1 << 20
	sizes := []int{23*mib + 900<<10, 23*mib + 900<<10, 32*mib - 64, 16*mib + 100<<10}
	var body strings.Builder
	var extra []zpart
	for i, n := range sizes {
		name := fmt.Sprintf("Img/%d.jpg", i)
		fmt.Fprintf(&body, secImgPath, "/"+name)
		head := append(append([]byte(nil), stub1x1JPEG...), byte(i))
		extra = append(extra, zpart{name: name, head: head, pad: n - len(head)})
	}
	parts := append(xpsParts(page(body.String())), extra...)
	parts = append(parts, zpart{name: "pad.bin", head: noise(2*mib, 7)})
	peak3(t, "jpeg doubling", zipOf(t, parts))
}

// bigFont is Go Regular with its fpgm table replaced by n bytes of noise.
func bigFont(n int, seed int64) []byte {
	src := goregular.TTF
	nt := int(binary.BigEndian.Uint16(src[4:]))
	out := append([]byte(nil), src[:12+16*nt]...)
	for i := 0; i < nt; i++ {
		rec := src[12+16*i:]
		off := binary.BigEndian.Uint32(rec[8:])
		ln := binary.BigEndian.Uint32(rec[12:])
		data := src[off : off+ln]
		if string(rec[:4]) == "fpgm" {
			data = noise(n, seed)
		}
		binary.BigEndian.PutUint32(out[12+16*i+8:], uint32(len(out)))
		binary.BigEndian.PutUint32(out[12+16*i+12:], uint32(len(data)))
		out = append(out, data...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	return out
}

func fontsPackage(t testing.TB, count, each int) []byte {
	var body strings.Builder
	var extra []zpart
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("Resources/Fonts/f%d.ttf", i)
		fmt.Fprintf(&body, `<Glyphs OriginX="5" OriginY="%d" FontRenderingEmSize="12" FontUri="/%s" UnicodeString="Hi" Fill="#FF000000"/>`, 20+20*i, name)
		extra = append(extra, zpart{name: name, head: bigFont(each, int64(i+1))})
	}
	return zipOf(t, append(xpsParts(page(body.String())), extra...))
}

// Fonts whose instruction tables are noise: the library copies them whole into
// the subset, so each is held as the part, the subset, the deflated stream and
// the emit buffer.
func TestSecPeakNoiseFonts(t *testing.T) {
	for _, tc := range []struct {
		name  string
		count int
		each  int
	}{{"4x30", 4, 30 << 20}, {"4x31", 4, 31 << 20}, {"6x20", 6, 20 << 20}} {
		t.Run(tc.name, func(t *testing.T) {
			peak3(t, "fonts "+tc.name, fontsPackage(t, tc.count, tc.each))
		})
	}
}

// A page whose XML tree is mostly attributes: 256 on each of 17,000 elements.
func TestSecPeakXMLTree(t *testing.T) {
	var el strings.Builder
	el.WriteString("<Canvas")
	for i := 0; i < 256; i++ {
		fmt.Fprintf(&el, ` a%d=""`, i)
	}
	el.WriteString("/>")
	one := el.String()
	n := (32<<20 - 4096) / len(one)
	pg := page(strings.Repeat(one, n))
	parts := append(xpsParts(pg), zpart{name: "pad.bin", head: noise(1<<20, 3)})
	peak3(t, "xml attributes", zipOf(t, parts))
}

// fontBase is what the instruction tables of Go Regular weigh without the
// fpgm bigFont replaces.
func fontBase(t testing.TB) int64 {
	n := instructionBytes(bigFont(0, 1))
	if n <= 0 || n > 1<<16 {
		t.Fatalf("Go Regular carries %d bytes of instructions", n)
	}
	return n
}

// The instruction ceiling has an n and an n+1, and reads the table directory
// of a font that is not one without panicking.
func TestSecFontInstructionBoundary(t *testing.T) {
	base := fontBase(t)
	ok := fontsPackage(t, 1, int(maxFontInstructions-base))
	if res, err := Convert(ok, Limits{}); err != nil || res == nil {
		t.Errorf("instructions exactly at the ceiling: %v", err)
	}
	refuses(t, "ceiling+1", fontsPackage(t, 1, int(maxFontInstructions-base)+1), Limits{}, ErrLimit)
	if n := instructionBytes(nil); n != 0 {
		t.Errorf("no font: %d", n)
	}
	if n := instructionBytes(make([]byte, 11)); n != 0 {
		t.Errorf("11 bytes: %d", n)
	}
	// A directory that claims more records than the bytes hold is read as far as
	// it goes.
	short := append([]byte{0, 1, 0, 0, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0}, []byte("fpgm\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x07")...)
	if n := instructionBytes(short); n != 7 {
		t.Errorf("a truncated directory: %d, want 7", n)
	}
	for _, tag := range []string{"fpgm", "prep", "cvt "} {
		d := append([]byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}, []byte(tag+"\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x03")...)
		if n := instructionBytes(d); n != 3 {
			t.Errorf("%q: %d, want 3", tag, n)
		}
	}
	d := append([]byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}, []byte("glyf\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x03")...)
	if n := instructionBytes(d); n != 0 {
		t.Errorf("a glyf table is no instruction: %d", n)
	}
}

// A font is charged its part once and its instruction tables four more times
// (the subset, the stream, the emit buffer, the output): an extra of e bytes of
// them moves the smallest memory budget that converts by exactly 5 e.
func TestSecFontInstructionsAreChargedAtTheirCopies(t *testing.T) {
	const extra = 512 << 10
	n0 := minMemory(t, fontsPackage(t, 1, 1<<10))
	n1 := minMemory(t, fontsPackage(t, 1, 1<<10+extra))
	if n1-n0 != (1+outputFactor)*extra {
		t.Errorf("%d more bytes of instructions cost %d, want %d", extra, n1-n0, (1+outputFactor)*extra)
	}
}

// A JPEG is stored once and written out outputFactor more times: d more bytes
// of it move the smallest budget that converts by (1+outputFactor) d.
func TestSecStoredBytesAreChargedAtTheirCopies(t *testing.T) {
	mk := func(pad int) []byte {
		p := newPkg(page(fmt.Sprintf(secImgPath, "/j.jpg")))
		p.extra["j.jpg"] = append(append([]byte(nil), stub1x1JPEG...), noise(pad, 3)...)
		return p.bytes(t)
	}
	const d = 1 << 20
	n0, n1 := minMemory(t, mk(1<<10)), minMemory(t, mk(1<<10+d))
	if n1-n0 != (1+outputFactor)*d {
		t.Errorf("%d more bytes of JPEG cost %d, want %d", d, n1-n0, (1+outputFactor)*d)
	}
}

// The tree of a part is charged by the element and the attribute: the smallest
// budget is exact, and no budget is no limit.
func TestSecXMLTreeBudgetBoundary(t *testing.T) {
	in := []byte(`<a b="xy"/>`)
	const cost = nodeBytes + 1 + attrBytes + 1 + 2
	if _, err := parseXMLWithin(in, 4, 10, 100, cost); err != nil {
		t.Errorf("a tree of %d bytes under a budget of %d: %v", cost, cost, err)
	}
	if _, err := parseXMLWithin(in, 4, 10, 100, cost-1); !errors.Is(err, ErrLimit) {
		t.Errorf("a tree of %d bytes under a budget of %d: %v", cost, cost-1, err)
	}
	if _, err := parseXMLWithin(in, 4, 10, 100, 0); err != nil {
		t.Errorf("no budget: %v", err)
	}
}

// A page the budget leaves no room for is refused before it is parsed: the
// part's own bytes come out of what is left.
func TestSecXMLPartNeedsRoomForItsTree(t *testing.T) {
	b := newPkg(page(``)).bytes(t)
	refuses(t, "no room", b, Limits{MaxMemory: 1}, ErrLimit)
	c := newConv(t, b, Limits{})
	raw, err := c.part("Documents/1/Pages/1.fpage")
	if err != nil {
		t.Fatal(err)
	}
	c.lim.MaxMemory = int64(len(raw))
	if _, err := c.xmlPart("Documents/1/Pages/1.fpage"); !errors.Is(err, ErrLimit) {
		t.Errorf("a budget of exactly the part's bytes: %v", err)
	}
}

// All three at once, each just inside what it is charged for: JPEG parts,
// fonts at the instruction ceiling and a page of attribute-heavy elements. It
// converts, and the live peak stays under the gate.
func TestSecPeakFontsImagesAndTreeTogether(t *testing.T) {
	var body strings.Builder
	var extra []zpart
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("Img/%d.jpg", i)
		fmt.Fprintf(&body, secImgPath, "/"+name)
		head := append(append([]byte(nil), stub1x1JPEG...), byte(i))
		extra = append(extra, zpart{name: name, head: head, pad: 6 << 20})
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("Resources/Fonts/f%d.ttf", i)
		fmt.Fprintf(&body, `<Glyphs OriginX="5" OriginY="%d" FontRenderingEmSize="12" FontUri="/%s" UnicodeString="Hi" Fill="#FF000000"/>`, 20+20*i, name)
		extra = append(extra, zpart{name: name, head: bigFont(int(maxFontInstructions-fontBase(t)), int64(i+1))})
	}
	var el strings.Builder
	el.WriteString("<Canvas")
	for i := 0; i < 256; i++ {
		fmt.Fprintf(&el, ` a%d=""`, i)
	}
	el.WriteString("/>")
	body.WriteString(strings.Repeat(el.String(), 400))
	extra = append(extra, zpart{name: "pad.bin", head: noise(1<<20, 5)})
	res, err, _ := peak3(t, "fonts, images and tree", zipOf(t, append(xpsParts(page(body.String())), extra...)))
	if err != nil || res == nil {
		t.Errorf("a document inside the budget was refused: %v", err)
	}
}

// Case aliases and duplicate names: the part that is embedded is the one that
// was charged, whichever comes first.
func TestSecPeakAliasedParts(t *testing.T) {
	small := append([]byte(nil), stub1x1JPEG...)
	for _, tc := range []struct {
		name  string
		parts []zpart
		ref   string
	}{
		{"alias small first", []zpart{{name: "Img/A.jpg", head: small}, {name: "img/a.jpg", head: small, pad: 20 << 20}}, "/IMG/A.JPG"},
		{"alias big first", []zpart{{name: "Img/A.jpg", head: small, pad: 20 << 20}, {name: "img/a.jpg", head: small}}, "/IMG/A.JPG"},
		{"duplicate small first", []zpart{{name: "Img/A.jpg", head: small}, {name: "Img/A.jpg", head: small, pad: 20 << 20}}, "/Img/A.jpg"},
		{"duplicate big first", []zpart{{name: "Img/A.jpg", head: small, pad: 20 << 20}, {name: "Img/A.jpg", head: small}}, "/Img/A.jpg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts := append(xpsParts(page(fmt.Sprintf(secImgPath, tc.ref))), tc.parts...)
			parts = append(parts, zpart{name: "pad.bin", head: noise(1<<20, 3)})
			peak3(t, tc.name, zipOf(t, parts))
		})
	}
}
