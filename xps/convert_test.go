// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"errors"
	"image/color"
	"strings"
	"testing"

	"github.com/go-pdfkit/extract"
	"github.com/go-pdfkit/reader"
	"golang.org/x/image/font/gofont/goregular"
)

func goFont() []byte { return goregular.TTF }

func nil2() color.Color { return color.RGBA{9, 9, 9, 255} }

// raw converts one page of drawing with the output readable, failing on error.
func raw(t *testing.T, body string, withFont bool) (string, *Result) {
	t.Helper()
	p := newPkg(page(body))
	if withFont {
		p.withFont()
	}
	return convertRaw(t, p.bytes(t), Limits{})
}

func has(t *testing.T, pdf string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(pdf, w) {
			t.Errorf("the PDF lacks %q", w)
		}
	}
}

func skipped(res *Result, what string) bool {
	for _, s := range res.Skipped {
		if strings.Contains(s, what) {
			return true
		}
	}
	return false
}

func pageText(t *testing.T, pdf []byte) string {
	t.Helper()
	src, err := reader.Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := extract.Runs(src, 1)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	for _, r := range runs {
		sb.WriteString(r.Text)
	}
	return strings.ReplaceAll(sb.String(), " ", "")
}

func TestBasePageTransformAndFill(t *testing.T) {
	pdf, res := raw(t, `<Path Data="M 10,10 L 110,10 110,60 10,60 Z" Fill="#FFFF0000"/>`, false)
	if res.Pages != 1 || len(res.Skipped) != 0 {
		t.Fatalf("%d pages, skipped %v", res.Pages, res.Skipped)
	}
	// 96 units per inch to 72 points, y flipped over the 300-unit page.
	has(t, pdf, "0.75 0 0 -0.75 0 225 cm", "1 0 0 rg", "10 10 m", "110 10 l", "h")
	has(t, pdf, "/MediaBox [0 0 300 225]")
}

func TestFillRulesAndStroke(t *testing.T) {
	pdf, _ := raw(t, `
<Path Data="F 0 M 0,0 L 50,0 50,50 Z" Fill="#FF00FF00"/>
<Path Data="F 1 M 0,0 L 50,0 50,50 Z" Fill="#FF00FF00"/>
<Path Data="M 0,0 L 50,50" Stroke="#FF0000FF" StrokeThickness="4" StrokeDashArray="2 1" StrokeDashCap="Round"
 StrokeStartLineCap="Round" StrokeLineJoin="Bevel" StrokeMiterLimit="10"/>`, false)
	has(t, pdf, "f*", "\nf\n", "0 0 1 RG", "4 w", "[8 4] 0 d", "1 J", "2 j", "5 M", "S")
}

func TestPathSyntaxVariants(t *testing.T) {
	pdf, res := raw(t, `
<Path Fill="#FF000000"><Path.Data><PathGeometry Figures="M 0,0 L 5,5 Z"/></Path.Data></Path>
<Path Fill="#FF000000">
 <Path.Data><PathGeometry FillRule="EvenOdd"><PathFigure StartPoint="0,0" IsClosed="true">
  <PolyLineSegment Points="10,0 10,10"/><PolyBezierSegment Points="1,2 3,4 5,6"/>
  <PolyQuadraticBezierSegment Points="1,2 3,4"/><ArcSegment Point="0,0" Size="5,5" SweepDirection="Clockwise"/>
 </PathFigure></PathGeometry></Path.Data></Path>
<Path Data="M 0,0 L 9,9" Fill="sc#1,0,0" Opacity="0.5"/>
<Path Data="M 0,0 L 9,9" Fill="#FF000000" RenderTransform="1,0,0,1,7,8"/>
<Path Data="M 0,0 L 9,9" Fill="#FF000000" Clip="M 0,0 L 5,0 5,5 Z"/>`, false)
	if len(res.Skipped) != 0 {
		t.Errorf("skipped %v", res.Skipped)
	}
	has(t, pdf, "0 0 m", "10 0 l", " c\n", "f*", "W*\nn", "1 0 0 1 7 8 cm", "/GS")
}

func TestCanvasResourcesTransformClipOpacity(t *testing.T) {
	pdf, res := raw(t, `
<Canvas RenderTransform="2,0,0,2,10,20" Opacity="0.5" Clip="M 0,0 L 100,0 100,100 0,100 Z">
 <Canvas.Resources><ResourceDictionary>
  <SolidColorBrush x:Key="red" Color="#FFFF0000" xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"/>
  <PathGeometry x:Key="tri" Figures="M 0,0 L 40,0 40,40 Z" xmlns:x="http://schemas.microsoft.com/winfx/2006/xaml"/>
 </ResourceDictionary></Canvas.Resources>
 <Path Data="{StaticResource tri}" Fill="{StaticResource red}"/>
</Canvas>
<Path Data="{StaticResource missing}" Fill="#FF000000"/>`, false)
	has(t, pdf, "2 0 0 2 10 20 cm", "W*\nn", "1 0 0 rg", "0 0 m", "40 0 l")
	if !skipped(res, "unresolved resource reference") {
		t.Errorf("a missing resource was not reported: %v", res.Skipped)
	}
}

func TestGradients(t *testing.T) {
	pdf, res := raw(t, `
<Path Data="M 0,0 L 100,0 100,100 0,100 Z">
 <Path.Fill><LinearGradientBrush StartPoint="0,0" EndPoint="1,0" MappingMode="RelativeToBoundingBox">
  <LinearGradientBrush.GradientStops><GradientStop Color="#FFFF0000" Offset="0"/><GradientStop Color="#FF0000FF" Offset="1"/></LinearGradientBrush.GradientStops>
 </LinearGradientBrush></Path.Fill></Path>
<Path Data="M 0,0 L 100,0 100,100 0,100 Z">
 <Path.Fill><RadialGradientBrush Center="0.5,0.5" GradientOrigin="0.5,0.5" RadiusX="0.5" RadiusY="0.25" SpreadMethod="Reflect">
  <RadialGradientBrush.GradientStops><GradientStop Color="#FFFFFF00" Offset="0"/><GradientStop Color="#80000000" Offset="1"/></RadialGradientBrush.GradientStops>
 </RadialGradientBrush></Path.Fill></Path>`, false)
	has(t, pdf, "/ShadingType 2", "/ShadingType 3")
	if !skipped(res, "SpreadMethod") || !skipped(res, "gradient stop alpha") {
		t.Errorf("approximations unreported: %v", res.Skipped)
	}
}

func TestImageBrush(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		data []byte
		mime string
	}{
		"png":  {"img/a.png", pngOf(t, 8, 8, color.RGBA{200, 0, 0, 255}), ""},
		"jpeg": {"img/a.jpg", jpegOf(t, 8, 8), ""},
	} {
		t.Run(name, func(t *testing.T) {
			p := newPkg(page(`<Path Data="M 0,0 L 80,0 80,80 0,80 Z"><Path.Fill>
<ImageBrush ImageSource="/` + tc.file + `" Viewbox="0,0,8,8" ViewboxUnits="Absolute" Viewport="0,0,80,80" ViewportUnits="Absolute"/></Path.Fill></Path>`))
			p.extra[tc.file] = tc.data
			pdf, res := convertRaw(t, p.bytes(t), Limits{})
			has(t, pdf, "/Subtype /Image", " Do")
			if len(res.Skipped) != 0 {
				t.Errorf("skipped %v", res.Skipped)
			}
		})
	}
}

func TestImageBrushTiling(t *testing.T) {
	for _, mode := range []string{"Tile", "FlipX", "FlipY", "FlipXY", "None"} {
		p := newPkg(page(`<Path Data="M 0,0 L 100,0 100,100 0,100 Z"><Path.Fill>
<ImageBrush ImageSource="/i.png" TileMode="` + mode + `" Viewbox="0,0,8,8" ViewboxUnits="Absolute" Viewport="0,0,25,25" ViewportUnits="Absolute"/></Path.Fill></Path>`))
		p.extra["i.png"] = pngOf(t, 8, 8, color.RGBA{0, 200, 0, 255})
		pdf, _ := convertRaw(t, p.bytes(t), Limits{})
		n := strings.Count(pdf, " Do")
		switch mode {
		case "None":
			if n != 1 {
				t.Errorf("None drew %d times", n)
			}
		default:
			if n < 4 {
				t.Errorf("%s drew %d tiles", mode, n)
			}
		}
	}
}

func TestJPEGXRIsReported(t *testing.T) {
	p := newPkg(page(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="/i.jxr" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`))
	p.extra["i.jxr"] = []byte("II\xbc\x01 not really")
	_, res := convertRaw(t, p.bytes(t), Limits{})
	if !skipped(res, "JPEG XR") {
		t.Errorf("skipped %v", res.Skipped)
	}
}

func TestGlyphsAreSelectableText(t *testing.T) {
	p := newPkg(page(`<Glyphs OriginX="20" OriginY="100" FontRenderingEmSize="24" FontUri="` + fontPart + `" UnicodeString="Hello XPS" Fill="#FF000000"/>`)).withFont()
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pageText(t, res.PDF); !strings.Contains(got, "HelloXPS") {
		t.Errorf("page text %q", got)
	}
	// Font embedded: the file is carried, not referenced by name only.
	if !strings.Contains(string(res.PDF), "/FontFile2") {
		t.Error("font not embedded")
	}
}

func TestGlyphsStylesAndIndices(t *testing.T) {
	for name, attrs := range map[string]string{
		"bold":     `StyleSimulations="BoldSimulation"`,
		"italic":   `StyleSimulations="ItalicSimulation"`,
		"both":     `StyleSimulations="BoldItalicSimulation"`,
		"rtl":      `BidiLevel="1"`,
		"sideways": `IsSideways="true"`,
		"indices":  `Indices="(1:1)43,500,2,3;48,600;;"`,
		"cluster":  `Indices="(2:1)43"`,
		"offsets":  `Indices=",300;,300;,300;,300;,300"`,
		"opacity":  `Opacity="0.5"`,
	} {
		t.Run(name, func(t *testing.T) {
			p := newPkg(page(`<Glyphs OriginX="50" OriginY="100" FontRenderingEmSize="20" FontUri="` + fontPart + `" UnicodeString="Hello" Fill="#FF112233" ` + attrs + `/>`)).withFont()
			pdf, res := convertRaw(t, p.bytes(t), Limits{})
			has(t, pdf, "BT", "ET", " rg")
			if name == "bold" && !strings.Contains(pdf, " Tr") {
				t.Error("bold lacks a text render mode")
			}
			if name == "sideways" && !skipped(res, "IsSideways") {
				t.Error("IsSideways unreported")
			}
		})
	}
}

func TestObfuscatedFontIsRestored(t *testing.T) {
	const name = "Resources/Fonts/00112233-4455-6677-8899-AABBCCDDEEFF.odttf"
	scrambled, _ := deobfuscate(name, goFont())
	p := newPkg(page(`<Glyphs OriginX="5" OriginY="50" FontRenderingEmSize="20" FontUri="/` + name + `" UnicodeString="Odd" Fill="#FF000000"/>`))
	p.extra[name] = scrambled
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got := pageText(t, res.PDF); !strings.Contains(got, "Odd") {
		t.Errorf("text %q", got)
	}
}

func TestBothRelationshipFlavours(t *testing.T) {
	for _, rel := range []string{relOXPS, relXPS} {
		p := newPkg(page(`<Path Data="M 0,0 L 9,9" Stroke="#FF000000"/>`))
		p.rel = rel
		res, err := Convert(p.bytes(t), Limits{})
		if err != nil || res.Pages != 1 {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestSeveralPagesKeepTheirSizes(t *testing.T) {
	p := newPkg(pageSized(100, 200, ""), pageSized(300, 100, ""))
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 2 {
		t.Fatalf("%v %v", res, err)
	}
	has(t, string(res.PDF), "/MediaBox [0 0 75 150]", "/MediaBox [0 0 225 75]")
}

func TestUnsupportedThingsAreReportedNotDropped(t *testing.T) {
	_, res := raw(t, `
<Path Data="M 0,0 L 9,9 9,0 Z"><Path.Fill><VisualBrush Visual="{StaticResource v}"/></Path.Fill></Path>
<Path Data="M 0,0 L 9,9 9,0 Z" Fill="#FF000000"><Path.OpacityMask><SolidColorBrush Color="#80000000"/></Path.OpacityMask></Path>
<Path Data="M 0,0 L 9,9" StrokeStartLineCap="Triangle" Stroke="#FF000000"/>
<Frob/>`, false)
	for _, w := range []string{"VisualBrush", "OpacityMask", "Triangle", "unknown element"} {
		if !skipped(res, w) {
			t.Errorf("%q unreported in %v", w, res.Skipped)
		}
	}
}

func TestPackageRefusals(t *testing.T) {
	good := newPkg(page("")).bytes(t)
	for name, tc := range map[string]struct {
		in   []byte
		want error
	}{
		"not a zip": {[]byte("hello"), nil},
		"empty":     {nil, nil},
		"truncated": {good[:len(good)/2], nil},
	} {
		if _, err := Convert(tc.in, Limits{}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	p := newPkg()
	if _, err := Convert(p.bytes(t), Limits{}); !errors.Is(err, ErrNoPages) {
		t.Errorf("no pages: %v", err)
	}
	if _, err := Convert(good, Limits{MaxPackageBytes: 10}); !errors.Is(err, ErrLimit) {
		t.Errorf("package cap: %v", err)
	}
	bad := newPkg(`<FixedPage Width="x" Height="3"/>`)
	if _, err := Convert(bad.bytes(t), Limits{}); !errors.Is(err, ErrBadPage) {
		t.Errorf("bad page size: %v", err)
	}
	bad = newPkg(`<Wrong/>`)
	if _, err := Convert(bad.bytes(t), Limits{}); !errors.Is(err, ErrBadPage) {
		t.Errorf("wrong root: %v", err)
	}
	bad = newPkg(`<FixedPage Width="3" Height="3"><Path`)
	if _, err := Convert(bad.bytes(t), Limits{}); !errors.Is(err, ErrBadXML) {
		t.Errorf("bad xml: %v", err)
	}
}

func TestBadFontsAndImagesAreRefusedOrReported(t *testing.T) {
	// A font part that is not a font.
	p := newPkg(page(`<Glyphs OriginX="1" OriginY="9" FontRenderingEmSize="9" FontUri="/Resources/Fonts/go.ttf" UnicodeString="x" Fill="#FF000000"/>`))
	p.extra["Resources/Fonts/go.ttf"] = []byte("not a font at all")
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil || !skipped(res, "malformed Glyphs") {
		t.Errorf("garbage font: %v %v", err, res)
	}
	// A TrueType collection is reported, not parsed.
	p.extra["Resources/Fonts/go.ttf"] = append([]byte("ttcf"), make([]byte, 64)...)
	res, err = Convert(p.bytes(t), Limits{})
	if err != nil || !skipped(res, "collection") {
		t.Errorf("ttc: %v %v", err, res)
	}
	// A broken image.
	q := newPkg(page(`<Path Data="M 0,0 L 9,9 9,0 Z"><Path.Fill><ImageBrush ImageSource="/i.png" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`))
	q.extra["i.png"] = []byte("\x89PNG\r\n\x1a\nxx")
	res, err = Convert(q.bytes(t), Limits{})
	if err != nil || !skipped(res, "malformed Path") {
		t.Errorf("garbage image: %v %v", err, res)
	}
}
