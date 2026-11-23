// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"errors"
	"strings"
	"testing"
)

const relsHead = `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`

func relsOf(rs ...string) string { return relsHead + strings.Join(rs, "") + `</Relationships>` }

func rel(id, typ, target string) string {
	return `<Relationship Id="` + id + `" Type="` + typ + `" Target="` + target + `"/>`
}

// A page reference that is empty, or cannot be decoded, names no part even
// when a part is called by the name the empty or broken reference would give.
func TestPageReferencesThatNameNoPart(t *testing.T) {
	for _, src := range []string{"", "%zz", "../../../../nothing", "missing.fpage"} {
		p := newPkg(page(""))
		p.doc = `<FixedDocument xmlns="` + nsOXPS + `"><PageContent Source="` + src + `"/></FixedDocument>`
		p.extra["Documents/1"] = []byte("a part named like the directory")
		p.ctypes["/Documents/1"] = "text/plain"
		res, err := Convert(p.bytes(t), Limits{})
		if !errors.Is(err, ErrNoPages) {
			t.Errorf("%q: %v %v", src, err, res)
		}
	}
	// The same document with a good reference is the control.
	p := newPkg(page(""))
	p.extra["Documents/1"] = []byte("x")
	p.ctypes["/Documents/1"] = "text/plain"
	if _, err := Convert(p.bytes(t), Limits{}); err != nil {
		t.Errorf("control: %v", err)
	}
}

// A document reference that names no part is reported, and the others are read.
func TestDocumentReferenceThatNamesNoPart(t *testing.T) {
	p := newPkg(page(""))
	p.seq = `<FixedDocumentSequence xmlns="` + nsOXPS + `"><DocumentReference Source=""/><DocumentReference Source="%zz"/><DocumentReference Source="/Documents/1/FixedDocument.fdoc"/></FixedDocumentSequence>`
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 1 || !skipped(res, "document reference that names no part") {
		t.Errorf("%v %v", err, res)
	}
}

func TestBrokenPackageRelationships(t *testing.T) {
	p := newPkg(page(""))
	p.rels = `<Relationships`
	if _, err := Convert(p.bytes(t), Limits{}); err == nil {
		t.Error("a broken _rels/.rels was accepted")
	}
}

// The start part is the one the relationship names, not the first sequence in
// the package, and a relationship of another type is not the start part.
func TestStartPartIsTheRelatedOne(t *testing.T) {
	decoy := `<FixedDocumentSequence xmlns="` + nsOXPS + `"/>`
	p := newPkg(page(""))
	p.extra["A.fdseq"] = []byte(decoy)
	p.ctypes["/A.fdseq"] = ctSequence
	res, err := Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 1 {
		t.Fatalf("by relationship: %v %v", err, res)
	}
	// A relationship of another type, first, to the decoy.
	p.rels = relsOf(
		rel("r0", "http://schemas.openxmlformats.org/package/2006/relationships/metadata/thumbnail", "/A.fdseq"),
		rel("r1", relOXPS, "/FixedDocumentSequence.fdseq"))
	res, err = Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 1 {
		t.Errorf("a relationship of another type: %v %v", err, res)
	}
	// An external one, first.
	p.rels = relsOf(
		`<Relationship Id="r0" Type="`+relOXPS+`" Target="http://example.com/A.fdseq" TargetMode="External"/>`,
		rel("r1", relOXPS, "/FixedDocumentSequence.fdseq"))
	res, err = Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 1 {
		t.Errorf("an external relationship: %v %v", err, res)
	}
	// A start relationship that points at nothing falls back to the part that
	// is declared a sequence.
	p.rels = relsOf(rel("r1", relOXPS, "/gone.fdseq"))
	res, err = Convert(p.bytes(t), Limits{})
	if err != nil || res.Pages != 1 {
		t.Errorf("a dangling relationship: %v %v", err, res)
	}
}

func TestNoStartPart(t *testing.T) {
	p := newPkg(page(""))
	p.rels = relsOf()
	p.noSeq = true
	if _, err := Convert(p.bytes(t), Limits{}); !errors.Is(err, ErrNoDocument) {
		t.Errorf("%v", err)
	}
}

func TestMalformedSequenceAndDocument(t *testing.T) {
	p := newPkg(page(""))
	p.seq = `<FixedDocumentSequence`
	if _, err := Convert(p.bytes(t), Limits{}); !errors.Is(err, ErrBadXML) {
		t.Errorf("sequence: %v", err)
	}
	p = newPkg(page(""))
	p.doc = `<FixedDocument`
	if _, err := Convert(p.bytes(t), Limits{}); !errors.Is(err, ErrBadXML) {
		t.Errorf("document: %v", err)
	}
}

// A start part that is itself a FixedDocument is read as one, and its pages
// are bounded.
func TestFixedDocumentAsStartPart(t *testing.T) {
	p := newPkg(page(""), page(""), page(""))
	p.seq = `<FixedDocument xmlns="` + nsOXPS + `"><PageContent Source="Documents/1/Pages/1.fpage"/><PageContent Source="Documents/1/Pages/2.fpage"/><PageContent Source="Documents/1/Pages/3.fpage"/></FixedDocument>`
	b := p.bytes(t)
	res, err := Convert(b, Limits{})
	if err != nil || res.Pages != 3 {
		t.Fatalf("%v %v", err, res)
	}
	refuses(t, "pages", b, Limits{MaxPages: 2}, ErrLimit)
}

// The documents are counted before any is read: three empty ones are over a
// limit of two as documents, not as the "no pages" of an empty package.
func TestDocumentsAreCountedBeforeTheirPages(t *testing.T) {
	p := newPkg(page(""))
	p.doc = `<FixedDocument xmlns="` + nsOXPS + `"/>`
	p.seq = `<FixedDocumentSequence xmlns="` + nsOXPS + `">` + strings.Repeat(`<DocumentReference Source="/Documents/1/FixedDocument.fdoc"/>`, 3) + `</FixedDocumentSequence>`
	b := p.bytes(t)
	refuses(t, "documents", b, Limits{MaxPages: 2}, ErrLimit)
	if _, err := Convert(b, Limits{MaxPages: 3}); !errors.Is(err, ErrNoPages) {
		t.Errorf("three empty documents under 3: %v", err)
	}
}
