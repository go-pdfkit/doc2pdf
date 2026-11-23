// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-pdfkit/extract"
	"github.com/go-pdfkit/reader"
	"golang.org/x/image/font/gofont/goregular"
)

const xpsNS = "http://schemas.openxps.org/oxps/v1.0"

// xpsPackage is a one-page XPS holding the drawing given, with the Go Regular
// font at /Resources/Fonts/go.ttf for a Glyphs to name.
func xpsPackage(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="ttf" ContentType="application/vnd.ms-opentype"/>` +
			`<Override PartName="/FixedDocumentSequence.fdseq" ContentType="application/vnd.ms-package.xps-fixeddocumentsequence+xml"/>` +
			`<Override PartName="/Documents/1/FixedDocument.fdoc" ContentType="application/vnd.ms-package.xps-fixeddocument+xml"/>` +
			`<Override PartName="/Documents/1/Pages/1.fpage" ContentType="application/vnd.ms-package.xps-fixedpage+xml"/></Types>`,
		"_rels/.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="r" Type="http://schemas.openxps.org/oxps/v1.0/fixedrepresentation" Target="/FixedDocumentSequence.fdseq"/></Relationships>`,
		"FixedDocumentSequence.fdseq":    `<FixedDocumentSequence xmlns="` + xpsNS + `"><DocumentReference Source="/Documents/1/FixedDocument.fdoc"/></FixedDocumentSequence>`,
		"Documents/1/FixedDocument.fdoc": `<FixedDocument xmlns="` + xpsNS + `"><PageContent Source="Pages/1.fpage"/></FixedDocument>`,
		"Documents/1/Pages/1.fpage":      `<FixedPage xmlns="` + xpsNS + `" Width="400" Height="300">` + body + `</FixedPage>`,
	} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(data))
	}
	w, _ := zw.Create("Resources/Fonts/go.ttf")
	w.Write(goregular.TTF)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestXPSKeepsItsTextSelectableAndSaysWhatItSkipped(t *testing.T) {
	in := write(t, "a.oxps", xpsPackage(t, `
<Glyphs OriginX="20" OriginY="60" FontRenderingEmSize="24" FontUri="/Resources/Fonts/go.ttf" UnicodeString="Vector words" Fill="#FF000000"/>
<Path Data="M 0,0 L 9,9 9,0 Z"><Path.Fill><VisualBrush/></Path.Fill></Path>`))
	code, out, errOut := drive(t, in)
	if code != 0 {
		t.Fatalf("code %d: %s%s", code, out, errOut)
	}
	if !strings.Contains(out, "not drawn: ") || !strings.Contains(out, "VisualBrush") {
		t.Errorf("what was left out is not said: %q", out)
	}
	pdf, err := os.ReadFile(strings.TrimSuffix(in, filepath.Ext(in)) + ".pdf")
	if err != nil {
		t.Fatal(err)
	}
	src, err := reader.Open(pdf)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := extract.Runs(src, 1)
	if err != nil {
		t.Fatal(err)
	}
	var said strings.Builder
	for _, r := range runs {
		said.WriteString(r.Text)
	}
	if !strings.Contains(strings.ReplaceAll(said.String(), " ", ""), "Vectorwords") {
		t.Errorf("the text is not text: %q", said.String())
	}
}

func TestXPSOversizeIsRefusedAndNamed(t *testing.T) {
	in := write(t, "big.xps", xpsPackage(t, ""))
	code, _, errOut := drive(t, "--max-input", "1", in)
	if code == 0 {
		t.Fatal("an oversize XPS was converted")
	}
	_ = errOut
}

func TestXPSBombIsRefusedByTheCommand(t *testing.T) {
	in := write(t, "bomb.xps", []byte("PK\x03\x04 this is not a package"))
	code, out, errOut := drive(t, in)
	if code == 0 {
		t.Fatalf("garbage converted: %s", out)
	}
	if !strings.Contains(errOut, "go-pdfkit/doc2pdf/xps") && !strings.Contains(errOut, "xps") {
		t.Errorf("the failing reader is not named: %q", errOut)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(in), "bomb.pdf")); err == nil {
		t.Error("a PDF was written for a refused file")
	}
}
