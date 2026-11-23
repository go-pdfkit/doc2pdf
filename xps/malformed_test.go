// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// inked says whether the page's content stream paints anything: a fill, a
// stroke or a shading.
func inked(pdf string) bool {
	return strings.Contains(pdf, "\nf\n") || strings.Contains(pdf, "f*") ||
		strings.Contains(pdf, "\nS\n") || strings.Contains(pdf, "sh\n")
}

// rejected is the shared shape of a malformed attribute: the page converts, the
// element is named in the report, and nothing of it is painted.
func rejected(t *testing.T, name, body, note string) {
	t.Helper()
	pdf, res := raw(t, body, false)
	if !skipped(res, note) {
		t.Errorf("%s: %q not reported: %v", name, note, res.Skipped)
	}
	if inked(pdf) {
		t.Errorf("%s: something was painted", name)
	}
}

// drawsInk is the control: the same shape, well formed, paints.
func drawsInk(t *testing.T, name, body string) {
	t.Helper()
	pdf, res := raw(t, body, false)
	if !inked(pdf) {
		t.Errorf("%s: nothing painted (%v)", name, res.Skipped)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("%s: skipped %v", name, res.Skipped)
	}
}

func pathGeom(fig string) string {
	return `<Path Fill="#FF000000"><Path.Data><PathGeometry>` + fig + `</PathGeometry></Path.Data></Path>`
}

func TestFigureControl(t *testing.T) {
	drawsInk(t, "poly", pathGeom(`<PathFigure StartPoint="0,0"><PolyLineSegment Points="9,0 9,9"/></PathFigure>`))
	drawsInk(t, "arc", pathGeom(`<PathFigure StartPoint="0,0"><ArcSegment Point="9,9" Size="5,5" RotationAngle="10"/></PathFigure>`))
}

func TestMalformedFigures(t *testing.T) {
	for name, fig := range map[string]string{
		"start missing":  `<PathFigure><PolyLineSegment Points="9,0 9,9"/></PathFigure>`,
		"start one":      `<PathFigure StartPoint="1"><PolyLineSegment Points="9,0 9,9"/></PathFigure>`,
		"start three":    `<PathFigure StartPoint="1,2,3"><PolyLineSegment Points="9,0 9,9"/></PathFigure>`,
		"start text":     `<PathFigure StartPoint="x,y"><PolyLineSegment Points="9,0 9,9"/></PathFigure>`,
		"line text":      `<PathFigure StartPoint="0,0"><PolyLineSegment Points="a b"/></PathFigure>`,
		"line odd":       `<PathFigure StartPoint="0,0"><PolyLineSegment Points="1,2,3"/></PathFigure>`,
		"bezier four":    `<PathFigure StartPoint="0,0"><PolyBezierSegment Points="1,2,3,4"/></PathFigure>`,
		"quad two":       `<PathFigure StartPoint="0,0"><PolyQuadraticBezierSegment Points="1,2"/></PathFigure>`,
		"arc rotation":   `<PathFigure StartPoint="0,0"><ArcSegment Point="9,9" Size="5,5" RotationAngle="x"/></PathFigure>`,
		"arc no point":   `<PathFigure StartPoint="0,0"><ArcSegment Size="5,5"/></PathFigure>`,
		"arc point one":  `<PathFigure StartPoint="0,0"><ArcSegment Point="9" Size="5,5"/></PathFigure>`,
		"arc no size":    `<PathFigure StartPoint="0,0"><ArcSegment Point="9,9"/></PathFigure>`,
		"arc size three": `<PathFigure StartPoint="0,0"><ArcSegment Point="9,9" Size="1,2,3"/></PathFigure>`,
		"second figure":  `<PathFigure StartPoint="0,0"><PolyLineSegment Points="9,0 9,9"/></PathFigure><PathFigure StartPoint="x"/>`,
	} {
		rejected(t, name, pathGeom(fig), "malformed Path")
	}
}

// A limit that ends one segment list is the error reported, not whatever
// malformed thing follows it.
func TestLimitBeforeMalformedSegment(t *testing.T) {
	body := pathGeom(`<PathFigure StartPoint="0,0"><PolyLineSegment Points="1,1 2,2 3,3"/><ArcSegment Size="x"/></PathFigure>`)
	refuses(t, "figure", newPkg(page(body)).bytes(t), Limits{MaxSegmentsPerPath: 3}, ErrLimit)
	// One segment more, and the figure is the malformed thing.
	if res, err := Convert(newPkg(page(body)).bytes(t), Limits{MaxSegmentsPerPath: 10}); err != nil || !skipped(res, "malformed Path") {
		t.Errorf("malformed arc: %v %v", err, res)
	}
}

func abbrev(s string, max int) (*geometry, error) {
	b := &builder{g: &geometry{evenOdd: true}, max: max}
	err := parseAbbrev(s, b)
	return b.g, err
}

func TestAbbrevErrors(t *testing.T) {
	for _, s := range []string{
		"H 5", "V 5", "C 1,2,3,4,5,6", "S 1,2,3,4", "Q 1,2,3,4", "A 1 1 0 0 1 5 5", "Z", "L 1,1",
		"M 0,0 H", "M 0,0 V", "M 0,0 C 1,2", "M 0,0 S 1,2", "M 0,0 Q 1", "M 0,0 A 1", "M 0,0 L",
		"M 0,0 L 1,1 F 1", "F 2 M 0,0", "F M 0,0", "M 0,0 X 1", "5", "M 0,0 Z 5",
		"M 0,0 L 1,1 L 2,2 L 3,3",
	} {
		max := 100
		if strings.HasSuffix(s, "L 3,3") {
			max = 3
		}
		if _, err := abbrev(s, max); err == nil {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestAbbrevLimitBeatsALaterError(t *testing.T) {
	_, err := abbrev("M 0,0 L 1,1 L 2,2 X", 2)
	if !errors.Is(err, ErrLimit) {
		t.Errorf("err = %v, want the limit", err)
	}
}

func TestAbbrevExponentsAndZeroRadius(t *testing.T) {
	g, err := abbrev("M 0,0 L 1e2,5E1", 100)
	if err != nil || len(g.segs) != 2 || g.segs[1].v[0] != 100 || g.segs[1].v[1] != 50 {
		t.Fatalf("exponent: %v %v", g, err)
	}
	// A zero radius is a line, whichever radius it is.
	for _, s := range []string{"M 0,0 A 0 5 0 0 1 10,10", "M 0,0 A 5 0 0 0 1 10,10", "M 0,0 A 0 0 0 1 0 10,10"} {
		g, err := abbrev(s, 100)
		if err != nil || len(g.segs) != 2 || g.segs[1].op != 'L' || g.segs[1].v[0] != 10 || g.segs[1].v[1] != 10 {
			t.Errorf("%q: %v %v", s, g, err)
		}
	}
	// A real arc is curves.
	g, _ = abbrev("M 0,0 A 5 5 0 0 1 10,0", 100)
	if len(g.segs) < 2 || g.segs[1].op != 'C' {
		t.Errorf("arc: %v", g.segs)
	}
}

func TestScannerNumbers(t *testing.T) {
	for _, s := range []string{"", ".", "+", "-", "e5", ".e5", "x"} {
		sc := scanner{s: s}
		if v, ok := sc.num(); ok {
			t.Errorf("%q read as %v", s, v)
		}
	}
	for s, want := range map[string]float64{"5": 5, ".5": 0.5, "-.5": -0.5, "5.": 5, "+1e1": 10, "1e": 1, "2E-1": 0.2} {
		sc := scanner{s: s}
		if v, ok := sc.num(); !ok || v != want {
			t.Errorf("%q = %v %v, want %v", s, v, ok, want)
		}
	}
	// Numbers that go wrong part-way are no numbers: nothing is read.
	if v, ok := parseNums("1 x", 9); ok || v != nil {
		t.Errorf("1 x: %v %v", v, ok)
	}
	if v, ok := parseNums("1 2", 9); !ok || len(v) != 2 {
		t.Errorf("1 2: %v %v", v, ok)
	}
	if _, ok := parseNums("1 2 3", 2); ok {
		t.Error("over the maximum")
	}
}

// A builder that has failed adds nothing and asks for nothing more.
func TestBuilderStopsAfterAnError(t *testing.T) {
	calls := 0
	b := &builder{g: &geometry{}, max: 100, spend: func(int) error {
		if calls++; calls > 1 {
			return ErrLimit
		}
		return nil
	}}
	b.add(seg{op: 'M'})
	b.add(seg{op: 'L'})
	b.add(seg{op: 'L'})
	b.add(seg{op: 'L'})
	if calls != 2 || len(b.g.segs) != 1 || !errors.Is(b.err, ErrLimit) {
		t.Errorf("calls %d, segs %d, err %v", calls, len(b.g.segs), b.err)
	}
	b = &builder{g: &geometry{}, max: 1}
	b.add(seg{op: 'M'})
	b.add(seg{op: 'L'})
	b.add(seg{op: 'L'})
	if len(b.g.segs) != 1 || !errors.Is(b.err, ErrLimit) {
		t.Errorf("segs %d, err %v", len(b.g.segs), b.err)
	}
}

func TestLinearToSRGB(t *testing.T) {
	for _, in := range []float64{0, 0.001, 0.0031308} {
		// At and below the knee the curve is the straight line, exactly.
		if got, want := linearToSRGB(in), 12.92*in; got != want {
			t.Errorf("linearToSRGB(%v) = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[float64]float64{1: 1, 2: 1, -1: 0} {
		if got := linearToSRGB(in); math.Abs(got-want) > 1e-12 {
			t.Errorf("linearToSRGB(%v) = %v, want %v", in, got, want)
		}
	}
	if got := linearToSRGB(0.5); math.Abs(got-0.735357) > 1e-5 {
		t.Errorf("linearToSRGB(0.5) = %v", got)
	}
	if got := linearToSRGB(0.0032); math.Abs(got-0.041323) > 1e-5 {
		t.Errorf("linearToSRGB(0.0032) = %v", got)
	}
}

func TestContextColors(t *testing.T) {
	for _, s := range []string{
		"ContextColor", "ContextColor p.icc", "ContextColor p.icc 1", "ContextColor p.icc x,y",
		"ContextColor p.icc 1,0.5,0.5", "ContextColor p.icc 1,1,2,3,4,5", "sc#1,2", "sc#1,2,3,4,5", "sc#x,y,z", "#12345", "#GG0000", "red", "",
	} {
		if _, _, err := parseColor(s); !errors.Is(err, ErrBadColor) {
			t.Errorf("%q: %v", s, err)
		}
	}
	for s, a := range map[string]float64{
		"ContextColor p.icc 0.5,0.2":         0.5,
		"ContextColor p.icc 1,0.1,0.2,0.3":   1,
		"ContextColor p.icc 0.25,1,0,0,0.5":  0.25,
		"ContextColor  p.icc  1 , 0.5":       1,
		"sc#0.5,0.1,0.2,0.3":                 0.5,
		"sc#0.1,0.2,0.3":                     1,
		"#80FF0000":                          128.0 / 255,
		"#FF0000":                            1,
		"  #FF0000  ":                        1,
		"ContextColor p.icc 2,5":             1,
		"ContextColor p.icc -1,-5":           0,
		"ContextColor p.icc 1,0.1,0.2,0.3,0": 1,
	} {
		col, got, err := parseColor(s)
		if s == "ContextColor p.icc 1,0.1,0.2,0.3,0" {
			if err != nil || col == nil {
				t.Errorf("%q: %v", s, err)
			}
			continue
		}
		if err != nil || col == nil || math.Abs(got-a) > 1e-9 {
			t.Errorf("%q: alpha %v err %v, want %v", s, got, err, a)
		}
	}
}

func TestOpacityAndStaticKey(t *testing.T) {
	for in, want := range map[string]float64{"0.5": 0.5, "x": 1, "": 1, "2": 1, "-1": 0, "0": 0, "1": 1} {
		n := &node{name: "P"}
		if in != "" {
			n.attrs = map[string]string{"Opacity": in}
		}
		if got := opacity(n); got != want {
			t.Errorf("opacity %q = %v, want %v", in, got, want)
		}
	}
	for in, want := range map[string]bool{
		"{StaticResource k}": true, "[StaticResource k]": false, "{StaticResource k]": false,
		"[StaticResource k}": false, "{StaticResource}": false, "{DynamicResource k}": false,
		"{StaticResource a b}": false, "k": false, "": false,
	} {
		k, ok := staticKey(in)
		if ok != want || (ok && k != "k") {
			t.Errorf("staticKey(%q) = %q %v", in, k, ok)
		}
	}
}

func TestParseXMLRefusesTwoRoots(t *testing.T) {
	if _, err := parseXML([]byte(`<a/><b/>`), 10, 10, 100); !errors.Is(err, ErrBadXML) {
		t.Errorf("two roots: %v", err)
	}
	if _, err := parseXML([]byte(`<a><b/></a>`), 10, 10, 100); err != nil {
		t.Errorf("one root: %v", err)
	}
}

func TestMalformedClipsAndBrushes(t *testing.T) {
	rejected(t, "canvas clip", `<Canvas Clip="junk"><Path Data="M 0,0 L 9,0 9,9 Z" Fill="#FF000000"/></Canvas>`, "malformed Canvas")
	rejected(t, "path clip", `<Path Data="M 0,0 L 9,0 9,9 Z" Clip="junk" Fill="#FF000000"/>`, "malformed Path")
	rejected(t, "fill colour", `<Path Data="M 0,0 L 9,0 9,9 Z" Fill="#12"/>`, "malformed Path")
	rejected(t, "stroke colour", `<Path Data="M 0,0 L 9,9" Stroke="#12"/>`, "malformed Path")
	rejected(t, "stroke thickness", `<Path Data="M 0,0 L 9,9" Stroke="#FF000000" StrokeThickness="x"/>`, "malformed Path")
	drawsInk(t, "canvas control", `<Canvas Clip="M 0,0 L 9,0 9,9 Z"><Path Data="M 0,0 L 9,0 9,9 Z" Fill="#FF000000"/></Canvas>`)
	drawsInk(t, "stroke control", `<Path Data="M 0,0 L 9,9" Stroke="#FF000000" StrokeThickness="2"/>`)
}

func TestInvisibleIsNotPainted(t *testing.T) {
	for name, body := range map[string]string{
		"clear fill":   `<Path Data="M 0,0 L 9,0 9,9 Z" Fill="#00FF0000"/>`,
		"clear stroke": `<Path Data="M 0,0 L 9,9" Stroke="#00FF0000"/>`,
		"no thickness": `<Path Data="M 0,0 L 9,9" Stroke="#FF000000" StrokeThickness="0"/>`,
		"negative":     `<Path Data="M 0,0 L 9,9" Stroke="#FF000000" StrokeThickness="-2"/>`,
		"opaque none":  `<Path Data="M 0,0 L 9,0 9,9 Z" Fill="#FF000000" Opacity="0"/>`,
	} {
		pdf, res := raw(t, body, false)
		if inked(pdf) || len(res.Skipped) != 0 {
			t.Errorf("%s: painted %v, skipped %v", name, inked(pdf), res.Skipped)
		}
	}
}

func gradPath(kind, attrs, stops string, as string) string {
	return `<Path Data="M 0,0 L 99,0 99,99 Z"><Path.` + as + `><` + kind + ` ` + attrs + `><` + kind + `.GradientStops>` + stops + `</` + kind + `.GradientStops></` + kind + `></Path.` + as + `></Path>`
}

const twoStops = `<GradientStop Color="#FF000000" Offset="0"/><GradientStop Color="#FFFF0000" Offset="1"/>`

func TestGradientsMalformed(t *testing.T) {
	const L, R = "LinearGradientBrush", "RadialGradientBrush"
	drawsInk(t, "linear", gradPath(L, `StartPoint="0,0" EndPoint="1,1"`, twoStops, "Fill"))
	drawsInk(t, "linear defaults", gradPath(L, ``, twoStops, "Fill"))
	drawsInk(t, "radial defaults", gradPath(R, ``, twoStops, "Fill"))
	drawsInk(t, "radial", gradPath(R, `Center="0.5,0.5" GradientOrigin="0.4,0.4" RadiusX="0.3" RadiusY="0.6"`, twoStops, "Fill"))
	for name, body := range map[string]string{
		"start one":    gradPath(L, `StartPoint="1"`, twoStops, "Fill"),
		"start text":   gradPath(L, `StartPoint="x,y"`, twoStops, "Fill"),
		"end three":    gradPath(L, `EndPoint="1,2,3"`, twoStops, "Fill"),
		"end text":     gradPath(L, `EndPoint="x"`, twoStops, "Fill"),
		"center":       gradPath(R, `Center="1"`, twoStops, "Fill"),
		"origin":       gradPath(R, `GradientOrigin="x,y"`, twoStops, "Fill"),
		"radius zero":  gradPath(R, `RadiusX="0"`, twoStops, "Fill"),
		"radius y 0":   gradPath(R, `RadiusY="0"`, twoStops, "Fill"),
		"radius neg":   gradPath(R, `RadiusX="-1"`, twoStops, "Fill"),
		"radius text":  gradPath(R, `RadiusX="x"`, twoStops, "Fill"),
		"radius y txt": gradPath(R, `RadiusY="x"`, twoStops, "Fill"),
	} {
		pdf, res := raw(t, body, false)
		if !skipped(res, "malformed Path") {
			t.Errorf("%s: not reported: %v", name, res.Skipped)
		}
		if strings.Contains(pdf, "sh\n") {
			t.Errorf("%s: shaded", name)
		}
	}
}

func TestGradientsWithoutUsableStops(t *testing.T) {
	const L = "LinearGradientBrush"
	bare := `<Path Data="M 0,0 L 99,0 99,99 Z"><Path.Fill><LinearGradientBrush/></Path.Fill></Path>`
	for name, body := range map[string]string{
		"no stops child": bare,
		"empty stops":    gradPath(L, ``, ``, "Fill"),
		"bad stops":      gradPath(L, ``, `<GradientStop Color="x" Offset="0"/><GradientStop Color="#FF000000" Offset="x"/>`, "Fill"),
		"stroke bare":    `<Path Data="M 0,0 L 9,9"><Path.Stroke><LinearGradientBrush/></Path.Stroke></Path>`,
		"stroke empty":   gradPath(L, ``, ``, "Stroke"),
	} {
		pdf, res := raw(t, body, false)
		if inked(pdf) || len(res.Skipped) != 0 {
			t.Errorf("%s: painted %v, skipped %v", name, inked(pdf), res.Skipped)
		}
	}
	// A gradient stroke is drawn in its first colour, and says so.
	pdf, res := raw(t, `<Path Data="M 0,0 L 9,9">`+`<Path.Stroke><LinearGradientBrush><LinearGradientBrush.GradientStops>`+twoStops+`</LinearGradientBrush.GradientStops></LinearGradientBrush></Path.Stroke></Path>`, false)
	if !inked(pdf) || !skipped(res, "gradient stroke") {
		t.Errorf("gradient stroke: %v", res.Skipped)
	}
}

func TestGradientWorkIsCharged(t *testing.T) {
	b := newPkg(page(gradPath("LinearGradientBrush", ``, twoStops+`<GradientStop Color="#FF00FF00" Offset="0.5"/>`, "Fill"))).bytes(t)
	// Four segments, then three stops.
	refuses(t, "gradient work", b, Limits{MaxWork: 6}, ErrLimit)
	if _, err := Convert(b, Limits{MaxWork: 7}); err != nil {
		t.Errorf("exactly the work: %v", err)
	}
}
