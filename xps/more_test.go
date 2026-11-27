// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"image"
	"image/color"
	"testing"

	"golang.org/x/image/tiff"
)

func tiffOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestTIFFIsDecodedAndDrawn(t *testing.T) {
	p := newPkg(page(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="/i.tif" Viewbox="0,0,4,4" ViewboxUnits="Absolute" Viewport="0,0,9,9" ViewportUnits="Absolute"/></Path.Fill></Path>`))
	p.extra["i.tif"] = tiffOf(t, 4, 4)
	pdf, res := convertRaw(t, p.bytes(t), Limits{})
	has(t, pdf, "/Subtype /Image")
	if len(res.Skipped) != 0 {
		t.Errorf("skipped %v", res.Skipped)
	}
}

func TestColorConvertedBitmapKeepsImageAndReportsProfile(t *testing.T) {
	p := newPkg(page(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="{ColorConvertedBitmap /i.png /p.icc}" Viewbox="0,0,4,4" ViewboxUnits="Absolute" Viewport="0,0,9,9" ViewportUnits="Absolute"/></Path.Fill></Path>`))
	p.extra["i.png"] = pngOf(t, 4, 4, color.RGBA{9, 9, 9, 255})
	pdf, res := convertRaw(t, p.bytes(t), Limits{})
	has(t, pdf, "/Subtype /Image")
	if !skipped(res, "ICC profile") {
		t.Errorf("profile unreported: %v", res.Skipped)
	}
}

func TestTextFillsThatAreNotPlainColours(t *testing.T) {
	_, res := raw(t, `
<Glyphs OriginX="5" OriginY="50" FontRenderingEmSize="20" FontUri="`+fontPart+`" UnicodeString="Grad">
 <Glyphs.Fill><LinearGradientBrush StartPoint="0,0" EndPoint="1,0"><LinearGradientBrush.GradientStops>
  <GradientStop Color="#FF0000FF" Offset="0"/><GradientStop Color="#FFFF0000" Offset="1"/></LinearGradientBrush.GradientStops></LinearGradientBrush></Glyphs.Fill></Glyphs>
<Glyphs OriginX="5" OriginY="80" FontRenderingEmSize="20" FontUri="`+fontPart+`" UnicodeString="Img">
 <Glyphs.Fill><ImageBrush ImageSource="/i.png" Viewbox="0,0,1,1" Viewport="0,0,1,1"/></Glyphs.Fill></Glyphs>`, true)
	for _, w := range []string{"gradient text fill", "text fill that is not"} {
		if !skipped(res, w) {
			t.Errorf("%q unreported: %v", w, res.Skipped)
		}
	}
}

func TestGradientStroke(t *testing.T) {
	_, res := raw(t, `<Path Data="M 0,0 L 50,50" StrokeThickness="3"><Path.Stroke><LinearGradientBrush StartPoint="0,0" EndPoint="1,1"><LinearGradientBrush.GradientStops>
<GradientStop Color="#FF0000FF" Offset="0"/><GradientStop Color="#FFFF0000" Offset="1"/></LinearGradientBrush.GradientStops></LinearGradientBrush></Path.Stroke></Path>`, false)
	if !skipped(res, "gradient stroke") {
		t.Errorf("unreported: %v", res.Skipped)
	}
}

func TestStartPartFoundByContentType(t *testing.T) {
	p := newPkg(page(`<Path Data="M 0,0 L 9,9" Stroke="#FF000000"/>`))
	p.rel = "http://example.com/something-else"
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 1 {
		t.Fatalf("%v %v", res, err)
	}
}

func TestPagesThatAreNotThereAreReported(t *testing.T) {
	p := newPkg(page(""))
	p.extra["Documents/1/FixedDocument.fdoc"] = []byte(`<FixedDocument xmlns="` + nsOXPS + `"><PageContent Source="Pages/1.fpage"/><PageContent Source="Pages/gone.fpage"/><PageContent Source="http://h/x"/></FixedDocument>`)
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 1 || !skipped(res, "page reference") {
		t.Errorf("%d pages, skipped %v", res.Pages, res.Skipped)
	}
}

func TestNestedAlternateContentUsesTheFallback(t *testing.T) {
	pdf, _ := raw(t, `<AlternateContent><Choice Requires="x"><Path Data="M 0,0 L 1,1" Fill="#FF00FF00"/></Choice><Fallback><Path Data="M 5,5 L 6,6" Fill="#FFFF0000"/></Fallback></AlternateContent>`, false)
	has(t, pdf, "5 5 m")
	if bytes.Contains([]byte(pdf), []byte("0 1 0 rg")) {
		t.Error("the Choice was drawn")
	}
}

func TestCanvasDepthStaysUnderTheLimit(t *testing.T) {
	deep := ""
	end := ""
	for i := 0; i < 40; i++ {
		deep += "<Canvas>"
		end += "</Canvas>"
	}
	if _, err := Convert(newPkg(page(deep+`<Path Data="M 0,0 L 1,1" Stroke="#FF000000"/>`+end)).bytes(t), Limits{}); err != nil {
		t.Errorf("40 nested canvases: %v", err)
	}
}
