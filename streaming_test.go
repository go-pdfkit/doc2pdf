// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"image"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-pdfkit/pdfkit"
)

// The document is written to the file as it is produced: serialising pictures that do
// not compress costs a few kilobytes above the document, not a second copy of it.
func TestTheDocumentIsStreamedNotBuffered(t *testing.T) {
	const side = 1024
	img := image.NewRGBA(image.Rect(0, 0, side, side))
	rng := rand.New(rand.NewPCG(1, 2))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.Uint32())
	}
	doc := pdfkit.New(pdfkit.Options{})
	doc.AddPage(pdfkit.NewPageSize(100, 100)).DrawImage(img, pdfkit.Rect{Width: 100, Height: 100})
	stored := doc.AddImage(img).StoredLen() // the same picture: shared, not stored twice
	if stored < side*side*3/2 {
		t.Fatalf("the picture was meant to be incompressible, stored %d bytes", stored)
	}

	dst := filepath.Join(t.TempDir(), "s.pdf")
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	err := writeAtomicWith(dst, func(f *os.File) error { return writePictures(doc, f) }, false)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("stored %d bytes, Write allocated %d", stored, after.TotalAlloc-before.TotalAlloc)
	if got := int64(after.TotalAlloc - before.TotalAlloc); got > 1<<20 {
		t.Errorf("writing a %d-byte picture allocated %d bytes: the document is buffered", stored, got)
	}
}

// With no hard links the file is written a second time, by the same fill.
func TestAStreamedDocumentWithoutHardLinks(t *testing.T) {
	old := linkFile
	defer func() { linkFile = old }()
	linkFile = func(string, string) error { return os.ErrPermission }
	dst := filepath.Join(t.TempDir(), "n.pdf")
	if code, _, errOut := drive(t, heic(t, "small.heic"), dst); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if b, err := os.ReadFile(dst); err != nil || len(b) < 100 {
		t.Errorf("output %d bytes, %v", len(b), err)
	}
}
