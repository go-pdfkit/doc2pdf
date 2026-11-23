// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/go-pdfkit/pdfkit"
)

// A renderer draws one FixedPage onto one pdfkit page.
//
// The page's user space is set once to the page's own: 1/96 inch, y down
// (ECMA-388 §9.1.4, §14.1), by a single cm of 0.75 0 0 -0.75 0 H. Everything
// below is then drawn in XPS units with XPS matrices and XPS clips, and the
// PDF reader does the scaling and the flip. Text and images, which are drawn
// y-up, flip locally.
type renderer struct {
	c    *converter
	p    *pdfkit.Page
	part string
}

// A scope is one resource dictionary and the ones around it (§14.2.4): a
// {StaticResource key} is looked up innermost first.
type scope struct {
	up *scope
	m  map[string]*node
}

func (s *scope) look(key string) *node {
	for ; s != nil; s = s.up {
		if n, ok := s.m[key]; ok {
			return n
		}
	}
	return nil
}

// A ctx is what flows down the tree: the resources in scope and the opacity
// accumulated from the canvases above.
type ctx struct {
	sc    *scope
	alpha float64
}

// work charges n units against the conversion's budget. Every repeated cost —
// a segment, a glyph, a gradient stop, a tile — goes through it, so that a
// resource drawn a hundred thousand times is paid for a hundred thousand
// times and not once.
func (c *converter) work(n int) error {
	c.spent += n
	if c.spent > c.lim.MaxWork {
		return limit("drawing work (segments, glyphs, stops and tiles)", c.lim.MaxWork)
	}
	return nil
}

// check measures the content written so far, on every page, against
// MaxContentBytes.
//
// ⛔ MaxWork counts units, and two segments and a 126-number dash array are one
// unit apiece to it. The content stream is what is held in memory, so it is
// what is budgeted, and the PDF library reports its own length (ContentLen,
// O(1)): nothing here estimates it. It is called after every drawn element, so
// what one element writes past the ceiling is bounded by its own input (MaxTiles
// tiles, a dash array as long as its attribute), never by the page.
func (r *renderer) check() error {
	if r.c.done+r.p.ContentLen() > r.c.lim.MaxContentBytes {
		return limit("bytes of page content", int(min(r.c.lim.MaxContentBytes, math.MaxInt32)))
	}
	return nil
}

// page draws one FixedPage part as the next page of the document.
func (c *converter) page(name string) error {
	root, err := c.xmlPart(name)
	if err != nil {
		return err
	}
	if root.name != "FixedPage" {
		return fmt.Errorf("%w: %s holds a %s", ErrBadPage, name, root.name)
	}
	w, ok1 := finiteNum(root.attr("Width"))
	h, ok2 := finiteNum(root.attr("Height"))
	if !ok1 || !ok2 || w <= 0 || h <= 0 {
		return fmt.Errorf("%w: %s has no usable Width and Height", ErrBadPage, name)
	}
	if w > c.lim.MaxPageUnits || h > c.lim.MaxPageUnits {
		return limit("page side in 1/96 inch", int(c.lim.MaxPageUnits))
	}
	p := c.doc.AddPage(pdfkit.NewPageSize(w*0.75, h*0.75))
	p.Transform(0.75, 0, 0, -0.75, 0, h*0.75)
	c.pages++
	r := &renderer{c: c, p: p, part: name}
	err = r.children(root, ctx{sc: r.newScope(nil, root), alpha: 1})
	c.done += p.ContentLen()
	return err
}

// newScope adds n's own Resources, if it has any, to the scope.
func (r *renderer) newScope(up *scope, n *node) *scope {
	res := n.child(n.name + ".Resources")
	if res == nil {
		return up
	}
	s := &scope{up: up, m: map[string]*node{}}
	for _, d := range res.kids {
		if d.name != "ResourceDictionary" {
			continue
		}
		if d.has("Source") {
			r.c.note("remote resource dictionary")
		}
		for _, e := range d.kids {
			if k := e.attr("Key"); k != "" {
				if _, dup := s.m[k]; !dup {
					s.m[k] = e
				}
			}
		}
	}
	return s
}

// children draws the drawable children of n, in order. An element that cannot
// be drawn is counted and skipped; a limit is not a thing to skip, and ends
// the conversion.
func (r *renderer) children(n *node, cx ctx) error {
	for _, k := range n.kids {
		var err error
		switch k.name {
		case "Canvas":
			err = r.canvas(k, cx)
		case "Path":
			err = r.path(k, cx)
		case "Glyphs":
			err = r.glyphs(k, cx)
		case "AlternateContent":
			// Markup Compatibility: the Fallback is what a reader that does
			// not know the Choice's namespace draws, and this one knows none.
			for _, fb := range k.kids {
				if fb.name == "Fallback" {
					if err = r.children(fb, cx); err != nil {
						break
					}
				}
			}
		default:
			if !strings.Contains(k.name, ".") {
				r.c.note("unknown element")
			}
		}
		// What the element wrote is measured, whatever became of it.
		if lerr := r.check(); lerr != nil {
			return lerr
		}
		if err != nil {
			if errors.Is(err, ErrLimit) {
				return err
			}
			r.c.note("malformed " + k.name)
		}
	}
	return nil
}

func (r *renderer) canvas(n *node, cx ctx) error {
	r.p.Save()
	defer r.p.Restore()
	cx.sc = r.newScope(cx.sc, n)
	r.transform(n, cx)
	if err := r.clipOf(n, cx); err != nil {
		return err
	}
	cx.alpha *= opacity(n)
	r.maskNote(n)
	return r.children(n, cx)
}

func (r *renderer) maskNote(n *node) {
	if n.has("OpacityMask") || n.child(n.name+".OpacityMask") != nil {
		r.c.note("OpacityMask")
	}
}

// setAlpha sets the constant alpha, rounded to the nearest 1/255.
//
// ⛔ pdfkit scans every alpha state a page has used to find the one asked for,
// so a page that uses N distinct opacities pays N per use: 120000 distinct
// values took fifteen seconds. 8-bit alpha is all a viewer shows anyway, and
// this caps the distinct states at 256.
func (r *renderer) setAlpha(v float64) {
	v = math.Round(clamp01(v)*255) / 255
	r.p.SetAlpha(v, v)
}

// opacity is the Opacity attribute, 1 when absent or unreadable.
func opacity(n *node) float64 {
	if v, ok := finiteNum(n.attr("Opacity")); ok {
		return clamp01(v)
	}
	return 1
}

// staticKey reads {StaticResource key}.
func staticKey(v string) (string, bool) {
	if !strings.HasPrefix(v, "{") || !strings.HasSuffix(v, "}") {
		return "", false
	}
	f := strings.Fields(v[1 : len(v)-1])
	if len(f) == 2 && f[0] == "StaticResource" {
		return f[1], true
	}
	return "", false
}

// propNode is the value of a property, however it was written: an attribute
// (a literal, or a reference to a resource) or a property element
// (<Path.Fill>). A literal comes back as a node named "#literal" holding it.
func (r *renderer) propNode(n *node, prop string, sc *scope) *node {
	if n.has(prop) {
		v := strings.TrimSpace(n.attr(prop))
		if key, ok := staticKey(v); ok {
			if x := sc.look(key); x != nil {
				return x
			}
			r.c.note("unresolved resource reference")
			return nil
		}
		return &node{name: "#literal", attrs: map[string]string{"v": v}}
	}
	if pe := n.child(n.name + "." + prop); pe != nil && len(pe.kids) > 0 {
		return pe.kids[0]
	}
	return nil
}

// matrix reads a transform property: a literal matrix, or a MatrixTransform
// element or resource.
func (r *renderer) matrix(n *node, prop string, sc *scope) ([6]float64, bool) {
	x := r.propNode(n, prop, sc)
	if x == nil {
		return identity, false
	}
	var s string
	switch x.name {
	case "#literal":
		s = x.attrs["v"]
	case "MatrixTransform":
		s = x.attr("Matrix")
	default:
		return identity, false
	}
	m, ok := parseMatrix(s)
	if !ok {
		r.c.note("unreadable transform")
	}
	return m, ok
}

// transform applies the element's RenderTransform to everything drawn after it,
// clip included (§14.3).
func (r *renderer) transform(n *node, cx ctx) {
	if m, ok := r.matrix(n, "RenderTransform", cx.sc); ok {
		r.p.Transform(m[0], m[1], m[2], m[3], m[4], m[5])
	}
}

// geomProp reads a geometry property: abbreviated, PathGeometry element, or
// resource. It returns nil with no error when the property is absent.
func (r *renderer) geomProp(n *node, prop string, sc *scope) (*geometry, error) {
	x := r.propNode(n, prop, sc)
	if x == nil {
		return nil, nil
	}
	switch x.name {
	case "#literal":
		b := &builder{g: &geometry{evenOdd: true}, max: r.c.lim.MaxSegmentsPerPath, spend: r.c.work}
		if err := parseAbbrev(x.attrs["v"], b); err != nil {
			return nil, err
		}
		return b.g, nil
	case "PathGeometry":
		return r.geometryFromNode(x, sc)
	}
	return nil, fmt.Errorf("%w: %s is not a geometry", ErrBadGeometry, x.name)
}

func (r *renderer) clipOf(n *node, cx ctx) error {
	g, err := r.geomProp(n, "Clip", cx.sc)
	if err != nil {
		return err
	}
	if g != nil {
		g.clip(r.p)
	}
	return nil
}

// brush is the brush property. A colour literal becomes a SolidColorBrush.
func (r *renderer) brush(n *node, prop string, sc *scope) *node {
	x := r.propNode(n, prop, sc)
	if x != nil && x.name == "#literal" {
		return &node{name: "SolidColorBrush", attrs: map[string]string{"Color": x.attrs["v"]}}
	}
	return x
}

func (r *renderer) path(n *node, cx ctx) error {
	g, err := r.geomProp(n, "Data", cx.sc)
	if err != nil {
		return err
	}
	if g == nil || len(g.segs) == 0 {
		return nil
	}
	r.p.Save()
	defer r.p.Restore()
	r.transform(n, cx)
	if err := r.clipOf(n, cx); err != nil {
		return err
	}
	r.maskNote(n)
	alpha := cx.alpha * opacity(n)
	if fill := r.brush(n, "Fill", cx.sc); fill != nil {
		if err := r.fill(g, fill, alpha, cx); err != nil {
			return err
		}
	}
	if stroke := r.brush(n, "Stroke", cx.sc); stroke != nil {
		return r.stroke(n, g, stroke, alpha)
	}
	return nil
}

// fill paints the inside of g with a brush.
func (r *renderer) fill(g *geometry, br *node, alpha float64, cx ctx) error {
	switch br.name {
	case "SolidColorBrush":
		col, a, err := parseColor(br.attr("Color"))
		if err != nil {
			return err
		}
		v := alpha * a * opacity(br)
		if v <= 0 {
			return nil
		}
		r.p.SetFillColor(col)
		r.setAlpha(v)
		g.emit(r.p)
		if g.evenOdd {
			r.p.FillEvenOdd()
		} else {
			r.p.Fill()
		}
	case "LinearGradientBrush", "RadialGradientBrush":
		r.p.Save()
		defer r.p.Restore()
		g.clip(r.p)
		return r.gradient(br, alpha*opacity(br), cx)
	case "ImageBrush":
		r.p.Save()
		defer r.p.Restore()
		g.clip(r.p)
		return r.imageBrush(g, br, alpha*opacity(br), cx)
	case "VisualBrush":
		r.c.note("VisualBrush")
	default:
		r.c.note("unknown brush")
	}
	return nil
}

// stroke draws g's outline (§11.1): thickness, caps, joins, miter limit and
// dashes, which XPS counts in multiples of the thickness.
//
// ⛔ The miter limit is the ratio of the miter's length to HALF the
// thickness in XPS and to the whole thickness in PDF, so it is halved.
func (r *renderer) stroke(n *node, g *geometry, br *node, alpha float64) error {
	th := 1.0
	if n.has("StrokeThickness") {
		v, ok := finiteNum(n.attr("StrokeThickness"))
		if !ok {
			return fmt.Errorf("xps: unreadable StrokeThickness")
		}
		th = v
	}
	if th <= 0 {
		return nil
	}
	var col pdfkit.Color
	a := 1.0
	switch br.name {
	case "SolidColorBrush":
		c, ca, err := parseColor(br.attr("Color"))
		if err != nil {
			return err
		}
		col, a = c, ca
	case "LinearGradientBrush", "RadialGradientBrush":
		st := gradientStops(br)
		if len(st) == 0 {
			return nil
		}
		col = st[0].col
		r.c.note("gradient stroke drawn in its first colour")
	default:
		r.c.note("stroke brush not drawn")
		return nil
	}
	v := alpha * a * opacity(br)
	if v <= 0 {
		return nil
	}
	r.p.SetStrokeColor(col)
	r.setAlpha(v)
	r.p.SetLineWidth(th)

	dashed := false
	if arr, ok := parseNums(n.attr("StrokeDashArray"), 64); ok && len(arr) > 0 {
		sum, neg := 0.0, false
		for _, d := range arr {
			sum += d
			neg = neg || d < 0
		}
		if sum > 0 && !neg {
			pat := make([]float64, 0, 2*len(arr))
			for _, d := range arr {
				pat = append(pat, d*th)
			}
			if len(arr)%2 == 1 {
				pat = append(pat, pat...)
			}
			off, _ := finiteNum(n.attr("StrokeDashOffset"))
			r.p.SetDash(pat, off*th)
			dashed = true
		}
	}
	capName := n.attr("StrokeStartLineCap")
	if dashed {
		capName = n.attr("StrokeDashCap")
	}
	switch strings.ToLower(capName) {
	case "round":
		r.p.SetLineCap(1)
	case "square":
		r.p.SetLineCap(2)
	case "triangle":
		r.c.note("Triangle line cap drawn flat")
		r.p.SetLineCap(0)
	default:
		r.p.SetLineCap(0)
	}
	switch strings.ToLower(n.attr("StrokeLineJoin")) {
	case "round":
		r.p.SetLineJoin(1)
	case "bevel":
		r.p.SetLineJoin(2)
	default:
		r.p.SetLineJoin(0)
	}
	ml := 10.0
	if v, ok := finiteNum(n.attr("StrokeMiterLimit")); ok {
		ml = v
	}
	if ml /= 2; ml < 1 {
		ml = 1
	}
	r.p.SetMiterLimit(ml)
	g.emit(r.p)
	r.p.Stroke()
	return nil
}

// A stop is one gradient colour.
type stop struct {
	off float64
	col pdfkit.Color
	a   float64
}

// gradientStops reads the GradientStops of a gradient brush, in offset order.
func gradientStops(br *node) []stop {
	gs := br.child(br.name + ".GradientStops")
	if gs == nil {
		return nil
	}
	var out []stop
	for _, s := range gs.kids {
		if s.name != "GradientStop" || len(out) >= 1024 {
			continue
		}
		col, a, err := parseColor(s.attr("Color"))
		off, ok := finiteNum(s.attr("Offset"))
		if err != nil || !ok {
			continue
		}
		out = append(out, stop{off: clamp01(off), col: col, a: a})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].off < out[j].off })
	return out
}

// gradient paints a linear or radial gradient through the current clip
// (§13.5, §13.6). Pad spreading is what is drawn; Reflect and Repeat are
// counted, and drawn as Pad.
func (r *renderer) gradient(br *node, alpha float64, cx ctx) error {
	raw := gradientStops(br)
	if len(raw) == 0 {
		return nil
	}
	if err := r.c.work(len(raw)); err != nil {
		return err
	}
	var stops []pdfkit.Stop
	prev := -1.0
	for _, s := range raw {
		if s.a < 1 {
			r.c.note("gradient stop alpha ignored")
		}
		off := s.off
		if off <= prev {
			off = prev + 1e-6
		}
		if off > 1 {
			continue
		}
		prev = off
		stops = append(stops, pdfkit.Stop{Offset: off, Color: asRGB(s.col)})
	}
	// The first stop is always kept (its offset is in [0, 1], and only a
	// stop pushed past 1 by the strict ordering is dropped), so one is at
	// least there.
	if len(stops) == 1 {
		stops = []pdfkit.Stop{{Offset: 0, Color: stops[0].Color}, {Offset: 1, Color: stops[0].Color}}
	}
	if sm := strings.ToLower(br.attr("SpreadMethod")); sm == "reflect" || sm == "repeat" {
		r.c.note("gradient SpreadMethod " + sm + " drawn as Pad")
	}
	r.p.Save()
	defer r.p.Restore()
	if m, ok := r.matrix(br, "Transform", cx.sc); ok {
		r.p.Transform(m[0], m[1], m[2], m[3], m[4], m[5])
	}
	r.setAlpha(alpha)
	pt := func(attr string, d0, d1 float64) (float64, float64, bool) {
		if !br.has(attr) {
			return d0, d1, true
		}
		v, ok := parseNums(br.attr(attr), 2)
		if !ok || len(v) != 2 {
			return 0, 0, false
		}
		return v[0], v[1], true
	}
	if br.name == "LinearGradientBrush" {
		x0, y0, ok1 := pt("StartPoint", 0, 0)
		x1, y1, ok2 := pt("EndPoint", 1, 0)
		if !ok1 || !ok2 {
			return fmt.Errorf("xps: unreadable gradient points")
		}
		return r.p.Shade(pdfkit.Shading{X0: x0, Y0: y0, X1: x1, Y1: y1, Stops: stops, ExtendStart: true, ExtendEnd: true})
	}
	cxx, cyy, ok1 := pt("Center", 0.5, 0.5)
	ox, oy, ok2 := pt("GradientOrigin", 0.5, 0.5)
	rx, ry := 0.5, 0.5
	if br.has("RadiusX") {
		v, ok := finiteNum(br.attr("RadiusX"))
		rx = v
		ok1 = ok1 && ok
	}
	if br.has("RadiusY") {
		v, ok := finiteNum(br.attr("RadiusY"))
		ry = v
		ok1 = ok1 && ok
	}
	if !ok1 || !ok2 || rx <= 0 || ry <= 0 {
		return fmt.Errorf("xps: unreadable radial gradient")
	}
	// An ellipse is a circle under a scale of one axis.
	//
	// ⛔ Every number handed to the PDF must be finite and sane: a ratio that
	// underflows (RadiusY 1e-300 over RadiusX 1e9) would divide the centre to
	// infinity, and the writer would print "Inf" into the file.
	s := ry / rx
	if !(s >= 1e-9 && s <= 1e9) {
		return fmt.Errorf("xps: unusable radial gradient geometry")
	}
	for _, v := range []float64{oy / s, cyy / s, ox, cxx, rx} {
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e12 {
			return fmt.Errorf("xps: unusable radial gradient geometry")
		}
	}
	r.p.Transform(1, 0, 0, s, 0, 0)
	return r.p.Shade(pdfkit.Shading{X0: ox, Y0: oy / s, X1: cxx, Y1: cyy / s, Stops: stops,
		ExtendStart: true, ExtendEnd: true, Radial: true, R0: 0, R1: rx})
}
