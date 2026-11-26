package xps

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
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
	if n1-n0 != (1+fontCopies)*extra {
		t.Errorf("%d more bytes of instructions cost %d, want %d", extra, n1-n0, (1+fontCopies)*extra)
	}
}

// minPrepare is the smallest MaxMemory at which b is drawn (Prepare), by
// bisection.
func minPrepare(t *testing.T, b []byte) int64 {
	t.Helper()
	lo, hi := int64(1), int64(192<<20)
	for lo < hi {
		mid := (lo + hi) / 2
		if _, err := Prepare(b, Limits{MaxMemory: mid}); err == nil {
			hi = mid
		} else if errors.Is(err, ErrLimit) {
			lo = mid + 1
		} else {
			t.Fatalf("MaxMemory %d: %v", mid, err)
		}
	}
	return lo
}

// A JPEG is stored once, at the size the library reports for it, and the PDF is
// streamed: d more bytes of it move the smallest budget that draws it by d, and
// the one that returns the bytes (Convert holds a buffer of them too) by 2 d.
func TestSecStoredBytesAreChargedAtTheirCopies(t *testing.T) {
	mk := func(pad int) []byte {
		p := newPkg(page(fmt.Sprintf(secImgPath, "/j.jpg")))
		p.extra["j.jpg"] = append(append([]byte(nil), stub1x1JPEG...), noise(pad, 3)...)
		return p.bytes(t)
	}
	const d = 1 << 20
	// Big enough that the image, not the page around it, decides the budget.
	const base = 4 << 20
	if n0, n1 := minPrepare(t, mk(base)), minPrepare(t, mk(base+d)); n1-n0 != 2*d {
		// The part just read and its stored copy are alive together at embed.
		t.Errorf("%d more bytes of JPEG cost %d to draw, want %d", d, n1-n0, 2*d)
	}
	if n0, n1 := minMemory(t, mk(base)), minMemory(t, mk(base+d)); n1-n0 != 2*d {
		t.Errorf("%d more bytes of JPEG cost %d to convert, want %d", d, n1-n0, 2*d)
	}
}

// A font is charged as soon as it is read, and its instruction tables as soon as
// their sizes are known, each BEFORE the parser looks at it: a part that is no
// usable font is refused on memory and not read as a bad font.
func TestSecFontIsChargedBeforeItIsParsed(t *testing.T) {
	load := func(b []byte, mem int64) error {
		p := newPkg(page(``))
		p.extra["Resources/Fonts/t.ttf"] = b
		c := newConv(t, p.bytes(t), Limits{MaxMemory: mem})
		_, err := c.font("/Resources/Fonts/t.ttf", "/Resources/Fonts/t.ttf")
		return err
	}
	// Too big for the budget, and a collection the library will not open.
	big := append([]byte("ttcf"), noise(4_000, 5)...)
	if err := load(big, 1<<10); !errors.Is(err, ErrLimit) {
		t.Errorf("a collection larger than the budget: %v", err)
	}
	if err := load(big, 1<<20); errors.Is(err, ErrLimit) {
		t.Errorf("a collection within the budget: %v", err)
	}
	// A directory that claims an fpgm of n bytes in a part of 28: held four more
	// times than it is, 2,000 of them do not fit 1 KiB, one byte of it does.
	stub := func(n uint32) []byte {
		d := []byte{0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0}
		d = append(d, "fpgm"...)
		d = append(d, 0, 0, 0, 0, 0, 0, 0, 28)
		return binary.BigEndian.AppendUint32(d, n)
	}
	if err := load(stub(2_000), 1<<10); !errors.Is(err, ErrLimit) {
		t.Errorf("2,000 bytes of instructions in 1 KiB: %v", err)
	}
	if err := load(stub(1), 1<<10); errors.Is(err, ErrLimit) {
		t.Errorf("a stub of one instruction byte: %v", err)
	}
}

// The header of a PNG says what its decode will cost; the budget is checked from
// it before the decoder runs, so a truncated file that would fail to decode is
// refused on memory, not read as a bad image.
func TestSecImageDecodeIsChargedBeforeItRuns(t *testing.T) {
	var enc bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	copy(img.Pix, noise(len(img.Pix), 9))
	if err := png.Encode(&enc, img); err != nil {
		t.Fatal(err)
	}
	cut := enc.Bytes()[:enc.Len()*3/4]
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.png")))
	p.extra["i.png"] = cut
	b := p.bytes(t)
	// With room the unreadable image is skipped, not an error.
	if _, err := Convert(b, Limits{}); errors.Is(err, ErrLimit) {
		t.Fatalf("a truncated PNG with room: %v", err)
	}
	refuses(t, "decode", b, Limits{MaxMemory: 120_000}, ErrLimit)
}

// The tree of a part is charged by the element and the attribute: the smallest
// budget is exact, and no budget is no limit.
func TestSecXMLTreeBudgetBoundary(t *testing.T) {
	in := []byte(`<a b="xy"/>`)
	cost := elementCost(1) + 1 + 1 + 2
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

// Prepare draws and WriteTo streams: the bytes are the ones Convert returns,
// counted, and the document can be written again.
func TestPrepareWriteToMatchesConvert(t *testing.T) {
	b := newPkg(page(`<Path Data="M 10,10 L 90,10 90,60 Z" Fill="#FFFF0000"/>`)).bytes(t)
	res, err := Convert(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	d, err := Prepare(b, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Pages != res.Pages || d.Pages != 1 {
		t.Errorf("pages: %d, %d", d.Pages, res.Pages)
	}
	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		n, err := d.WriteTo(&out)
		if err != nil || n != int64(out.Len()) || n != int64(len(res.PDF)) {
			t.Fatalf("write %d: n=%d len=%d want %d err=%v", i, n, out.Len(), len(res.PDF), err)
		}
	}
}

// A writer that fails ends the write with its error, and says how far it got.
func TestWriteToReportsItsWriter(t *testing.T) {
	d, err := Prepare(newPkg(page(`<Path Data="M 10,10 L 90,10 90,60 Z" Fill="#FFFF0000"/>`)).bytes(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("disk full")
	n, err := d.WriteTo(&failAfter{n: 100, err: boom})
	if !errors.Is(err, boom) || n != 100 {
		t.Errorf("n=%d err=%v", n, err)
	}
}

type failAfter struct {
	n   int
	err error
}

func (f *failAfter) Write(p []byte) (int, error) {
	if len(p) > f.n {
		n := f.n
		f.n = 0
		return n, f.err
	}
	f.n -= len(p)
	return len(p), nil
}

// An image is charged what the library says it stores, not the bound it is
// admitted by: a flat PNG deflates to a few bytes, far under 3 a pixel.
func TestImageIsChargedAtItsStoredLength(t *testing.T) {
	var enc bytes.Buffer
	if err := png.Encode(&enc, image.NewNRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.png")))
	p.extra["i.png"] = enc.Bytes()
	d, err := Prepare(p.bytes(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if d.c.stored <= 0 || d.c.stored >= 3*256*256/10 {
		t.Errorf("a flat 256x256 PNG is charged %d bytes", d.c.stored)
	}
}

// Convert holds the bytes it returns beside the document: it needs the room for
// them, which Prepare, streaming, does not.
func TestConvertChargesItsBuffer(t *testing.T) {
	p := newPkg(page(fmt.Sprintf(secImgPath, "/j.jpg")))
	p.extra["j.jpg"] = append(append([]byte(nil), stub1x1JPEG...), noise(4<<20, 3)...)
	b := p.bytes(t)
	n := minPrepare(t, b)
	if _, err := Prepare(b, Limits{MaxMemory: n}); err != nil {
		t.Fatalf("Prepare at its minimum: %v", err)
	}
	refuses(t, "convert beside prepare", b, Limits{MaxMemory: n}, ErrLimit)
}

// A write that fails is an error from Convert, not a PDF cut short.
func TestConvertReportsAFailedWrite(t *testing.T) {
	old := writeDoc
	t.Cleanup(func() { writeDoc = old })
	boom := errors.New("disk on fire")
	writeDoc = func(*Doc, io.Writer) (int64, error) { return 0, boom }
	r, err := Convert(newPkg(page(``)).bytes(t), Limits{})
	if !errors.Is(err, boom) || r != nil {
		t.Errorf("result %v, error %v", r, err)
	}
}
