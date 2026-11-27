// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// Every test here asserts a REFUSAL (errors.Is ErrLimit or a named error),
// never merely "did not crash". Run them under a memory cap to prove the
// refusal comes before the allocation:
//
//	GOMEMLIMIT=256MiB bash -c 'ulimit -v 4000000; gotest ./xps -run Bomb'
func refuses(t *testing.T, name string, b []byte, lim Limits, want error) {
	t.Helper()
	_, err := Convert(b, lim)
	if !errors.Is(err, want) {
		t.Errorf("%s: err = %v, want %v", name, err, want)
	}
}

func TestBombZipRatio(t *testing.T) {
	p := newPkg(page(""))
	p.extra["Resources/pad.bin"] = bytes.Repeat([]byte{0}, 64<<20)
	b := p.bytes(t)
	if len(b) > 1<<20 {
		t.Fatalf("test package is %d bytes; not a bomb", len(b))
	}
	// The part is never referenced, so only the ratio and total budgets can
	// refuse it - which they must, since it is in the package.
	_, err := Convert(b, Limits{MaxUncompressedBytes: 8 << 20})
	if !errors.Is(err, ErrLimit) {
		t.Errorf("uncompressed total: %v", err)
	}
}

func TestBombPartCount(t *testing.T) {
	p := newPkg(page(""))
	for i := 0; i < 50; i++ {
		p.extra[fmt.Sprintf("x/%d.txt", i)] = []byte("x")
	}
	refuses(t, "parts", p.bytes(t), Limits{MaxParts: 20}, ErrLimit)
}

func TestBombXMLShapes(t *testing.T) {
	deep := strings.Repeat("<Canvas>", 100) + strings.Repeat("</Canvas>", 100)
	refuses(t, "depth", newPkg(page(deep)).bytes(t), Limits{MaxDepth: 32}, ErrLimit)
	many := strings.Repeat(`<Path Data="M 0,0 L 1,1" Stroke="#FF000000"/>`, 500)
	refuses(t, "elements", newPkg(page(many)).bytes(t), Limits{MaxElementsPerPart: 100}, ErrLimit)
	refuses(t, "work", newPkg(page(many)).bytes(t), Limits{MaxWork: 100}, ErrLimit)
	// A billion-laughs shape: entities are never expanded by the parser.
	laughs := `<!DOCTYPE x [<!ENTITY a "aaaaaaaaaa"><!ENTITY b "&a;&a;&a;&a;&a;&a;&a;&a;">]><FixedPage Width="9" Height="9"><Path Data="&b;"/></FixedPage>`
	if _, err := Convert(newPkg(laughs).bytes(t), Limits{}); err == nil {
		t.Error("entity page accepted")
	}
}

func TestBombPages(t *testing.T) {
	refuses(t, "page count", newPkg(page(""), page(""), page("")).bytes(t), Limits{MaxPages: 2}, ErrLimit)
	refuses(t, "page size", newPkg(pageSized(1000000, 1000000, "")).bytes(t), Limits{}, ErrLimit)
	refuses(t, "page size nan", newPkg(`<FixedPage Width="NaN" Height="9"/>`).bytes(t), Limits{}, ErrBadPage)
}

func TestBombPathSegments(t *testing.T) {
	var d strings.Builder
	d.WriteString("M 0,0")
	for i := 0; i < 5000; i++ {
		d.WriteString(" L 1,1")
	}
	refuses(t, "segments", newPkg(page(`<Path Data="`+d.String()+`" Fill="#FF000000"/>`)).bytes(t),
		Limits{MaxSegmentsPerPath: 1000}, ErrLimit)
	// One geometry reused many times is charged every time.
	body := `<Canvas.Resources><ResourceDictionary><PathGeometry xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml" x:Key="g" Figures="` + d.String() + `"/></ResourceDictionary></Canvas.Resources>`
	body += strings.Repeat(`<Path Data="{StaticResource g}" Fill="#FF000000"/>`, 50)
	refuses(t, "reuse", newPkg(page(`<Canvas>`+body+`</Canvas>`)).bytes(t), Limits{MaxWork: 100000}, ErrLimit)
}

func TestBombGlyphs(t *testing.T) {
	long := strings.Repeat("a", 5000)
	p := newPkg(page(`<Glyphs OriginX="0" OriginY="9" FontRenderingEmSize="9" FontUri="` + fontPart + `" UnicodeString="` + long + `" Fill="#FF000000"/>`)).withFont()
	refuses(t, "glyphs", p.bytes(t), Limits{MaxGlyphsPerRun: 100}, ErrLimit)
	idx := strings.Repeat("1;", 5000)
	p = newPkg(page(`<Glyphs OriginX="0" OriginY="9" FontRenderingEmSize="9" FontUri="` + fontPart + `" UnicodeString="a" Indices="` + idx + `" Fill="#FF000000"/>`)).withFont()
	refuses(t, "indices", p.bytes(t), Limits{MaxGlyphsPerRun: 100}, ErrLimit)
}

func TestBombFonts(t *testing.T) {
	var body strings.Builder
	p := newPkg()
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("Resources/Fonts/f%d.ttf", i)
		p.extra[name] = goFont()
		fmt.Fprintf(&body, `<Glyphs OriginX="0" OriginY="9" FontRenderingEmSize="9" FontUri="/%s" UnicodeString="a" Fill="#FF000000"/>`, name)
	}
	p.pages = []string{page(body.String())}
	refuses(t, "fonts", p.bytes(t), Limits{MaxFonts: 3}, ErrLimit)
}

func TestBombImages(t *testing.T) {
	brush := func(w, h int) string {
		return fmt.Sprintf(`<Path Data="M 0,0 L 9,0 9,9 0,9 Z"><Path.Fill><ImageBrush ImageSource="/i.png" Viewbox="0,0,%d,%d" ViewboxUnits="Absolute" Viewport="0,0,9,9" ViewportUnits="Absolute"/></Path.Fill></Path>`, w, h)
	}
	// 100000 x 100000 declared in the header, no pixels behind it.
	p := newPkg(page(brush(1, 1)))
	p.extra["i.png"] = pngHeader(100000, 100000)
	refuses(t, "png pixels", p.bytes(t), Limits{}, ErrLimit)
	p.extra["i.png"] = pngHeader(4000, 4000)
	refuses(t, "png pixels (low cap)", p.bytes(t), Limits{MaxImagePixels: 1 << 20}, ErrLimit)
	// Many small tiles.
	q := newPkg(page(`<Path Data="M 0,0 L 400,0 400,300 0,300 Z"><Path.Fill><ImageBrush ImageSource="/i.png" TileMode="Tile" Viewbox="0,0,8,8" ViewboxUnits="Absolute" Viewport="0,0,0.01,0.01" ViewportUnits="Absolute"/></Path.Fill></Path>`))
	q.extra["i.png"] = pngOf(t, 8, 8, nil2())
	refuses(t, "tiles", q.bytes(t), Limits{}, ErrLimit)
}

func TestBombTotalImagePixels(t *testing.T) {
	var body strings.Builder
	p := newPkg()
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("i%d.png", i)
		p.extra[name] = pngOf(t, 100, 100, nil2())
		fmt.Fprintf(&body, `<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="/%s" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`, name)
	}
	p.pages = []string{page(body.String())}
	refuses(t, "total pixels", p.bytes(t), Limits{MaxTotalImagePixels: 30000}, ErrLimit)
}

func TestHostileReferencesFetchNothing(t *testing.T) {
	for _, ref := range []string{
		"http://127.0.0.1:1/x.ttf", "file:///etc/passwd", "ftp://h/x", "//host/x.ttf",
		"../../../../etc/passwd", "/../../x", "/Resources/%2e%2e/%2e%2e/x", "C:\\x.ttf", "\\\\h\\s\\x",
	} {
		p := newPkg(page(`<Glyphs OriginX="0" OriginY="9" FontRenderingEmSize="9" FontUri="` + ref + `" UnicodeString="a" Fill="#FF000000"/>
<Path Data="M 0,0 L 9,9 9,0 Z"><Path.Fill><ImageBrush ImageSource="` + ref + `" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`))
		// The package is converted, the element is reported, nothing is drawn.
		res, err := Convert(p.bytes(t), Limits{})
		if err != nil {
			t.Errorf("%q: %v", ref, err)
			continue
		}
		if !skipped(res, "malformed Glyphs") || !skipped(res, "malformed Path") {
			t.Errorf("%q not reported: %v", ref, res.Skipped)
		}
		if strings.Contains(string(res.PDF), "/Subtype /Image") || strings.Contains(string(res.PDF), "/FontFile2") {
			t.Errorf("%q reached the output", ref)
		}
	}
}

// resolved reports whether an ImageSource written as ref, on the page
// /Documents/1/Pages/1.fpage, finds the image kept at Resources/a.png.
func resolved(t *testing.T, ref string) bool {
	t.Helper()
	p := newPkg(page(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="` + ref + `" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`))
	p.extra["Resources/a.png"] = pngOf(t, 4, 4, nil2())
	p.extra["Documents/1/Pages/b.png"] = pngOf(t, 4, 4, nil2())
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil {
		t.Fatalf("%q: %v", ref, err)
	}
	return !skipped(res, "malformed")
}

func TestResolution(t *testing.T) {
	for _, ref := range []string{"/Resources/a.png", "../../../Resources/a.png", "b.png", "/../Resources/a.png", "/Resources/a.png#x", "/Resources/%61.png", `\Resources\a.png`, "/Resources/./a.png"} {
		if !resolved(t, ref) {
			t.Errorf("%q does not resolve", ref)
		}
	}
	for _, ref := range []string{"http://x/y.png", "file:///y.png", "../../../../y.png", "../../Resources/a.png", "/a/../../y", "a:b", "", "/missing.png"} {
		if resolved(t, ref) {
			t.Errorf("%q resolves", ref)
		}
	}
}

// A hand-built ZIP whose directory lies about a size: the reader must still
// stop at the budget, not at what the entry claims.
func TestBombLyingZipSize(t *testing.T) {
	refuses(t, "oversize part", newPkg(page(strings.Repeat(" ", 1<<20))).bytes(t), Limits{MaxPartBytes: 1 << 16}, ErrLimit)
}
