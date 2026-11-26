package xps

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-pdfkit/pdfkit"
	"golang.org/x/image/font/gofont/goregular"
)

// Fifth review: four accounting gaps. A WOFF font inflates behind the part's
// size, a gradient is parsed for text and strokes without a work charge, the
// shadings a gradient creates are held by nobody's budget, and the font bytes
// the PDF buffer must hold were left out of Convert's buffer.

// ---- WOFF ----

// woffBytes wraps Go Regular as a WOFF 1 file whose fpgm table is n zero bytes
// (deflated: a few hundred kilobytes stand for hundreds of megabytes).
func woff1Of(t testing.TB, n int) []byte {
	src := goregular.TTF
	nt := int(binary.BigEndian.Uint16(src[4:]))
	type tab struct {
		tag        string
		orig, comp []byte
	}
	var tabs []tab
	for i := 0; i < nt; i++ {
		rec := src[12+16*i:]
		off := binary.BigEndian.Uint32(rec[8:])
		ln := binary.BigEndian.Uint32(rec[12:])
		tg := string(rec[:4])
		if tg == "fpgm" {
			var buf bytes.Buffer
			zw, _ := zlib.NewWriterLevel(&buf, zlib.BestCompression)
			chunk := make([]byte, 1<<20)
			for k := n; k > 0; {
				m := min(k, len(chunk))
				zw.Write(chunk[:m])
				k -= m
			}
			zw.Close()
			tabs = append(tabs, tab{tg, nil, buf.Bytes()})
			continue
		}
		tabs = append(tabs, tab{tg, src[off : off+ln], src[off : off+ln]})
	}
	hdr := 44 + 20*len(tabs)
	out := make([]byte, hdr)
	copy(out, "wOFF")
	binary.BigEndian.PutUint32(out[4:], 0x00010000)
	binary.BigEndian.PutUint16(out[12:], uint16(len(tabs)))
	for i, tb := range tabs {
		orig := len(tb.orig)
		if tb.tag == "fpgm" {
			orig = n
		}
		e := out[44+20*i:]
		copy(e, tb.tag)
		binary.BigEndian.PutUint32(e[4:], uint32(len(out)))
		binary.BigEndian.PutUint32(e[8:], uint32(len(tb.comp)))
		binary.BigEndian.PutUint32(e[12:], uint32(orig))
		out = append(out, tb.comp...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
	}
	binary.BigEndian.PutUint32(out[8:], uint32(len(out)))
	return out
}

func fontOnlyPackage(t testing.TB, font []byte) []byte {
	p := newPkg(page(fmt.Sprintf(`<Glyphs OriginX="5" OriginY="20" FontRenderingEmSize="12" FontUri="/Resources/Fonts/w.ttf" UnicodeString="Hi" Fill="#FF000000"/>`)))
	p.extra["Resources/Fonts/w.ttf"] = font
	return p.bytes(t)
}

// A WOFF inflates to far more than its part: its instruction tables hold the
// 1 MiB ceiling like a plain font's, and its decoded size is charged.
func TestSecWOFFInstructionCeiling(t *testing.T) {
	for _, n := range []int{100 << 20, 1<<20 + 1} {
		b := fontOnlyPackage(t, woff1Of(t, n))
		var err error
		t0 := time.Now()
		heap := peakHeap(t, func() { _, err = Convert(b, Limits{}) })
		t.Logf("woff fpgm %d MiB: package %d bytes, %v, peak heap %d MiB, err %v", n>>20, len(b), time.Since(t0).Round(time.Millisecond), heap, err)
		if !errors.Is(err, ErrLimit) {
			t.Errorf("fpgm of %d bytes: %v, want ErrLimit", n, err)
		}
		if heap > worstPeakMiB {
			t.Errorf("peak heap %d MiB", heap)
		}
	}
}

// The ceiling is the same for a WOFF as for a plain font: exactly 1 MiB passes.
func TestSecWOFFAtTheCeilingConverts(t *testing.T) {
	base := fontBase(t)
	b := fontOnlyPackage(t, woff1Of(t, int(maxFontInstructions-base)))
	if _, err := Convert(b, Limits{}); err != nil {
		t.Errorf("WOFF with instructions exactly at the ceiling: %v", err)
	}
}

// A WOFF whose tables other than the instructions are large is charged at its
// decoded size, refused before it is inflated.
func TestSecWOFFDecodedSizeIsCharged(t *testing.T) {
	d := woff1Of(t, 1<<10)
	// Say the head table inflates to 100 MiB: the directory is what is read.
	nt := int(binary.BigEndian.Uint16(d[12:]))
	for i := 0; i < nt; i++ {
		e := d[44+20*i:]
		if string(e[:4]) == "glyf" {
			binary.BigEndian.PutUint32(e[12:], 100<<20)
		}
	}
	var err error
	heap := peakHeap(t, func() { _, err = Convert(fontOnlyPackage(t, d), Limits{MaxMemory: 64 << 20}) })
	t.Logf("woff decoded 100 MiB in 64 MiB: peak heap %d MiB, err %v", heap, err)
	if !errors.Is(err, ErrLimit) {
		t.Errorf("a font decoding past the budget: %v, want ErrLimit", err)
	}
}

func base128(n uint32) []byte {
	var out []byte
	for i := 4; i >= 0; i-- {
		if n>>(7*uint(i)) != 0 || i == 0 {
			b := byte(n>>(7*uint(i))) & 0x7f
			if i > 0 {
				b |= 0x80
			}
			out = append(out, b)
		}
	}
	return out
}

// woff2Dir is a WOFF2 header and directory of (index, origLength) entries with
// a stream that is not brotli: the directory is all the sizes are read from.
func woff2Dir(entries ...[2]uint32) []byte {
	out := make([]byte, 48)
	copy(out, "wOF2")
	binary.BigEndian.PutUint32(out[4:], 0x00010000)
	binary.BigEndian.PutUint16(out[12:], uint16(len(entries)))
	for _, e := range entries {
		out = append(out, byte(e[0]))
		out = append(out, base128(e[1])...)
	}
	return append(out, 1, 2, 3, 4)
}

func TestSecWOFF2Directory(t *testing.T) {
	const fpgm, glyf = 9, 10 // WOFF2 §5.1 known-tag indexes; glyf 0xC0|10 is the null transform
	d := woff2Dir([2]uint32{fpgm, 100 << 20})
	if n := instructionBytes(d); n != 100<<20 {
		t.Errorf("WOFF2 fpgm: instructionBytes = %d", n)
	}
	if n := decodedBytes(d); n != 12+16+100<<20 {
		t.Errorf("WOFF2 decoded size = %d, want %d", n, 12+16+100<<20)
	}
	if err := fontOnlyErr(t, d, Limits{}); !errors.Is(err, ErrLimit) {
		t.Errorf("WOFF2 declaring 100 MiB of fpgm: %v, want ErrLimit", err)
	}
	d = woff2Dir([2]uint32{0xC0 | glyf, 150 << 20})
	if err := fontOnlyErr(t, d, Limits{}); !errors.Is(err, ErrLimit) {
		t.Errorf("WOFF2 declaring 150 MiB of glyf: %v, want ErrLimit", err)
	}
	// A directory cut short is the library's to refuse.
	if n := decodedBytes(d[:50]); n != 0 {
		t.Errorf("truncated WOFF2 directory = %d", n)
	}
	if n := decodedBytes([]byte("wOFF")); n != 0 {
		t.Errorf("truncated WOFF directory = %d", n)
	}
}

// The directory reader, entry by entry: the escape tag, the transformed entries
// that carry a second length, and every way a directory can be cut short.
func TestSecWOFFDirectoryReader(t *testing.T) {
	hdr := func(sig string, n int) []byte {
		h := make([]byte, 48)
		copy(h, sig)
		binary.BigEndian.PutUint16(h[12:], uint16(n))
		return h
	}
	// WOFF2: an escaped "prep" of 7 bytes, a transformed glyf (flag 0x0A with
	// transform 0) of 100 whose stream is 40, a null-transform glyf (0xCA), a
	// transformed hmtx (0x43) of 20 whose stream is 8.
	d := hdr("wOF2", 4)
	d = append(d, 0x3f, 'p', 'r', 'e', 'p', 7)
	d = append(d, 0x0A, 100, 40)
	d = append(d, 0xCA, 4)
	d = append(d, 0x43, 20, 8)
	tabs, ok := woffTables(d)
	want := []woffTable{{true, 7}, {false, 100}, {false, 4}, {false, 20}}
	if !ok || len(tabs) != len(want) {
		t.Fatalf("tables %v ok %v", tabs, ok)
	}
	for i := range want {
		if tabs[i] != want[i] {
			t.Errorf("table %d = %v, want %v", i, tabs[i], want[i])
		}
	}
	if n, w := decodedBytes(d), int64(12+16*4+8+100+4+20); n != w {
		t.Errorf("decoded %d, want %d", n, w)
	}
	// An index that names an instruction table.
	for _, idx := range []byte{8, 9, 12} {
		if n := instructionBytes(append(hdr("wOF2", 1), idx, 9)); n != 9 {
			t.Errorf("index %d: %d instruction bytes, want 9", idx, n)
		}
	}
	// The largest length a base-128 holds is read whole, from its five bytes.
	if tabs, ok := woffTables(append(hdr("wOF2", 1), 0x00, 0x8F, 0xFF, 0xFF, 0xFF, 0x7F)); !ok || len(tabs) != 1 || tabs[0].orig != 0xFFFFFFFF {
		t.Errorf("a length of 2^32-1: %v %v", tabs, ok)
	}
	// Fewer bytes than the header's count never panics.
	for n := 0; n < 14; n++ {
		if tabs, ok := woffTables(hdr("wOFF", 1)[:n]); ok || tabs != nil {
			t.Errorf("%d bytes: %v %v", n, tabs, ok)
		}
	}
	// A WOFF1 entry is 20 bytes: 19 of them are no entry, 20 are.
	entry := func(n int) []byte { return append(hdr("wOFF", 1), make([]byte, 16)[:n-4]...) }
	if tabs, ok := woffTables(entry(19)); ok || tabs != nil {
		t.Errorf("a WOFF1 entry of 19 bytes: %v %v", tabs, ok)
	}
	if tabs, ok := woffTables(entry(20)); !ok || len(tabs) != 1 {
		t.Errorf("a WOFF1 entry of 20 bytes: %v %v", tabs, ok)
	}
	// Cut short anywhere: no tables, and the library refuses the file itself.
	for name, b := range map[string][]byte{
		"empty":            nil,
		"no tables":        hdr("wOF2", 0),
		"no entry":         hdr("wOF2", 1),
		"one entry of two": append(hdr("wOF2", 2), 0x00, 5),
		"no escaped tag":   append(hdr("wOF2", 1), 0x3f, 'a'),
		"no length":        append(hdr("wOF2", 1), 0x00),
		"unterminated":     append(hdr("wOF2", 1), 0x00, 0x81, 0x81, 0x81, 0x81, 0x81),
		"leading zero":     append(hdr("wOF2", 1), 0x00, 0x80, 0x01),
		"over 32 bits":     append(hdr("wOF2", 1), 0x00, 0xFF, 0xFF, 0xFF, 0xFF, 0x7F),
		"no stream length": append(hdr("wOF2", 1), 0x43, 20),
		"woff1 cut":        hdr("wOFF", 1)[:47],
		"unknown":          hdr("true", 1),
	} {
		if tabs, ok := woffTables(b); ok || tabs != nil {
			t.Errorf("%s: %v %v", name, tabs, ok)
		}
	}
	// A WOFF1 directory holds a table's instruction flag and its length.
	w := hdr("wOFF", 2)
	w = append(w[:44], make([]byte, 40)...)
	copy(w[44:], "fpgm")
	binary.BigEndian.PutUint32(w[44+12:], 9)
	copy(w[64:], "glyf")
	binary.BigEndian.PutUint32(w[64+12:], 5)
	if n := instructionBytes(w); n != 9 {
		t.Errorf("WOFF1 instructions %d", n)
	}
	if n := decodedBytes(w); n != 12+32+12+8 {
		t.Errorf("WOFF1 decoded %d, want %d", n, 12+32+12+8)
	}
}

// Each shading is charged its fixed cost and its stops: one more stop on a
// brush of three moves the smallest budget that draws three uses by 3 stops.
func TestSecShadingCostIsExact(t *testing.T) {
	mk := func(stops int) []byte { return newPkg(gradientPage(stops, 3, shadeUse)).bytes(t) }
	n2, n3 := minPrepare(t, mk(2)), minPrepare(t, mk(3))
	// The brush grows by one stop element in the XML too: a tree and a part of
	// some tens of bytes, far under a stop's charge.
	if d := n3 - n2; d < 3*shadeStopBytes || d > 3*shadeStopBytes+4096 {
		t.Errorf("a third stop on 3 shadings costs %d, want about %d", d, 3*shadeStopBytes)
	}
	if n2 < 3*(shadeBytes+2*shadeStopBytes) {
		t.Errorf("3 shadings of 2 stops draw in %d, under their %d charge", n2, 3*(shadeBytes+2*shadeStopBytes))
	}
}

func fontOnlyErr(t testing.TB, font []byte, lim Limits) error {
	_, err := Convert(fontOnlyPackage(t, font), lim)
	return err
}

// ---- gradients ----

const gradNS = ` xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"`

func gradientPage(stops int, uses int, use string) string {
	var b strings.Builder
	b.WriteString(`<Canvas.Resources><ResourceDictionary` + gradNS + `><LinearGradientBrush x:Key="gb"><LinearGradientBrush.GradientStops>`)
	for i := 0; i < stops; i++ {
		fmt.Fprintf(&b, `<GradientStop Color="#FF%06X" Offset="%g"/>`, i*7919%0xFFFFFF, float64(i)/float64(stops))
	}
	b.WriteString(`</LinearGradientBrush.GradientStops></LinearGradientBrush></ResourceDictionary></Canvas.Resources>`)
	b.WriteString(strings.Repeat(use, uses))
	return page(`<Canvas>` + b.String() + `</Canvas>`)
}

// Text fills and strokes read every stop of a gradient and charge none of
// them: the work budget refuses a page that parses 2 million stops once.
func TestSecGradientStopsAreChargedOnEveryPath(t *testing.T) {
	text := `<Glyphs OriginX="5" OriginY="20" FontRenderingEmSize="12" FontUri="` + fontPart + `" UnicodeString="H" Fill="{StaticResource gb}"/>`
	stroke := `<Path Data="M 0,0 L 5,5" Stroke="{StaticResource gb}" StrokeThickness="1"/>`
	for _, tc := range []struct{ name, use string }{{"text", text}, {"stroke", stroke}, {"fill", shadeUse}} {
		b := newPkg(gradientPage(1000, 4000, tc.use)).withFont().bytes(t)
		var err error
		t0 := time.Now()
		heap := peakHeap(t, func() { _, err = Convert(b, Limits{}) })
		t.Logf("%s: 4000 uses of 1000 stops: package %d bytes, %v, peak heap %d MiB, err %v", tc.name, len(b), time.Since(t0).Round(time.Millisecond), heap, err)
		if !errors.Is(err, ErrLimit) {
			t.Errorf("%s: %v, want ErrLimit", tc.name, err)
		}
	}
}

// Each stop is one unit of work, exactly: n uses of s stops fit MaxWork when
// n*(s+1) does (a glyph is a unit too), and not one use more.
func TestSecGradientStopWorkBoundary(t *testing.T) {
	stroke := `<Path Data="M 0,0 L 5,5" Stroke="{StaticResource gb}" StrokeThickness="1"/>`
	probe := func(work int) error {
		_, err := Convert(newPkg(gradientPage(10, 3, stroke)).bytes(t), Limits{MaxWork: work})
		return err
	}
	lo, hi := 1, 1<<20
	for lo < hi {
		mid := (lo + hi) / 2
		if probe(mid) == nil {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if err := probe(lo - 1); !errors.Is(err, ErrLimit) {
		t.Errorf("one unit under: %v", err)
	}
	// 3 strokes x (10 stops + 1 segment) at least; the stops alone are 30.
	if lo < 33 {
		t.Errorf("the smallest budget that draws 3 strokes of 10 stops is %d, want at least 33", lo)
	}
}

// The last gradient of a page is charged too: one fill of 20 stops is refused
// by a budget of 10, not drawn without its stops.
func TestSecOneGradientOverTheWorkBudget(t *testing.T) {
	for name, use := range map[string]string{
		"fill":   shadeUse,
		"stroke": `<Path Data="M 0,0 L 5,5" Stroke="{StaticResource gb}" StrokeThickness="1"/>`,
	} {
		refuses(t, name, newPkg(gradientPage(20, 1, use)).bytes(t), Limits{MaxWork: 10}, ErrLimit)
	}
}

// ---- shadings ----

// A shading is refused before it is created, and charges nothing when it is.
func TestSecShadeIsRefusedBeforeItIsCreated(t *testing.T) {
	c := newConv(t, newPkg(page(``)).bytes(t), Limits{MaxMemory: 64 << 10})
	r := &renderer{c: c, p: c.doc.AddPage(pdfkit.NewPageSize(100, 100))}
	stops := make([]pdfkit.Stop, 300)
	for i := range stops {
		stops[i] = pdfkit.Stop{Offset: float64(i) / 300, Color: pdfkit.RGB{}}
	}
	// 5 KiB and 300 stops of 256 bytes are more than the 64 KiB left.
	if err := r.shade(pdfkit.Shading{X1: 1, Stops: stops, ExtendStart: true}); !errors.Is(err, ErrLimit) {
		t.Errorf("a shading over the budget: %v", err)
	}
	if c.stored != 0 {
		t.Errorf("a refused shading is charged %d bytes", c.stored)
	}
	if err := r.shade(pdfkit.Shading{X1: 1, Stops: stops[:2]}); err != nil {
		t.Errorf("a two-stop shading: %v", err)
	}
	if want := int64(shadeBytes + 2*shadeStopBytes); c.stored != want {
		t.Errorf("a shading is charged %d bytes, want %d", c.stored, want)
	}
}

const shadeUse = `<Path Data="M 0,0 L 5,0 5,5 Z" Fill="{StaticResource gb}"/>`

// Every use of a gradient creates its own shading object; they were in neither
// the content length nor the memory budget.
func TestSecShadingsAreChargedToMemory(t *testing.T) {
	b := newPkg(gradientPage(2, 100000, shadeUse)).bytes(t)
	var err error
	var res *Result
	t0 := time.Now()
	heap := peakHeap(t, func() { res, err = Convert(b, Limits{MaxRatio: 1 << 20}) })
	t.Logf("100000 two-stop shadings: package %d bytes, %v, peak heap %d MiB, err %v", len(b), time.Since(t0).Round(time.Millisecond), heap, err)
	if heap > worstPeakMiB {
		t.Errorf("peak heap %d MiB", heap)
	}
	if err == nil {
		t.Logf("converted: %d bytes of PDF", len(res.PDF))
	}
	// A budget that holds the page's content cannot hold its shadings.
	small := newPkg(gradientPage(2, 2000, shadeUse)).bytes(t)
	n := minMemory(t, small)
	if n < 2000*shadeBytes {
		t.Errorf("2000 shadings convert in %d bytes: under the %d each is charged", n, shadeBytes)
	}
}

// ---- the Convert buffer ----

// The buffer Convert returns the PDF in holds the font subsets too: a font's
// bytes beyond its instruction tables move the smallest budget that converts
// by exactly one more copy.
func TestSecConvertBufferHoldsFontSubsets(t *testing.T) {
	b := newPkg(page(`<Glyphs OriginX="5" OriginY="20" FontRenderingEmSize="12" FontUri="` + fontPart + `" UnicodeString="Hi" Fill="#FF000000"/>`)).withFont().bytes(t)
	conv, prep := minMemory(t, b), minPrepare(t, b)
	want := int64(len(goregular.TTF)) - instructionBytes(goregular.TTF)
	if conv-prep < want {
		t.Errorf("Convert needs %d beyond Prepare's %d, want at least %d for the font subset", conv, prep, want)
	}
}
