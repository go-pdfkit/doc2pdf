// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-odf/odf"
	"github.com/go-ooxml/ooxml"
	"github.com/go-ooxml/opc"
	"github.com/go-richdoc/data/csv"
	"github.com/go-richdoc/data/json"
	"github.com/go-richdoc/data/text"
	"github.com/go-richdoc/data/xml"
	"github.com/go-richdoc/email"
	"github.com/go-richdoc/epub"
	"github.com/go-richdoc/fb2"
	"github.com/go-richdoc/html"
	"github.com/go-richdoc/markdown"
	"github.com/go-richdoc/mobi"
	"github.com/go-richdoc/richdoc"
	"github.com/go-richdoc/rst"
)

// A format is one kind of file this can read, by the suffixes it is written
// with and the reader that reads it.
type format struct {
	// Name is what a person calls it.
	Name string
	// Suffixes are the file extensions, with their dots, lowercased.
	Suffixes []string
	// Read turns the bytes into a document.
	Read func([]byte) (*richdoc.Document, error)
	// From names the package that does the reading, so that a conversion says
	// which library interpreted the file rather than leaving it to be guessed.
	From string
}

// formats is every kind of file this reads.
//
// ⛔ THE TABLE IS THE POINT OF THIS COMMAND. Each of these readers had no way
// to reach a page: measured on 2026-11-05, nothing in go-pdfkit took a richdoc
// at all, and the two things that did — go-richdoc/rst/pdf and
// go-richdoc/latex/pdf — go through a six-megabyte TeX engine. Thirteen
// libraries were finished, tested, released and unusable from a shell, which
// is a different thing from unfinished and is easy to mistake for it.
//
// ⛔ And a library with no caller is a library nothing holds to its word. This
// command is the caller: every reader below is exercised by a test that
// converts a real file of that format, so a reader that stops reading is a
// test that stops passing rather than a defect somebody meets in a year.
var formats = []format{
	{"Markdown", []string{".md", ".markdown"}, markdown.Parse, "go-richdoc/markdown"},
	{"reStructuredText", []string{".rst"}, rst.Parse, "go-richdoc/rst"},
	{"HTML", []string{".html", ".htm", ".xhtml"}, func(b []byte) (*richdoc.Document, error) {
		return html.Parse(b, html.Limits{})
	}, "go-richdoc/html"},
	{"plain text", []string{".txt"}, func(b []byte) (*richdoc.Document, error) {
		return text.Parse(b, text.Options{})
	}, "go-richdoc/data/text"},
	{"CSV", []string{".csv", ".tsv"}, func(b []byte) (*richdoc.Document, error) {
		return csv.Parse(b, csv.Options{})
	}, "go-richdoc/data/csv"},
	{"JSON", []string{".json"}, func(b []byte) (*richdoc.Document, error) {
		return json.Parse(b, json.Options{})
	}, "go-richdoc/data/json"},
	{"XML", []string{".xml"}, func(b []byte) (*richdoc.Document, error) {
		return xml.Parse(b, xml.Options{})
	}, "go-richdoc/data/xml"},
	{"EPUB", []string{".epub"}, func(b []byte) (*richdoc.Document, error) {
		return epub.Parse(b, epub.Limits{})
	}, "go-richdoc/epub"},
	{"MOBI", []string{".mobi", ".azw", ".azw3"}, func(b []byte) (*richdoc.Document, error) {
		return mobi.Parse(b, mobi.Limits{})
	}, "go-richdoc/mobi"},
	{"FictionBook", []string{".fb2"}, func(b []byte) (*richdoc.Document, error) {
		return fb2.Parse(b, fb2.Limits{})
	}, "go-richdoc/fb2"},
	{"email", []string{".eml", ".mbox"}, func(b []byte) (*richdoc.Document, error) {
		return email.Parse(b, email.Limits{})
	}, "go-richdoc/email"},
	{"Word", []string{".docx"}, func(b []byte) (*richdoc.Document, error) {
		return ooxml.ParseWord(b, opc.Limits{})
	}, "go-ooxml/ooxml"},
	{"Excel", []string{".xlsx"}, func(b []byte) (*richdoc.Document, error) {
		return ooxml.ParseWorkbook(b, opc.Limits{})
	}, "go-ooxml/ooxml"},
	{"PowerPoint", []string{".pptx"}, func(b []byte) (*richdoc.Document, error) {
		return ooxml.ParsePresentation(b, opc.Limits{})
	}, "go-ooxml/ooxml"},
	{"OpenDocument Text", []string{".odt"}, odf.Parse, "go-odf/odf"},
}

// formatFor finds the reader for a file name, or says what it does read.
//
// ⛔ BY SUFFIX, AND IT SAYS SO. Sniffing the content would be cleverer and
// would guess wrong on the formats that share a container: .docx, .xlsx,
// .pptx, .odt and .epub are all ZIP files, and telling them apart means
// reading the package anyway. A suffix is what the person typed, it is what
// they can change, and when it is wrong the error says which suffixes are
// known rather than silently reading the file as something else.
func formatFor(path string) (format, error) {
	want := strings.ToLower(filepath.Ext(path))
	if want == "" {
		return format{}, fmt.Errorf("doc2pdf: %s has no suffix, so there is nothing to say what it is; %s",
			filepath.Base(path), knownSuffixes())
	}
	for _, f := range formats {
		for _, s := range f.Suffixes {
			if s == want {
				return f, nil
			}
		}
	}
	return format{}, fmt.Errorf("doc2pdf: nothing here reads %s; %s", want, knownSuffixes())
}

// knownSuffixes is what this reads, for an error message and for -list.
func knownSuffixes() string {
	var all []string
	for _, f := range formats {
		all = append(all, f.Suffixes...)
	}
	return "it reads " + strings.Join(all, " ")
}
