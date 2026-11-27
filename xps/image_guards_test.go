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

	"github.com/go-ooxml/opc"
)

// imgBody is one triangle painted with an ImageBrush whose attributes the
// caller chooses.
func imgBody(attrs string) string {
	return `<Path Data="M 0,0 L 90,0 90,90 0,90 Z"><Path.Fill><ImageBrush ImageSource="/i.png" ` + attrs + `/></Path.Fill></Path>`
}

const okBrush = `Viewbox="0,0,4,4" ViewboxUnits="Absolute" Viewport="0,0,90,90" ViewportUnits="Absolute"`

func imgPkg(t *testing.T, body string, img []byte) []byte {
	t.Helper()
	p := newPkg(page(body))
	p.extra["i.png"] = img
	return p.bytes(t)
}

// noisyPNG is an incompressible w x h PNG.
func noisyPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i*7919 + i>>3*31)
	}
	var enc bytes.Buffer
	if err := png.Encode(&enc, img); err != nil {
		t.Fatal(err)
	}
	return enc.Bytes()
}

// The pixels of an image are charged once, however often it is drawn: a total
// of exactly one image's pixels admits the same image twice.
func TestImageCacheChargesOnce(t *testing.T) {
	two := imgBody(okBrush) + imgBody(okBrush)
	b := imgPkg(t, two, pngOf(t, 4, 4, nil2()))
	if _, err := Convert(b, Limits{MaxTotalImagePixels: 16}); err != nil {
		t.Errorf("one image drawn twice under its own pixel count: %v", err)
	}
	refuses(t, "total", b, Limits{MaxTotalImagePixels: 15}, ErrLimit)
}

// A part bigger than MaxPartBytes is a limit, not an unreadable image.
func TestImagePartOverTheLimit(t *testing.T) {
	img := noisyPNG(t, 64, 64)
	b := imgPkg(t, imgBody(okBrush), img)
	if _, err := Convert(b, Limits{}); err != nil {
		t.Fatalf("under the default: %v", err)
	}
	refuses(t, "part", b, Limits{MaxPartBytes: int64(len(img)) - 1}, ErrLimit)
}

// A header that cannot be read, or that declares nothing, is a malformed
// brush and nothing is painted.
func TestImageHeaderUnreadable(t *testing.T) {
	full := pngOf(t, 4, 4, nil2())
	for name, img := range map[string][]byte{
		"truncated png":  full[:20],
		"zero png":       pngHeader(0, 0),
		"truncated jpeg": jpegOf(t, 8, 8)[:6],
		"not an image":   []byte("hello"),
	} {
		pdf, res := convertRaw(t, imgPkg(t, imgBody(okBrush), img), Limits{})
		if !skipped(res, "malformed") || inked(pdf) {
			t.Errorf("%s: %v inked=%v", name, res.Skipped, inked(pdf))
		}
	}
}

// An image whose header is fine and whose pixels are not is reported when it
// is drawn, plain and tiled.
func TestImageDecodeFailure(t *testing.T) {
	bad := pngHeader(4, 4)
	for name, attrs := range map[string]string{
		"plain": okBrush,
		"tiled": okBrush + ` TileMode="Tile"`,
	} {
		_, res := convertRaw(t, imgPkg(t, imgBody(attrs), bad), Limits{})
		if !skipped(res, "malformed") {
			t.Errorf("%s: %v", name, res.Skipped)
		}
	}
}

// A brush whose source names no part is a malformed brush, not an error of
// the package.
func TestImageSourceNamesNothing(t *testing.T) {
	for _, src := range []string{"/missing.png", "", "%zz", "a:b.png"} {
		body := `<Path Data="M 0,0 L 90,0 90,90 0,90 Z"><Path.Fill><ImageBrush ImageSource="` + src + `" ` + okBrush + `/></Path.Fill></Path>`
		pdf, res := raw(t, body, false)
		if !skipped(res, "malformed") || inked(pdf) {
			t.Errorf("%q: %v inked=%v", src, res.Skipped, inked(pdf))
		}
	}
}

// Each way a brush can be unusable is refused and reported, and the control
// beside them paints.
func TestImageBrushRefusals(t *testing.T) {
	img := pngOf(t, 4, 4, nil2())
	conv := func(attrs string) (string, *Result) {
		pdf, res := convertRaw(t, imgPkg(t, imgBody(attrs), img), Limits{})
		return pdf, res
	}
	if pdf, res := conv(okBrush); !strings.Contains(pdf, " Do") || len(res.Skipped) != 0 {
		t.Fatalf("control: %v", res.Skipped)
	}
	for name, attrs := range map[string]string{
		"relative viewbox units": `Viewbox="0,0,4,4" ViewboxUnits="RelativeToBoundingBox" Viewport="0,0,90,90"`,
		"relative viewport":      `Viewbox="0,0,4,4" Viewport="0,0,1,1" ViewportUnits="RelativeToBoundingBox"`,
		"no viewbox":             `Viewport="0,0,90,90"`,
		"no viewport":            `Viewbox="0,0,4,4"`,
		"bad viewbox":            `Viewbox="a,0,4,4" Viewport="0,0,90,90"`,
		"short viewport":         `Viewbox="0,0,4,4" Viewport="0,0,90"`,
		"zero viewport width":    `Viewbox="0,0,4,4" Viewport="0,0,0,90"`,
		"zero viewport height":   `Viewbox="0,0,4,4" Viewport="0,0,90,0"`,
		"zero viewbox height":    `Viewbox="0,0,4,0" Viewport="0,0,90,90"`,
		"huge scale":             `Viewbox="0,0,0.00001,0.00001" Viewport="0,0,1e9,1e9"`,
		"huge offset":            `Viewbox="0,0,4,4" Viewport="1e13,0,90,90"`,
		"bogus tile mode":        okBrush + ` TileMode="bogus"`,
		"singular transform":     okBrush + ` TileMode="Tile" Transform="0,0,0,0,0,0"`,
	} {
		pdf, res := conv(attrs)
		if !skipped(res, "malformed") || inked(pdf) || strings.Contains(pdf, " Do") {
			t.Errorf("%s: %v", name, res.Skipped)
		}
	}
}

// A singular transform is refused only when there are tiles to place.
func TestSingularTransformWithoutTiling(t *testing.T) {
	b := imgPkg(t, imgBody(okBrush+` Transform="0,0,0,0,0,0"`), pngOf(t, 4, 4, nil2()))
	if _, err := Convert(b, Limits{}); err != nil {
		t.Error(err)
	}
}

// The work a tiled brush costs is charged: 12 tiles are refused under a
// budget of 11 and accepted under 12, with the ceiling out of the way.
func TestTilesAreChargedAsWork(t *testing.T) {
	b := tiled(t, "100")
	if _, err := Convert(b, Limits{MaxWork: 1 << 30}); err != nil {
		t.Fatal(err)
	}
	var ok, no int
	for w := 1; w < 200; w++ {
		if _, err := Convert(b, Limits{MaxWork: w}); err == nil {
			ok = w
			break
		} else if !errors.Is(err, ErrLimit) {
			t.Fatalf("work %d: %v", w, err)
		}
		no = w
	}
	if ok == 0 || ok != no+1 || ok < 12 {
		t.Errorf("a 12-tile brush was admitted at a work budget of %d (refused at %d)", ok, no)
	}
}

// An unreadable header is refused where it is read: nothing is charged and
// nothing is remembered.
func TestUnreadableHeaderIsRefusedAtTheRead(t *testing.T) {
	full := pngOf(t, 4, 4, nil2())
	for name, img := range map[string][]byte{"png": full[:20], "jpeg": jpegOf(t, 8, 8)[:6], "empty png": pngHeader(0, 0)} {
		p := newPkg(page(""))
		p.extra["i.img"] = img
		pk, err := opc.Open(p.bytes(t), opc.Limits{MaxPackageBytes: 1 << 26, MaxPartBytes: 1 << 26, MaxUncompressedBytes: 1 << 26, MaxParts: 100})
		if err != nil {
			t.Fatal(err)
		}
		c := &converter{lim: Limits{}.withDefaults(), pkg: pk, budget: 1 << 40, skip: map[string]int{}}
		im, err := c.image("i.img")
		if !errors.Is(err, ErrBadImage) || im != nil || c.pixels != 0 || len(c.images) != 0 {
			t.Errorf("%s: %v %v pixels=%d cached=%d", name, im, err, c.pixels, len(c.images))
		}
	}
}
