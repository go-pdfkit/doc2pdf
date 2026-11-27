// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"encoding/xml"
	"errors"
	"image/color"
	"testing"
)

// fuzzLimits are small, so that a hostile input is refused fast.
var fuzzLimits = Limits{
	MaxPackageBytes: 1 << 20, MaxPartBytes: 1 << 20, MaxUncompressedBytes: 8 << 20,
	MaxParts: 64, MaxPages: 8, MaxElementsPerPart: 2000, MaxWork: 1 << 18,
	MaxImagePixels: 1 << 20, MaxTotalImagePixels: 4 << 20, MaxDrawnPixels: 4 << 20,
	MaxTiles: 256, MaxFonts: 4,
}

// fuzzPackage is a valid package, holding a font and a PNG, around one page.
func fuzzPackage(t testing.TB, pageXML string) []byte {
	p := newPkg(pageXML).withFont()
	p.extra["i.png"] = pngOf(t, 4, 4, color.RGBA{1, 2, 3, 255})
	return p.bytes(t)
}

func checkOutcome(t *testing.T, res *Result, err error) {
	t.Helper()
	if err == nil && (res == nil || len(res.PDF) == 0) {
		t.Fatal("no error and no PDF")
	}
	if err != nil && res != nil {
		t.Fatalf("an error and a result: %v", err)
	}
}

// FuzzFixedPage fuzzes the FixedPage XML inside a VALID package. FuzzConvert
// mutates deflated ZIP bytes, which rarely survive to the parsers; this one
// always reaches the XML decoder, the tree, the brushes and the text.
func FuzzFixedPage(f *testing.F) {
	for _, body := range []string{
		`<Path Data="M 0,0 L 50,0 50,50 Z" Fill="#FFFF0000" Stroke="#FF0000FF" StrokeThickness="2" StrokeDashArray="1 2"/>`,
		`<Canvas RenderTransform="2,0,0,2,1,1" Opacity="0.5" Clip="M0,0 L9,0 9,9Z"><Path Data="M 0,0 A 5,5 0 1 1 9,9 Z" Fill="sc#1,0.5,0"/></Canvas>`,
		`<Path Data="M0,0L99,0 99,99z"><Path.Fill><RadialGradientBrush Center="5,5" GradientOrigin="5,1e9" RadiusX="1e9" RadiusY="1e-300"><RadialGradientBrush.GradientStops><GradientStop Color="#FF000000" Offset="0"/><GradientStop Color="#FFFFFFFF" Offset="1"/></RadialGradientBrush.GradientStops></RadialGradientBrush></Path.Fill></Path>`,
		`<Glyphs OriginX="5" OriginY="50" FontRenderingEmSize="20" FontUri="` + fontPart + `" UnicodeString="Fuzz" Indices="(1:1)43;;" StyleSimulations="BoldItalicSimulation" Fill="#FF000000"/>`,
		`<Path Data="M0,0L9,0 9,9z"><Path.Fill><ImageBrush ImageSource="/I.PNG" TileMode="FlipXY" Viewbox="0,0,4,4" ViewboxUnits="Absolute" Viewport="0,0,3,3" ViewportUnits="Absolute"/></Path.Fill></Path>`,
		`<Path Data="M0,0h1" Opacity="0.3"><Path.Stroke><SolidColorBrush Color="#80FF0000" Opacity="0.7"/></Path.Stroke></Path>`,
		`<Path Data="M0,0h1" a="1" a="2"/>`,
	} {
		f.Add(body)
	}
	f.Fuzz(func(t *testing.T, body string) {
		res, err := Convert(fuzzPackage(t, page(body)), fuzzLimits)
		checkOutcome(t, res, err)
	})
}

// FuzzGeometry fuzzes the Data of a Path (and a Clip) inside a valid package,
// escaped so that it is the geometry parser that reads it, not the XML's.
func FuzzGeometry(f *testing.F) {
	for _, d := range []string{
		"M 0,0 L 50,0 50,50 Z",
		"F1 M 10,10 C 20,0 30,20 40,10 S 50,0 60,10 Q 70,0 80,10 T 90,10 H 5 V 5 Z",
		"M 0,0 A 5,5 0 1 1 9,9 Z",
		"M1e9,1e9h1h1h1",
		"M 0,0 A 1e308,1e-308 720 1 1 1e308,9",
		"m 1 2 l 3 4 z m 1 1",
	} {
		f.Add(d)
	}
	f.Fuzz(func(t *testing.T, data string) {
		var esc bytes.Buffer
		xml.EscapeText(&esc, []byte(data))
		body := `<Path Data="` + esc.String() + `" Clip="` + esc.String() + `" Fill="#FF0000" Stroke="#0000FF"/>`
		res, err := Convert(fuzzPackage(t, page(body)), fuzzLimits)
		checkOutcome(t, res, err)
	})
}

// FuzzConvert feeds whole packages. The invariant is the one a converter of
// untrusted files owes: it returns a PDF or an error, never panics, and never
// outruns the (small) limits given here.
func FuzzConvert(f *testing.F) {
	seeds := []*pkg{
		newPkg(page(`<Path Data="M 0,0 L 50,0 50,50 Z" Fill="#FFFF0000" Stroke="#FF0000FF" StrokeThickness="2" StrokeDashArray="1 2"/>`)),
		newPkg(page(`<Canvas RenderTransform="2,0,0,2,1,1" Opacity="0.5"><Path Data="M 0,0 A 5,5 0 1 1 9,9 Z" Fill="sc#1,0.5,0"/></Canvas>`)),
		newPkg(page(`<Path Data="M 0,0 L 99,0 99,99 Z"><Path.Fill><LinearGradientBrush StartPoint="0,0" EndPoint="1,1"><LinearGradientBrush.GradientStops><GradientStop Color="#FF000000" Offset="0"/><GradientStop Color="#FFFFFFFF" Offset="1"/></LinearGradientBrush.GradientStops></LinearGradientBrush></Path.Fill></Path>`)),
		newPkg(page(`<Glyphs OriginX="5" OriginY="50" FontRenderingEmSize="20" FontUri="` + fontPart + `" UnicodeString="Fuzz" Indices="(1:1)43;;" StyleSimulations="BoldItalicSimulation" Fill="#FF000000"/>`)).withFont(),
	}
	img := newPkg(page(`<Path Data="M 0,0 L 99,0 99,99 Z"><Path.Fill><ImageBrush ImageSource="/i.png" TileMode="FlipXY" Viewbox="0,0,4,4" ViewboxUnits="Absolute" Viewport="0,0,30,30" ViewportUnits="Absolute"/></Path.Fill></Path>`))
	img.extra["i.png"] = pngOf(f, 4, 4, color.RGBA{1, 2, 3, 255})
	seeds = append(seeds, img)
	for _, s := range seeds {
		f.Add(s.bytes(f))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := Convert(b, fuzzLimits)
		checkOutcome(t, res, err)
		_ = errors.Is(err, ErrLimit)
	})
}

// FuzzAttrScan holds the raw attribute scan to the decoder it guards: whatever
// encoding/xml reads as a start tag with more attributes than the limit, the
// scan must have refused. (The review found `<!DOCTYPE a '>' "<!--" >`, where
// the two disagreed about where a declaration ends.)
func FuzzAttrScan(f *testing.F) {
	for _, s := range []string{
		`<a b="1" c='>' d="2"/>`,
		`<!DOCTYPE a '>' "<!--" ><a b="1" c="2" d="3" e="4"/>`,
		`<!-- <x a="1"/> --><a b="1" c="2" d="3"/>`,
		`<![CDATA[ x ]]><a b="1"/>`,
		`<?pi a="1" b="2"?><a b="1" c="2" d="3"/>`,
		`<a b="1"><c d="2" e="3" f="4"/></a>`,
	} {
		f.Add([]byte(s), 2)
	}
	f.Fuzz(func(t *testing.T, b []byte, max int) {
		if max < 0 || max > 64 {
			return
		}
		if msg := scanAgrees(b, max); msg != "" {
			t.Fatalf("%q: %s", b, msg)
		}
	})
}
