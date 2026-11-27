// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdfkit/document"
	"github.com/go-richdoc/richdoc"
)

// A document that cannot be laid out is reported, exits 1, and leaves no PDF.
func TestLayoutFailureIsReportedAndWritesNothing(t *testing.T) {
	old := writeDocument
	writeDocument = func(*richdoc.Document, document.WriteOptions) ([]byte, error) {
		return nil, errors.New("no room")
	}
	defer func() { writeDocument = old }()

	in := write(t, "a.docx", docx(t))
	code, out, errOut := drive(t, in)
	if code != 1 || !strings.Contains(errOut, "no room") {
		t.Fatalf("code %d: %s%s", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(in), "a.pdf")); err == nil {
		t.Error("a PDF was written for a document that failed")
	}
}
