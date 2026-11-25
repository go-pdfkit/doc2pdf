// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-opentype/opentype"
	"github.com/go-pdfkit/pdfkit"
)

// errUnsupportedFont is a font that is recognised and not used: a TrueType
// collection, which go-opentype does not read. The run is counted, not failed.
var errUnsupportedFont = errors.New("xps: unsupported font format")

// A face is one font part, ready to draw with.
type face struct {
	ot *opentype.Font
	pf *pdfkit.Font
}

// deobfuscate undoes the font obfuscation of ECMA-388 §9.1.7.3. The part's
// name is a GUID, B03B02B01B00-B11B10-B21B20-B30B31-B32B33B34B35B36B37, and the
// key is its sixteen bytes in the order B37..B30, B20, B21, B10, B11, B00..B03:
// the name's own hex bytes, read left to right, reversed. The first 32 bytes
// of the font are XORed with the key twice over. Nothing else is touched.
//
// It reports false when the name is not a GUID, and then the bytes are left
// alone (and the font will most likely not parse, which is the honest outcome).
func deobfuscate(name string, b []byte) ([]byte, bool) {
	base := path.Base(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	raw, err := hex.DecodeString(strings.ReplaceAll(base, "-", ""))
	if err != nil || len(raw) != 16 {
		return b, false
	}
	var key [16]byte
	for i := range key {
		key[i] = raw[15-i]
	}
	out := bytes.Clone(b)
	for i := 0; i < 32 && i < len(out); i++ {
		out[i] ^= key[i%16]
	}
	return out, true
}

// maxFontInstructions bounds the TrueType instruction tables (fpgm, prep, cvt)
// of one font, 1 MiB. The tables hinting needs are measured in kilobytes (Go
// Regular carries 4 KiB of them, the long hinted Latin and CJK fonts a few
// hundred KiB at most); more is data nobody executes.
const maxFontInstructions = 1 << 20

// instructionBytes is the size of the fpgm, prep and cvt tables a font's own
// table directory declares, read before the font is parsed.
func instructionBytes(b []byte) int64 {
	if len(b) < 12 {
		return 0
	}
	var sum int64
	n := int(binary.BigEndian.Uint16(b[4:]))
	for i := 0; i < n && 12+16*(i+1) <= len(b); i++ {
		rec := b[12+16*i:]
		switch string(rec[:4]) {
		case "fpgm", "prep", "cvt ":
			sum += int64(binary.BigEndian.Uint32(rec[12:]))
		}
	}
	return sum
}

// font loads the font a FontUri names, once per part.
//
// ⛔ A fragment on the URI is a face index inside a TrueType collection
// (§12.1); only face 0 of a plain font is read, because go-opentype reads no
// collection. The number of distinct fonts is bounded, and a font that failed
// is remembered as failed, so a thousand runs naming one bad font read it once.
func (c *converter) font(base, uri string) (*face, error) {
	if i := strings.IndexByte(uri, '#'); i >= 0 {
		if f := uri[i+1:]; f != "" && f != "0" {
			return nil, errUnsupportedFont
		}
	}
	name, err := c.resolve(base, uri)
	if err != nil {
		return nil, err
	}
	if f, ok := c.fonts[name]; ok {
		if f == nil {
			return nil, fmt.Errorf("%w: %s", ErrBadFont, name)
		}
		return f, nil
	}
	if len(c.fonts) >= c.lim.MaxFonts {
		return nil, limit("fonts", c.lim.MaxFonts)
	}
	if c.fonts == nil {
		c.fonts = map[string]*face{}
	}
	c.fonts[name] = nil
	b, err := c.part(name)
	if err != nil {
		return nil, err
	}
	// A font is kept for the whole conversion: its bytes are charged to the
	// memory budget, whatever the part turns out to be.
	c.kept += int64(len(b))
	if err := c.fits(c.done, 0); err != nil {
		return nil, err
	}
	if ct := c.pkg.ContentType(name); ct == ctObfuscatedFont || strings.HasSuffix(strings.ToLower(name), ".odttf") {
		b, _ = deobfuscate(name, b)
	}
	if bytes.HasPrefix(b, []byte("ttcf")) {
		return nil, errUnsupportedFont
	}
	// ⛔ The subsetter copies the instruction tables whole, so a font of noise in
	// them is held again as the subset, the deflated stream and the output: they
	// are bounded, and what is left is charged at its copies.
	ins := instructionBytes(b)
	if ins > maxFontInstructions {
		return nil, limit("bytes of font instructions", maxFontInstructions)
	}
	c.kept += outputFactor * ins
	if err := c.fits(c.done, 0); err != nil {
		return nil, err
	}
	ot, err := opentype.Parse(b)
	var pf *pdfkit.Font
	if err == nil {
		pf, err = pdfkit.NewFont(ot)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrBadFont, name, err)
	}
	f := &face{ot: ot, pf: pf}
	c.fonts[name] = f
	return f, nil
}

// A mapping is one entry of an Indices attribute (§12.1.3):
//
//	[ "(" n [":" m] ")" ] [glyph] ["," advance ["," uOffset ["," vOffset]]]
//
// n code units become m glyphs; advance and offsets are in hundredths of an em.
type mapping struct {
	n, m       int
	cluster    bool
	gid        int
	hasGid     bool
	adv        float64
	hasAdv     bool
	uOff, vOff float64
}

// parseIndices reads the whole attribute, refusing more than max entries.
func parseIndices(s string, max int) ([]mapping, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	if strings.Count(s, ";") >= max {
		return nil, limit("glyphs in one run", max)
	}
	var out []mapping
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		mp := mapping{n: 1, m: 1}
		if strings.HasPrefix(part, "(") {
			end := strings.IndexByte(part, ')')
			if end < 0 {
				return nil, fmt.Errorf("%w: unclosed cluster", ErrBadGlyphs)
			}
			nm := strings.SplitN(part[1:end], ":", 2)
			n, err := strconv.Atoi(strings.TrimSpace(nm[0]))
			if err != nil || n < 1 || n > max {
				return nil, fmt.Errorf("%w: cluster size", ErrBadGlyphs)
			}
			mp.n, mp.cluster = n, true
			if len(nm) == 2 {
				m, err := strconv.Atoi(strings.TrimSpace(nm[1]))
				if err != nil || m < 1 || m > max {
					return nil, fmt.Errorf("%w: cluster glyph count", ErrBadGlyphs)
				}
				mp.m = m
			}
			part = part[end+1:]
		}
		f := strings.Split(part, ",")
		if len(f) > 4 {
			return nil, fmt.Errorf("%w: too many fields", ErrBadGlyphs)
		}
		if g := strings.TrimSpace(f[0]); g != "" {
			v, err := strconv.Atoi(g)
			if err != nil || v < 0 || v > 0xFFFF {
				return nil, fmt.Errorf("%w: glyph index", ErrBadGlyphs)
			}
			mp.gid, mp.hasGid = v, true
		}
		num := func(i int) (float64, bool, error) {
			if i >= len(f) || strings.TrimSpace(f[i]) == "" {
				return 0, false, nil
			}
			v, ok := finiteNum(f[i])
			if !ok {
				return 0, false, fmt.Errorf("%w: number", ErrBadGlyphs)
			}
			return v, true, nil
		}
		var err error
		if mp.adv, mp.hasAdv, err = num(1); err != nil {
			return nil, err
		}
		if mp.uOff, _, err = num(2); err != nil {
			return nil, err
		}
		if mp.vOff, _, err = num(3); err != nil {
			return nil, err
		}
		out = append(out, mp)
	}
	return out, nil
}

// textColor is the colour a run is drawn in. A gradient or image fill is not
// drawn on text; the run takes the gradient's first colour, and says so.
func (r *renderer) textColor(br *node) (pdfkit.Color, float64, bool, error) {
	switch br.name {
	case "SolidColorBrush":
		c, a, err := parseColor(br.attr("Color"))
		return c, a * opacity(br), err == nil, err
	case "LinearGradientBrush", "RadialGradientBrush":
		if st := gradientStops(br); len(st) > 0 {
			r.c.note("gradient text fill drawn in its first colour")
			return st[0].col, st[0].a * opacity(br), true, nil
		}
		return nil, 0, false, nil
	}
	r.c.note("text fill that is not a colour or gradient")
	return nil, 0, false, nil
}

// glyphs draws one Glyphs element (§12.1) as real text: the glyphs the file
// chose, at the positions the file chose, in the embedded font.
func (r *renderer) glyphs(n *node, cx ctx) error {
	fill := r.brush(n, "Fill", cx.sc)
	if fill == nil {
		return nil // §12.1: without a Fill a Glyphs element has no visible effect.
	}
	size, ok := finiteNum(n.attr("FontRenderingEmSize"))
	if !ok || size <= 0 || size > 1e5 {
		return fmt.Errorf("%w: FontRenderingEmSize", ErrBadGlyphs)
	}
	ox, ok1 := finiteNum(n.attr("OriginX"))
	oy, ok2 := finiteNum(n.attr("OriginY"))
	if !ok1 || !ok2 {
		return fmt.Errorf("%w: origin", ErrBadGlyphs)
	}
	col, ca, drawable, err := r.textColor(fill)
	if err != nil {
		return err
	}
	if !drawable {
		return nil
	}
	f, err := r.c.font(r.part, n.attr("FontUri"))
	if errors.Is(err, errUnsupportedFont) {
		r.c.note("TrueType collection font")
		return nil
	}
	if err != nil {
		return err
	}
	if strings.EqualFold(n.attr("IsSideways"), "true") {
		r.c.note("IsSideways drawn unrotated")
	}
	us := strings.TrimPrefix(n.attr("UnicodeString"), "{}")
	if utf8.RuneCountInString(us) > r.c.lim.MaxGlyphsPerRun {
		return limit("characters in one run", r.c.lim.MaxGlyphsPerRun)
	}
	runes := []rune(us)
	maps, err := parseIndices(n.attr("Indices"), r.c.lim.MaxGlyphsPerRun)
	if err != nil {
		return err
	}
	if len(maps) == 0 {
		maps = make([]mapping, len(runes))
		for i := range maps {
			maps[i] = mapping{n: 1, m: 1}
		}
	}
	// No count is checked again here: the string was bounded above, and
	// parseIndices refuses more than the same ceiling of entries.
	if err := r.c.work(len(maps)); err != nil {
		return err
	}

	sims := strings.ToLower(n.attr("StyleSimulations"))
	bold := strings.Contains(sims, "bold")
	italic := strings.Contains(sims, "italic")
	bidi, _ := finiteNum(n.attr("BidiLevel"))
	rtl := int(bidi)%2 != 0

	// opentype.Parse refuses a font with no units per em (TestZeroUnitsPerEm),
	// so the divisor below is positive for every font that was loaded.
	upem := float64(f.ot.UnitsPerEm())
	nGlyphs := f.ot.NumGlyphs()
	glyphs := make([]pdfkit.Glyph, 0, len(maps))
	pen, pos, pending := 0.0, 0, 0
	var lead rune
	for _, mp := range maps {
		text := ""
		if pending > 0 {
			pending--
			lead = 0
		} else {
			end := pos + mp.n
			if end > len(runes) {
				end = len(runes)
			}
			if pos < len(runes) {
				lead = runes[pos]
			} else {
				lead = 0
			}
			text = string(runes[min(pos, len(runes)):end])
			pos = end
			pending = mp.m - 1
		}
		gid := 0
		switch {
		case mp.hasGid:
			gid = mp.gid
		case lead != 0:
			if g, ok := f.ot.GlyphIndex(lead); ok {
				gid = int(g)
			}
		}
		if gid >= nGlyphs {
			r.c.note("glyph index beyond the font")
			gid = 0
		}
		adv := float64(f.ot.GlyphAdvance(opentype.GlyphIndex(gid))) / upem * size
		if bold {
			adv += 0.02 * size
		}
		if mp.hasAdv {
			adv = mp.adv / 100 * size
		}
		x := pen + mp.uOff/100*size
		if rtl {
			x = pen - adv + mp.uOff/100*size
			pen -= adv
		} else {
			pen += adv
		}
		glyphs = append(glyphs, pdfkit.Glyph{ID: opentype.GlyphIndex(gid), X: x, Y: mp.vOff / 100 * size, Text: text})
	}
	// A run of no glyphs falls through: pdfkit shows no text for it, and its
	// clip and transform are still read, so a malformed one is still named.
	r.p.Save()
	defer r.p.Restore()
	r.transform(n, cx)
	if err := r.clipOf(n, cx); err != nil {
		return err
	}
	r.maskNote(n)
	// From here y is up, as pdfkit draws text; the run origin is (ox, -oy).
	r.p.Transform(1, 0, 0, -1, 0, 0)
	if italic {
		// ItalicSimulation (§12.1.5): a 20 degree skew to the right about the
		// origin, no change of advance.
		t := math.Tan(20 * math.Pi / 180)
		r.p.Transform(1, 0, t, 1, t*oy, 0)
	}
	v := cx.alpha * opacity(n) * ca
	r.p.SetFont(f.pf, size)
	r.p.SetFillColor(col)
	r.setAlpha(v)
	if bold {
		// BoldSimulation (§12.1.5): strokes 1% of the em wider on each side.
		r.p.SetStrokeColor(col)
		r.p.SetLineWidth(0.02 * size)
		r.p.SetRenderMode(2)
	} else {
		r.p.SetRenderMode(0)
	}
	return r.p.ShowGlyphs(ox, -oy, glyphs)
}
