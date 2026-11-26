package xps

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-pdfkit/pdfkit"
	"golang.org/x/image/font/gofont/goregular"
)

// Sixth review: the shading charge undercharged high-stop shadings 2.7 times, the
// tree charge undercharged few-attribute elements 2.3 times, and WOFF fonts are
// not XPS font parts.

// ---- WOFF ----

// ECMA-388 §9.1.7 names the OpenType format (TrueType and CFF outlines) as the
// font part; a WOFF or WOFF2 is refused by its signature, however much it would
// inflate to, before the parser sees it.
func TestSecWOFFIsRefused(t *testing.T) {
	woff2 := append([]byte("wOF2"), make([]byte, 60)...)
	for name, font := range map[string][]byte{
		"woff1":          woff1Of(t, 1<<10),
		"woff1 100 MiB":  woff1Of(t, 100<<20),
		"woff2 (header)": woff2,
		"woff1 (4 byte)": []byte("wOFF"),
	} {
		b := fontOnlyPackage(t, font)
		var err error
		var res *Result
		heap := peakHeap(t, func() { res, err = Convert(b, Limits{}) })
		if err != nil || res == nil || !strings.Contains(strings.Join(res.Skipped, ";"), "WOFF font (not an XPS font part, ECMA-388 §9.1.7) (1)") {
			t.Errorf("%s: %v %v, want the run skipped with the WOFF note", name, err, res)
		}
		if heap > 64 {
			t.Errorf("%s: peak heap %d MiB for a refused font", name, heap)
		}
		// The loader itself names the refusal.
		c := newConv(t, b, Limits{})
		_, ferr := c.font("/Resources/Fonts/w.ttf", "/Resources/Fonts/w.ttf")
		if !errors.Is(ferr, ErrBadFont) || !errors.Is(ferr, errWOFF) || !strings.Contains(ferr.Error(), "§9.1.7") {
			t.Errorf("%s: font(): %v, want ErrBadFont and errWOFF naming §9.1.7", name, ferr)
		}
	}
}

// A plain OpenType font still converts, and one that merely starts like a WOFF
// signature further in is not refused.
func TestSecOpenTypeStillConverts(t *testing.T) {
	if isWOFF([]byte("wOF")) || isWOFF(nil) || !isWOFF([]byte("wOF2")) || !isWOFF([]byte("wOFF")) {
		t.Error("isWOFF signature test")
	}
	if _, err := Convert(fontOnlyPackage(t, goregular.TTF), Limits{}); err != nil {
		t.Errorf("Go Regular: %v", err)
	}
}

// ---- shadings ----

// What a shading is charged covers what pdfkit retains for it: the heap that
// n shadings of s stops hold once the garbage is collected, per shading, is under
// the charge at every stop count (the written PDF, about 100 B a stop, is the
// part of the charge Convert reserves its buffer from).
func TestSecShadingCostIsMeasured(t *testing.T) {
	for _, c := range []struct{ stops, uses int }{{2, 4000}, {16, 1600}, {64, 600}, {256, 200}, {1024, 50}} {
		st := make([]pdfkit.Stop, c.stops)
		for i := range st {
			st[i] = pdfkit.Stop{Offset: float64(i+1) / float64(c.stops+1), Color: pdfkit.RGB{R: float64(i%7) / 7}}
		}
		runtime.GC()
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		doc := pdfkit.New(pdfkit.Options{})
		p := doc.AddPage(pdfkit.NewPageSize(100, 100))
		for i := 0; i < c.uses; i++ {
			if err := p.Shade(pdfkit.Shading{X1: 1, Stops: st, ExtendStart: true}); err != nil {
				t.Fatal(err)
			}
		}
		runtime.GC()
		runtime.ReadMemStats(&b)
		per := (int64(b.HeapAlloc) - int64(a.HeapAlloc)) / int64(c.uses)
		charge := int64(shadeBytes + shadeStopBytes*c.stops)
		t.Logf("%d stops: pdfkit retains %d B a shading, charged %d B (x%.2f)", c.stops, per, charge, float64(charge)/float64(per))
		if per > charge {
			t.Errorf("%d stops: pdfkit holds %d B a shading, over the %d B charged", c.stops, per, charge)
		}
		runtime.KeepAlive(doc)
	}
}

// The reviewer's probe: many fills of many stops, at the default limits, stay
// under the heap gate and are refused rather than drawn.
func TestSecShadingManyStops(t *testing.T) {
	for _, stops := range []int{256, 512, 1024} {
		b := newPkg(gradientPage(stops, 4000, shadeUse)).bytes(t)
		var err error
		t0 := time.Now()
		heap := peakHeap(t, func() { _, err = Convert(b, Limits{}) })
		t.Logf("fill %d stops x4000: %v, peak heap %d MiB, err %v", stops, time.Since(t0).Round(time.Millisecond), heap, err)
		if heap > worstPeakMiB {
			t.Errorf("%d stops: peak heap %d MiB over the %d MiB gate", stops, heap, worstPeakMiB)
		}
		if !errors.Is(err, ErrLimit) {
			t.Errorf("%d stops: %v, want ErrLimit", stops, err)
		}
	}
}

// ---- the XML tree ----

// An element is charged at least what the tree holds for it, with 0, 1, 8, 9 and
// 64 attributes (a map's first bucket holds eight).
func TestSecXMLTreeCostIsMeasured(t *testing.T) {
	for _, attrs := range []int{0, 1, 2, 8, 9, 64} {
		var el strings.Builder
		el.WriteString("<Canvas")
		for i := 0; i < attrs; i++ {
			fmt.Fprintf(&el, ` a%d="x"`, i)
		}
		el.WriteString("/>")
		const n = 20000
		doc := []byte("<FixedPage>" + strings.Repeat(el.String(), n) + "</FixedPage>")
		runtime.GC()
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		root, err := parseXML(doc, 100, 1<<20, 1<<24)
		runtime.GC()
		runtime.ReadMemStats(&b)
		if err != nil {
			t.Fatal(err)
		}
		per := (int64(b.HeapAlloc) - int64(a.HeapAlloc)) / n
		charge := elementCost(attrs) + int64(len("Canvas")) + int64(attrs*len("a0")+attrs*len("x"))
		t.Logf("%d attributes: retained %d B an element, charged %d B", attrs, per, charge)
		if per > charge {
			t.Errorf("%d attributes: the tree holds %d B an element, over the %d B charged", attrs, per, charge)
		}
		runtime.KeepAlive(root)
	}
}

// The reviewer's probe: a part of 240,000 elements with 0, 1 or 2 attributes stays
// under the heap gate.
func TestSecXMLFewAttrsPeak(t *testing.T) {
	for _, attrs := range []int{0, 1, 2} {
		var el strings.Builder
		el.WriteString("<Canvas")
		for i := 0; i < attrs; i++ {
			fmt.Fprintf(&el, ` a%d="x"`, i)
		}
		el.WriteString("/>")
		const n = 240000
		pg := page(strings.Repeat(el.String(), n))
		b := zipOf(t, append(xpsParts(pg), zpart{name: "pad.bin", head: noise(1<<20, 5)}))
		var err error
		heap := peakHeap(t, func() { _, err = Convert(b, Limits{}) })
		t.Logf("%d attrs x %d elements: peak heap %d MiB, err %v", attrs, n, heap, err)
		if heap > worstPeakMiB {
			t.Errorf("%d attrs: peak heap %d MiB over the %d MiB gate", attrs, heap, worstPeakMiB)
		}
	}
}

// ---- gradient edges ----

// Gradient stops that are not drawn as given: a stop pushed past 1 by the strict
// ordering is dropped, a brush of one stop becomes two, an element that is not a
// stop is skipped, and a brush Transform is applied. A resource part that is not
// a dictionary is skipped and a remote one is counted.
func TestSecGradientEdges(t *testing.T) {
	body := `<Canvas.Resources><Other/><ResourceDictionary Source="remote.xaml"` + gradNS + `>` +
		`<LinearGradientBrush x:Key="two" Transform="2,0,0,2,0,0"><LinearGradientBrush.GradientStops><Marker/>` +
		`<GradientStop Color="#FFFF0000" Offset="1"/><GradientStop Color="#FF0000FF" Offset="1"/>` +
		`</LinearGradientBrush.GradientStops></LinearGradientBrush>` +
		`<LinearGradientBrush x:Key="one"><LinearGradientBrush.GradientStops>` +
		`<GradientStop Color="#FF00FF00" Offset="0.5"/></LinearGradientBrush.GradientStops></LinearGradientBrush>` +
		`</ResourceDictionary></Canvas.Resources>` +
		`<Path Data="M 0,0 L 5,0 5,5 Z" Fill="{StaticResource two}"/>` +
		`<Path Data="M 0,0 L 5,0 5,5 Z" Fill="{StaticResource one}"/>`
	res, err := Convert(newPkg(page(`<Canvas>`+body+`</Canvas>`)).bytes(t), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(res.Skipped, ";"), "remote resource dictionary") {
		t.Errorf("skipped %v, want the remote dictionary counted", res.Skipped)
	}
	if !strings.Contains(string(res.PDF), "/ShadingType 2") {
		t.Error("no shading was drawn")
	}
}
