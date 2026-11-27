// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"errors"
	"image/color"
	"math"
	"strings"
	"testing"
)

func build(t *testing.T, s string) (*geometry, error) {
	t.Helper()
	b := &builder{g: &geometry{}, max: 1000}
	err := parseAbbrev(s, b)
	if err == nil {
		err = b.err
	}
	return b.g, err
}

func TestParseAbbrev(t *testing.T) {
	ops := func(g *geometry) string {
		var sb strings.Builder
		for _, s := range g.segs {
			sb.WriteByte(s.op)
		}
		return sb.String()
	}
	tests := []struct {
		in   string
		ops  string
		last [2]float64 // end point of the last non-Z segment
		eo   bool
	}{
		{"M 10,10 L 20,20", "ML", [2]float64{20, 20}, false},
		{"M10 10 20 20 30 5", "MLL", [2]float64{30, 5}, false}, // implicit repeat
		{"m 10,10 l 5,5", "ML", [2]float64{15, 15}, false},
		{"M 0,0 H 10 V 7", "MLL", [2]float64{10, 7}, false},
		{"M 0,0 h 10 v 7", "MLL", [2]float64{10, 7}, false},
		{"M 0,0 C 1,1 2,2 3,3", "MC", [2]float64{3, 3}, false},
		{"M 0,0 c 1,1 2,2 3,3", "MC", [2]float64{3, 3}, false},
		{"M 0,0 Q 5,5 10,0", "MC", [2]float64{10, 0}, false},
		{"M 0,0 C 1,1 2,2 3,3 S 5,5 6,6", "MCC", [2]float64{6, 6}, false},
		{"M 0,0 L 10,0 Z", "MLZ", [2]float64{10, 0}, false},
		{"F 0 M 0,0 L 1,1", "ML", [2]float64{1, 1}, true},
		{"F1 M 0,0 L 1,1", "ML", [2]float64{1, 1}, false},
		{"M 0,0 A 10,10 0 0 1 20,0", "MCC", [2]float64{20, 0}, false},
		{"M 0,0 a 10,10 0 1 0 20,0", "MCC", [2]float64{20, 0}, false},
		{"M1e1,-2.5e0 L.5.5", "ML", [2]float64{0.5, 0.5}, false},
	}
	for _, tc := range tests {
		g, err := build(t, tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got := ops(g); !strings.HasPrefix(got, tc.ops[:1]) || len(got) < len(tc.ops) {
			t.Errorf("%q: ops %q, want at least %q", tc.in, got, tc.ops)
		}
		var end [2]float64
		for _, s := range g.segs {
			switch s.op {
			case 'M', 'L':
				end = [2]float64{s.v[0], s.v[1]}
			case 'C':
				end = [2]float64{s.v[4], s.v[5]}
			}
		}
		if math.Abs(end[0]-tc.last[0]) > 1e-9 || math.Abs(end[1]-tc.last[1]) > 1e-9 {
			t.Errorf("%q: ends at %v, want %v", tc.in, end, tc.last)
		}
		if g.evenOdd != tc.eo {
			t.Errorf("%q: evenOdd = %v", tc.in, g.evenOdd)
		}
	}
}

func TestParseAbbrevSmoothReflection(t *testing.T) {
	g, err := build(t, "M 0,0 C 1,1 2,2 3,3 S 5,5 6,6")
	if err != nil {
		t.Fatal(err)
	}
	// The first control point of S is the reflection of (2,2) about (3,3).
	c := g.segs[2]
	if c.op != 'C' || c.v[0] != 4 || c.v[1] != 4 {
		t.Errorf("S reflected to %v, want (4,4)", c.v[:2])
	}
}

func TestParseAbbrevArcDegenerate(t *testing.T) {
	// Same end point: nothing is drawn; zero radius: a straight line.
	g, err := build(t, "M 5,5 A 10,10 0 0 1 5,5")
	if err != nil || len(g.segs) != 1 {
		t.Errorf("zero-length arc: %v %v", g.segs, err)
	}
	g, err = build(t, "M 5,5 A 0,0 0 0 1 9,9")
	if err != nil || g.segs[len(g.segs)-1].op != 'L' {
		t.Errorf("zero-radius arc: %v %v", g.segs, err)
	}
	// Radii too small are scaled up so the arc still reaches the end point.
	g, err = build(t, "M 0,0 A 1,1 0 0 1 100,0")
	if err != nil {
		t.Fatal(err)
	}
	last := g.segs[len(g.segs)-1]
	if math.Abs(last.v[4]-100) > 1e-6 || math.Abs(last.v[5]) > 1e-6 {
		t.Errorf("scaled arc ends at %v", last.v[4:])
	}
}

func TestParseAbbrevErrors(t *testing.T) {
	for _, in := range []string{
		"L 1,1",         // no move first
		"M 1",           // missing coordinate
		"M 1,1 X 2,2",   // unknown command
		"M 1,1 L NaN,1", // not finite
		"M 1,1 L 1e999,1",
		"M 1,1 C 1,1 2,2", // short
		"F 7 M 1,1",
	} {
		if _, err := build(t, in); err == nil {
			t.Errorf("%q: no error", in)
		}
	}
	// Segment ceiling.
	b := &builder{g: &geometry{}, max: 3}
	err := parseAbbrev("M 0,0 L 1,1 2,2 3,3 4,4", b)
	if err == nil {
		err = b.err
	}
	if !errors.Is(err, ErrLimit) {
		t.Errorf("segment ceiling: %v", err)
	}
}

func TestParseMatrixAndNums(t *testing.T) {
	m, ok := parseMatrix("1,0,0,1,5,6")
	if !ok || m != [6]float64{1, 0, 0, 1, 5, 6} {
		t.Errorf("matrix %v %v", m, ok)
	}
	for _, bad := range []string{"1,2,3", "1,2,3,4,5,6,7", "a,b,c,d,e,f", "1,0,0,1,NaN,0"} {
		if _, ok := parseMatrix(bad); ok {
			t.Errorf("matrix %q accepted", bad)
		}
	}
	if v, ok := parseNums("1 2,3  4", 4); !ok || len(v) != 4 {
		t.Errorf("nums %v %v", v, ok)
	}
	if _, ok := parseNums("1 2 3", 2); ok {
		t.Error("parseNums ignored its ceiling")
	}
}

func TestGeometryBoundsAndApply(t *testing.T) {
	g, _ := build(t, "M 10,20 L 30,50")
	x0, y0, x1, y1, ok := g.bounds()
	if !ok || x0 != 10 || y0 != 20 || x1 != 30 || y1 != 50 {
		t.Errorf("bounds %v %v %v %v %v", x0, y0, x1, y1, ok)
	}
	g.apply([6]float64{2, 0, 0, 3, 1, 1})
	x0, y0, x1, y1, _ = g.bounds()
	if x0 != 21 || y0 != 61 || x1 != 61 || y1 != 151 {
		t.Errorf("applied bounds %v %v %v %v", x0, y0, x1, y1)
	}
	if _, _, _, _, ok := (&geometry{}).bounds(); ok {
		t.Error("empty geometry has bounds")
	}
}

func TestParseColor(t *testing.T) {
	for _, tc := range []struct {
		in    string
		r     float64
		alpha float64
	}{
		{"#FF0000", 1, 1},
		{"#80FF0000", 1, 128.0 / 255},
		{"sc#1,0,0", 1, 1},
		{"sc#0.5,1,0,0", 1, 0.5},
	} {
		c, a, err := parseColor(tc.in)
		if err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if got := asRGB(c).R; math.Abs(float64(got)-tc.r) > 1e-6 {
			t.Errorf("%q: red %v", tc.in, got)
		}
		if math.Abs(a-tc.alpha) > 1e-6 {
			t.Errorf("%q: alpha %v", tc.in, a)
		}
	}
	// scRGB is linear: 0.5 is lighter than 0.5 in sRGB.
	c, _, _ := parseColor("sc#1,0.5,0")
	if g := float64(asRGB(c).G); g < 0.7 || g > 0.75 {
		t.Errorf("linear 0.5 -> sRGB %v", g)
	}
	// The first number of a ContextColor is its alpha.
	for _, ok := range []string{"ContextColor /x.icc 1,0.5", "ContextColor /x.icc 1,0.5,0.25,0", "ContextColor /x.icc 0.5,1,0,0,0"} {
		if _, _, err := parseColor(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	if _, a, _ := parseColor("ContextColor /x.icc 0.5,1,0,0,0"); a != 0.5 {
		t.Errorf("context alpha %v", a)
	}
	for _, bad := range []string{"ContextColor /x.icc 1,0.5,0.25", "ContextColor /x.icc 1", "ContextColor"} {
		if _, _, err := parseColor(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	for _, bad := range []string{"", "red", "#GG0000", "#12345", "sc#1,NaN,0", "sc#1,2", "sc#1e999,0,0", "#"} {
		if _, _, err := parseColor(bad); !errors.Is(err, ErrBadColor) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	if _, ok := finiteNum("1e10"); ok {
		t.Error("huge number accepted")
	}
	if clamp01(-1) != 0 || clamp01(2) != 1 || clamp01(0.25) != 0.25 {
		t.Error("clamp01")
	}
}

func TestParseIndices(t *testing.T) {
	m, err := parseIndices("(2:1)43;48,150,1,2;;5", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 4 || !m[0].cluster || m[0].n != 2 || m[0].m != 1 || m[0].gid != 43 {
		t.Fatalf("mappings %+v", m)
	}
	if !m[1].hasAdv || m[1].adv != 150 || m[1].uOff != 1 || m[1].vOff != 2 {
		t.Errorf("second %+v", m[1])
	}
	if m[2].hasGid || m[3].gid != 5 {
		t.Errorf("third/fourth %+v %+v", m[2], m[3])
	}
	if m, err := parseIndices("  ", 3); m != nil || err != nil {
		t.Errorf("blank: %v %v", m, err)
	}
	for _, bad := range []string{"(2:1", "(x)1", "1,x", "1;2;3;4;5;6", "(0)1", "99999999999999999999", "1,NaN"} {
		if _, err := parseIndices(bad, 5); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestDeobfuscateKnownAnswer(t *testing.T) {
	// ECMA-388 §9.1.7.3: the key is the GUID's hex bytes read in reverse.
	name := "/Resources/Fonts/00112233-4455-6677-8899-AABBCCDDEEFF.odttf"
	out, ok := deobfuscate(name, make([]byte, 40))
	if !ok {
		t.Fatal("not a GUID")
	}
	want := make([]byte, 40)
	for i := 0; i < 32; i++ {
		want[i] = byte(0xFF - 0x11*(i%16))
	}
	if !bytes.Equal(out, want) {
		t.Errorf("got % x", out[:20])
	}
	back, _ := deobfuscate(name, out)
	if !bytes.Equal(back, make([]byte, 40)) {
		t.Error("not an involution")
	}
	if _, ok := deobfuscate("/x/not-a-guid.odttf", []byte{1}); ok {
		t.Error("non-GUID accepted")
	}
	if out, _ := deobfuscate(name, []byte{1, 2}); len(out) != 2 {
		t.Error("short input")
	}
}

func TestImageDPI(t *testing.T) {
	x, y := pngDPI(pngOf(t, 4, 4, color.RGBA{1, 2, 3, 255}))
	if x != 96 || y != 96 {
		t.Errorf("png without pHYs: %v %v", x, y)
	}
	// pHYs: 11811 pixels per metre is 300 dpi; unit 0 is only an aspect ratio.
	withPHYs := func(unit byte) []byte {
		p := pngOf(t, 2, 2, color.RGBA{1, 2, 3, 255})
		d := []byte{0, 0, 0x2E, 0x23, 0, 0, 0x2E, 0x23, unit}
		chunk := append([]byte{0, 0, 0, 9}, "pHYs"...)
		chunk = append(append(chunk, d...), 0, 0, 0, 0) // CRC unchecked by the reader
		return append(append(append([]byte{}, p[:33]...), chunk...), p[33:]...)
	}
	if x, y := pngDPI(withPHYs(1)); math.Abs(x-300) > 0.1 || math.Abs(y-300) > 0.1 {
		t.Errorf("pHYs dpi %v %v", x, y)
	}
	if x, _ := pngDPI(withPHYs(0)); x != 96 {
		t.Errorf("pHYs unit 0: %v", x)
	}
	// JFIF: units 1 is per inch, 2 per cm, 0 an aspect ratio.
	jfif := func(units byte, d uint16) []byte {
		return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, units, byte(d >> 8), byte(d), byte(d >> 8), byte(d), 0, 0}
	}
	if x, y := jpegDPI(jfif(1, 200)); x != 200 || y != 200 {
		t.Errorf("jfif inch %v %v", x, y)
	}
	if x, _ := jpegDPI(jfif(2, 100)); math.Abs(x-254) > 0.01 {
		t.Errorf("jfif cm %v", x)
	}
	if x, _ := jpegDPI(jfif(0, 1)); x != 96 {
		t.Errorf("jfif aspect %v", x)
	}
	if x, y := jpegDPI([]byte{1, 2}); x != 96 || y != 96 {
		t.Errorf("garbage jpeg: %v %v", x, y)
	}
	if saneDPI(0) != 96 || saneDPI(1e9) != 96 || saneDPI(math.NaN()) != 96 || saneDPI(300) != 300 {
		t.Error("saneDPI")
	}
}

func TestParseXMLBounds(t *testing.T) {
	if _, err := parseXML([]byte(`<a><b x="1"/></a>`), 4, 10, 100); err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat("<a>", 20) + strings.Repeat("</a>", 20)
	for name, in := range map[string]string{
		"deep":   deep,
		"nodes":  "<r>" + strings.Repeat("<a/>", 20) + "</r>",
		"tokens": "<r>" + strings.Repeat("<!-- c -->", 200) + "</r>",
		"bad":    "<a><b></a>",
		"empty":  "",
	} {
		_, err := parseXML([]byte(in), 8, 10, 100)
		if err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func FuzzParseAbbrev(f *testing.F) {
	for _, s := range []string{"M 0,0 L 1,1 Z", "F1 M0 0 C1 1 2 2 3 3 S4 4 5 5", "M 0,0 A 5,5 30 1 1 9,9", "m1 1h2v3q1 1 2 2"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		b := &builder{g: &geometry{}, max: 500}
		_ = parseAbbrev(s, b)
		for _, sg := range b.g.segs {
			for _, v := range sg.v {
				if math.IsNaN(v) || math.IsInf(v, 0) {
					t.Fatalf("non-finite coordinate from %q", s)
				}
			}
		}
	})
}

func FuzzParseIndices(f *testing.F) {
	for _, s := range []string{"(2:1)43;48,150,1,2;;5", "1;2;3", "(1)", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		m, err := parseIndices(s, 64)
		if err == nil && len(m) > 64 {
			t.Fatalf("%d mappings past the ceiling", len(m))
		}
	})
}

func FuzzParseXML(f *testing.F) {
	for _, s := range []string{`<a b="c"><d/></a>`, `<?xml version="1.0"?><x:r xmlns:x="u"/>`, `<a>&amp;</a>`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _ = parseXML(b, 16, 200, 1000)
	})
}
