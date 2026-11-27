// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/go-opentype/opentype"
	"golang.org/x/image/font/gofont/goregular"
)

func glyphRun(attrs string) string {
	return `<Glyphs OriginX="0" OriginY="20" FontRenderingEmSize="12" FontUri="` + fontPart + `" Fill="#FF000000" UnicodeString="Hi" ` + attrs + `/>`
}

// withAttr is a run with one attribute replaced.
func glyphWith(drop, set string) string {
	s := `<Glyphs OriginX="0" OriginY="20" FontRenderingEmSize="12" FontUri="` + fontPart + `" Fill="#FF000000" UnicodeString="Hi"/>`
	if drop != "" {
		i := strings.Index(s, drop)
		j := i + strings.Index(s[i:], `" `) + 2
		if j < i+2 {
			j = len(s) - 2
		}
		s = s[:i] + s[j:]
	}
	return strings.Replace(s, "/>", " "+set+"/>", 1)
}

func textRaw(t *testing.T, body string) (string, *Result) {
	t.Helper()
	return raw(t, body, true)
}

// The control: a well formed run draws text.
func TestGlyphControl(t *testing.T) {
	pdf, res := textRaw(t, glyphRun(""))
	if !strings.Contains(pdf, " Tf") || len(res.Skipped) != 0 {
		t.Fatalf("control: %v", res.Skipped)
	}
}

// Every unusable attribute of a run is named and nothing is drawn.
func TestGlyphsMalformedAttributes(t *testing.T) {
	for name, body := range map[string]string{
		"size zero":      glyphWith(`FontRenderingEmSize="12"`, `FontRenderingEmSize="0"`),
		"size negative":  glyphWith(`FontRenderingEmSize="12"`, `FontRenderingEmSize="-1"`),
		"size huge":      glyphWith(`FontRenderingEmSize="12"`, `FontRenderingEmSize="1e6"`),
		"size text":      glyphWith(`FontRenderingEmSize="12"`, `FontRenderingEmSize="x"`),
		"size missing":   glyphWith(`FontRenderingEmSize="12"`, ``),
		"origin x":       glyphWith(`OriginX="0"`, `OriginX="x"`),
		"origin y":       glyphWith(`OriginY="20"`, `OriginY="x"`),
		"origin missing": glyphWith(`OriginX="0"`, ``),
		"colour":         glyphWith(`Fill="#FF000000"`, `Fill="notacolour"`),
		"indices":        glyphRun(`Indices="x"`),
		"bad clip":       glyphRun(`Clip="M 0,0 X"`),
	} {
		pdf, res := textRaw(t, body)
		if !skipped(res, "malformed") || strings.Contains(pdf, " Tf") {
			t.Errorf("%s: %v", name, res.Skipped)
		}
	}
}

// Without a Fill a run has no visible effect, and says nothing.
func TestGlyphsWithoutFillDrawNothing(t *testing.T) {
	body := `<Glyphs OriginX="0" OriginY="20" FontRenderingEmSize="12" FontUri="` + fontPart + `" UnicodeString="Hi"/>`
	pdf, res := textRaw(t, body)
	if strings.Contains(pdf, " Tf") || len(res.Skipped) != 0 {
		t.Errorf("no fill: %v", res.Skipped)
	}
}

func TestEmptyRunDrawsNoText(t *testing.T) {
	pdf, res := textRaw(t, glyphWith(`UnicodeString="Hi"`, `UnicodeString=""`))
	if strings.Contains(pdf, " Tf") || len(res.Skipped) != 0 {
		t.Errorf("empty: %v", res.Skipped)
	}
}

// A face index other than 0 is a collection, which is named; 0 and an empty
// fragment are the plain font.
func TestFontFragment(t *testing.T) {
	for frag, drawn := range map[string]bool{"#0": true, "#": true, "#1": false, "#x": false} {
		body := strings.Replace(glyphRun(""), fontPart, fontPart+frag, 1)
		pdf, res := textRaw(t, body)
		if got := strings.Contains(pdf, " Tf"); got != drawn {
			t.Errorf("%q: drawn=%v %v", frag, got, res.Skipped)
		}
		if !drawn && !skipped(res, "TrueType collection") {
			t.Errorf("%q: %v", frag, res.Skipped)
		}
	}
}

// A font that cannot be found does not use up the budget of fonts.
func TestFontLimitCountsOnlyFontsRead(t *testing.T) {
	bad := strings.Replace(glyphRun(""), fontPart, "/Resources/Fonts/missing.ttf", 1)
	pdf, res := raw(t, bad+glyphRun(""), true)
	_ = res
	if !strings.Contains(pdf, " Tf") {
		t.Error("the good font was refused after a missing one")
	}
	b := newPkg(page(bad + glyphRun(""))).withFont().bytes(t)
	if _, err := Convert(b, Limits{MaxFonts: 1}); err != nil {
		t.Errorf("one font under a limit of one: %v", err)
	}
}

// The same font in two runs is one font.
func TestSameFontTwiceIsOneFont(t *testing.T) {
	b := newPkg(page(glyphRun("") + glyphRun(""))).withFont().bytes(t)
	if _, err := Convert(b, Limits{MaxFonts: 1}); err != nil {
		t.Errorf("%v", err)
	}
	// A second distinct font is over the limit.
	p := newPkg(page(glyphRun("") + strings.Replace(glyphRun(""), fontPart, "/Resources/Fonts/two.ttf", 1))).withFont()
	p.extra["Resources/Fonts/two.ttf"] = goregular.TTF
	refuses(t, "two fonts", p.bytes(t), Limits{MaxFonts: 1}, ErrLimit)
}

// A font that failed to load fails every run that names it, the same way.
func TestCorruptFontUsedTwice(t *testing.T) {
	p := newPkg(page(strings.Replace(glyphRun("")+glyphRun(""), fontPart, "/Resources/Fonts/bad.ttf", 2)))
	p.extra["Resources/Fonts/bad.ttf"] = []byte("this is not a font at all")
	_, res := convertRaw(t, p.bytes(t), Limits{})
	if !skipped(res, "malformed Glyphs") {
		t.Fatalf("%v", res.Skipped)
	}
	n := 0
	for _, s := range res.Skipped {
		if strings.HasPrefix(s, "malformed Glyphs") {
			n++
		}
	}
	if n != 1 || !strings.Contains(strings.Join(res.Skipped, ""), "(2)") {
		t.Errorf("both runs should be named: %v", res.Skipped)
	}
}

func TestFontPartOverTheLimit(t *testing.T) {
	b := newPkg(page(glyphRun(""))).withFont().bytes(t)
	refuses(t, "font", b, Limits{MaxPartBytes: int64(len(goregular.TTF)) - 1}, ErrLimit)
}

func TestParseIndicesErrors(t *testing.T) {
	for _, s := range []string{
		"(1:x)1", "(1:0)1", "(1:101)1", "(x)1", "(0)1", "(101)1", "(1", "1,2,3,4,5", "1,x", "1,2,x", "1,2,3,x",
		"x", "-1", "65536",
	} {
		if _, err := parseIndices(s, 100); !errors.Is(err, ErrBadGlyphs) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if _, err := parseIndices("1;2;3;4", 3); !errors.Is(err, ErrLimit) {
		t.Errorf("four entries over 3: %v", err)
	}
	if got, err := parseIndices("1;2;3", 3); err != nil || len(got) != 3 {
		t.Errorf("three entries under 3: %v %v", got, err)
	}
	for _, s := range []string{"(1:100)1", "(100)", "1,2,3,4", "65535", ",5", "1,,3"} {
		if _, err := parseIndices(s, 100); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
}

// A font that declares no units per em cannot place anything. The opentype
// reader may refuse it first; either way no text is drawn and nothing panics.
func TestZeroUnitsPerEm(t *testing.T) {
	b := append([]byte(nil), goregular.TTF...)
	n := int(binary.BigEndian.Uint16(b[4:]))
	patched := false
	for i := 0; i < n; i++ {
		rec := b[12+16*i:]
		if string(rec[:4]) == "head" {
			off := int(binary.BigEndian.Uint32(rec[8:]))
			binary.BigEndian.PutUint16(b[off+18:], 0)
			patched = true
		}
	}
	if !patched {
		t.Fatal("no head table")
	}
	if _, err := opentype.Parse(b); err == nil {
		t.Fatal("opentype.Parse accepts unitsPerEm 0: glyphs would divide by it")
	}
	p := newPkg(page(glyphRun(""))).withFont()
	p.extra["Resources/Fonts/go.ttf"] = b
	pdf, res := convertRaw(t, p.bytes(t), Limits{})
	if strings.Contains(pdf, " Tf") || len(res.Skipped) == 0 {
		t.Errorf("drawn with no units per em: %v", res.Skipped)
	}
}

// The glyphs of a run are charged as work.
func TestGlyphsAreChargedAsWork(t *testing.T) {
	body := strings.Replace(glyphRun(""), `UnicodeString="Hi"`, `UnicodeString="`+strings.Repeat("a", 50)+`"`, 1)
	b := newPkg(page(body)).withFont().bytes(t)
	if _, err := Convert(b, Limits{}); err != nil {
		t.Fatalf("default: %v", err)
	}
	refuses(t, "work", b, Limits{MaxWork: 20}, ErrLimit)
}

// An empty run with a malformed clip is still a malformed run.
func TestEmptyRunWithBadClip(t *testing.T) {
	body := strings.Replace(glyphRun(`Clip="M 0,0 X"`), `UnicodeString="Hi"`, `UnicodeString=""`, 1)
	_, res := textRaw(t, body)
	if !skipped(res, "malformed") {
		t.Errorf("%v", res.Skipped)
	}
}
