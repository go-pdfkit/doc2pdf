package xps

import (
	"archive/zip"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math/rand"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/go-pdfkit/pdfkit"
)

// A JPEG pdfkit refuses is an ErrBadImage, and the refusal is remembered: the
// draws after the first neither embed nor charge the memory again, so a
// thousand draws of it cannot end the conversion by the budget.
func TestSecRefusedJPEGIsRememberedNotRetried(t *testing.T) {
	data := []byte{0xFF, 0xD8, 0xFF, 0xD9} // a start and an end of image, no frame
	p := newPkg(page(``))
	p.extra["j.jpg"] = data
	c := newConv(t, p.bytes(t), Limits{})
	r := &renderer{c: c, p: c.doc.AddPage(pdfkit.NewPageSize(100, 100))}
	im := &imageInfo{part: "j.jpg", kind: "jpeg", w: 8, h: 8, stored: int64(len(data))}
	for i := 0; i < 5; i++ {
		h, err := r.register(im)
		if !errors.Is(err, ErrBadImage) || h != nil {
			t.Fatalf("draw %d: handle %v, err %v, want ErrBadImage", i, h, err)
		}
	}
	if c.stored != 0 {
		t.Errorf("a refused image holds %d bytes", c.stored)
	}
	if c.read != int64(len(data)) {
		t.Errorf("the part was read for %d bytes after five draws, want %d: the refusal was tried again", c.read, len(data))
	}
}

// ---- builders: packages whose padding is never held in the test's heap ----

type zpart struct {
	name string
	head []byte
	pad  int
}

func zipOf(t testing.TB, parts []zpart) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	chunk := make([]byte, 1<<20)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(p.head); err != nil {
			t.Fatal(err)
		}
		for n := p.pad; n > 0; {
			k := min(n, len(chunk))
			if _, err := w.Write(chunk[:k]); err != nil {
				t.Fatal(err)
			}
			n -= k
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// xpsParts is the scaffolding around one page.
func xpsParts(pageXML string) []zpart {
	types := `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="` + ctRels + `"/><Override PartName="/FixedDocumentSequence.fdseq" ContentType="` + ctSequence + `"/></Types>`
	rels := fmt.Sprintf(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="%s" Target="/FixedDocumentSequence.fdseq"/></Relationships>`, relOXPS)
	seq := fmt.Sprintf(`<FixedDocumentSequence xmlns="%s"><DocumentReference Source="/Documents/1/FixedDocument.fdoc"/></FixedDocumentSequence>`, nsOXPS)
	doc := fmt.Sprintf(`<FixedDocument xmlns="%s"><PageContent Source="Pages/1.fpage"/></FixedDocument>`, nsOXPS)
	return []zpart{
		{name: "[Content_Types].xml", head: []byte(types)},
		{name: "_rels/.rels", head: []byte(rels)},
		{name: "FixedDocumentSequence.fdseq", head: []byte(seq)},
		{name: "Documents/1/FixedDocument.fdoc", head: []byte(doc)},
		{name: "Documents/1/Pages/1.fpage", head: []byte(pageXML)},
	}
}

func noise(n int, seed int64) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(seed)).Read(b)
	return b
}

func zlibOf(t testing.TB, level int, write func(w *zlib.Writer)) []byte {
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatal(err)
	}
	write(zw)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zerosZ(t testing.TB, n int) []byte {
	return zlibOf(t, zlib.BestCompression, func(w *zlib.Writer) {
		chunk := make([]byte, 1<<20)
		for n > 0 {
			k := min(n, len(chunk))
			w.Write(chunk[:k])
			n -= k
		}
	})
}

// rawPNG assembles a PNG from its header fields and a ready zlib stream.
func rawPNG(w, h int, depth, ctype, interlace byte, plte, idat []byte) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		binary.Write(&b, binary.BigEndian, crc.Sum32())
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9], ihdr[12] = depth, ctype, interlace
	chunk("IHDR", ihdr)
	if plte != nil {
		chunk("PLTE", plte)
	}
	chunk("IDAT", idat)
	chunk("IEND", nil)
	return b.Bytes()
}

// noisePalettedPNG is side x side, 8-bit indices of noise, 256 random colours:
// a part a third the size of the RGB samples the PDF library stores for it.
func noisePalettedPNG(t testing.TB, side int, seed int64) []byte {
	row := make([]byte, 1+side)
	rng := rand.New(rand.NewSource(seed))
	idat := zlibOf(t, zlib.NoCompression, func(w *zlib.Writer) {
		for y := 0; y < side; y++ {
			rng.Read(row[1:])
			w.Write(row)
		}
	})
	return rawPNG(side, side, 8, 3, 0, noise(768, seed+1), idat)
}

// interlacedRGBA is side x side 8-bit RGBA, Adam7, all zero.
func interlacedRGBA(t testing.TB, side int) []byte {
	type pass struct{ x0, y0, dx, dy int }
	n := 0
	for _, p := range []pass{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
		pw := (side - p.x0 + p.dx - 1) / p.dx
		ph := (side - p.y0 + p.dy - 1) / p.dy
		if pw > 0 && ph > 0 {
			n += ph * (1 + 4*pw)
		}
	}
	return rawPNG(side, side, 8, 6, 1, nil, zerosZ(t, n))
}

// grayTIFF is side x side 8-bit gray, one deflate strip that inflates to
// inflated bytes (the reader takes up to 8 bytes a pixel of it).
func grayTIFF(t testing.TB, side, inflated int) []byte {
	strip := zerosZ(t, inflated)
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II*\x00")
	binary.Write(&b, le, uint32(8))
	type tag struct {
		id, typ uint16
		n, v    uint32
	}
	const nTags = 9
	dataAt := uint32(8 + 2 + nTags*12 + 4)
	tags := []tag{
		{256, 4, 1, uint32(side)}, {257, 4, 1, uint32(side)}, {258, 3, 1, 8}, {259, 3, 1, 8}, {262, 3, 1, 1},
		{273, 4, 1, dataAt}, {277, 3, 1, 1}, {278, 4, 1, uint32(side)}, {279, 4, 1, uint32(len(strip))},
	}
	binary.Write(&b, le, uint16(nTags))
	for _, tg := range tags {
		binary.Write(&b, le, tg.id)
		binary.Write(&b, le, tg.typ)
		binary.Write(&b, le, tg.n)
		binary.Write(&b, le, tg.v)
	}
	binary.Write(&b, le, uint32(0))
	b.Write(strip)
	return b.Bytes()
}

// stub1x1JPEG is the smallest thing DecodeConfig and pdfkit both call a JPEG.
var stub1x1JPEG = []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00,
	0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00}

const (
	// keepRef is a brush whose Viewbox is empty: it is refused after its image
	// part was read.
	keepRef = `<Path Data="M0,0L9,0 9,9z"><Path.Fill><ImageBrush ImageSource="%s" Viewbox="0,0,0,0" ViewboxUnits="Absolute" Viewport="0,0,9,9" ViewportUnits="Absolute"/></Path.Fill></Path>`
	// tileRef tiles a one-pixel image over a page-sized path: content for the
	// content ceiling.
	tileRef = `<Path Data="M0.0001,0.0001L63500000,0.0001 63500000,63500000 0.0001,63500000z"><Path.Fill><ImageBrush ImageSource="%s" TileMode="FlipXY" Viewbox="0.0001,0.0001,0.0101,0.0101" ViewboxUnits="Absolute" Viewport="0.0001,0.0001,1000000.0001,1000000.0001" ViewportUnits="Absolute"/></Path.Fill></Path>`
)

func tiny1x1PNG(t testing.TB) []byte {
	return rawPNG(1, 1, 8, 0, 0, nil, zlibOf(t, zlib.BestCompression, func(w *zlib.Writer) { w.Write([]byte{0, 0}) }))
}

// peak3 converts at the default limits under the repository's judge and fails
// above worstPeakMiB, the bound the README states.
func peak3(t *testing.T, name string, b []byte) (*Result, error, uint64) {
	t.Helper()
	var (
		res *Result
		err error
	)
	t0 := time.Now()
	heap := peakHeap(t, func() { res, err = Convert(b, Limits{}) })
	t.Logf("%s: package %d bytes, %v, peak heap %d MiB, err %v", name, len(b), time.Since(t0).Round(time.Millisecond), heap, err)
	if heap > worstPeakMiB {
		t.Errorf("%s: peak heap %d MiB passes the %d MiB the README states", name, heap, worstPeakMiB)
	}
	return res, err, heap
}

// Third review, the budgets were additive: every probe below stayed inside each
// ceiling and together they passed the 400 MiB gate (901 MiB for the worst).
// The inputs are the reviewer's.

// Parts that are read and never drawn are held nowhere: eight 31 MiB image
// parts behind a brush that is refused cost 333 MiB of kept bytes.
func TestSecPeakKeptPartsNothingDrawn(t *testing.T) {
	tiny := tiny1x1PNG(t)
	var body strings.Builder
	var extra []zpart
	for i := 0; i < 8; i++ {
		n := fmt.Sprintf("r%d.png", i)
		extra = append(extra, zpart{name: n, head: tiny, pad: 31 << 20})
		fmt.Fprintf(&body, keepRef, "/"+n)
	}
	extra = append(extra, zpart{name: "pad.bin", head: noise(1100<<10, 7)})
	peak3(t, "kept parts 8x31 MiB, nothing drawn", zipOf(t, append(xpsParts(page(body.String())), extra...)))
}

// Six 3170 x 3170 noise PNGs: 10 MB parts the library stores as 30 MB of RGB.
func TestSecPeakManyNoiseImages(t *testing.T) {
	var body strings.Builder
	var extra []zpart
	for i := 0; i < 6; i++ {
		n := fmt.Sprintf("n%d.png", i)
		extra = append(extra, zpart{name: n, head: noisePalettedPNG(t, 3170, int64(10+i))})
		fmt.Fprintf(&body, secImgPath, "/"+n)
	}
	peak3(t, "six noise images", zipOf(t, append(xpsParts(page(body.String())), extra...)))
}

// Everything at once: noise images, padded parts, JPEGs, a decode near the
// ceiling and content near its own.
func TestSecPeakCombined(t *testing.T) {
	tiny := tiny1x1PNG(t)
	var body strings.Builder
	var extra []zpart
	for i := 0; i < 6; i++ {
		n := fmt.Sprintf("r%d.png", i)
		extra = append(extra, zpart{name: n, head: tiny, pad: 31 << 20})
		fmt.Fprintf(&body, keepRef, "/"+n)
	}
	for i := 0; i < 2; i++ {
		n := fmt.Sprintf("j%d.jpg", i)
		head := append(append([]byte{}, stub1x1JPEG...), byte(i))
		extra = append(extra, zpart{name: n, head: head, pad: 31 << 20})
		fmt.Fprintf(&body, secImgPath, "/"+n)
	}
	extra = append(extra, zpart{name: "d.png", head: interlacedRGBA(t, 2500)})
	fmt.Fprintf(&body, secImgPath, "/d.png")
	extra = append(extra, zpart{name: "t.jpg", head: stub1x1JPEG})
	body.WriteString(strings.Repeat(fmt.Sprintf(tileRef, "/t.jpg"), 40))
	extra = append(extra, zpart{name: "pad.bin", head: noise(1200<<10, 7)})
	peak3(t, "combined", zipOf(t, append(xpsParts(page(body.String())), extra...)))
}

// The worst the reviewer found: noise images embedded, padded parts, content.
func TestSecPeakWorst(t *testing.T) {
	tiny := tiny1x1PNG(t)
	var body strings.Builder
	var extra []zpart
	for i := 0; i < 6; i++ {
		n := fmt.Sprintf("n%d.png", i)
		extra = append(extra, zpart{name: n, head: noisePalettedPNG(t, 3170, int64(10+i))})
		fmt.Fprintf(&body, secImgPath, "/"+n)
	}
	for i := 0; i < 6; i++ {
		n := fmt.Sprintf("r%d.png", i)
		extra = append(extra, zpart{name: n, head: tiny, pad: 31 << 20})
		fmt.Fprintf(&body, keepRef, "/"+n)
	}
	extra = append(extra, zpart{name: "t.jpg", head: stub1x1JPEG})
	body.WriteString(strings.Repeat(fmt.Sprintf(tileRef, "/t.jpg"), 40))
	peak3(t, "worst", zipOf(t, append(xpsParts(page(body.String())), extra...)))
}

// Decoders against the estimate at the default ceiling: a TIFF inflates a strip
// through a doubling buffer, 8 bytes a pixel whatever its colour model.
func TestSecPeakDecoders(t *testing.T) {
	cases := []struct {
		name, part string
		data       []byte
	}{
		{"png paletted noise 3170^2", "i.png", noisePalettedPNG(t, 3170, 3)},
		{"png interlaced RGBA 2500^2", "i.png", interlacedRGBA(t, 2500)},
		{"tiff gray deflate 3170^2, strip inflates 8 B/px", "i.tif", grayTIFF(t, 3170, 3170*3170*8)},
	}
	for _, tc := range cases {
		b := zipOf(t, append(xpsParts(page(fmt.Sprintf(secImgPath, "/"+tc.part))), zpart{name: tc.part, head: tc.data},
			zpart{name: "pad.bin", head: noise(200<<10, 7)}))
		peak3(t, tc.name, b)
	}
}

// The estimate is an upper bound for the TIFF reader too: the 8-bit gray
// deflate TIFF of 3170 x 3170 measured 251 MiB where the estimate was 192.
func TestSecTIFFDecodeCost(t *testing.T) {
	data := grayTIFF(t, 3170, 3170*3170*8)
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.tif")))
	p.extra["i.tif"] = data
	c := newConv(t, p.bytes(t), Limits{})
	im, err := c.image("i.tif")
	if err != nil {
		t.Fatal(err)
	}
	est := uint64(im.cost) >> 20
	var res *Result
	// A collector that runs often keeps the garbage the race detector's
	// allocator leaves out of the peak: the estimate is of what is live.
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	heap := peakHeap(t, func() { res, err = Convert(p.bytes(t), Limits{MaxDecodeBytes: 1 << 40, MaxMemory: 1 << 40}) })
	if err != nil || res == nil {
		t.Fatal(err)
	}
	t.Logf("tiff gray deflate: estimated %d MiB, measured peak heap %d MiB", est, heap)
	if heap > est {
		t.Errorf("peak heap %d MiB passes the estimate %d MiB", heap, est)
	}
	if im.cost < int64(3170*3170)*(8+16) {
		t.Errorf("a TIFF is charged %d bytes a pixel, want at least 24", im.cost/int64(3170*3170))
	}
}

// A JPEG declares its size in its first bytes and pdfkit reads no further: a
// 36-byte file declaring 6000 x 6000 made a 1 KB PDF with a 36 Mpx image. More
// than jpegMaxPixelsPerByte pixels a byte is refused, and a real JPEG is not.
func TestSecJPEGPixelsPerByte(t *testing.T) {
	stub := func(side uint16) []byte {
		return []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x0B, 0x08, byte(side >> 8), byte(side), byte(side >> 8), byte(side), 0x01, 0x01, 0x11, 0x00,
			0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00}
	}
	for _, tc := range []struct {
		side uint16
		ok   bool
	}{
		{150, true},  // 22500 px in 25 bytes: 900 a byte
		{160, true},  // 25600 px in 25 bytes: exactly 1024 a byte, the ceiling, is taken
		{161, false}, // one more row and column is over
		{6000, false},
	} {
		d := stub(tc.side)
		px := int(tc.side) * int(tc.side)
		if (px <= jpegMaxPixelsPerByte*len(d)) != tc.ok {
			t.Fatalf("side %d: test table is wrong, %d px in %d bytes", tc.side, px, len(d))
		}
		p := newPkg(page(fmt.Sprintf(secImgPath, "/j.jpg")))
		p.extra["j.jpg"] = d
		res, err := Convert(p.bytes(t), Limits{})
		if err != nil {
			t.Fatalf("side %d: %v", tc.side, err)
		}
		drawn := strings.Contains(string(res.PDF), "DCTDecode")
		if drawn != tc.ok {
			t.Errorf("side %d (%d px in %d bytes): embedded %v, want %v; skipped %v", tc.side, px, len(d), drawn, tc.ok, res.Skipped)
		}
	}
	// Exactly at the ceiling is taken: a 1x1 stub, and a real encoder's output.
	p := newPkg(page(fmt.Sprintf(secImgPath, "/j.jpg")))
	p.extra["j.jpg"] = jpegOf(t, 64, 64)
	if res, err := Convert(p.bytes(t), Limits{}); err != nil || !strings.Contains(string(res.PDF), "DCTDecode") {
		t.Errorf("a real JPEG: %v", err)
	}
}

// The memory budget has an n and an n-1: the smallest MaxMemory at which a
// document with a font-less page of one stored image converts refuses one byte
// less, and MaxDecodeBytes is exact the same way.
func TestSecMemoryBoundary(t *testing.T) {
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.png")))
	p.extra["i.png"] = noisePalettedPNG(t, 200, 5)
	minMemory(t, p.bytes(t))
}

func TestSecDecodeBytesBoundary(t *testing.T) {
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.png")))
	p.extra["i.png"] = noisePalettedPNG(t, 200, 5)
	b := p.bytes(t)
	c := newConv(t, b, Limits{})
	im, err := c.image("i.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Convert(b, Limits{MaxDecodeBytes: im.cost}); err != nil {
		t.Errorf("MaxDecodeBytes = cost (%d): %v", im.cost, err)
	}
	refuses(t, "decode", b, Limits{MaxDecodeBytes: im.cost - 1}, ErrLimit)
}

// A part nothing draws is not kept: the bytes of an image are read again when
// it is embedded, and the stored count is the library's, not the part's.
func TestSecImageInfoKeepsNoBytes(t *testing.T) {
	p := newPkg(page(`<Path Data="M0,0L9,0 9,9z" Fill="#FF000000"/>`))
	p.extra["i.png"] = noisePalettedPNG(t, 100, 9)
	c := newConv(t, p.bytes(t), Limits{})
	im, err := c.image("i.png")
	if err != nil {
		t.Fatal(err)
	}
	if c.kept != 0 || c.stored != 0 {
		t.Errorf("a measured image holds kept %d stored %d", c.kept, c.stored)
	}
	if im.stored != 100*100*3 {
		t.Errorf("a paletted opaque PNG is stored at %d bytes, want %d", im.stored, 100*100*3)
	}
}
