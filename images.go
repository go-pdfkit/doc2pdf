// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"path/filepath"
	"strings"

	"github.com/go-images/heif"
	"github.com/go-pdfkit/pdfkit"
)

// imageFormat is the picture format this reads. It is not a richdoc reader: a
// picture is a page, so it goes to pdfkit directly, one page for each file.
var imageFormat = format{Name: "HEIF image", Suffixes: []string{".heic", ".heif", ".hif"}, From: "go-images/heif"}

// maxPageSide is the longest side a PDF page may have, in points (PDF 1.6
// implementation limit, 200 inches, which viewers enforce).
const maxPageSide = 14400

// The bounds of one run on pictures, which are untrusted: each one is a page of the PDF
// held in memory, and decoding one costs up to eight bytes a pixel.
const (
	maxImages      = 256        // pictures in one PDF
	minDPI, maxDPI = 0.001, 1e6 // the resolutions a page size can be computed from
)

// Variables only so that a test can lower them to the size of its pictures.
var (
	maxImagePixels = int64(1 << 26) // pixels of one picture (67 million: 8192 x 8192)
	maxTotalPixels = int64(1 << 28)
)

// writeDocument serialises the document; a variable so a test can make it fail, which
// a bytes.Buffer never does.
var writeDocument = func(doc *pdfkit.Document, w io.Writer) error { return doc.Write(w) }

// isImage reports whether a file name has the suffix of a picture this reads.
func isImage(path string) bool {
	want := strings.ToLower(filepath.Ext(path))
	for _, s := range imageFormat.Suffixes {
		if s == want {
			return true
		}
	}
	return false
}

// runImages turns one or more HEIF files into one PDF, a page for each file.
//
// ⛔ THE PAGE IS THE PICTURE. Its size comes from the decoded image, which is the
// size AFTER rotation, mirroring and cropping, so a phone photo held upright is
// a portrait page whatever its sensor orientation was. The pixels are laid at
// --dpi, and a picture too large for a PDF page is scaled down to fit one rather
// than refused.
func runImages(out, errOut io.Writer, o options, args []string) int {
	inputs, dst := args, ""
	if n := len(args); n > 1 && strings.EqualFold(filepath.Ext(args[n-1]), ".pdf") {
		inputs, dst = args[:n-1], args[n-1]
	}
	for _, in := range inputs {
		if !isImage(in) {
			fmt.Fprintf(errOut, "doc2pdf: %s is not a %s; pictures and documents cannot be joined into one PDF\n",
				filepath.Base(in), imageFormat.Name)
			return 2
		}
	}
	if len(inputs) > maxImages {
		fmt.Fprintf(errOut, "doc2pdf: %d pictures, at most %d go into one PDF\n", len(inputs), maxImages)
		return 2
	}
	// NaN, the infinities, zero and the negatives fail the comparison; so do the
	// denormals, which divide a page size into an infinity.
	if !(o.dpi >= minDPI && o.dpi <= maxDPI) {
		fmt.Fprintf(errOut, "doc2pdf: --dpi must be between %g and %g\n", minDPI, maxDPI)
		return 2
	}
	if dst == "" {
		dst = outputName(inputs[0], inputs[:1])
	}
	if o.maxInputSet && (o.maxInput <= 0 || o.maxInput >= math.MaxInt64) {
		fmt.Fprintf(errOut, "doc2pdf: --max-input must be between 1 and %d bytes, not %d\n",
			int64(math.MaxInt64-1), o.maxInput)
		return 2
	}

	limit := imageFormat.MaxInput()
	if o.maxInput > 0 {
		limit = o.maxInput
	}
	lim := heif.Limits{MaxBytes: limit, MaxPixels: maxImagePixels}
	doc := pdfkit.New(pdfkit.Options{})
	var total int64 // pixels of the pictures read so far
	for _, in := range inputs {
		src, err := readBounded(in, imageFormat, o.maxInput)
		if err != nil {
			fmt.Fprintf(errOut, "doc2pdf: %v\n", err)
			return 1
		}
		// The size is read before the pixels are, so that a picture that would take the
		// PDF over its budget is refused without being decoded.
		cfg, err := heif.DecodeConfigOptions(bytes.NewReader(src), heif.Options{Limits: lim})
		if err != nil {
			fmt.Fprintf(errOut, "doc2pdf: %s could not read %s: %v\n", imageFormat.From, in, err)
			return 1
		}
		if total += int64(cfg.Width) * int64(cfg.Height); total > maxTotalPixels {
			fmt.Fprintf(errOut, "doc2pdf: the pictures hold more than %d pixels together; %s is over it\n", maxTotalPixels, in)
			return 1
		}
		// Each picture is drawn, which compresses it, and then let go: no more than one
		// decoded picture is alive at a time.
		res, err := heif.DecodeOptions(bytes.NewReader(src), heif.Options{Limits: lim})
		if err != nil {
			fmt.Fprintf(errOut, "doc2pdf: %s could not read %s: %v\n", imageFormat.From, in, err)
			return 1
		}
		img := res.Image
		b := img.Bounds()
		w, h := float64(b.Dx())*72/o.dpi, float64(b.Dy())*72/o.dpi
		if s := max(w, h); s > maxPageSide {
			w, h = w*maxPageSide/s, h*maxPageSide/s
		}
		doc.AddPage(pdfkit.NewPageSize(w, h)).DrawImage(img, pdfkit.Rect{Width: w, Height: h})
	}
	var pdf bytes.Buffer
	if err := writeDocument(doc, &pdf); err != nil {
		fmt.Fprintf(errOut, "doc2pdf: %v\n", err)
		return 1
	}
	if err := writeAtomic(dst, pdf.Bytes(), o.force); err != nil {
		if errors.Is(err, fs.ErrExist) {
			fmt.Fprintf(errOut, "doc2pdf: %s is already there; -f overwrites it\n", dst)
		} else {
			fmt.Fprintf(errOut, "doc2pdf: %v\n", err)
		}
		return 1
	}
	fmt.Fprintf(out, "%s → %s (%s, read by %s, %d pages, %d bytes)\n",
		strings.Join(inputs, " "), dst, imageFormat.Name, imageFormat.From, len(inputs), pdf.Len())
	return 0
}
