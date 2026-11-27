// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The oracle is MuPDF: it draws the XPS itself, and it draws our PDF, both at
// 96 dpi. The test is skipped where mutool is not installed.

func mutoolDraw(t *testing.T, in, out string) image.Image {
	t.Helper()
	cmd := exec.Command("mutool", "draw", "-q", "-r", "96", "-F", "png", "-o", out, in)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("mutool draw %s: %v\n%s", in, err, b)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// diff is the mean absolute per-channel difference (0-255) and the share of
// pixels whose largest channel difference exceeds 48.
func diff(a, b image.Image) (mean, share float64) {
	r := a.Bounds()
	var sum, n, bad float64
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ca := color.NRGBAModel.Convert(a.At(x, y)).(color.NRGBA)
			cb := color.NRGBAModel.Convert(b.At(x, y)).(color.NRGBA)
			worst := 0
			for _, d := range []int{int(ca.R) - int(cb.R), int(ca.G) - int(cb.G), int(ca.B) - int(cb.B)} {
				if d < 0 {
					d = -d
				}
				sum += float64(d)
				worst = max(worst, d)
			}
			n += 3
			if worst > 48 {
				bad++
			}
		}
	}
	return sum / n, 100 * bad / (n / 3)
}

// ink counts the pixels that are not white.
func ink(a image.Image) int {
	n := 0
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := color.NRGBAModel.Convert(a.At(x, y)).(color.NRGBA)
			if c.R < 250 || c.G < 250 || c.B < 250 {
				n++
			}
		}
	}
	return n
}

func TestRasterAgainstMuPDF(t *testing.T) {
	if _, err := exec.LookPath("mutool"); err != nil {
		t.Skip("mutool is not installed")
	}
	pages := map[string]*pkg{
		"shapes": newPkg(page(`
<Path Data="F 1 M 20,20 L 200,20 200,120 20,120 Z" Fill="#FFE04040" Stroke="#FF202080" StrokeThickness="6" StrokeLineJoin="Round"/>
<Path Data="M 240,40 A 60,40 0 1 1 340,200 Z" Fill="#8000A000"/>
<Canvas RenderTransform="1,0.2,0,1,10,120"><Path Data="M 0,0 L 120,0 120,60 0,60 Z" Fill="#FF3070D0"/></Canvas>`)),
		"gradient": newPkg(page(`<Path Data="M 20,20 L 380,20 380,280 20,280 Z"><Path.Fill>
<LinearGradientBrush StartPoint="0,0" EndPoint="1,1"><LinearGradientBrush.GradientStops>
<GradientStop Color="#FFFF0000" Offset="0"/><GradientStop Color="#FF00FF00" Offset="0.5"/><GradientStop Color="#FF0000FF" Offset="1"/>
</LinearGradientBrush.GradientStops></LinearGradientBrush></Path.Fill></Path>`)),
		"text": newPkg(page(`<Glyphs OriginX="30" OriginY="80" FontRenderingEmSize="32" FontUri="` + fontPart + `" UnicodeString="Vector text 123" Fill="#FF000000"/>
<Glyphs OriginX="30" OriginY="140" FontRenderingEmSize="20" FontUri="` + fontPart + `" UnicodeString="Second line, smaller" Fill="#FF0030A0"/>`)).withFont(),
	}
	for name, p := range pages {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, name+".xps")
			b := p.bytes(t)
			if err := os.WriteFile(src, b, 0o600); err != nil {
				t.Fatal(err)
			}
			res, err := Convert(b, Limits{})
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(dir, name+".pdf")
			if err := os.WriteFile(out, res.PDF, 0o600); err != nil {
				t.Fatal(err)
			}
			want := mutoolDraw(t, src, filepath.Join(dir, "want.png"))
			got := mutoolDraw(t, out, filepath.Join(dir, "got.png"))
			if want.Bounds() != got.Bounds() {
				t.Fatalf("sizes differ: mutool draws the XPS at %v, our PDF at %v", want.Bounds(), got.Bounds())
			}
			// The control: a match of two blank pages proves nothing.
			if iw, ig := ink(want), ink(got); iw < 500 || ig < 500 {
				t.Fatalf("inked pixels: MuPDF's rendering of the XPS %d, of our PDF %d", iw, ig)
			}
			blank := image.NewNRGBA(want.Bounds())
			for i := range blank.Pix {
				blank.Pix[i] = 255
			}
			if m, _ := diff(want, blank); m < 1 {
				t.Fatalf("the measure cannot tell the page from a blank one (%.2f)", m)
			}
			mean, share := diff(want, got)
			t.Logf("%s: mean channel difference %.2f/255, %.2f%% of pixels differ by more than 48", name, mean, share)
			if mean > 8 || share > 6 {
				t.Errorf("%s drifts from MuPDF's own rendering: mean %.2f, %.2f%% differ", name, mean, share)
			}
		})
	}
}
