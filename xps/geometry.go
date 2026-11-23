// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"fmt"
	"math"
	"strings"

	"github.com/go-pdfkit/pdfkit"
)

// A seg is one drawing step: 'M' and 'L' use v[0:2], 'C' uses v[0:6], 'Z' none.
type seg struct {
	op byte
	v  [6]float64
}

// A geometry is a path: figures made of lines and cubic curves, and the rule
// that says which side of them is inside. XPS's quadratic and arc segments are
// converted to cubics when read, so nothing downstream knows they existed.
type geometry struct {
	segs    []seg
	evenOdd bool
}

// emit writes the path to the page, with no painting operator.
func (g *geometry) emit(p *pdfkit.Page) {
	for _, s := range g.segs {
		switch s.op {
		case 'M':
			p.MoveTo(s.v[0], s.v[1])
		case 'L':
			p.LineTo(s.v[0], s.v[1])
		case 'C':
			p.CurveTo(s.v[0], s.v[1], s.v[2], s.v[3], s.v[4], s.v[5])
		case 'Z':
			p.ClosePath()
		}
	}
}

// clip makes the path the clip, honouring the fill rule.
func (g *geometry) clip(p *pdfkit.Page) {
	g.emit(p)
	if g.evenOdd {
		p.ClipEvenOdd()
	} else {
		p.Clip()
	}
	p.EndPath()
}

// bounds is the box of every point, control points included, which is a
// superset of the path's own.
func (g *geometry) bounds() (x0, y0, x1, y1 float64, ok bool) {
	x0, y0 = math.Inf(1), math.Inf(1)
	x1, y1 = math.Inf(-1), math.Inf(-1)
	for _, s := range g.segs {
		n := 0
		switch s.op {
		case 'M', 'L':
			n = 2
		case 'C':
			n = 6
		}
		for i := 0; i < n; i += 2 {
			x0, x1 = math.Min(x0, s.v[i]), math.Max(x1, s.v[i])
			y0, y1 = math.Min(y0, s.v[i+1]), math.Max(y1, s.v[i+1])
			ok = true
		}
	}
	return
}

// apply maps every point through the matrix.
func (g *geometry) apply(m [6]float64) {
	for i := range g.segs {
		s := &g.segs[i]
		n := 0
		switch s.op {
		case 'M', 'L':
			n = 2
		case 'C':
			n = 6
		}
		for j := 0; j < n; j += 2 {
			x, y := s.v[j], s.v[j+1]
			s.v[j] = m[0]*x + m[2]*y + m[4]
			s.v[j+1] = m[1]*x + m[3]*y + m[5]
		}
	}
}

// A scanner reads the numbers and command letters of abbreviated geometry,
// which separates with spaces and commas and whose numbers may touch a letter.
type scanner struct {
	s   string
	pos int
}

func (sc *scanner) skip() {
	for sc.pos < len(sc.s) {
		switch sc.s[sc.pos] {
		case ' ', '\t', '\r', '\n', ',':
			sc.pos++
		default:
			return
		}
	}
}

func (sc *scanner) eof() bool { sc.skip(); return sc.pos >= len(sc.s) }

// letter returns the next command letter, or 0 when a number is next.
func (sc *scanner) letter() byte {
	sc.skip()
	if sc.pos < len(sc.s) {
		c := sc.s[sc.pos]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' {
			if c == 'e' || c == 'E' {
				return 0
			}
			return c
		}
	}
	return 0
}

// num reads a decimal number: sign, digits, fraction, exponent. It does not
// read "inf" or "nan", which strconv would, and it refuses what finiteNum does.
func (sc *scanner) num() (float64, bool) {
	sc.skip()
	i, start := sc.pos, sc.pos
	s := sc.s
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	digits := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
		digits++
	}
	if i < len(s) && s[i] == '.' {
		i++
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
			digits++
		}
	}
	if digits == 0 {
		return 0, false
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		if j < len(s) && s[j] >= '0' && s[j] <= '9' {
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			i = j
		}
	}
	v, ok := finiteNum(s[start:i])
	if !ok {
		return 0, false
	}
	sc.pos = i
	return v, true
}

// parseNums reads a list of numbers separated by spaces and commas, at most
// max of them.
func parseNums(s string, max int) ([]float64, bool) {
	sc := scanner{s: s}
	var out []float64
	for !sc.eof() {
		if len(out) >= max {
			return nil, false
		}
		v, ok := sc.num()
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// parseMatrix reads M11,M12,M21,M22,OffsetX,OffsetY (ECMA-388 §14.4.1), which
// is the order PDF's cm operator takes.
func parseMatrix(s string) ([6]float64, bool) {
	n, ok := parseNums(s, 6)
	var m [6]float64
	if !ok || len(n) != 6 {
		return m, false
	}
	copy(m[:], n)
	return m, true
}

var identity = [6]float64{1, 0, 0, 1, 0, 0}

// A builder accumulates a geometry under a segment ceiling, and a shared
// budget so that one geometry referenced a hundred thousand times costs what
// it costs a hundred thousand times.
type builder struct {
	g          *geometry
	max        int
	spend      func(int) error
	err        error
	cur, start [2]float64
	started    bool
}

func (b *builder) add(s seg) {
	if b.err != nil {
		return
	}
	if len(b.g.segs) >= b.max {
		b.err = limit("segments in one path", b.max)
		return
	}
	if b.spend != nil {
		if err := b.spend(1); err != nil {
			b.err = err
			return
		}
	}
	b.g.segs = append(b.g.segs, s)
}

func (b *builder) move(x, y float64) {
	b.add(seg{op: 'M', v: [6]float64{x, y}})
	b.cur, b.start, b.started = [2]float64{x, y}, [2]float64{x, y}, true
}

func (b *builder) line(x, y float64) {
	b.add(seg{op: 'L', v: [6]float64{x, y}})
	b.cur = [2]float64{x, y}
}

func (b *builder) cubic(x1, y1, x2, y2, x, y float64) {
	b.add(seg{op: 'C', v: [6]float64{x1, y1, x2, y2, x, y}})
	b.cur = [2]float64{x, y}
}

func (b *builder) quad(qx, qy, x, y float64) {
	px, py := b.cur[0], b.cur[1]
	b.cubic(px+2.0/3*(qx-px), py+2.0/3*(qy-py), x+2.0/3*(qx-x), y+2.0/3*(qy-y), x, y)
}

func (b *builder) closeFigure() {
	b.add(seg{op: 'Z'})
	b.cur = b.start
}

// arc draws an elliptical arc from the current point (ECMA-388 §11.2.2.2, and
// the endpoint-to-centre conversion of the SVG implementation notes F.6.5,
// which describes the same arc with the same sweep sense: y down, clockwise
// is sweep 1). Radii too small to reach the end are scaled up, as §11.2.2.2.1
// requires; a zero radius is a line.
func (b *builder) arc(rx, ry, rotDeg float64, large, sweep bool, x2, y2 float64) {
	x1, y1 := b.cur[0], b.cur[1]
	if x1 == x2 && y1 == y2 {
		return
	}
	rx, ry = math.Abs(rx), math.Abs(ry)
	if rx == 0 || ry == 0 {
		b.line(x2, y2)
		return
	}
	phi := rotDeg * math.Pi / 180
	cos, sin := math.Cos(phi), math.Sin(phi)
	dx, dy := (x1-x2)/2, (y1-y2)/2
	x1p, y1p := cos*dx+sin*dy, -sin*dx+cos*dy
	if l := x1p*x1p/(rx*rx) + y1p*y1p/(ry*ry); l > 1 {
		s := math.Sqrt(l)
		rx, ry = rx*s, ry*s
	}
	num := rx*rx*ry*ry - rx*rx*y1p*y1p - ry*ry*x1p*x1p
	den := rx*rx*y1p*y1p + ry*ry*x1p*x1p
	coef := 0.0
	if den != 0 {
		coef = math.Sqrt(math.Max(0, num/den))
	}
	if large == sweep {
		coef = -coef
	}
	cxp, cyp := coef*rx*y1p/ry, -coef*ry*x1p/rx
	cx := cos*cxp - sin*cyp + (x1+x2)/2
	cy := sin*cxp + cos*cyp + (y1+y2)/2
	t1 := math.Atan2((y1p-cyp)/ry, (x1p-cxp)/rx)
	t2 := math.Atan2((-y1p-cyp)/ry, (-x1p-cxp)/rx)
	d := t2 - t1
	if sweep && d < 0 {
		d += 2 * math.Pi
	} else if !sweep && d > 0 {
		d -= 2 * math.Pi
	}
	n := int(math.Ceil(math.Abs(d) / (math.Pi / 2)))
	if n < 1 {
		n = 1
	}
	delta := d / float64(n)
	k := 4.0 / 3 * math.Tan(delta/4)
	pt := func(a float64) (float64, float64) {
		return cx + rx*math.Cos(a)*cos - ry*math.Sin(a)*sin, cy + rx*math.Cos(a)*sin + ry*math.Sin(a)*cos
	}
	dv := func(a float64) (float64, float64) {
		return -rx*math.Sin(a)*cos - ry*math.Cos(a)*sin, -rx*math.Sin(a)*sin + ry*math.Cos(a)*cos
	}
	type pt6 [6]float64
	curves := make([]pt6, 0, n)
	ok := true
	for i := 0; i < n; i++ {
		a1 := t1 + float64(i)*delta
		a2 := a1 + delta
		p1x, p1y := pt(a1)
		p2x, p2y := pt(a2)
		d1x, d1y := dv(a1)
		d2x, d2y := dv(a2)
		c := pt6{p1x + k*d1x, p1y + k*d1y, p2x - k*d2x, p2y - k*d2y, p2x, p2y}
		if i == n-1 {
			c[4], c[5] = x2, y2
		}
		for _, v := range c {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				ok = false
			}
		}
		curves = append(curves, c)
	}
	if !ok {
		b.line(x2, y2)
		return
	}
	for _, c := range curves {
		b.cubic(c[0], c[1], c[2], c[3], c[4], c[5])
	}
}

// parseAbbrev reads the abbreviated geometry syntax of ECMA-388 §11.2.3: an
// optional leading F0 or F1, then M, L, H, V, C, Q, S, A and Z in upper case
// (absolute) or lower case (relative to the current point, which after Z is
// the figure's first point). A command's letter may be left out when it
// repeats, and after M it becomes L. Anything else is refused.
func parseAbbrev(s string, b *builder) error {
	sc := scanner{s: s}
	var cmd byte
	prevCubic := false
	var ctrl [2]float64
	first := true
	for !sc.eof() {
		if c := sc.letter(); c != 0 {
			sc.pos++
			cmd = c
			if cmd == 'F' {
				if !first {
					return fmt.Errorf("%w: F is only allowed first", ErrBadGeometry)
				}
				v, ok := sc.num()
				if !ok || (v != 0 && v != 1) {
					return fmt.Errorf("%w: F takes 0 or 1", ErrBadGeometry)
				}
				b.g.evenOdd = v == 0
				first = false
				continue
			}
		} else {
			switch cmd {
			case 0, 'Z', 'z':
				return fmt.Errorf("%w: a number with no command", ErrBadGeometry)
			case 'M':
				cmd = 'L'
			case 'm':
				cmd = 'l'
			}
		}
		first = false
		rel := cmd >= 'a'
		cx, cy := b.cur[0], b.cur[1]
		if !rel {
			cx, cy = 0, 0
		}
		read := func(n int) ([]float64, bool) {
			out := make([]float64, n)
			for i := range out {
				v, ok := sc.num()
				if !ok {
					return nil, false
				}
				out[i] = v
			}
			return out, true
		}
		wasCubic := false
		switch cmd {
		case 'M', 'm', 'L', 'l':
			v, ok := read(2)
			if !ok {
				return fmt.Errorf("%w: %c needs x,y", ErrBadGeometry, cmd)
			}
			if cmd == 'M' || cmd == 'm' {
				b.move(cx+v[0], cy+v[1])
			} else if !b.started {
				return fmt.Errorf("%w: a figure must start with M", ErrBadGeometry)
			} else {
				b.line(cx+v[0], cy+v[1])
			}
		case 'H', 'h':
			v, ok := read(1)
			if !ok || !b.started {
				return fmt.Errorf("%w: bad H", ErrBadGeometry)
			}
			if rel {
				b.line(b.cur[0]+v[0], b.cur[1])
			} else {
				b.line(v[0], b.cur[1])
			}
		case 'V', 'v':
			v, ok := read(1)
			if !ok || !b.started {
				return fmt.Errorf("%w: bad V", ErrBadGeometry)
			}
			if rel {
				b.line(b.cur[0], b.cur[1]+v[0])
			} else {
				b.line(b.cur[0], v[0])
			}
		case 'C', 'c':
			v, ok := read(6)
			if !ok || !b.started {
				return fmt.Errorf("%w: bad C", ErrBadGeometry)
			}
			b.cubic(cx+v[0], cy+v[1], cx+v[2], cy+v[3], cx+v[4], cy+v[5])
			ctrl, wasCubic = [2]float64{cx + v[2], cy + v[3]}, true
		case 'S', 's':
			v, ok := read(4)
			if !ok || !b.started {
				return fmt.Errorf("%w: bad S", ErrBadGeometry)
			}
			c1x, c1y := b.cur[0], b.cur[1]
			if prevCubic {
				c1x, c1y = 2*b.cur[0]-ctrl[0], 2*b.cur[1]-ctrl[1]
			}
			b.cubic(c1x, c1y, cx+v[0], cy+v[1], cx+v[2], cy+v[3])
			ctrl, wasCubic = [2]float64{cx + v[0], cy + v[1]}, true
		case 'Q', 'q':
			v, ok := read(4)
			if !ok || !b.started {
				return fmt.Errorf("%w: bad Q", ErrBadGeometry)
			}
			b.quad(cx+v[0], cy+v[1], cx+v[2], cy+v[3])
		case 'A', 'a':
			v, ok := read(7)
			if !ok || !b.started {
				return fmt.Errorf("%w: bad A", ErrBadGeometry)
			}
			b.arc(v[0], v[1], v[2], v[3] != 0, v[4] != 0, cx+v[5], cy+v[6])
		case 'Z', 'z':
			if !b.started {
				return fmt.Errorf("%w: Z before M", ErrBadGeometry)
			}
			b.closeFigure()
		default:
			return fmt.Errorf("%w: unknown command %q", ErrBadGeometry, cmd)
		}
		prevCubic = wasCubic
		if b.err != nil {
			return b.err
		}
	}
	return b.err
}

// geometryFromNode reads a PathGeometry element (ECMA-388 §11.2.1.1).
func (r *renderer) geometryFromNode(n *node, sc *scope) (*geometry, error) {
	b := &builder{g: &geometry{evenOdd: true}, max: r.c.lim.MaxSegmentsPerPath, spend: r.c.work}
	if n.has("FillRule") {
		b.g.evenOdd = !strings.EqualFold(strings.TrimSpace(n.attr("FillRule")), "NonZero")
	}
	if f := n.attr("Figures"); f != "" {
		fb := &builder{g: b.g, max: b.max, spend: b.spend}
		// The Figures string has no fill-rule prefix of its own worth honouring
		// over the attribute, but F is accepted if present.
		if err := parseAbbrev(f, fb); err != nil {
			return nil, err
		}
		b.cur, b.start, b.started = fb.cur, fb.start, fb.started
	}
	for _, fig := range n.kids {
		if fig.name != "PathFigure" {
			continue
		}
		if err := r.figure(fig, b); err != nil {
			return nil, err
		}
	}
	if m, ok := r.matrix(n, "Transform", sc); ok {
		b.g.apply(m)
	}
	return b.g, b.err
}

// figure reads one PathFigure and its segments.
func (r *renderer) figure(fig *node, b *builder) error {
	pt, ok := parseNums(fig.attr("StartPoint"), 2)
	if !ok || len(pt) != 2 {
		return fmt.Errorf("%w: PathFigure StartPoint", ErrBadGeometry)
	}
	b.move(pt[0], pt[1])
	for _, s := range fig.kids {
		switch s.name {
		case "PolyLineSegment", "PolyBezierSegment", "PolyQuadraticBezierSegment":
			maxN := 2 * b.max
			pts, ok := parseNums(s.attr("Points"), maxN)
			if !ok || len(pts)%2 != 0 {
				return fmt.Errorf("%w: %s Points", ErrBadGeometry, s.name)
			}
			step := map[string]int{"PolyLineSegment": 2, "PolyBezierSegment": 6, "PolyQuadraticBezierSegment": 4}[s.name]
			if len(pts)%step != 0 {
				return fmt.Errorf("%w: %s Points", ErrBadGeometry, s.name)
			}
			for i := 0; i < len(pts); i += step {
				switch step {
				case 2:
					b.line(pts[i], pts[i+1])
				case 6:
					b.cubic(pts[i], pts[i+1], pts[i+2], pts[i+3], pts[i+4], pts[i+5])
				default:
					b.quad(pts[i], pts[i+1], pts[i+2], pts[i+3])
				}
				if b.err != nil {
					return b.err
				}
			}
		case "ArcSegment":
			p, ok1 := parseNums(s.attr("Point"), 2)
			sz, ok2 := parseNums(s.attr("Size"), 2)
			rot := 0.0
			if s.has("RotationAngle") {
				v, ok := finiteNum(s.attr("RotationAngle"))
				if !ok {
					return fmt.Errorf("%w: ArcSegment RotationAngle", ErrBadGeometry)
				}
				rot = v
			}
			if !ok1 || !ok2 || len(p) != 2 || len(sz) != 2 {
				return fmt.Errorf("%w: ArcSegment", ErrBadGeometry)
			}
			large := strings.EqualFold(s.attr("IsLargeArc"), "true")
			sweep := strings.EqualFold(s.attr("SweepDirection"), "Clockwise")
			b.arc(sz[0], sz[1], rot, large, sweep, p[0], p[1])
		}
		if b.err != nil {
			return b.err
		}
	}
	if strings.EqualFold(fig.attr("IsClosed"), "true") {
		b.closeFigure()
	}
	return b.err
}
