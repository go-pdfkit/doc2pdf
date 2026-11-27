// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"strings"
	"testing"
)

// tiled is one page filling a 400 x 300 area with square tiles of the given
// side, so the tile count is (400/side) x (300/side), rounded up.
func tiled(t *testing.T, side string) []byte {
	t.Helper()
	p := newPkg(page(`<Path Data="M 0,0 L 400,0 400,300 0,300 Z"><Path.Fill><ImageBrush ImageSource="/i.png" TileMode="Tile" Viewbox="0,0,8,8" ViewboxUnits="Absolute" Viewport="0,0,` + side + `,` + side + `" ViewportUnits="Absolute"/></Path.Fill></Path>`))
	p.extra["i.png"] = pngOf(t, 8, 8, nil2())
	return p.bytes(t)
}

// The tile ceiling is its own guard: the work budget is left generous so
// that only MaxTiles can refuse.
func TestTileCeilingIsExactlyTheCeiling(t *testing.T) {
	b := tiled(t, "100") // 4 x 3 = 12 tiles
	lim := Limits{MaxWork: 1 << 30}
	lim.MaxTiles = 12
	if _, err := Convert(b, lim); err != nil {
		t.Errorf("12 tiles under a ceiling of 12: %v", err)
	}
	lim.MaxTiles = 11
	refuses(t, "11", b, lim, ErrLimit)
	// 10000 tiles under the default ceiling of 4096.
	refuses(t, "default ceiling", tiled(t, "4"), Limits{MaxWork: 1 << 30}, ErrLimit)
}

// A tile smaller than one unit is refused, not rounded up to one.
func TestFractionalTileCount(t *testing.T) {
	refuses(t, "tiny", tiled(t, "0.5"), Limits{MaxWork: 1 << 30}, ErrLimit)
}

// The same image drawn again and again is embedded once, and budgeted once.
func TestImagesDrawnRepeatedlyAreEmbeddedOnce(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 40; i++ {
		body.WriteString(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="/i.png" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`)
	}
	p := newPkg(page(body.String()))
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	for i := range img.Pix {
		img.Pix[i] = byte(i*7919 + i>>3*31)
	}
	var enc bytes.Buffer
	if err := png.Encode(&enc, img); err != nil {
		t.Fatal(err)
	}
	p.extra["i.png"] = enc.Bytes()
	b := p.bytes(t)
	if _, err := Convert(b, Limits{}); err != nil {
		t.Fatalf("under the default: %v", err)
	}
	// Forty draws cost what one does: the smallest budget that converts is the
	// same for one draw as for forty, and one byte less refuses.
	one := newPkg(page(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="/i.png" Viewbox="0,0,1,1" Viewport="0,0,9,9"/></Path.Fill></Path>`))
	one.extra["i.png"] = enc.Bytes()
	n40, n1 := minMemory(t, b), minMemory(t, one.bytes(t))
	if n40 > n1+1<<16 {
		t.Errorf("forty draws need %d bytes of memory, one needs %d", n40, n1)
	}
	if floor := 2 * 64 * 64 * 4; n40 < int64(floor) {
		t.Errorf("an image stored at %d bytes fits in %d", floor, n40)
	}
}

// minMemory is the smallest MaxMemory at which b converts, found by bisection,
// and it checks that one byte less is refused as a limit.
func minMemory(t *testing.T, b []byte) int64 {
	t.Helper()
	lo, hi := int64(1), int64(192<<20)
	if _, err := Convert(b, Limits{MaxMemory: hi}); err != nil {
		t.Fatalf("not even %d bytes of memory is enough: %v", hi, err)
	}
	for lo < hi {
		mid := (lo + hi) / 2
		if _, err := Convert(b, Limits{MaxMemory: mid}); err == nil {
			hi = mid
		} else if errors.Is(err, ErrLimit) {
			lo = mid + 1
		} else {
			t.Fatalf("MaxMemory %d: %v", mid, err)
		}
	}
	refuses(t, "memory", b, Limits{MaxMemory: lo - 1}, ErrLimit)
	return lo
}

// A scale that is not a number, or a degenerate viewbox, draws nothing and
// says so, rather than dividing by zero.
func TestDegenerateImageBrushes(t *testing.T) {
	for _, vb := range []string{"0,0,0,0", "0,0,-1,4", "NaN,0,4,4", "0,0,4", "1e99,0,4,4"} {
		p := newPkg(page(`<Path Data="M 0,0 L 9,0 9,9 Z"><Path.Fill><ImageBrush ImageSource="/i.png" Viewbox="` + vb + `" ViewboxUnits="Absolute" Viewport="0,0,9,9" ViewportUnits="Absolute"/></Path.Fill></Path>`))
		p.extra["i.png"] = pngOf(t, 4, 4, nil2())
		res, err := Convert(p.bytes(t), Limits{})
		if err != nil {
			t.Errorf("%q: %v", vb, err)
			continue
		}
		if !skipped(res, "malformed") && strings.Contains(string(res.PDF), "NaN") {
			t.Errorf("%q leaked NaN", vb)
		}
	}
}

// A long UnicodeString is refused even when Indices names few glyphs.
func TestLongStringShortIndices(t *testing.T) {
	long := strings.Repeat("a", 200)
	p := newPkg(page(`<Glyphs OriginX="0" OriginY="9" FontRenderingEmSize="9" FontUri="` + fontPart + `" UnicodeString="` + long + `" Indices="5" Fill="#FF000000"/>`)).withFont()
	refuses(t, "string", p.bytes(t), Limits{MaxGlyphsPerRun: 100}, ErrLimit)
	// Exactly at the ceiling is accepted.
	p = newPkg(page(`<Glyphs OriginX="0" OriginY="9" FontRenderingEmSize="9" FontUri="` + fontPart + `" UnicodeString="` + strings.Repeat("a", 100) + `" Fill="#FF000000"/>`)).withFont()
	if _, err := Convert(p.bytes(t), Limits{MaxGlyphsPerRun: 100}); err != nil {
		t.Errorf("100 under 100: %v", err)
	}
}

// A page whose root is not a FixedPage is refused, whatever its size says.
func TestRootMustBeFixedPage(t *testing.T) {
	bad := `<Canvas xmlns="` + nsOXPS + `" Width="400" Height="300"/>`
	refuses(t, "root", newPkg(bad).bytes(t), Limits{}, ErrBadPage)
}

// More documents than MaxPages are refused as documents, before any is read.
func TestDocumentCount(t *testing.T) {
	p := newPkg(page(""))
	p.seq = `<FixedDocumentSequence xmlns="` + nsOXPS + `">` + strings.Repeat(`<DocumentReference Source="/Documents/1/FixedDocument.fdoc"/>`, 3) + `</FixedDocumentSequence>`
	b := p.bytes(t)
	refuses(t, "documents", b, Limits{MaxPages: 2}, ErrLimit)
	if _, err := Convert(b, Limits{MaxPages: 3}); err != nil {
		t.Errorf("3 documents under 3: %v", err)
	}
}

// The inflated bytes are bounded against the package's own size.
func TestRatioBudget(t *testing.T) {
	b := newPkg(page(strings.Repeat(" ", 1200<<10))).bytes(t)
	refuses(t, "ratio", b, Limits{MaxRatio: 1}, ErrLimit)
	if _, err := Convert(b, Limits{}); err != nil {
		t.Errorf("under the default ratio: %v", err)
	}
}

// A URI with a scheme is never followed, even when a part of that very name
// exists in the package.
func TestSchemeIsRefusedEvenWhenAPartExists(t *testing.T) {
	p := newPkg(page(""))
	p.doc = `<FixedDocument xmlns="` + nsOXPS + `"><PageContent Source="a:b.fpage"/></FixedDocument>`
	p.extra["Documents/1/a:b.fpage"] = []byte(page(`<Path Data="M 0,0 L 9,0 9,9 Z" Fill="#FF000000"/>`))
	res, err := Convert(p.bytes(t), Limits{})
	if err == nil && res.Pages != 0 {
		t.Errorf("a scheme-named page was drawn: %d pages", res.Pages)
	}
	if err == nil && !skipped(res, "page reference") {
		t.Error("the refusal was not reported")
	}
}
