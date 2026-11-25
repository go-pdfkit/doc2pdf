// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

// Package xps turns an XPS or OpenXPS document into a PDF, as vector
// graphics and real text.
//
// An XPS file is an OPC package (a ZIP) holding a FixedDocumentSequence, which
// names FixedDocuments, which name FixedPages (ECMA-388 §9). A FixedPage is
// already a drawing: Canvas, Path and Glyphs elements in a coordinate space of
// 1/96 inch with y pointing down. That is why this package does not go through
// the richdoc model the other doc2pdf readers use: richdoc is a flow of
// blocks, and a page whose every glyph has a position its producer chose
// cannot be turned into one without losing the thing that makes it an XPS file.
// The elements are drawn straight onto go-pdfkit pages instead, scaled by
// 72/96 with the y axis flipped, so that paths stay paths and a run of text
// stays text — embedded, subsetted and selectable.
//
// # What is drawn
//
// Canvas (RenderTransform, Clip, Opacity, Resources), Path (abbreviated and
// element geometry, SolidColorBrush, ImageBrush with JPEG, PNG and TIFF,
// LinearGradientBrush and RadialGradientBrush with Pad spreading, stroke
// thickness, dashes, caps and joins), Glyphs (FontUri with obfuscated
// fonts, Indices clusters, advances and offsets, UnicodeString, BidiLevel,
// StyleSimulations), colours in #RRGGBB, #AARRGGBB, sc# and ContextColor
// form, and {StaticResource} references resolved from the innermost
// dictionary outwards.
//
// # What is not, and how it is said
//
// VisualBrush, OpacityMask, IsSideways, JPEG XR images, TrueType collections,
// remote resource dictionaries, hyperlinks, the document outline and
// SpreadMethod Reflect or Repeat are not drawn. Nothing is dropped silently:
// each is counted, and [Result.Skipped] says what and how often, so a page
// with a hole in it is reported rather than discovered.
//
// # Untrusted input
//
// An XPS file is a ZIP full of XML whose author chose every number in it.
// Every ceiling is in [Limits] and every refusal is an error that
// satisfies errors.Is(err, [ErrLimit]) and names what was exceeded: package
// size, entry count, bytes inflated (absolutely and as a ratio of the package),
// XML depth, elements and tokens, pages, page size, segments per path, glyphs
// per run, fonts, image pixels and tiles. Part names that climb out of the
// package are not parts, and nothing is fetched: an external relationship or
// a URI with a scheme is refused, never followed.
package xps

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/go-ooxml/opc"
	"github.com/go-pdfkit/pdfkit"
)

// Errors. Those that mean "this file asks for more than allowed" also satisfy
// errors.Is(err, ErrLimit).
var (
	// ErrLimit is what every ceiling refusal matches.
	ErrLimit = errors.New("xps: limit exceeded")
	// ErrNoDocument is a package that names no fixed document sequence.
	ErrNoDocument = errors.New("xps: the package names no fixed document sequence")
	// ErrNoPages is a document sequence that leads to no page.
	ErrNoPages = errors.New("xps: the document has no pages")
	// ErrBadXML is a part that is not well-formed XML.
	ErrBadXML = errors.New("xps: malformed XML")
	// ErrBadPage is a FixedPage that cannot be drawn at all (no usable size).
	ErrBadPage = errors.New("xps: unusable FixedPage")
	// ErrBadPartName is a URI that does not name a part of this package.
	ErrBadPartName = errors.New("xps: not a part of this package")
	// ErrBadGeometry is abbreviated geometry that does not parse.
	ErrBadGeometry = errors.New("xps: malformed geometry")
	// ErrBadColor is a colour that does not parse.
	ErrBadColor = errors.New("xps: malformed colour")
	// ErrBadGlyphs is a Glyphs element whose Indices do not parse.
	ErrBadGlyphs = errors.New("xps: malformed glyph run")
	// ErrBadFont is a font part that cannot be read.
	ErrBadFont = errors.New("xps: unreadable font")
	// ErrBadImage is an image part that cannot be read.
	ErrBadImage = errors.New("xps: unreadable image")
)

// A limitError is a refusal by a ceiling.
type limitError struct {
	what  string
	limit int
}

func (e *limitError) Error() string {
	return fmt.Sprintf("xps: %s exceeds the limit of %d", e.what, e.limit)
}

// Is makes every limit refusal match ErrLimit.
func (e *limitError) Is(target error) bool { return target == ErrLimit }

func limit(what string, n int) error { return &limitError{what: what, limit: n} }

// Limits bound what a file may ask for. The zero value means the defaults,
// which are generous for real documents and small for hostile ones.
type Limits struct {
	// MaxPackageBytes is the file itself. Default 128 MiB.
	MaxPackageBytes int64
	// MaxPartBytes is one part, inflated. Default 32 MiB.
	MaxPartBytes int64
	// MaxUncompressedBytes is every part together, by declared size. Default
	// 256 MiB.
	MaxUncompressedBytes int64
	// MaxParts is the entries of the ZIP. Default 8192.
	MaxParts int
	// MaxRatio caps the bytes actually inflated, over the whole run, at this
	// multiple of the package's size plus 1 MiB. Default 250: ordinary XML
	// compresses ten to fifty to one, and a bomb a thousand.
	MaxRatio int
	// MaxPages is the pages converted. Default 2000.
	MaxPages int
	// MaxElementsPerPart is the XML elements of one part. Default 250000.
	MaxElementsPerPart int
	// MaxDepth is the XML nesting, and so the Canvas nesting. Default 64.
	MaxDepth int
	// MaxSegmentsPerPath is the figure segments of one geometry. Default 100000.
	MaxSegmentsPerPath int
	// MaxGlyphsPerRun is the glyphs of one Glyphs element. Default 50000.
	MaxGlyphsPerRun int
	// MaxFonts is the distinct font parts used. Default 256.
	MaxFonts int
	// MaxWork is the total of segments, glyphs, gradient stops and tiles drawn,
	// each charged every time it is drawn, so that a resource referenced a
	// hundred thousand times is paid for a hundred thousand times. Default 2
	// million (2097152). It counts units, not bytes: [Limits.MaxContentBytes]
	// is what bounds the content stream, and a vector-heavy document can raise
	// both.
	MaxWork int
	// MaxContentBytes is the page content stream, in bytes, across all pages, as
	// the PDF library reports it (Page.ContentLen) after every drawn element.
	// MaxWork counts units and cannot see that two segments and a
	// 126-number dash array are not the same size. Default 48 MiB.
	MaxContentBytes int64
	// MaxMemory is the one memory budget of a conversion, in bytes. It is
	// charged for what the conversion holds: the font parts kept, each image the
	// PDF library stores (the JPEG as it is; a PNG or TIFF at its worst, three
	// bytes a pixel, four with an alpha channel, however well it deflates), the
	// page content as the library counts it, and the output, which the library
	// builds in a buffer that doubles and this package copies (so images and
	// content count five times), and for a font its instruction tables
	// (fpgm, prep, cvt: 1 MiB at most) four times more. While an image is
	// decoded it is also charged for the part and the decode estimate, and that
	// is released after. Default 192 MiB, which keeps the peak heap under
	// 400 MiB (see the README).
	MaxMemory int64
	// MaxDecodeBytes is the most memory one image decode may be estimated to
	// need (pixels times a per-pixel cost by format and depth), checked from the
	// header BEFORE the image is decoded: 24 bytes a pixel for an 8-bit PNG
	// colour image, 40 for a 16-bit one, and 32 more for a TIFF, whose reader
	// inflates a strip through a doubling buffer. A JPEG is embedded as it is and
	// never decoded, so it costs nothing here. [Limits.MaxMemory] bounds the sum.
	// Default 192 MiB.
	MaxDecodeBytes int64
	// MaxDrawnPixels is the pixels of PNG and TIFF images decoded, in all: each
	// image is decoded and embedded once, however often it is drawn, so this
	// bounds the time of the decodes. Default 128 million.
	MaxDrawnPixels int
	// MaxImagePixels is the pixels of one image. Default 36 million.
	MaxImagePixels int
	// MaxTotalImagePixels is the pixels of every image, counted once per part.
	// Default 512 million.
	MaxTotalImagePixels int
	// MaxTiles is the tiles one tiling brush may draw. Default 4096.
	MaxTiles int
	// MaxPageUnits is the largest page side, in 1/96 inch. Default 19200 (200
	// inches, which is PDF's own ceiling of 14400 points).
	MaxPageUnits float64
}

func (l Limits) withDefaults() Limits {
	def := func(v *int, d int) {
		if *v <= 0 {
			*v = d
		}
	}
	def64 := func(v *int64, d int64) {
		if *v <= 0 {
			*v = d
		}
	}
	def64(&l.MaxPackageBytes, 128<<20)
	def64(&l.MaxPartBytes, 32<<20)
	def64(&l.MaxUncompressedBytes, 256<<20)
	def64(&l.MaxContentBytes, 48<<20)
	def64(&l.MaxMemory, 192<<20)
	def64(&l.MaxDecodeBytes, 192<<20)
	def(&l.MaxParts, 8192)
	def(&l.MaxRatio, 250)
	def(&l.MaxPages, 2000)
	def(&l.MaxElementsPerPart, 250000)
	def(&l.MaxDepth, 64)
	def(&l.MaxSegmentsPerPath, 100000)
	def(&l.MaxGlyphsPerRun, 50000)
	def(&l.MaxFonts, 256)
	def(&l.MaxWork, 2<<20)
	def(&l.MaxImagePixels, 36<<20)
	def(&l.MaxTotalImagePixels, 512<<20)
	def(&l.MaxDrawnPixels, 128<<20)
	def(&l.MaxTiles, 4096)
	if l.MaxPageUnits <= 0 {
		l.MaxPageUnits = 19200
	}
	return l
}

// Result is a converted document.
type Result struct {
	// PDF is the file.
	PDF []byte
	// Pages is how many pages it has.
	Pages int
	// Skipped says what the file used that was not drawn, and how often, one
	// line each, sorted. Empty when everything was.
	Skipped []string
}

// Relationship type and content types, ECMA-388 Annex D.
const (
	relStartPartSuffix = "/fixedrepresentation"
	ctSequence         = "application/vnd.ms-package.xps-fixeddocumentsequence+xml"
	ctObfuscatedFont   = "application/vnd.ms-package.obfuscated-opentype"
)

// compressOutput is on; a test turns it off to read the content streams.
var compressOutput = true

// Convert turns the bytes of an XPS or OpenXPS file into a PDF.
func Convert(b []byte, lim Limits) (*Result, error) {
	lim = lim.withDefaults()
	pkg, err := opc.Open(b, opc.Limits{
		MaxPackageBytes:      lim.MaxPackageBytes,
		MaxPartBytes:         lim.MaxPartBytes,
		MaxUncompressedBytes: lim.MaxUncompressedBytes,
		MaxParts:             lim.MaxParts,
	})
	if err != nil {
		if opc.TooLarge(err) {
			return nil, fmt.Errorf("%w: %v", ErrLimit, err)
		}
		return nil, fmt.Errorf("xps: not a package: %w", err)
	}
	c := &converter{
		lim:    lim,
		pkg:    pkg,
		budget: int64(lim.MaxRatio)*int64(len(b)) + 1<<20,
		skip:   map[string]int{},
		doc:    pdfkit.New(pdfkit.Options{Compress: compressOutput, Producer: "doc2pdf xps"}),
	}
	return c.run()
}

// converter is one conversion.
type converter struct {
	lim    Limits
	pkg    *opc.Package
	doc    *pdfkit.Document
	read   int64 // bytes inflated so far
	budget int64 // the ratio ceiling, in bytes
	skip   map[string]int
	pages  int
	fonts  map[string]*face
	images map[string]*imageInfo
	pixels int
	// exact and byLower index the package's real part names (see canonical).
	exact   map[string]bool
	byLower map[string]string
	// drawn is the pixels decoded to embed images, once per image.
	drawn int64
	// badImages remembers an image part that failed, so that it is read once.
	badImages map[string]error
	// spent is the drawing work charged so far.
	spent int
	// done is the content bytes of the pages finished so far.
	done int64
	// kept is the font bytes held, and stored the bytes the document holds for
	// the images embedded: the two charges of MaxMemory that outlast a draw.
	kept, stored int64
}

// outputFactor is how many more times the PDF is held while it is written than
// the images and content the document stores. pdfkit v0.26.0 builds the whole
// file in a bytes.Buffer it does not size (document.go, Write), so the buffer
// doubles as it grows (up to twice its final size, with the old one still
// uncollected) and this package copies it once more into the result: four
// copies on top of the one the document holds (555 MiB measured for 96 MiB of
// JPEG parts, TestSecPeakJPEGDoubling). It goes when pdfkit writes to its
// writer as it goes and the output is streamed.
const outputFactor = 4

// held is what the conversion holds for good, given the content bytes of the
// pages so far: the kept parts, and the stored images and the content, once
// for the document and outputFactor times for the output.
func (c *converter) held(content int64) int64 {
	return c.kept + (1+outputFactor)*(c.stored+content)
}

// fits refuses the conversion when what it holds, plus a transient charge, is
// more than MaxMemory.
func (c *converter) fits(content, transient int64) error {
	if c.held(content)+transient > c.lim.MaxMemory {
		return limit("bytes of memory held", int(min(c.lim.MaxMemory, math.MaxInt32)))
	}
	return nil
}

// note counts something that was not drawn.
func (c *converter) note(what string) { c.skip[what]++ }

// part reads one part, counting what it inflates against the ratio ceiling.
func (c *converter) part(name string) ([]byte, error) {
	b, err := c.pkg.Part(name)
	if err != nil {
		if opc.TooLarge(err) {
			return nil, fmt.Errorf("%w: %v", ErrLimit, err)
		}
		return nil, err
	}
	c.read += int64(len(b))
	if c.read > c.budget {
		return nil, limit("inflated bytes against the package size (a ratio bomb)", c.lim.MaxRatio)
	}
	return b, nil
}

// xmlPart reads and parses a part.
func (c *converter) xmlPart(name string) (*node, error) {
	b, err := c.part(name)
	if err != nil {
		return nil, err
	}
	// ⛔ The tree is held beside the part's bytes and what the conversion holds
	// already: whatever is left of the memory budget is all it may take.
	left := c.lim.MaxMemory - c.held(c.done) - int64(len(b))
	if left <= 0 {
		return nil, limit("bytes of memory held", int(min(c.lim.MaxMemory, math.MaxInt32)))
	}
	n, err := parseXMLWithin(b, c.lim.MaxDepth, c.lim.MaxElementsPerPart, 8*c.lim.MaxElementsPerPart, left)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}

// resolve turns a Source, FontUri or ImageSource value into a part name,
// relative to the part that wrote it.
//
// ⛔ A URI with a scheme is refused, not followed: this reads a package and
// nothing else. A fragment (a TrueType collection's face index) and a query
// are dropped, the rest is percent-decoded, backslashes become slashes as
// opc does, and a name that climbs out of the package is no part at all.
func (c *converter) resolve(base, uri string) (string, error) {
	if i := strings.IndexAny(uri, "#?"); i >= 0 {
		uri = uri[:i]
	}
	uri = strings.TrimSpace(uri)
	if uri == "" {
		return "", fmt.Errorf("%w: empty URI", ErrBadPartName)
	}
	if i := strings.Index(uri, ":"); i >= 0 && !strings.ContainsAny(uri[:i], "/\\") {
		return "", fmt.Errorf("%w: %q has a scheme, and nothing is fetched", ErrBadPartName, uri)
	}
	dec, err := url.PathUnescape(uri)
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrBadPartName, uri)
	}
	dec = strings.ReplaceAll(dec, `\`, "/")
	var joined string
	if strings.HasPrefix(dec, "/") {
		joined = path.Clean(dec)
	} else {
		joined = path.Join("/"+path.Dir(base), dec)
	}
	// Both branches above are cleaned from the root, and a cleaned rooted
	// path cannot climb out of it ("/.." is "/"), so a name that "leaves the
	// package" arrives here as a name inside it; and the root itself, "", is no
	// part, which Has says.
	joined = strings.TrimPrefix(joined, "/")
	real, ok := c.canonical(joined)
	if !ok {
		return "", fmt.Errorf("%w: %q is not there", ErrBadPartName, joined)
	}
	return real, nil
}

// canonical is the spelling a part really has in the package, or false when
// there is no such part.
//
// ⛔ opc finds a part case-insensitively, so "/A.png" and "/a.png" are one part
// read through two names. Every cache (images, fonts) keys on THIS spelling, so
// forty-eight aliases of one 31 MiB part are one entry and not forty-eight.
func (c *converter) canonical(name string) (string, bool) {
	if c.exact == nil {
		c.exact = map[string]bool{}
		c.byLower = map[string]string{}
		for _, p := range c.pkg.Parts() {
			c.exact[p] = true
			if _, ok := c.byLower[strings.ToLower(p)]; !ok {
				c.byLower[strings.ToLower(p)] = p
			}
		}
	}
	if c.exact[name] {
		return name, true
	}
	real, ok := c.byLower[strings.ToLower(name)]
	return real, ok
}

// startPart finds the document sequence: the package's start part, or failing
// that any part declared to be one.
func (c *converter) startPart() (string, error) {
	rels, err := c.pkg.Rels("")
	if err != nil {
		return "", err
	}
	for _, r := range rels {
		if r.External || !strings.HasSuffix(strings.ToLower(r.Type), relStartPartSuffix) {
			continue
		}
		if name, err := c.pkg.Resolve("", r); err == nil && c.pkg.Has(name) {
			return name, nil
		}
	}
	for _, name := range c.pkg.Parts() {
		if c.pkg.ContentType(name) == ctSequence {
			return name, nil
		}
	}
	return "", ErrNoDocument
}

// pageList walks the sequence and its documents to the pages, in order.
func (c *converter) pageList() ([]string, error) {
	start, err := c.startPart()
	if err != nil {
		return nil, err
	}
	root, err := c.xmlPart(start)
	if err != nil {
		return nil, err
	}
	var pages []string
	addDoc := func(docPart string, doc *node) error {
		for _, pc := range doc.kids {
			if pc.name != "PageContent" {
				continue
			}
			if len(pages) >= c.lim.MaxPages {
				return limit("pages", c.lim.MaxPages)
			}
			name, err := c.resolve(docPart, pc.attr("Source"))
			if err != nil {
				c.note("page reference that names no part")
				continue
			}
			pages = append(pages, name)
		}
		return nil
	}
	switch root.name {
	case "FixedDocument":
		if err := addDoc(start, root); err != nil {
			return nil, err
		}
	default:
		seen := 0
		for _, dr := range root.kids {
			if dr.name != "DocumentReference" {
				continue
			}
			if seen++; seen > c.lim.MaxPages {
				return nil, limit("documents", c.lim.MaxPages)
			}
			docPart, err := c.resolve(start, dr.attr("Source"))
			if err != nil {
				c.note("document reference that names no part")
				continue
			}
			doc, err := c.xmlPart(docPart)
			if err != nil {
				return nil, err
			}
			if err := addDoc(docPart, doc); err != nil {
				return nil, err
			}
		}
	}
	if len(pages) == 0 {
		return nil, ErrNoPages
	}
	return pages, nil
}

func (c *converter) run() (*Result, error) {
	pages, err := c.pageList()
	if err != nil {
		return nil, err
	}
	for _, name := range pages {
		if err := c.page(name); err != nil {
			return nil, err
		}
	}
	// The output is as large as the images and the content, which the budget has
	// charged: a buffer of that size is not grown, and so not doubled.
	var out bytes.Buffer
	out.Grow(int(min(c.stored+c.done+64<<10, c.lim.MaxMemory)))
	err = c.doc.Write(&out)
	if err == nil {
		res := &Result{PDF: out.Bytes(), Pages: c.pages}
		for what, n := range c.skip {
			res.Skipped = append(res.Skipped, fmt.Sprintf("%s (%d)", what, n))
		}
		sort.Strings(res.Skipped)
		return res, nil
	}
	return nil, fmt.Errorf("xps: writing the PDF: %w", err)
}
