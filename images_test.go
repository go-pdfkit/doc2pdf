// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdfkit/pdfkit"
	"github.com/go-pdfkit/reader"
)

// testdata/small.heic is 67x45 pixels; testdata/rotated.heic is the same picture
// turned a quarter, which Apple's encoder stores with an irot property, so the
// page it gets is 45x67 pixels. Both were made with sips from the author's own
// picture.
const (
	smallW, smallH = 67, 45
)

func heic(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return write(t, name, b)
}

// pageSizes reads the PDF and returns the MediaBox size of each page.
func pageSizes(t *testing.T, path string) [][2]float64 {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, err := reader.Open(b)
	if err != nil {
		t.Fatal(err)
	}
	var sizes [][2]float64
	for i := 1; i <= doc.PageCount(); i++ { // reader pages are numbered from 1
		page, err := doc.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		box, ok := page.Get("MediaBox").(reader.Array)
		if !ok || len(box) != 4 {
			t.Fatalf("page %d has no MediaBox: %v", i, page.Get("MediaBox"))
		}
		num := func(o reader.Object) float64 {
			switch v := o.(type) {
			case reader.Integer:
				return float64(v)
			case reader.Real:
				return float64(v)
			}
			t.Fatalf("MediaBox entry %v is not a number", o)
			return 0
		}
		sizes = append(sizes, [2]float64{num(box[2]) - num(box[0]), num(box[3]) - num(box[1])})
	}
	return sizes
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.01 }

func TestAHEICBecomesAPageTheSizeOfThePicture(t *testing.T) {
	in := heic(t, "small.heic")
	code, out, errOut := drive(t, in)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	dst := strings.TrimSuffix(in, ".heic") + ".pdf"
	if !strings.Contains(out, "go-images/heif") || !strings.Contains(out, "1 pages") {
		t.Errorf("it did not say what read it: %q", out)
	}
	sizes := pageSizes(t, dst)
	if len(sizes) != 1 || !near(sizes[0][0], smallW*72/150.0) || !near(sizes[0][1], smallH*72/150.0) {
		t.Errorf("page sizes %v, want one of %.2fx%.2f", sizes, smallW*72/150.0, smallH*72/150.0)
	}
}

// ⛔ The page is the picture AFTER its rotation: a quarter turn swaps the sides.
func TestThePageSizeIsTakenAfterRotation(t *testing.T) {
	in := heic(t, "rotated.heic")
	if code, _, errOut := drive(t, "--dpi", "72", in); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	sizes := pageSizes(t, strings.TrimSuffix(in, ".heic")+".pdf")
	if len(sizes) != 1 || !near(sizes[0][0], smallH) || !near(sizes[0][1], smallW) {
		t.Errorf("page sizes %v, want one of %dx%d", sizes, smallH, smallW)
	}
}

func TestSeveralPicturesMakeOnePDFAPageEach(t *testing.T) {
	a, b := heic(t, "small.heic"), heic(t, "rotated.heic")
	dst := filepath.Join(t.TempDir(), "both.pdf")
	if code, _, errOut := drive(t, "--dpi", "72", a, b, a, dst); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	sizes := pageSizes(t, dst)
	want := [][2]float64{{smallW, smallH}, {smallH, smallW}, {smallW, smallH}}
	if len(sizes) != len(want) {
		t.Fatalf("%d pages, want %d", len(sizes), len(want))
	}
	for i := range want {
		if !near(sizes[i][0], want[i][0]) || !near(sizes[i][1], want[i][1]) {
			t.Errorf("page %d is %v, want %v", i, sizes[i], want[i])
		}
	}
}

// Without a last argument ending in .pdf, the output is named after the first.
func TestSeveralPicturesWithoutAnOutputNameUseTheFirst(t *testing.T) {
	a := heic(t, "small.heic")
	b := filepath.Join(filepath.Dir(a), "second.heif")
	if err := os.Link(a, b); err != nil {
		data, _ := os.ReadFile(a)
		if err := os.WriteFile(b, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, errOut := drive(t, a, b); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if n := len(pageSizes(t, strings.TrimSuffix(a, ".heic")+".pdf")); n != 2 {
		t.Errorf("%d pages, want 2", n)
	}
}

// A picture too large for a PDF page is scaled to fit one, not refused.
func TestAPictureTooLargeForAPageIsScaledDown(t *testing.T) {
	in := heic(t, "small.heic")
	if code, _, errOut := drive(t, "--dpi", "0.001", in); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	sizes := pageSizes(t, strings.TrimSuffix(in, ".heic")+".pdf")
	if len(sizes) != 1 || !near(sizes[0][0], maxPageSide) || !near(sizes[0][1], maxPageSide*float64(smallH)/float64(smallW)) {
		t.Errorf("page sizes %v", sizes)
	}
}

func TestPictureUsageErrors(t *testing.T) {
	in := heic(t, "small.heic")
	md := write(t, "a.md", []byte("# x\n"))
	for name, args := range map[string][]string{
		"dpi zero":        {"--dpi", "0", in},
		"dpi negative":    {"--dpi=-3", in},
		"documents mixed": {in, md},
		"max-input zero":  {"--max-input", "0", in},
	} {
		if code, _, errOut := drive(t, args...); code != 2 || errOut == "" {
			t.Errorf("%s: code %d, %q", name, code, errOut)
		}
	}
	_, _, errOut := drive(t, in, md)
	if !strings.Contains(errOut, "cannot be joined") {
		t.Errorf("mixing did not say why: %q", errOut)
	}
}

// --dpi is a size, so it is refused when it is not a number a page can be computed
// from: NaN and the infinities, the denormals, and what is below or above the bounds.
func TestDPIMustBeUsable(t *testing.T) {
	in := heic(t, "small.heic")
	for _, dpi := range []string{"NaN", "Inf", "-Inf", "1e-310", "5e-324", "0.0009", "1000001", "1e300", "-0"} {
		if code, _, errOut := drive(t, "--dpi="+dpi, in); code != 2 || !strings.Contains(errOut, "--dpi must be between") {
			t.Errorf("--dpi=%s: code %d, %q", dpi, code, errOut)
		}
	}
	for _, dpi := range []string{"0.001", "1000000"} { // exactly at the bounds
		if code, _, errOut := drive(t, "-f", "--dpi="+dpi, in); code != 0 {
			t.Errorf("--dpi=%s: code %d, %q", dpi, code, errOut)
		}
	}
}

// --max-input at the largest value would make the read's one byte more wrap: it is
// refused for pictures as it is for the other formats, and one below it is read.
func TestMaxInputOfAPictureIsBounded(t *testing.T) {
	in := heic(t, "small.heic")
	for _, v := range []string{"9223372036854775807", "0", "-1"} {
		if code, _, errOut := drive(t, "--max-input="+v, in); code != 2 || !strings.Contains(errOut, "--max-input") {
			t.Errorf("--max-input=%s: code %d, %q", v, code, errOut)
		}
	}
	if code, _, errOut := drive(t, "-f", "--max-input=9223372036854775806", in); code != 0 {
		t.Errorf("--max-input one below the largest: code %d, %q", code, errOut)
	}
}

// One run reads a bounded number of pictures, of a bounded size, and a bounded number of
// pixels together; each of them refused before the next is decoded.
func TestPicturesAreBounded(t *testing.T) {
	in := heic(t, "small.heic")
	dst := filepath.Join(t.TempDir(), "all.pdf")
	many := func(n int) []string {
		args := []string{"-f"}
		for range n {
			args = append(args, in)
		}
		return append(args, dst)
	}
	if code, _, errOut := drive(t, many(maxImages+1)...); code != 2 || !strings.Contains(errOut, "at most 256") {
		t.Errorf("too many pictures: code %d, %q", code, errOut)
	}
	if code, _, errOut := drive(t, many(maxImages)...); code != 0 {
		t.Errorf("exactly %d pictures: code %d, %q", maxImages, code, errOut)
	}

	// a run is charged what is decoded: the coded size, padded to 68 x 46
	const px = 68 * 46
	defer func(a, b int64) { maxImagePixels, maxTotalPixels = a, b }(maxImagePixels, maxTotalPixels)
	maxTotalPixels = 2 * px
	if code, _, errOut := drive(t, many(2)...); code != 0 {
		t.Errorf("pictures at the total: code %d, %q", code, errOut)
	}
	if code, _, errOut := drive(t, many(3)...); code != 1 || !strings.Contains(errOut, "pixels together") {
		t.Errorf("pictures over the total: code %d, %q", code, errOut)
	}
	maxTotalPixels = 1 << 28
	// the decoder counts the pixels of the coded picture: padded to 68x46, and to whole
	// 64x64 coding tree blocks in the bitstream (160x64)
	const coded = 160 * 64
	maxImagePixels = coded
	if code, _, errOut := drive(t, "-f", in, dst); code != 0 {
		t.Errorf("a picture at the bound: code %d, %q", code, errOut)
	}
	maxImagePixels = coded - 1
	if code, _, errOut := drive(t, "-f", in, dst); code != 1 || !strings.Contains(errOut, "could not read") {
		t.Errorf("a picture over the bound: code %d, %q", code, errOut)
	}
}

// A grid of large tiles cropped to a sliver draws as a few pixels and decodes every tile:
// the run is charged what is decoded, so a few such files cannot be had for free.
func TestSliverGridIsChargedItsTiles(t *testing.T) {
	// nine tiles of 4096 x 4096 under an output one pixel high: 151 million decoded pixels,
	// more than the budget of a run, for 36865 pixels of output
	in := write(t, "sliver.heic", mustRead(t, filepath.Join("testdata", "sliver-grid.heic")))
	dst := filepath.Join(t.TempDir(), "s.pdf")
	if code, _, errOut := drive(t, "-f", in, dst); code != 1 || !strings.Contains(errOut, "pixels together") {
		t.Errorf("a sliver of nine large tiles: code %d, %q", code, errOut)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("a refused run left a PDF")
	}
}

func mustRead(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPictureFailuresAreReported(t *testing.T) {
	good := heic(t, "small.heic")
	bad := write(t, "bad.heic", []byte("this is not a picture"))
	// the boxes that describe the picture are whole and its coded data is cut: the size
	// can be read, the pixels cannot
	whole, err := os.ReadFile(filepath.Join("testdata", "small.heic"))
	if err != nil {
		t.Fatal(err)
	}
	cut := write(t, "cut.heic", whole[:len(whole)-200])
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"not a HEIF":      {[]string{bad, filepath.Join(dir, "a.pdf")}, "go-images/heif could not read"},
		"cut picture":     {[]string{cut, filepath.Join(dir, "e.pdf")}, "go-images/heif could not read"},
		"missing file":    {[]string{filepath.Join(dir, "none.heic"), filepath.Join(dir, "b.pdf")}, "no such file"},
		"over max-input":  {[]string{"--max-input", "10", good, filepath.Join(dir, "c.pdf")}, "-max-input raises it"},
		"no output place": {[]string{good, filepath.Join(dir, "no", "such", "d.pdf")}, "doc2pdf:"},
	} {
		func() {
			defer func() { // a panic is a failure of this case, not of the test binary
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v", name, r)
				}
			}()
			code, _, errOut := drive(t, tc.args...)
			if code != 1 || !strings.Contains(errOut, tc.want) {
				t.Errorf("%s: code %d, %q", name, code, errOut)
			}
		}()
	}
}

func TestADocumentThatCannotBeWrittenIsReported(t *testing.T) {
	old := writeDocument
	defer func() { writeDocument = old }()
	writeDocument = func(*pdfkit.Document, io.Writer) error { return errors.New("disk of glass") }
	dst := filepath.Join(t.TempDir(), "w.pdf")
	code, _, errOut := drive(t, heic(t, "small.heic"), dst)
	if code != 1 || !strings.Contains(errOut, "disk of glass") {
		t.Errorf("code %d, %q", code, errOut)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Error("a failed write left an output behind")
	}
}

func TestAnExistingPictureOutputIsRefusedUnlessForced(t *testing.T) {
	in := heic(t, "small.heic")
	dst := filepath.Join(t.TempDir(), "x.pdf")
	if code, _, errOut := drive(t, in, dst); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if code, _, errOut := drive(t, in, dst); code != 1 || !strings.Contains(errOut, "already there") {
		t.Errorf("second run: code %d, %q", code, errOut)
	}
	if code, _, errOut := drive(t, "-f", in, dst); code != 0 {
		t.Errorf("forced run: code %d, %q", code, errOut)
	}
}

func TestListNamesThePictureFormat(t *testing.T) {
	_, out, _ := drive(t, "--list")
	if !strings.Contains(out, imageFormat.Name) || !strings.Contains(out, ".heic") || !strings.Contains(out, imageFormat.From) {
		t.Errorf("the listing leaves out pictures:\n%s", out)
	}
	_, _, errOut := drive(t, write(t, "a.doc", []byte("x")))
	if !strings.Contains(errOut, ".heic") {
		t.Errorf("the suffixes it reads leave out .heic: %q", errOut)
	}
}

func TestIsImageIgnoresCase(t *testing.T) {
	for name, want := range map[string]bool{"a.HEIC": true, "a.Heif": true, "a.hif": true, "a.png": false, "heic": false} {
		if isImage(name) != want {
			t.Errorf("isImage(%q) = %v", name, !want)
		}
	}
}
