// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

// Regression tests for the second independent review of PR #11 (head c4fd4b2).
// Same protocol as security_test.go: a small hostile package, the DEFAULT
// limits, the measured peak heap under a soft memory limit. The probes are the
// reviewer's own (zz_review_probe2_test.go).

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"github.com/go-pdfkit/pdfkit"
)

// worstPeakMiB is the most any probe of either review may reach at the default
// limits. The README states it; a probe that passes it fails here.
const worstPeakMiB = 400

// zlibZeros is the zlib stream of n zero bytes, made without holding them.
func zlibZeros(t testing.TB, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zlib.NewWriterLevel(&buf, zlib.BestCompression)
	if err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 1<<20)
	for n > 0 {
		k := min(n, len(chunk))
		if _, err := zw.Write(chunk[:k]); err != nil {
			t.Fatal(err)
		}
		n -= k
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A 1x1 JPEG both DecodeConfig and pdfkit take.
var secStubJPEG = []byte{0xFF, 0xD8, 0xFF, 0xC0, 0x00, 0x0B, 0x08, 0x00, 0x01, 0x00, 0x01, 0x01, 0x01, 0x11, 0x00,
	0xFF, 0xDA, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x3F, 0x00}

// A progressive JPEG declaring 6144 x 6144 in 36 bytes, with a stray byte
// before the first marker that pdfkit refuses and image/jpeg tolerates.
var secProgressiveStub = []byte{0xFF, 0xD8, 0x00,
	0xFF, 0xC2, 0x00, 0x11, 0x08, 0x18, 0x00, 0x18, 0x00, 0x03, 0x01, 0x11, 0x00, 0x02, 0x11, 0x00, 0x03, 0x11, 0x00,
	0xFF, 0xDA, 0x00, 0x0C, 0x03, 0x01, 0x00, 0x02, 0x00, 0x03, 0x00, 0x00, 0x00, 0x00}

// png16 is a side x side 16-bit RGBA PNG of zeros.
func png16(t testing.TB, side int) []byte {
	t.Helper()
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
	binary.BigEndian.PutUint32(ihdr, uint32(side))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(side))
	ihdr[8], ihdr[9] = 16, 6
	chunk("IHDR", ihdr)
	chunk("IDAT", zlibZeros(t, side*(1+side*8)))
	chunk("IEND", nil)
	return b.Bytes()
}

// tiff16 is a side x side 16-bit RGBA TIFF of one deflate strip of zeros.
func tiff16(t testing.TB, side int) []byte {
	t.Helper()
	strip := zlibZeros(t, side*side*8)
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("II*\x00")
	binary.Write(&b, le, uint32(8))
	type tag struct {
		id, typ uint16
		n, v    uint32
	}
	const nTags = 10
	bitsAt := uint32(8 + 2 + nTags*12 + 4)
	dataAt := bitsAt + 8
	s := uint32(side)
	tags := []tag{
		{256, 4, 1, s}, {257, 4, 1, s}, {258, 3, 4, bitsAt}, {259, 3, 1, 8}, {262, 3, 1, 2},
		{273, 4, 1, dataAt}, {277, 3, 1, 4}, {278, 4, 1, s}, {279, 4, 1, uint32(len(strip))}, {338, 3, 1, 2},
	}
	binary.Write(&b, le, uint16(nTags))
	for _, tg := range tags {
		binary.Write(&b, le, tg.id)
		binary.Write(&b, le, tg.typ)
		binary.Write(&b, le, tg.n)
		binary.Write(&b, le, tg.v)
	}
	binary.Write(&b, le, uint32(0))
	binary.Write(&b, le, [4]uint16{16, 16, 16, 16})
	b.Write(strip)
	return b.Bytes()
}

// secRun converts at the default limits and returns the result, the error, the
// peak heap in MiB and the time taken.
func secRun(t *testing.T, b []byte) (res *Result, err error, heap uint64, took time.Duration) {
	t.Helper()
	t0 := time.Now()
	heap = peakHeap(t, func() { res, err = lim0(t, b) })
	return res, err, heap, time.Since(t0)
}

// Second review, finding 1: tile indices are integers. As float64 they stop
// advancing at 2^53, the column count rounds to one, one unit of work is
// charged, and the loop runs until memory ends (1.9 KB, more than 3 GiB).
func TestSecTileIndexSaturation(t *testing.T) {
	body := `<Path Data="M9e8,0L9e8,0.5"><Path.Fill><ImageBrush ImageSource="/j.jpg" TileMode="Tile" Viewbox="0,0,1,1" ViewboxUnits="Absolute" Viewport="0,0,1e-8,1" ViewportUnits="Absolute"/></Path.Fill></Path>`
	p := newPkg(page(body))
	p.extra["j.jpg"] = secStubJPEG
	_, err, heap, took := secRun(t, p.bytes(t))
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "tile index") {
		t.Fatalf("a tile index of 9e16: %v", err)
	}
	t.Logf("tile index: refused after %v, peak heap %d MiB", took.Round(time.Millisecond), heap)
	if heap > 100 {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// The index ceiling is 2^31, itself allowed: a shape at x = 2^29 under a
// viewport 0.25 wide sits exactly on tile 2^31, and 0.249 wide is past it.
func TestSecTileIndexBoundary(t *testing.T) {
	brush := func(w string) []byte {
		body := `<Path Data="M536870912,0L536870912,0.5"><Path.Fill><ImageBrush ImageSource="/j.jpg" TileMode="Tile" Viewbox="0,0,1,1" ViewboxUnits="Absolute" Viewport="0,0,` + w + `,1" ViewportUnits="Absolute"/></Path.Fill></Path>`
		p := newPkg(page(body))
		p.extra["j.jpg"] = secStubJPEG
		return p.bytes(t)
	}
	if _, err := lim0(t, brush("0.25")); err != nil {
		t.Errorf("tile 2^31: %v", err)
	}
	if _, err := lim0(t, brush("0.249")); !errors.Is(err, ErrLimit) {
		t.Errorf("a tile past 2^31: %v", err)
	}
}

// Tiles and flips are still drawn, across negative indices too.
func TestSecTilesStillDrawn(t *testing.T) {
	body := `<Path Data="M-25,-25L25,-25 25,25 -25,25z"><Path.Fill><ImageBrush ImageSource="/j.jpg" TileMode="FlipXY" Viewbox="0,0,1,1" ViewboxUnits="Absolute" Viewport="0,0,10,10" ViewportUnits="Absolute"/></Path.Fill></Path>`
	p := newPkg(page(body))
	p.extra["j.jpg"] = secStubJPEG
	res, err := lim0(t, p.bytes(t))
	if err != nil || len(res.Skipped) != 0 {
		t.Fatalf("err %v, skipped %v", err, res.Skipped)
	}
	if n := bytes.Count(res.PDF, []byte("/Subtype /Image")) + bytes.Count(res.PDF, []byte("/Subtype/Image")); n == 0 {
		t.Errorf("no image in the PDF")
	}
}

// Second review, finding 3a: a tile is one unit of MaxWork and some 270 bytes
// of content stream. 505 paths of 4096 tiles each (2.8 KB of XML a path) were
// 768 MiB.
func TestSecTileContentIsBytesBudgeted(t *testing.T) {
	one := `<Path Data="M0.0001,0.0001L63500000,0.0001 63500000,63500000 0.0001,63500000z"><Path.Fill><ImageBrush ImageSource="/j.jpg" TileMode="FlipXY" Viewbox="0.0001,0.0001,0.0101,0.0101" ViewboxUnits="Absolute" Viewport="0.0001,0.0001,1000000.0001,1000000.0001" ViewportUnits="Absolute"/></Path.Fill></Path>`
	p := newPkg(page(strings.Repeat(one, 505)))
	p.extra["j.jpg"] = secStubJPEG
	_, err, heap, took := secRun(t, p.bytes(t))
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "bytes of ") { // the content ceiling or, five copies of it, the memory budget
		t.Fatalf("505 paths of 4096 tiles: %v", err)
	}
	t.Logf("tile content: refused after %v, peak heap %d MiB", took.Round(time.Millisecond), heap)
	if heap > worstPeakMiB {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// Each charge has its own refusal: the image's, the tile's, the single
// image's pixels.
func TestSecEachContentChargeRefuses(t *testing.T) {
	mk := func(brush string) []byte {
		p := newPkg(page(`<Path Data="M0,0L640,0 640,640 0,640z"><Path.Fill>` + brush + `</Path.Fill></Path>`))
		p.extra["j.jpg"] = secStubJPEG
		return p.bytes(t)
	}
	once := mk(`<ImageBrush ImageSource="/j.jpg" Viewbox="0,0,1,1" ViewboxUnits="Absolute" Viewport="0,0,640,640" ViewportUnits="Absolute"/>`)
	tiled := mk(`<ImageBrush ImageSource="/j.jpg" TileMode="Tile" Viewbox="0,0,1,1" ViewboxUnits="Absolute" Viewport="0,0,10,10" ViewportUnits="Absolute"/>`)
	if _, err := Convert(once, Limits{MaxContentBytes: 1 << 20}); err != nil {
		t.Fatalf("the image once: %v", err)
	}
	// 4096 tiles are far more than 100 KB of content, and far less than 4 MiB.
	if _, err := Convert(tiled, Limits{MaxContentBytes: 100_000}); !errors.Is(err, ErrLimit) {
		t.Errorf("a tile's bytes are not counted: %v", err)
	}
	if _, err := Convert(tiled, Limits{MaxContentBytes: 4 << 20}); err != nil {
		t.Errorf("4096 tiles within budget: %v", err)
	}
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.png")))
	p.extra["i.png"] = bitonal(t, 64)
	if _, err := Convert(p.bytes(t), Limits{MaxImagePixels: 64*64 - 1}); !errors.Is(err, ErrLimit) {
		t.Errorf("one image's pixels: %v", err)
	}
	if _, err := Convert(p.bytes(t), Limits{MaxImagePixels: 64 * 64}); err != nil {
		t.Errorf("exactly the pixel limit: %v", err)
	}
}

// Second review, finding 3b: a dash array of 126 numbers of 18 digits is two
// units of MaxWork and 3 KB of content stream; 568 KB of package were 1409 MiB.
func TestSecDashContentIsBytesBudgeted(t *testing.T) {
	const perPage = 40000
	one := `<Path Data="M0,0h1" Stroke="#000000" StrokeThickness="7e8" StrokeDashOffset="7e8" StrokeDashArray="` + strings.TrimSpace(strings.Repeat("7e8 ", 63)) + `"/>`
	pg := page(strings.Repeat(one, perPage))
	p := newPkg(pg)
	var doc strings.Builder
	fmt.Fprintf(&doc, `<FixedDocument xmlns="%s">`, nsOXPS)
	for i := 0; i < 3; i++ {
		doc.WriteString(`<PageContent Source="Pages/1.fpage"/>`)
	}
	doc.WriteString(`</FixedDocument>`)
	p.doc = doc.String()
	p.extra["pad.bin"] = secJunk(3 * len(pg) / 250)
	b := p.bytes(t)
	_, err, heap, took := secRun(t, b)
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "bytes of ") { // the content ceiling or, five copies of it, the memory budget
		t.Fatalf("%d dashed paths: %v", perPage, err)
	}
	t.Logf("dash content: %d byte package refused after %v, peak heap %d MiB", len(b), took.Round(time.Millisecond), heap)
	if heap > worstPeakMiB {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// What is within the byte budget is still drawn, and dashes with it.
func TestSecDashWithinBudget(t *testing.T) {
	one := `<Path Data="M0,0h9" Stroke="#000000" StrokeThickness="1" StrokeDashArray="2 1"/>`
	res, err := lim0(t, newPkg(page(strings.Repeat(one, 50))).bytes(t))
	if err != nil || len(res.Skipped) != 0 {
		t.Fatalf("err %v, skipped %v", err, res.Skipped)
	}
}

// The content bytes are the library's own count, the pages before this one
// included, and the ceiling is exact.
func TestSecContentCheckIsExact(t *testing.T) {
	c := newConv(t, newPkg(page(``)).bytes(t), Limits{})
	r := &renderer{c: c, p: c.doc.AddPage(pdfkit.NewPageSize(100, 100))}
	for i := 0; i < 20; i++ {
		r.p.MoveTo(float64(i), 1)
		r.p.LineTo(float64(i), 9)
	}
	n := r.p.ContentLen()
	if n < 100 {
		t.Fatalf("a page of 20 lines is %d bytes", n)
	}
	c.lim.MaxContentBytes = n
	if err := r.check(); err != nil {
		t.Fatalf("exactly the ceiling: %v", err)
	}
	c.lim.MaxContentBytes = n - 1
	if err := r.check(); !errors.Is(err, ErrLimit) {
		t.Fatalf("one byte over: %v", err)
	}
	// The pages already finished count.
	c.lim.MaxContentBytes = n + 5
	c.done = 5
	if err := r.check(); err != nil {
		t.Fatalf("5 + n against n + 5: %v", err)
	}
	c.done = 6
	if err := r.check(); !errors.Is(err, ErrLimit) {
		t.Fatalf("6 + n against n + 5: %v", err)
	}
}

// Every page adds its content to the count of the next.
func TestSecContentCountsAllPages(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&body, `<Path Data="M0,%d L90,%d" Stroke="#000000" StrokeThickness="1"/>`, i, i)
	}
	p := newPkg(page(body.String()), page(body.String()), page(body.String()))
	b := p.bytes(t)
	if _, err := Convert(b, Limits{}); err != nil {
		t.Fatal(err)
	}
	// One page of 60 lines is more than 2 KB and the three are less than 100 KB.
	if _, err := Convert(b, Limits{MaxContentBytes: 100_000}); err != nil {
		t.Fatalf("three pages under the ceiling: %v", err)
	}
	if _, err := Convert(b, Limits{MaxContentBytes: 5_000}); !errors.Is(err, ErrLimit) {
		t.Fatalf("three pages over the ceiling: %v", err)
	}
}

// Second review, finding 4: a decode is bounded from the header BEFORE it
// happens. 16-bit RGBA PNG at 6144 x 6144 cost 584 MiB for one draw.
func TestSecPNG16IsRefusedBeforeDecode(t *testing.T) {
	data := png16(t, 6144)
	for _, n := range []int{1, 3} {
		p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/i.png"), n)))
		p.extra["i.png"] = data
		_, err, heap, took := secRun(t, p.bytes(t))
		if !errors.Is(err, ErrLimit) {
			t.Fatalf("draws=%d: %v", n, err)
		}
		t.Logf("png16 draws=%d: %d byte PNG refused after %v, peak heap %d MiB", n, len(data), took.Round(time.Millisecond), heap)
		if heap > 150 {
			t.Errorf("peak heap %d MiB", heap)
		}
	}
}

// A deflate TIFF of one strip is inflated whole: 1056 MiB for one draw.
func TestSecTIFF16IsRefusedBeforeDecode(t *testing.T) {
	data := tiff16(t, 6144)
	p := newPkg(page(fmt.Sprintf(secImgPath, "/i.tif")))
	p.extra["i.tif"] = data
	_, err, heap, took := secRun(t, p.bytes(t))
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	t.Logf("tiff16: %d byte TIFF refused after %v, peak heap %d MiB", len(data), took.Round(time.Millisecond), heap)
	if heap > 150 {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// A 36-byte progressive JPEG: image/jpeg allocates the coefficients at the
// first scan and fails afterwards (540 MiB). A JPEG is never decoded here:
// pdfkit embeds it as it is, and the pixels are charged to no budget because
// none is allocated.
func TestSecProgressiveJPEGStubIsNotDecoded(t *testing.T) {
	for _, n := range []int{1, 3} {
		p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/p.jpg"), n)))
		p.extra["p.jpg"] = secProgressiveStub
		res, err, heap, took := secRun(t, p.bytes(t))
		if err != nil {
			t.Fatalf("draws=%d: err %v, skipped %v", n, err, res.Skipped)
		}
		t.Logf("progressive stub draws=%d: %d bytes, %v, peak heap %d MiB", n, len(secProgressiveStub), took.Round(time.Millisecond), heap)
		if heap > 100 {
			t.Errorf("peak heap %d MiB", heap)
		}
		if _, err := Convert(p.bytes(t), Limits{MaxDrawnPixels: 1}); err != nil {
			t.Errorf("a JPEG was charged decoded pixels: %v", err)
		}
	}
}

// The per-pixel cost is an upper bound of what a decode really holds. Each
// image is decoded and drawn with the ceiling lifted, and the peak heap is
// compared with the estimate.
func TestSecDecodeCostBoundsMeasured(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		ref  string
	}{
		{"png 16-bit RGBA", png16(t, 1400), "/i.png"},
		{"tiff 16-bit RGBA deflate", tiff16(t, 1400), "/i.tif"},
		{"png 8-bit bitonal", bitonal(t, 4000), "/i.png"},
	} {
		p := newPkg(page(fmt.Sprintf(secImgPath, tc.ref)))
		p.extra[strings.TrimPrefix(tc.ref, "/")] = tc.data
		c := newConv(t, p.bytes(t), Limits{})
		im, err := c.image(strings.TrimPrefix(tc.ref, "/"))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		var res *Result
		heap := peakHeap(t, func() { res, err = Convert(p.bytes(t), Limits{MaxDecodeBytes: 1 << 40, MaxMemory: 1 << 40}) })
		if err != nil || res == nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		est := uint64(im.cost) >> 20
		t.Logf("%s: estimated %d MiB, measured peak heap %d MiB", tc.name, est, heap)
		if heap > est {
			t.Errorf("%s: peak heap %d MiB passes the estimate %d MiB", tc.name, heap, est)
		}
	}
}

// Second review, finding 5: the PDF is built in one buffer and copied into
// another to be returned. Seven JPEG parts of 32 MiB that deflate a thousand
// to one (a 1.25 MiB package) were 673 MiB at the write.
func TestSecEmbeddedImagesAreBudgeted(t *testing.T) {
	mk := func(id byte) []byte {
		var b bytes.Buffer
		b.Write([]byte{0xFF, 0xD8})
		seg := make([]byte, 65537)
		seg[0], seg[1], seg[2], seg[3] = 0xFF, 0xFE, 0xFF, 0xFF
		seg[4] = id
		for i := 0; i < 511; i++ {
			b.Write(seg)
		}
		b.Write(secStubJPEG[2:])
		return b.Bytes()
	}
	p := newPkg()
	var body strings.Builder
	total := 0
	for i := 0; i < 7; i++ {
		name := fmt.Sprintf("j%d.jpg", i)
		d := mk(byte(i + 1))
		total += len(d)
		p.extra[name] = d
		fmt.Fprintf(&body, secImgPath, "/"+name)
	}
	p.pages = []string{page(body.String())}
	p.extra["pad.bin"] = secJunk(total / 100) // a part is read twice (here and at the embed): the ratio must leave room
	b := p.bytes(t)
	_, err, heap, took := secRun(t, b)
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "bytes of memory held") {
		t.Fatalf("seven 32 MiB JPEG parts: %v", err)
	}
	t.Logf("embedded images: %d byte package refused after %v, peak heap %d MiB", len(b), took.Round(time.Millisecond), heap)
	if heap > worstPeakMiB {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// Everything at once, each inside its own budget: tiles and a progressive
// stub, as the reviewer combined them.
func TestSecCombinedProbe(t *testing.T) {
	tile := `<Path Data="M0.0001,0.0001L63500000,0.0001 63500000,63500000 0.0001,63500000z"><Path.Fill><ImageBrush ImageSource="/j.jpg" TileMode="FlipXY" Viewbox="0.0001,0.0001,0.0101,0.0101" ViewboxUnits="Absolute" Viewport="0.0001,0.0001,1000000.0001,1000000.0001" ViewportUnits="Absolute"/></Path.Fill></Path>`
	p := newPkg(page(strings.Repeat(tile, 505) + fmt.Sprintf(secImgPath, "/p.jpg")))
	p.extra["j.jpg"] = secStubJPEG
	p.extra["p.jpg"] = secProgressiveStub
	_, err, heap, _ := secRun(t, p.bytes(t))
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("%v", err)
	}
	t.Logf("combined: peak heap %d MiB", heap)
	if heap > worstPeakMiB {
		t.Errorf("peak heap %d MiB", heap)
	}
}
