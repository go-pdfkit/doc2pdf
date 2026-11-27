// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"

	"github.com/go-pdfkit/pdfkit"
	"golang.org/x/image/tiff"
)

// errUnsupportedImage is an image format that is recognised and not drawn
// (JPEG XR). It is counted, not failed.
var errUnsupportedImage = errors.New("xps: unsupported image format")

// An imageInfo is one image part, measured before anything is decoded.
//
// ⛔ It keeps no bytes of the part: the part is read again when the image is
// embedded, so an image that is never drawn, or drawn once, holds nothing.
type imageInfo struct {
	part       string // the canonical part name
	kind       string // "jpeg", "png" or "tiff"
	w, h       int
	dpiX, dpiY float64
	// cost estimates the memory one decode of the image needs (see decodeCost),
	// and stored the most the PDF library keeps for it (see storedBound).
	cost, stored int64
	// handle is the image in the PDF, made at the first draw; err is why it
	// could not be (a limit is not kept).
	handle *pdfkit.Image
	err    error
}

// decodeCost estimates the bytes one decode of a w*h image needs, from its
// colour model, before anything is decoded: the decoded samples, the decoder's
// own working copy (a TIFF strip is inflated whole, a PNG row buffer and
// image/jpeg coefficient blocks are kept) and the conversion pdfkit makes to
// draw it. Four times the decoded bytes and sixteen more per pixel is an upper
// bound across the 16-bit PNG and a bitonal PNG, with the race detector too
// (eight more was passed by 2 MiB under -race, CI 2026-11-23; see
// TestSecDecodeCostBoundsMeasured). A TIFF costs thirty-two more again: its
// reader inflates a strip into a buffer that doubles, up to 8 bytes a pixel
// whatever the colour model (x/image/tiff reader.go), and an 8-bit gray deflate
// TIFF of 3170 x 3170 measured 251 MiB (368 under -race) where this was 190
// (TestSecTIFFDecodeCost).
func decodeCost(cfg image.Config, kind string) int64 {
	extra := int64(16)
	if kind == "tiff" {
		extra = 48
	}
	bpp := int64(4)
	switch m := cfg.ColorModel.(type) {
	case color.Palette:
		bpp = 1
	default:
		switch m {
		case color.GrayModel:
			bpp = 1
		case color.Gray16Model:
			bpp = 2
		case color.RGBA64Model, color.NRGBA64Model:
			bpp = 8
		}
	}
	return int64(cfg.Width) * int64(cfg.Height) * (bpp*4 + extra)
}

// storedBound is the most the PDF library keeps for a decoded PNG or TIFF: it
// stores the samples as 8-bit RGB, deflated, plus an alpha plane when the model
// has one, and deflate can do no better than the samples on noise. That is what
// is charged, and not the size of the part, which a paletted noise PNG makes a
// third of it.
func storedBound(cfg image.Config) int64 {
	per := int64(3)
	switch m := cfg.ColorModel.(type) {
	case color.Palette:
		for _, c := range m {
			if _, _, _, a := c.RGBA(); a != 0xFFFF {
				per = 4
			}
		}
	default:
		switch m {
		case color.GrayModel, color.Gray16Model, color.YCbCrModel, color.CMYKModel:
		default:
			per = 4
		}
	}
	return int64(cfg.Width) * int64(cfg.Height) * per
}

// jpegMaxPixelsPerByte is the most pixels a JPEG may declare per byte of file.
// pdfkit reads only the frame header of a JPEG and embeds the rest unread, so a
// 36-byte file can declare 36 million pixels and leave the reader to build them.
// A blank 4:2:0 image costs about three bytes per 256 pixels (85 a byte), so
// 1024 is twelve times the best a real encoder does.
const jpegMaxPixelsPerByte = 1024

// image reads an image part and measures it.
//
// ⛔ The pixel count comes from DecodeConfig, which reads the header and
// allocates nothing, and is checked BEFORE any decode: a 40 KB PNG can declare
// four billion pixels. The count is charged once per part against the total.
func (c *converter) image(part string) (*imageInfo, error) {
	if im, ok := c.images[part]; ok {
		return im, nil
	}
	if err, bad := c.badImages[part]; bad {
		return nil, err
	}
	im, err := c.readImage(part)
	if err != nil && !errors.Is(err, ErrLimit) {
		// A part that failed is remembered as failed, so a thousand brushes
		// naming it read it once. (A limit refusal ends the conversion.)
		if c.badImages == nil {
			c.badImages = map[string]error{}
		}
		c.badImages[part] = err
	}
	return im, err
}

// readImage reads and measures an image part not yet in the cache. part is a
// canonical name (see resolve), so each real part is read and kept once.
func (c *converter) readImage(part string) (*imageInfo, error) {
	b, err := c.part(part)
	if err != nil {
		return nil, err
	}
	im := &imageInfo{part: part, dpiX: 96, dpiY: 96}
	var cfg image.Config
	switch {
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8}):
		im.kind = "jpeg"
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(b))
		im.dpiX, im.dpiY = jpegDPI(b)
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		im.kind = "png"
		cfg, err = png.DecodeConfig(bytes.NewReader(b))
		im.dpiX, im.dpiY = pngDPI(b)
	case bytes.HasPrefix(b, []byte("II*\x00")), bytes.HasPrefix(b, []byte("MM\x00*")):
		im.kind = "tiff"
		cfg, err = tiff.DecodeConfig(bytes.NewReader(b))
	case bytes.HasPrefix(b, []byte("II\xBC")):
		return nil, errUnsupportedImage
	default:
		return nil, fmt.Errorf("%w: %s is not JPEG, PNG or TIFF", ErrBadImage, part)
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, fmt.Errorf("%w: %s is unreadable or empty: %v", ErrBadImage, part, err)
	}
	px := int64(cfg.Width) * int64(cfg.Height)
	if px > int64(c.lim.MaxImagePixels) {
		return nil, limit("pixels in one image", c.lim.MaxImagePixels)
	}
	if c.pixels += int(px); c.pixels > c.lim.MaxTotalImagePixels {
		return nil, limit("pixels in all images", c.lim.MaxTotalImagePixels)
	}
	if im.kind == "jpeg" {
		im.stored = int64(len(b))
		if px > jpegMaxPixelsPerByte*int64(len(b)) {
			return nil, fmt.Errorf("%w: %s declares %d pixels in %d bytes", ErrBadImage, part, px, len(b))
		}
	} else {
		im.stored = storedBound(cfg)
	}
	im.w, im.h = cfg.Width, cfg.Height
	im.cost = decodeCost(cfg, im.kind)
	if c.images == nil {
		c.images = map[string]*imageInfo{}
	}
	c.images[part] = im
	return im, nil
}

func saneDPI(v float64) float64 {
	if math.IsNaN(v) || v < 1 || v > 100000 {
		return 96
	}
	return v
}

// jpegDPI reads the JFIF density (units 1 is dots per inch, 2 per cm), 96 when
// there is none or it only gives an aspect ratio.
func jpegDPI(b []byte) (float64, float64) {
	for i := 2; i+4 <= len(b) && b[i] == 0xFF; {
		marker := b[i+1]
		n := int(binary.BigEndian.Uint16(b[i+2:]))
		if marker == 0xDA || n < 2 {
			break
		}
		if marker == 0xE0 && i+16 <= len(b) && n >= 16 && string(b[i+4:i+9]) == "JFIF\x00" {
			units := b[i+11]
			x := float64(binary.BigEndian.Uint16(b[i+12:]))
			y := float64(binary.BigEndian.Uint16(b[i+14:]))
			switch units {
			case 1:
				return saneDPI(x), saneDPI(y)
			case 2:
				return saneDPI(x * 2.54), saneDPI(y * 2.54)
			}
			return 96, 96
		}
		i += 2 + n
	}
	return 96, 96
}

// pngDPI reads the pHYs chunk (pixels per metre when the unit is 1).
func pngDPI(b []byte) (float64, float64) {
	for i := 8; i+12 <= len(b); {
		n := int(binary.BigEndian.Uint32(b[i:]))
		typ := string(b[i+4 : i+8])
		if typ == "IDAT" || n < 0 || i+12+n > len(b) {
			break
		}
		if typ == "pHYs" && n == 9 {
			d := b[i+8:]
			if d[8] == 1 {
				return saneDPI(float64(binary.BigEndian.Uint32(d)) * 0.0254), saneDPI(float64(binary.BigEndian.Uint32(d[4:])) * 0.0254)
			}
			return 96, 96
		}
		i += 12 + n
	}
	return 96, 96
}

// imageSource reads an ImageSource: a URI, or {ColorConvertedBitmap uri profile}.
func (r *renderer) imageSource(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "{") && strings.HasSuffix(v, "}") {
		f := strings.Fields(v[1 : len(v)-1])
		if len(f) >= 2 && f[0] == "ColorConvertedBitmap" {
			r.c.note("ICC profile of an image ignored")
			return f[1]
		}
	}
	return v
}

// drawImage places the whole image in the rectangle (x, y, w, h), in page
// units with y down. The image is drawn y-up, so the space is flipped for it.
func (r *renderer) drawImage(im *imageInfo, x, y, w, h float64) error {
	h0, err := r.register(im)
	if err != nil {
		return err
	}
	r.p.Save()
	defer r.p.Restore()
	r.p.Transform(1, 0, 0, -1, 0, 0)
	return r.p.DrawImageHandle(h0, pdfkit.Rect{X: x, Y: -(y + h), Width: w, Height: h})
}

// register gives the image's handle in the PDF, embedding it the first time.
//
// ⛔ One handle per image part: a draw after the first costs the few bytes of
// its operator, which MaxContentBytes counts, and neither a decode nor a
// copy. What an embedding costs is charged once, here. A part that could not be
// embedded is remembered, so a thousand draws of it fail alike without trying
// again; a limit ends the conversion and is not remembered.
func (r *renderer) register(im *imageInfo) (*pdfkit.Image, error) {
	if im.handle != nil {
		return im.handle, nil
	}
	if im.err != nil {
		return nil, im.err
	}
	h, err := r.embed(im)
	if err != nil {
		if !errors.Is(err, ErrLimit) {
			im.err = err
		}
		return nil, err
	}
	im.handle = h
	return h, nil
}

// embed registers the image with the document: a JPEG pdfkit takes as it is,
// otherwise the decoded pixels.
func (r *renderer) embed(im *imageInfo) (*pdfkit.Image, error) {
	// ⛔ The memory budget: this image's part is read now and held while it is
	// embedded, a PNG or TIFF is decoded on top of that, and what the document
	// stores for it is held to the end (and copied into the output).
	// A JPEG is read and then copied into the document: the part and the stored
	// copy, both at once. A PNG or TIFF is checked below, with its decode.
	if im.kind == "jpeg" {
		if err := r.fits(2 * im.stored); err != nil {
			return nil, err
		}
	}
	b, err := r.c.part(im.part)
	if err != nil {
		return nil, err
	}
	// ⛔ A JPEG is embedded as it is and never decoded here: image/jpeg allocates
	// a progressive image's coefficients at the first scan (a 36-byte file cost
	// 540 MiB), and pdfkit takes the files image/jpeg takes, progressive and
	// stray bytes included.
	if im.kind == "jpeg" {
		h, err := r.c.doc.AddJPEG(b)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadImage, err)
		}
		r.c.stored += h.StoredLen()
		return h, nil
	}
	// ⛔ Everything that bounds a decode is checked HERE, from the header, before
	// the decoder allocates.
	if im.cost > r.c.lim.MaxDecodeBytes {
		return nil, limit("estimated bytes to decode one image", int(min(r.c.lim.MaxDecodeBytes, math.MaxInt32)))
	}
	// The part, the decode and the stored copy, all at once.
	if err := r.fits(int64(len(b)) + im.cost + im.stored); err != nil {
		return nil, err
	}
	r.c.drawn += int64(im.w) * int64(im.h)
	if r.c.drawn > int64(r.c.lim.MaxDrawnPixels) {
		return nil, limit("pixels decoded to draw images", r.c.lim.MaxDrawnPixels)
	}
	var img image.Image
	switch im.kind {
	case "png":
		img, err = png.Decode(bytes.NewReader(b))
	default:
		img, err = tiff.Decode(bytes.NewReader(b))
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadImage, err)
	}
	// The decoded image and the part are not kept: the handle holds the
	// compressed samples, charged at their worst.
	h := r.c.doc.AddImage(img)
	r.c.stored += h.StoredLen()
	return h, nil
}

// clipRect clips to an axis-aligned rectangle.
func (r *renderer) clipRect(x, y, w, h float64) {
	r.p.MoveTo(x, y)
	r.p.LineTo(x+w, y)
	r.p.LineTo(x+w, y+h)
	r.p.LineTo(x, y+h)
	r.p.ClosePath()
	r.p.Clip()
	r.p.EndPath()
}

// imageBrush fills the current clip with an ImageBrush (§13.2, §13.4).
//
// The Viewbox is a window onto the image, in units of 1/96 inch of the
// image's PHYSICAL size — its pixels scaled by 96 over its resolution — and it
// is mapped onto the Viewport, which is in page units. With TileMode None the
// viewport is painted once; with a tiling mode it is repeated, mirrored on
// alternate columns (FlipX), rows (FlipY) or both.
func (r *renderer) imageBrush(g *geometry, br *node, alpha float64, cx ctx) error {
	part, err := r.c.resolve(r.part, r.imageSource(br.attr("ImageSource")))
	var im *imageInfo
	if err == nil {
		im, err = r.c.image(part)
	}
	if errors.Is(err, errUnsupportedImage) {
		r.c.note("JPEG XR image")
		return nil
	}
	if err != nil {
		return err
	}
	for _, u := range []string{br.attr("ViewboxUnits"), br.attr("ViewportUnits")} {
		if u != "" && !strings.EqualFold(u, "Absolute") {
			return fmt.Errorf("xps: only Absolute viewbox units are defined")
		}
	}
	vb, ok1 := parseNums(br.attr("Viewbox"), 4)
	vp, ok2 := parseNums(br.attr("Viewport"), 4)
	if !ok1 || !ok2 || len(vb) != 4 || len(vp) != 4 || vb[2] <= 0 || vb[3] <= 0 || vp[2] <= 0 || vp[3] <= 0 {
		return fmt.Errorf("xps: unusable Viewbox or Viewport")
	}
	wp := float64(im.w) * 96 / im.dpiX
	hp := float64(im.h) * 96 / im.dpiY
	sx, sy := vp[2]/vb[2], vp[3]/vb[3]
	ix, iy := vp[0]-vb[0]*sx, vp[1]-vb[1]*sy
	iw, ih := wp*sx, hp*sy
	for _, v := range []float64{ix, iy, iw, ih} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e12 {
			return fmt.Errorf("xps: unusable Viewbox or Viewport")
		}
	}
	r.p.Save()
	defer r.p.Restore()
	m, hasM := r.matrix(br, "Transform", cx.sc)
	if hasM {
		r.p.Transform(m[0], m[1], m[2], m[3], m[4], m[5])
	}
	r.setAlpha(alpha)

	mode := strings.ToLower(strings.TrimSpace(br.attr("TileMode")))
	if mode == "" || mode == "none" {
		r.p.Save()
		defer r.p.Restore()
		r.clipRect(vp[0], vp[1], vp[2], vp[3])
		return r.drawImage(im, ix, iy, iw, ih)
	}
	flipX := mode == "flipx" || mode == "flipxy"
	flipY := mode == "flipy" || mode == "flipxy"
	if mode != "tile" && !flipX && !flipY {
		return fmt.Errorf("xps: unknown TileMode")
	}

	// The tiles that touch the shape: its box, taken back into brush space.
	x0, y0, x1, y1, _ := g.bounds()
	if hasM {
		det := m[0]*m[3] - m[1]*m[2]
		if det == 0 || math.IsNaN(det) || math.IsInf(det, 0) {
			return fmt.Errorf("xps: singular brush transform")
		}
		inv := [6]float64{m[3] / det, -m[1] / det, -m[2] / det, m[0] / det, 0, 0}
		inv[4] = -(inv[0]*m[4] + inv[2]*m[5])
		inv[5] = -(inv[1]*m[4] + inv[3]*m[5])
		bx0, by0, bx1, by1 := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, c := range [][2]float64{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}} {
			px := inv[0]*c[0] + inv[2]*c[1] + inv[4]
			py := inv[1]*c[0] + inv[3]*c[1] + inv[5]
			bx0, bx1 = math.Min(bx0, px), math.Max(bx1, px)
			by0, by1 = math.Min(by0, py), math.Max(by1, py)
		}
		x0, y0, x1, y1 = bx0, by0, bx1, by1
	}
	i0, i1 := math.Floor((x0-vp[0])/vp[2]), math.Ceil((x1-vp[0])/vp[2])-1
	j0, j1 := math.Floor((y0-vp[1])/vp[3]), math.Ceil((y1-vp[1])/vp[3])-1
	// ⛔ Tile indices are integers, and bounded as such. As float64 they stop
	// advancing at 2^53 (i++ changes nothing), where the column count rounds to
	// one, one unit of work is charged, and the loop never ends: a 1.9 KB file
	// drew more than 3 GiB. A tile index beyond 2^31 is no tile of any page.
	const maxIndex = 1 << 31
	for _, v := range []float64{i0, i1, j0, j1} {
		if !(math.Abs(v) <= maxIndex) {
			return limit("tile index in one brush", maxIndex)
		}
	}
	cols, rows := i1-i0+1, j1-j0+1
	// Compared as floats: the product of two huge counts overflows an int
	// before it can be refused. Written so that what is not a number, which
	// no box of a real shape gives, is refused with the rest, and a count
	// below one (nothing to draw) is charged nothing.
	if !(cols*rows <= float64(r.c.lim.MaxTiles)) {
		return limit("tiles in one brush", r.c.lim.MaxTiles)
	}
	if err := r.c.work(int(math.Max(cols*rows, 0))); err != nil {
		return err
	}
	for j := int64(j0); j <= int64(j1); j++ {
		for i := int64(i0); i <= int64(i1); i++ {
			a, e := 1.0, float64(i)*vp[2]
			if flipX && i&1 != 0 {
				a, e = -1, 2*vp[0]+float64(i+1)*vp[2]
			}
			d, f := 1.0, float64(j)*vp[3]
			if flipY && j&1 != 0 {
				d, f = -1, 2*vp[1]+float64(j+1)*vp[3]
			}
			r.p.Save()
			r.p.Transform(a, 0, 0, d, e, f)
			r.clipRect(vp[0], vp[1], vp[2], vp[3])
			err := r.drawImage(im, ix, iy, iw, ih)
			r.p.Restore()
			if err != nil {
				return err
			}
		}
	}
	return nil
}
