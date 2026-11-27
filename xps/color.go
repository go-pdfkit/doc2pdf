// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-pdfkit/pdfkit"
)

// finiteNum reads one number, refusing NaN, the infinities and anything so
// large that a later product would overflow: a document's coordinates are
// 1/96 inch, so a value beyond 1e9 is not a coordinate.
func finiteNum(s string) (float64, bool) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || math.Abs(v) > 1e9 {
		return 0, false
	}
	return v, true
}

// clamp01 limits v to [0, 1].
func clamp01(v float64) float64 {
	switch {
	case v < 0 || math.IsNaN(v):
		return 0
	case v > 1:
		return 1
	}
	return v
}

// linearToSRGB is the sRGB transfer function (IEC 61966-2-1), which is what an
// scRGB value (linear light, ECMA-388 §15.2.3) needs to become before it can be
// written as a device colour.
func linearToSRGB(v float64) float64 {
	v = clamp01(v)
	if v <= 0.0031308 {
		return 12.92 * v
	}
	return 1.055*math.Pow(v, 1/2.4) - 0.055
}

// parseColor reads an XPS colour (ECMA-388 §15.2): #RRGGBB, #AARRGGBB,
// sc#r,g,b, sc#a,r,g,b, and ContextColor <profile> a,c0,...,cn. It returns the
// colour and its alpha.
//
// ICC profiles are not applied: a ContextColor of one channel is device grey,
// of three device RGB, of four device CMYK, and any other count is refused.
func parseColor(s string) (pdfkit.Color, float64, error) {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "#"):
		h := s[1:]
		if len(h) != 6 && len(h) != 8 {
			return nil, 0, fmt.Errorf("%w: %q", ErrBadColor, s)
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %q", ErrBadColor, s)
		}
		a := 1.0
		if len(h) == 8 {
			a = float64(v>>24&0xFF) / 255
		}
		return pdfkit.RGB{R: float64(v>>16&0xFF) / 255, G: float64(v>>8&0xFF) / 255, B: float64(v&0xFF) / 255}, a, nil
	case strings.HasPrefix(s, "sc#"):
		nums, ok := parseNums(s[3:], 5)
		if !ok || (len(nums) != 3 && len(nums) != 4) {
			return nil, 0, fmt.Errorf("%w: %q", ErrBadColor, s)
		}
		a := 1.0
		if len(nums) == 4 {
			a, nums = clamp01(nums[0]), nums[1:]
		}
		return pdfkit.RGB{R: linearToSRGB(nums[0]), G: linearToSRGB(nums[1]), B: linearToSRGB(nums[2])}, a, nil
	case strings.HasPrefix(s, "ContextColor"):
		f := strings.Fields(s)
		if len(f) < 3 {
			return nil, 0, fmt.Errorf("%w: %q", ErrBadColor, s)
		}
		nums, ok := parseNums(strings.Join(f[2:], " "), 8)
		if !ok || len(nums) < 2 {
			return nil, 0, fmt.Errorf("%w: %q", ErrBadColor, s)
		}
		a, ch := clamp01(nums[0]), nums[1:]
		for i := range ch {
			ch[i] = clamp01(ch[i])
		}
		switch len(ch) {
		case 1:
			return pdfkit.Gray{V: ch[0]}, a, nil
		case 3:
			return pdfkit.RGB{R: ch[0], G: ch[1], B: ch[2]}, a, nil
		case 4:
			return pdfkit.CMYK{C: ch[0], M: ch[1], Y: ch[2], K: ch[3]}, a, nil
		}
	}
	return nil, 0, fmt.Errorf("%w: %q", ErrBadColor, s)
}

// asRGB converts any colour this package makes to RGB, for a gradient, whose
// stops must share one colour space.
func asRGB(c pdfkit.Color) pdfkit.RGB {
	switch v := c.(type) {
	case pdfkit.RGB:
		return v
	case pdfkit.Gray:
		return pdfkit.RGB{R: v.V, G: v.V, B: v.V}
	case pdfkit.CMYK:
		return pdfkit.RGB{R: (1 - v.C) * (1 - v.K), G: (1 - v.M) * (1 - v.K), B: (1 - v.Y) * (1 - v.K)}
	}
	return pdfkit.RGB{}
}
