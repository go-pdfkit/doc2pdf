// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

// Regression tests for the findings of the independent security review of
// PR #11. Each one builds a SMALL hostile package, converts it with the
// DEFAULT limits (Limits{}, what doc2pdf passes) under a soft memory limit
// (what GOMEMLIMIT sets), and asserts a refusal or a bounded cost: the
// measured peak heap, and the time.

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-ooxml/opc"
	"github.com/go-pdfkit/pdfkit"
	"golang.org/x/image/tiff"
)

// secMemLimit is the soft memory limit every test here runs under.
const secMemLimit = 640 << 20

// peakHeap runs f and returns the highest HeapAlloc seen, sampled every 2 ms,
// in MiB, with the soft memory limit set for the duration.
func peakHeap(t testing.TB, f func()) uint64 {
	t.Helper()
	old := debug.SetMemoryLimit(secMemLimit)
	defer debug.SetMemoryLimit(old)
	runtime.GC()
	var hi atomic.Uint64
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		var m runtime.MemStats
		for {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc > hi.Load() {
				hi.Store(m.HeapAlloc)
			}
			// A regression must show its cost, not take the host down: the
			// test binary ends well before the machine's memory does.
			if m.HeapAlloc > 2<<30 {
				fmt.Fprintf(os.Stderr, "%s: heap passed 2 GiB and was still growing; aborted\n", t.Name())
				os.Exit(3)
			}
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}()
	f()
	close(stop)
	<-done
	return hi.Load() >> 20
}

// secJunk is n incompressible bytes, so that the package is not "small
// because it deflates well" and the ratio ceiling is not what refuses.
func secJunk(n int) []byte {
	b := make([]byte, n)
	rand.New(rand.NewSource(1)).Read(b)
	return b
}

func lim0(t testing.TB, b []byte) (*Result, error) {
	t.Helper()
	return Convert(b, Limits{})
}

const secImgPath = `<Path Data="M0,0L9,0 9,9z"><Path.Fill><ImageBrush ImageSource="%s" Viewbox="0,0,1,1" ViewboxUnits="Absolute" Viewport="0,0,9,9" ViewportUnits="Absolute"/></Path.Fill></Path>`

// bitonal is a side x side 1-bit PNG: tiny in bytes, large in pixels.
func bitonal(t testing.TB, side int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, side, side), color.Palette{color.Black, color.White})
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Finding 1: a PNG used to be decoded again on every draw. 2048 x 2048 is 4.2
// million pixels in about 1 KB, and forty draws were 168 million. It is one
// handle now: decoded and embedded once, and each draw is the few bytes of its
// operator, which MaxContentBytes counts.
func TestSecPNGRedrawIsOneEmbedding(t *testing.T) {
	data := bitonal(t, 2048)
	if len(data) > 8<<10 {
		t.Fatalf("the PNG is %d bytes; the point is that it is small", len(data))
	}
	conv := func(n int) ([]byte, uint64, time.Duration) {
		p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/i.png"), n)))
		p.extra["i.png"] = data
		res, err, heap, took := secRun(t, p.bytes(t))
		if err != nil {
			t.Fatalf("%d draws of a 4.2 Mpx PNG at the default limits: %v", n, err)
		}
		return res.PDF, heap, took
	}
	one, _, _ := conv(1)
	forty, heap, took := conv(40)
	t.Logf("png redraw: 40 draws in %v, peak heap %d MiB, PDF %d then %d bytes", took.Round(time.Millisecond), heap, len(one), len(forty))
	if heap > 700 {
		t.Errorf("peak heap %d MiB", heap)
	}
	// Thirty-nine more draws add their operators and not 39 images.
	if len(forty) > len(one)+39*1024 {
		t.Errorf("40 draws are %d bytes, one is %d: the image is embedded again", len(forty), len(one))
	}
}

// The pixels are charged once per image, when it is decoded.
func TestSecPNGPixelsAreChargedOnce(t *testing.T) {
	p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/i.png"), 40)))
	p.extra["i.png"] = bitonal(t, 2048)
	b := p.bytes(t)
	if _, err := Convert(b, Limits{MaxDrawnPixels: 2048 * 2048}); err != nil {
		t.Fatalf("exactly the pixels of one decode: %v", err)
	}
	refuses(t, "decode", b, Limits{MaxDrawnPixels: 2048*2048 - 1}, ErrLimit)
}

// A repeat of an image within the budget is allowed: the charge is a budget,
// not a ban.
func TestSecPNGRedrawWithinBudget(t *testing.T) {
	p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/i.png"), 5)))
	p.extra["i.png"] = bitonal(t, 64)
	if _, err := lim0(t, p.bytes(t)); err != nil {
		t.Fatal(err)
	}
}

// A JPEG with a stray byte before its first marker (finding 4) was the one
// pdfkit refused and image/jpeg accepted, so it was decoded. pdfkit takes it as
// it is now: nothing is decoded, whatever the draws.
func TestSecJPEGWithAStrayByteIsEmbedded(t *testing.T) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2048, 2048)), nil); err != nil {
		t.Fatal(err)
	}
	j := buf.Bytes()
	data := append([]byte{0xFF, 0xD8, 0x00}, j[2:]...) // a stray byte before the first marker
	p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/j.jpg"), 40)))
	p.extra["j.jpg"] = data
	b := p.bytes(t)
	var err error
	heap := peakHeap(t, func() { _, err = lim0(t, b) })
	if err != nil {
		t.Fatalf("forty draws of a 4.2 Mpx fallback JPEG: %v", err)
	}
	t.Logf("jpeg with a stray byte: %d byte package, peak heap %d MiB", len(b), heap)
	if heap > 100 {
		t.Errorf("peak heap %d MiB", heap)
	}
	if _, err := Convert(b, Limits{MaxDrawnPixels: 1}); err != nil {
		t.Errorf("a JPEG was charged decoded pixels: %v", err)
	}
}

// A TIFF is charged its pixels the same way, once.
func TestSecTIFFIsChargedOnce(t *testing.T) {
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 100, 100)), nil); err != nil {
		t.Fatal(err)
	}
	p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/i.tif"), 4)))
	p.extra["i.tif"] = buf.Bytes()
	b := p.bytes(t)
	if _, err := Convert(b, Limits{MaxDrawnPixels: 10000}); err != nil {
		t.Fatalf("four draws of one 10000 px image are 10000: %v", err)
	}
	refuses(t, "tiff decode", b, Limits{MaxDrawnPixels: 9999}, ErrLimit)
}

// A JPEG pdfkit takes is embedded as it is: it costs no pixels.
func TestSecDirectJPEGCostsNoPixels(t *testing.T) {
	p := newPkg(page(strings.Repeat(fmt.Sprintf(secImgPath, "/j.jpg"), 4)))
	p.extra["j.jpg"] = jpegOf(t, 64, 64)
	if _, err := Convert(p.bytes(t), Limits{MaxDrawnPixels: 1}); err != nil {
		t.Fatalf("a direct JPEG was charged pixels: %v", err)
	}
}

// newConv is a converter over a package, with the package's own ceilings set
// apart from the converter's, so a converter ceiling can be tested alone.
func newConv(t testing.TB, b []byte, lim Limits) *converter {
	t.Helper()
	lim = lim.withDefaults()
	pkg, err := opc.Open(b, opc.Limits{MaxPackageBytes: 1 << 30, MaxPartBytes: 1 << 30, MaxUncompressedBytes: 1 << 30, MaxParts: lim.MaxParts})
	if err != nil {
		t.Fatal(err)
	}
	return &converter{lim: lim, pkg: pkg, budget: 1 << 40, skip: map[string]int{}, doc: pdfkit.New(pdfkit.Options{})}
}

// caseAliases is the spellings "/Resources/ImagePart.png" can take.
func caseAliases(k int) []string {
	var out []string
	for i := 0; i < k; i++ {
		name := []byte("imagepart")
		for bit := range name {
			if i>>bit&1 == 1 {
				name[bit] -= 32
			}
		}
		out = append(out, string(name)+".png")
	}
	return out
}

// Finding 2: opc finds a part case-insensitively; every alias of one part is
// ONE part, read and cached once.
func TestSecCaseAliasesAreOnePart(t *testing.T) {
	p := newPkg(page(``))
	p.extra["imagepart.png"] = pngHeader(1, 1)
	c := newConv(t, p.bytes(t), Limits{})
	var first *imageInfo
	for _, alias := range caseAliases(16) {
		name, err := c.resolve("Documents/1/Pages/1.fpage", "/"+alias)
		if err != nil {
			t.Fatal(err)
		}
		if name != "imagepart.png" {
			t.Fatalf("%q resolved to %q, not the part's real name", alias, name)
		}
		im, err := c.image(name)
		if err != nil {
			t.Fatal(err)
		}
		if first == nil {
			first = im
		}
		if im != first {
			t.Fatalf("alias %q is a second cache entry", alias)
		}
	}
	if len(c.images) != 1 {
		t.Fatalf("%d cached images for one part", len(c.images))
	}
	if c.pixels != 1 {
		t.Fatalf("one pixel charged %d times", c.pixels)
	}
}

// Two parts that differ only in case in the ZIP stay two parts.
func TestSecDistinctCaseParts(t *testing.T) {
	p := newPkg(page(``))
	p.extra["a.png"] = pngHeader(1, 1)
	p.extra["A.png"] = pngHeader(2, 2)
	c := newConv(t, p.bytes(t), Limits{})
	for _, want := range []string{"a.png", "A.png"} {
		got, err := c.resolve("x/y", "/"+want)
		if err != nil || got != want {
			t.Fatalf("resolve %q = %q, %v", want, got, err)
		}
	}
	if got, err := c.resolve("x/y", "/a.PNG"); err != nil || (got != "a.png" && got != "A.png") {
		t.Fatalf("an alias of either: %q, %v", got, err)
	}
	if _, err := c.resolve("x/y", "/nothere.png"); !errors.Is(err, ErrBadPartName) {
		t.Fatalf("a missing part: %v", err)
	}
}

// The whole conversion: sixteen aliases of an 8 MiB part, each brush refused
// AFTER the part was read (no Viewbox), keep it once, not sixteen times.
func TestSecCaseAliasRetention(t *testing.T) {
	const partSize = 8 << 20
	var body strings.Builder
	for _, alias := range caseAliases(16) {
		fmt.Fprintf(&body, `<Path Data="M0,0L9,0 9,9z"><Path.Fill><ImageBrush ImageSource="/%s"/></Path.Fill></Path>`, alias)
	}
	p := newPkg(page(body.String()))
	p.extra["imagepart.png"] = append(pngHeader(1, 1), make([]byte, partSize)...)
	p.extra["pad.bin"] = secJunk(partSize / 100)
	b := p.bytes(t)
	var res *Result
	var err error
	heap := peakHeap(t, func() { res, err = lim0(t, b) })
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("case aliases: %d byte package, peak heap %d MiB, skipped %v", len(b), heap, res.Skipped)
	// Sixteen retained copies would be 128 MiB on their own.
	if heap > 72 {
		t.Errorf("peak heap %d MiB for one 8 MiB part", heap)
	}
}

// The bytes of a font are charged against the memory budget, and an image part
// is not kept at all until it is drawn.
func TestSecRetainedBytesAreCharged(t *testing.T) {
	p := newPkg(page(``)).withFont()
	p.extra["big.png"] = append(pngHeader(1, 1), make([]byte, 2<<20)...)
	b := p.bytes(t)
	c := newConv(t, b, Limits{MaxMemory: 1 << 10})
	if _, err := c.font("/Resources/Fonts/go.ttf", "/Resources/Fonts/go.ttf"); !errors.Is(err, ErrLimit) {
		t.Fatalf("a font larger than the budget: %v", err)
	}
	c = newConv(t, b, Limits{MaxMemory: 1 << 10})
	c.image("big.png")
	if c.kept != 0 {
		t.Errorf("an image that is not drawn holds %d bytes", c.kept)
	}
}

// A part that failed is read once, however many brushes name it.
func TestSecBadImageIsRemembered(t *testing.T) {
	p := newPkg(page(``))
	p.extra["bad.png"] = []byte("not an image")
	c := newConv(t, p.bytes(t), Limits{})
	_, e1 := c.image("bad.png")
	read := c.read
	_, e2 := c.image("bad.png")
	if !errors.Is(e1, ErrBadImage) || e2 != e1 || c.read != read {
		t.Fatalf("errors %v then %v, read %d then %d", e1, e2, read, c.read)
	}
}

// Finding 3: the content stream is an in-memory buffer; the default MaxWork
// keeps it near a hundred MiB. The probe is 16 paths of 100000 segments, each
// filled and stroked, on a page named four times.
func TestSecContentStreamBound(t *testing.T) {
	const per = 100000
	onePath := `<Path Data="M1e9,1e9` + strings.Repeat("h1", per-1) + `" Fill="#FF0000" Stroke="#0000FF"/>`
	pg := page(strings.Repeat(onePath, 16))
	p := newPkg(pg)
	var doc strings.Builder
	fmt.Fprintf(&doc, `<FixedDocument xmlns="%s">`, nsOXPS)
	for i := 0; i < 6; i++ {
		doc.WriteString(`<PageContent Source="Pages/1.fpage"/>`)
	}
	doc.WriteString(`</FixedDocument>`)
	p.doc = doc.String()
	p.extra["pad.bin"] = secJunk(6 * len(pg) / 250)
	b := p.bytes(t)
	var err error
	t0 := time.Now()
	heap := peakHeap(t, func() { _, err = lim0(t, b) })
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "bytes of ") { // the content ceiling or, five copies of it, the memory budget
		t.Fatalf("9.6 million segments at the default limits: %v", err)
	}
	t.Logf("content stream: %d byte package refused after %v, peak heap %d MiB", len(b), time.Since(t0).Round(time.Millisecond), heap)
	if heap > 500 {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// Finding 5: a start tag with millions of attributes is refused before the
// decoder allocates for them.
func TestSecAttributeBomb(t *testing.T) {
	n := 4_000_000
	pg := `<FixedPage Width="9" Height="9" ` + strings.Repeat(`a="" `, n) + `/>`
	p := newPkg(pg)
	p.extra["pad.bin"] = secJunk(len(pg) / 100)
	b := p.bytes(t)
	var err error
	heap := peakHeap(t, func() { _, err = lim0(t, b) })
	if !errors.Is(err, ErrLimit) || !strings.Contains(err.Error(), "attributes") {
		t.Fatalf("%d attributes: %v", n, err)
	}
	t.Logf("attribute bomb: %d byte package, peak heap %d MiB", len(b), heap)
	if heap > 120 {
		t.Errorf("peak heap %d MiB", heap)
	}
}

func TestSecDuplicateAttributeIsRefused(t *testing.T) {
	p := newPkg(`<FixedPage xmlns="` + nsOXPS + `" Width="9" Width="400" Height="9"/>`)
	_, err := lim0(t, p.bytes(t))
	if !errors.Is(err, ErrBadXML) {
		t.Fatalf("a duplicate attribute: %v", err)
	}
}

func TestSecCheckAttrCounts(t *testing.T) {
	attrs := func(n int) string { return strings.Repeat(` a="1"`, n) }
	for _, tc := range []struct {
		name string
		xml  string
		ok   bool
	}{
		{"exactly max", `<a` + attrs(4) + `/>`, true},
		{"one over", `<a` + attrs(5) + `/>`, false},
		{"over on a later element", `<r><a/><b` + attrs(5) + `/></r>`, false},
		{"single quotes", `<a b='1' c='2' d='3' e='4' f='5'/>`, false},
		{"equals and gt inside values", `<a b="=>=>=" c='>=' d="" e="">text = <b/></a>`, true},
		{"equals in text", `<a>= = = = = = = = =</a>`, true},
		{"comment is not a tag", `<a><!-- <x a="1" b="2" c="3" d="4" e="5"/> --></a>`, true},
		{"cdata is not a tag", `<a><![CDATA[ <x a="1" b="2" c="3" d="4" e="5"/> ]]></a>`, true},
		{"pi is not a tag", `<?x a="1" b="2" c="3" d="4" e="5"?><a/>`, true},
		{"after a comment", `<!-- x --><a` + attrs(5) + `/>`, false},
		{"unterminated comment", `<!--        <x` + attrs(9) + `/>`, true},
		{"unterminated cdata", `<a><![CDATA[        <x` + attrs(9) + `/>`, true},
		{"unterminated pi", `<?        <x` + attrs(9) + `/>`, true},
		{"unterminated tag", `<a` + attrs(4), true},
		{"unterminated tag over", `<a` + attrs(5), false},
		{"unterminated quote", `<a b="` + attrs(9), true},
	} {
		err := checkAttrCounts([]byte(tc.xml), 4)
		if (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
		if err != nil && !errors.Is(err, ErrLimit) {
			t.Errorf("%s: %v is not a limit refusal", tc.name, err)
		}
	}
}

// Second review, finding 2: where a DOCTYPE ends is a matter of quotes that
// encoding/xml and a byte scan read differently; `<!DOCTYPE a '>' "<!--" >`
// hid the root element, and six million attributes, from the scan. No `<!`
// declaration is allowed at all.
func TestSecDoctypeIsRefused(t *testing.T) {
	for _, prefix := range []string{
		`<!DOCTYPE a '>' "<!--" >`,
		`<!DOCTYPE a [ <!ENTITY e "=====">]>`,
		`<!DOCTYPE a>`,
		`<!x`,
	} {
		if _, err := parseXML([]byte(prefix+`<a/>`), 8, 100, 100); !errors.Is(err, ErrBadXML) {
			t.Errorf("%q: %v", prefix, err)
		}
	}
	// And through Convert, with the attributes the prefix was meant to hide.
	n := 2_000_000
	pg := `<!DOCTYPE a '>' "<!--" >` + `<FixedPage Width="9" Height="9" ` + strings.Repeat(`a="" `, n) + `/>`
	p := newPkg(pg)
	p.extra["pad.bin"] = secJunk(len(pg) / 100)
	b := p.bytes(t)
	var err error
	heap := peakHeap(t, func() { _, err = lim0(t, b) })
	if !errors.Is(err, ErrBadXML) {
		t.Fatalf("a hidden attribute bomb: %v", err)
	}
	t.Logf("doctype attribute bomb: %d byte package, peak heap %d MiB", len(b), heap)
	if heap > 120 {
		t.Errorf("peak heap %d MiB", heap)
	}
}

// Whatever the decoder reads as a start tag, the scan must have counted: a
// tag the decoder returns with more attributes than the limit is a tag the
// scan refused. The same invariant is the fuzz target FuzzAttrScan.
func TestSecScanAgreesWithDecoder(t *testing.T) {
	for _, in := range []string{
		`<a b="1" c='>' d="2"/>`,
		`<a b="1"><!-- <x y="1" z="2"/> --><c d="3" e="4" f="5"/></a>`,
		`<?pi a="1" b="2"?><a b="1" c="2" d="3"/>`,
		`<a><![CDATA[<x y="1" z="2" w="3"/>]]><b q="1" r="2" s="3" t="4"/></a>`,
	} {
		if err := scanAgrees([]byte(in), 2); err != "" {
			t.Errorf("%q: %s", in, err)
		}
	}
}

// scanAgrees reports "" when checkAttrCounts and encoding/xml agree about a
// document, and what differs otherwise.
func scanAgrees(b []byte, max int) string {
	refused := checkAttrCounts(b, max) != nil
	dec := xml.NewDecoder(bytes.NewReader(b))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if st, ok := tok.(xml.StartElement); ok && len(st.Attr) > max && !refused {
			return fmt.Sprintf("the decoder read %d attributes (limit %d) that the scan let pass", len(st.Attr), max)
		}
	}
}

// Finding 6: 120000 distinct opacities made pdfkit's SetAlpha scan quadratic
// (14.8 s a page); they are rounded to 1/255, so there are at most 256.
func TestSecAlphaIsQuantised(t *testing.T) {
	const n = 120000
	var body strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&body, `<Path Data="M0,0h1" Fill="#000000" Opacity="%g"/>`, float64(i)/float64(n+1))
	}
	pg := page(body.String())
	p := newPkg(pg)
	p.extra["pad.bin"] = secJunk(len(pg) / 100)
	b := p.bytes(t)
	t0 := time.Now()
	out, res := convertRaw(t, b, Limits{})
	d := time.Since(t0)
	t.Logf("alpha: %d distinct opacities, %v, %d bytes", n, d.Round(time.Millisecond), len(res.PDF))
	// The structural check below is the proof; the time only catches a return of
	// the quadratic scan (14.8 s plain), with room for -race and a loaded host
	// (12 s under -race here).
	if d > 40*time.Second {
		t.Errorf("%v for one page of %d opacities", d, n)
	}
	if got := strings.Count(out, "/ca "); got > 256 {
		t.Errorf("%d distinct alpha states", got)
	}
}

func TestSecSetAlphaRounds(t *testing.T) {
	doc := pdfkit.New(pdfkit.Options{})
	r := &renderer{p: doc.AddPage(pdfkit.NewPageSize(10, 10))}
	r.setAlpha(0.5004)
	r.setAlpha(0.5006)
	r.setAlpha(2)
	r.setAlpha(-1)
	var out bytes.Buffer
	if err := doc.Write(&out); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "/ca "); got != 3 {
		t.Fatalf("%d states for 0.5004, 0.5006, 2 and -1: want 3 (128/255, 1, 0)", got)
	}
}

// Finding 7: a radial gradient whose radii ratio underflows or overflows must
// not write Inf or NaN into the PDF.
func TestSecRadialGradientIsFinite(t *testing.T) {
	for _, c := range []struct{ rx, ry, oy string }{
		{"1e9", "1e-320", "1e9"},
		{"1e9", "5e-324", "1e9"},
		{"1e9", "1e-300", "1e9"},
		{"1", "1e-10", "5"}, // a ratio below 1e-9
		{"1", "1e10", "5"},  // a ratio above 1e9
		{"1e-300", "1e300", "1"},
		{"1", "1e-9", "1e9"},   // in range, but 1e9/1e-9 is 1e18 once divided by the ratio
		{"1", "1e-9", "1e300"}, // beyond what a number may be at all
		{"1", "1", "1e300"},    // a coordinate beyond 1e12
		{"1e300", "1", "1"},
	} {
		body := `<Path Data="M0,0L99,0 99,99z"><Path.Fill><RadialGradientBrush Center="5,5" GradientOrigin="5,` + c.oy +
			`" RadiusX="` + c.rx + `" RadiusY="` + c.ry + `"><RadialGradientBrush.GradientStops><GradientStop Color="#FF000000" Offset="0"/><GradientStop Color="#FFFFFFFF" Offset="1"/></RadialGradientBrush.GradientStops></RadialGradientBrush></Path.Fill></Path>`
		out, res := convertRaw(t, newPkg(page(body)).bytes(t), Limits{})
		out = strings.ReplaceAll(out, "/Info", "") // the trailer's own key
		for _, bad := range []string{"Inf", "NaN"} {
			if strings.Contains(out, bad) {
				t.Errorf("rx=%s ry=%s oy=%s: %s in the PDF", c.rx, c.ry, c.oy, bad)
			}
		}
		// Refused, not drawn with a wrong number: the page is still made and
		// the brush is counted as not drawn.
		if len(res.Skipped) == 0 || strings.Contains(out, "/Shading") {
			t.Errorf("rx=%s ry=%s oy=%s: the gradient was drawn (skipped %v)", c.rx, c.ry, c.oy, res.Skipped)
		}
	}
}

// An ordinary ellipse is still drawn.
func TestSecRadialGradientEllipseStillDrawn(t *testing.T) {
	body := `<Path Data="M0,0L99,0 99,99z"><Path.Fill><RadialGradientBrush Center="50,50" GradientOrigin="50,50" RadiusX="40" RadiusY="20"><RadialGradientBrush.GradientStops><GradientStop Color="#FF000000" Offset="0"/><GradientStop Color="#FFFFFFFF" Offset="1"/></RadialGradientBrush.GradientStops></RadialGradientBrush></Path.Fill></Path>`
	out, res := convertRaw(t, newPkg(page(body)).bytes(t), Limits{})
	if !strings.Contains(out, "/Shading") || len(res.Skipped) != 0 {
		t.Fatalf("shading drawn %v, skipped %v", strings.Contains(out, "/Shading"), res.Skipped)
	}
}
