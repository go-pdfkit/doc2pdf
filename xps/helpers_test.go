// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"

	"golang.org/x/image/font/gofont/goregular"
)

const (
	nsOXPS      = "http://schemas.openxps.org/oxps/v1.0"
	relOXPS     = "http://schemas.openxps.org/oxps/v1.0/fixedrepresentation"
	relXPS      = "http://schemas.microsoft.com/xps/2005/06/fixedrepresentation"
	ctRels      = "application/vnd.openxmlformats-package.relationships+xml"
	ctDocument  = "application/vnd.ms-package.xps-fixeddocument+xml"
	ctPage      = "application/vnd.ms-package.xps-fixedpage+xml"
	fontPart    = "/Resources/Fonts/go.ttf"
	fontPartRel = "../../Resources/Fonts/go.ttf"
)

// A pkg is an XPS package under construction.
type pkg struct {
	rel    string            // the start-part relationship type
	pages  []string          // FixedPage XML, one per page
	extra  map[string][]byte // other parts, by name without a leading slash
	ctypes map[string]string // extra overrides, by part name with a slash
	seq    string            // the FixedDocumentSequence, when not the default
	doc    string            // the FixedDocument, when not the default
	rels   string            // the package relationships, when not the default
	noSeq  bool              // leave the sequence without its content type
}

func newPkg(pages ...string) *pkg {
	return &pkg{rel: relOXPS, pages: pages, extra: map[string][]byte{}, ctypes: map[string]string{}}
}

// page wraps drawing in a 400 x 300 FixedPage.
func page(body string) string { return pageSized(400, 300, body) }

func pageSized(w, h int, body string) string {
	return fmt.Sprintf(`<FixedPage xmlns="%s" xml:lang="en-US" Width="%d" Height="%d">%s</FixedPage>`, nsOXPS, w, h, body)
}

// bytes is the package as a ZIP.
func (p *pkg) bytes(t testing.TB) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, data []byte) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	var types strings.Builder
	types.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	for ext, ct := range map[string]string{
		"rels": ctRels, "ttf": "application/vnd.ms-opentype", "odttf": ctObfuscatedFont,
		"png": "image/png", "jpg": "image/jpeg", "tif": "image/tiff", "jxr": "image/vnd.ms-photo",
	} {
		fmt.Fprintf(&types, `<Default Extension="%s" ContentType="%s"/>`, ext, ct)
	}
	if !p.noSeq {
		fmt.Fprintf(&types, `<Override PartName="/FixedDocumentSequence.fdseq" ContentType="%s"/>`, ctSequence)
	}
	fmt.Fprintf(&types, `<Override PartName="/Documents/1/FixedDocument.fdoc" ContentType="%s"/>`, ctDocument)
	for i := range p.pages {
		fmt.Fprintf(&types, `<Override PartName="/Documents/1/Pages/%d.fpage" ContentType="%s"/>`, i+1, ctPage)
	}
	for name, ct := range p.ctypes {
		fmt.Fprintf(&types, `<Override PartName="%s" ContentType="%s"/>`, name, ct)
	}
	types.WriteString(`</Types>`)
	add("[Content_Types].xml", []byte(types.String()))
	rels := fmt.Sprintf(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r1" Type="%s" Target="/FixedDocumentSequence.fdseq"/></Relationships>`, p.rel)
	if p.rels != "" {
		rels = p.rels
	}
	add("_rels/.rels", []byte(rels))
	seq := fmt.Sprintf(`<FixedDocumentSequence xmlns="%s"><DocumentReference Source="/Documents/1/FixedDocument.fdoc"/></FixedDocumentSequence>`, nsOXPS)
	if p.seq != "" {
		seq = p.seq
	}
	add("FixedDocumentSequence.fdseq", []byte(seq))
	var doc strings.Builder
	fmt.Fprintf(&doc, `<FixedDocument xmlns="%s">`, nsOXPS)
	for i := range p.pages {
		fmt.Fprintf(&doc, `<PageContent Source="Pages/%d.fpage"/>`, i+1)
	}
	doc.WriteString(`</FixedDocument>`)
	if p.doc != "" {
		add("Documents/1/FixedDocument.fdoc", []byte(p.doc))
	} else {
		add("Documents/1/FixedDocument.fdoc", []byte(doc.String()))
	}
	for i, pg := range p.pages {
		add(fmt.Sprintf("Documents/1/Pages/%d.fpage", i+1), []byte(pg))
	}
	for name, data := range p.extra {
		add(name, data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// withFont adds the Go Regular font at fontPart.
func (p *pkg) withFont() *pkg {
	p.extra[strings.TrimPrefix(fontPart, "/")] = goregular.TTF
	return p
}

// convert converts with the output left uncompressed, so a test can read the
// operators.
func convertRaw(t testing.TB, b []byte, lim Limits) (string, *Result) {
	t.Helper()
	old := compressOutput
	compressOutput = false
	defer func() { compressOutput = old }()
	res, err := Convert(b, lim)
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return string(res.PDF), res
}

// pngOf is a w x h PNG filled with one colour.
func pngOf(t testing.TB, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jpegOf is a w x h JPEG.
func jpegOf(t testing.TB, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 9), uint8(y * 9), 128, 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// pngHeader is a PNG that declares w x h pixels and holds no pixels: enough
// for DecodeConfig, which is the point of a bomb that must be refused from its
// header.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		binary.Write(&b, binary.BigEndian, uint32(len(data)))
		b.WriteString(typ)
		b.Write(data)
		binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(typ), data...)))
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr, w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	return b.Bytes()
}
