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
	// 12 KiB and 300 stops of 1200 bytes are more than the 64 KiB left.
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
