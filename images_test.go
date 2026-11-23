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

func TestPictureFailuresAreReported(t *testing.T) {
	good := heic(t, "small.heic")
	bad := write(t, "bad.heic", []byte("this is not a picture"))
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"not a HEIF":      {[]string{bad, filepath.Join(dir, "a.pdf")}, "go-images/heif could not read"},
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
