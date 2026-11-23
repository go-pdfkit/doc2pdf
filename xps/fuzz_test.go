// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"errors"
	"image/color"
	"testing"
)

// FuzzConvert feeds whole packages. The invariant is the one a converter of
// untrusted files owes: it returns a PDF or an error, never panics, and never
// outruns the (small) limits given here.
func FuzzConvert(f *testing.F) {
	seeds := []*pkg{
		newPkg(page(`<Path Data="M 0,0 L 50,0 50,50 Z" Fill="#FFFF0000" Stroke="#FF0000FF" StrokeThickness="2" StrokeDashArray="1 2"/>`)),
		newPkg(page(`<Canvas RenderTransform="2,0,0,2,1,1" Opacity="0.5"><Path Data="M 0,0 A 5,5 0 1 1 9,9 Z" Fill="sc#1,0.5,0"/></Canvas>`)),
		newPkg(page(`<Path Data="M 0,0 L 99,0 99,99 Z"><Path.Fill><LinearGradientBrush StartPoint="0,0" EndPoint="1,1"><LinearGradientBrush.GradientStops><GradientStop Color="#FF000000" Offset="0"/><GradientStop Color="#FFFFFFFF" Offset="1"/></LinearGradientBrush.GradientStops></LinearGradientBrush></Path.Fill></Path>`)),
		newPkg(page(`<Glyphs OriginX="5" OriginY="50" FontRenderingEmSize="20" FontUri="` + fontPart + `" UnicodeString="Fuzz" Indices="(1:1)43;;" StyleSimulations="BoldItalicSimulation" Fill="#FF000000"/>`)).withFont(),
	}
	img := newPkg(page(`<Path Data="M 0,0 L 99,0 99,99 Z"><Path.Fill><ImageBrush ImageSource="/i.png" TileMode="FlipXY" Viewbox="0,0,4,4" ViewboxUnits="Absolute" Viewport="0,0,30,30" ViewportUnits="Absolute"/></Path.Fill></Path>`))
	img.extra["i.png"] = pngOf(f, 4, 4, color.RGBA{1, 2, 3, 255})
	seeds = append(seeds, img)
	for _, s := range seeds {
		f.Add(s.bytes(f))
	}
	lim := Limits{
		MaxPackageBytes: 1 << 20, MaxPartBytes: 1 << 20, MaxUncompressedBytes: 4 << 20,
		MaxParts: 64, MaxPages: 8, MaxElementsPerPart: 2000, MaxWork: 1 << 18,
		MaxImagePixels: 1 << 20, MaxTotalImagePixels: 4 << 20, MaxTiles: 256, MaxFonts: 4,
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		res, err := Convert(b, lim)
		if err == nil && (res == nil || len(res.PDF) == 0) {
			t.Fatal("no error and no PDF")
		}
		if err != nil && res != nil {
			t.Fatalf("an error and a result: %v", err)
		}
		_ = errors.Is(err, ErrLimit)
	})
}
