# doc2pdf

**Any document into a PDF.** Word, Excel, PowerPoint, OpenDocument Text,
Spreadsheet, Presentation and Graphics, XPS, Markdown, reStructuredText, HTML,
plain text, CSV, JSON, XML, EPUB, MOBI, FictionBook and email — through the
[richdoc](https://github.com/go-richdoc/richdoc) document model and
[go-pdfkit/document](https://github.com/go-pdfkit/document).

Pure Go, CGO-free, no TeX.

```sh
go install github.com/go-pdfkit/doc2pdf@latest

doc2pdf report.docx            # writes report.pdf beside it
doc2pdf notes.md out.pdf
doc2pdf --list
```

```
/tmp/demo.md → /tmp/demo.pdf (Markdown, read by go-richdoc/markdown, 8267 bytes)
```

## Why it exists

⛔ **Thirteen libraries were finished, tested, released and unusable from a
shell.** Measured on 2026-11-05 across thirty repositories of this fleet: four
shipped a command and twenty-five did not. A reader that nothing can call is in
a different state from an unfinished one, and it is easy to mistake for it — an
inventory of this fleet's capabilities was revised **five times** that morning,
largely over that distinction ([go-pdfkit/app#71](https://github.com/go-pdfkit/app/issues/71)).

A library with no caller is also a library nothing holds to its word. This
command is the caller: every reader in the table is exercised by a test that
converts a real file of that format, so a reader that stops reading is a test
that stops passing rather than a defect somebody meets in a year.

## ⛔ Nothing is fetched

A document arrives from something somebody was handed — a `.docx`, an EPUB, an
email — and its images carry URLs that **whoever wrote the file chose**.
Following them would make every conversion a request to an address of the
author's choosing, from this machine and with its network position. That is a
server-side request forgery with a document as the payload.

`go-pdfkit/document` refuses by default and this does not override it. What was
refused is **printed**, because a page with holes in it must not be silent:

```
report.docx → report.pdf (Word, read by go-ooxml/ooxml, 44102 bytes)
  not fetched: https://tracker.example.invalid/pixel.png
```

## What it reads

| | | |
|---|---|---|
| Markdown | `.md` `.markdown` | `go-richdoc/markdown` |
| reStructuredText | `.rst` | `go-richdoc/rst` |
| HTML | `.html` `.htm` `.xhtml` | `go-richdoc/html` |
| plain text | `.txt` | `go-richdoc/data/text` |
| CSV | `.csv` `.tsv` | `go-richdoc/data/csv` |
| JSON | `.json` | `go-richdoc/data/json` |
| XML | `.xml` | `go-richdoc/data/xml` |
| EPUB | `.epub` | `go-richdoc/epub` |
| MOBI | `.mobi` `.azw` `.azw3` | `go-richdoc/mobi` |
| FictionBook | `.fb2` | `go-richdoc/fb2` |
| RTF | `.rtf` | `go-richdoc/rtf` |
| email | `.eml` `.mbox` | `go-richdoc/email` |
| Word | `.docx` | `go-ooxml/ooxml` |
| Excel | `.xlsx` | `go-ooxml/ooxml` |
| PowerPoint | `.pptx` | `go-ooxml/ooxml` |
| OpenDocument Text | `.odt` | `go-odf/odf` |
| OpenDocument Spreadsheet | `.ods` | `go-odf/odf` |
| OpenDocument Presentation | `.odp` | `go-odf/odf` |
| OpenDocument Graphics | `.odg` | `go-odf/odf` |
| XPS / OpenXPS | `.xps` `.oxps` | `go-pdfkit/doc2pdf/xps` |

### XPS and OpenXPS: drawn, not rasterised

XPS is a fixed-layout format, so it does not go through richdoc: the
`xps` package reads the package (ECMA-388, an Open Packaging Conventions ZIP) and draws each
page straight onto a PDF page. The output is **vector**: paths stay paths,
gradients stay shadings, and a `Glyphs` run becomes real text in an **embedded
font** (the font part, de-obfuscated when it is a `.odttf`), so the text is
selectable and searchable. Drawn: `Path` (abbreviated and element geometry,
fill rules, strokes with dashes, caps, joins and miter limit), `Canvas`
(transform, clip, opacity, resource dictionaries), linear and radial
gradients, `ImageBrush` with PNG, JPEG and TIFF (including the tile modes),
`Glyphs` (indices, clusters, advances, offsets, bold and italic simulation,
right-to-left), and Markup Compatibility fallbacks.

What it does **not** draw is printed, never silent:

```
page.oxps → page.pdf (XPS, read by go-pdfkit/doc2pdf/xps, 5120 bytes)
  not drawn: VisualBrush
```

Not drawn or approximated: `VisualBrush`, `OpacityMask`, gradient spread
`Reflect` and `Repeat` (drawn as `Pad`), `IsSideways`, TrueType collections,
JPEG XR images, ICC profiles, remote resource dictionaries, hyperlinks and
outlines, gradient stop alpha, gradient or image fills on text and strokes
(the first colour is used), and the `Triangle` line cap.

**Untrusted files, and what they can cost.** An XPS file is a ZIP of XML whose
author chose every number in it, so every ceiling is a `xps.Limits` field and
a refusal names what was exceeded. At the **default limits** (what `doc2pdf`
passes) the peak heap was measured, under a 640 MiB soft memory limit
(`GOMEMLIMIT`), on the worst input found for each cost: a package of 85 KB that
asks for ten million path segments peaks at about **400 MiB** (the content
stream is an in-memory buffer, about 200 MiB per million segments, so
`MaxWork` defaults to 2 million); 12 draws of a 1.8 KB PNG of 16.7 million
pixels are refused at 144 MiB and 0.8 s (every draw is charged its pixels,
`MaxDrawnPixels`, because a PNG is decoded on each draw); sixteen
case-spelled aliases of one 8 MiB part cost 8 MiB, because a part is cached
under its real name; four million attributes on one element are refused before
they are allocated (at most 256 per element, duplicates refused) at 47 MiB.
Plan on **under 500 MiB** of heap per conversion at the defaults; the figures
are asserted by `xps/security_test.go`, and a vector-heavy document can raise
`MaxWork` at about 200 MiB per million segments.

⛔ **A package is hostile input.** Nothing is fetched: a `FontUri` or
`ImageSource` with a scheme, a `..` that leaves the package, or a part that
is not there is refused. Every size is bounded before it is allocated: package,
part and total uncompressed bytes, the compression ratio, part count, pages,
elements and nesting per part, segments per path, glyphs per run, fonts, image
pixels (read from the header, before any decode), tiles, and an overall work
budget charged for every draw. A bound that is hit is an error that names it,
and the file is refused whole.

**What RTF conversion keeps.** Structure: headings, lists, tables, hyperlinks
(http, https, mailto), footnotes, PNG and JPEG pictures, bold, italic, strike,
and paragraph alignment (as the classes `align-center`, `align-right`,
`align-justify`). It does not keep font sizes, colours, underline, fonts,
indents, headers and footers, or WMF and EMF pictures: the richdoc model has no
place for them. See the
[go-richdoc/rtf README](https://github.com/go-richdoc/rtf#what-it-cannot-carry).

⛔ **By suffix, and it says so.** Sniffing the content would be cleverer and
would guess wrong on the formats that share a container: `.docx`, `.xlsx`,
`.pptx`, `.odt` and `.epub` are all ZIP files, and telling them apart means
reading the package anyway. A suffix is what the person typed and what they can
change; when it is wrong the error names the suffixes that *are* read rather
than silently reading the file as something else.

⛔ **A failure names the reader.** Thirteen libraries stand behind this, and
*"cannot read it"* sends somebody to the wrong one:

```
doc2pdf: go-ooxml/ooxml could not read this Word: …
```

⛔ **The input is bounded before it is read.** The file's size is checked first
and a larger one is refused without reading it, because the readers bound what
they allocate only once the file is in memory. The limit is 16 MiB for RTF (the
default of `go-richdoc/rtf` v0.1.2, whose worst case is then about 80 times the
input) and 256 MiB for every other format. `--max-input BYTES` replaces it (from 1 up to 9223372036854775806; anything else
is a usage error, exit 2):

```
doc2pdf: big.rtf is 20000000 bytes, over the 16777216-byte limit for RTF; -max-input raises it
```

The PDF is written to a temporary file beside the output and then published:
renamed over the output with `-f`, and hard-linked without it, which fails if
the output exists even if it appeared a moment ago. A failed write never leaves
a partial file. Where the file system has no hard links (FAT, exFAT, some SMB),
the output is created with an exclusive open instead, which refuses an existing
file just as atomically.

## What it cannot do

It inherits `go-pdfkit/html2pdf`: no JavaScript, no `@font-face`, no CSS
background images, and **CJK characters render as nothing**. Its own
documentation is the list.

Two other richdoc → PDF paths exist and go through a real TeX compile —
`go-richdoc/rst/pdf` and `go-richdoc/latex/pdf`. Reach for those when TeX's
hyphenation and justification are the point and a six-megabyte engine is
acceptable; reach for this one when CGO-free, `GOOS=js`-capable and keeping
emphasis, links and tables matter more.

⛔ **Sixteen of the twenty formats are exercised end to end**, and the test
says so by asserting that number against the table. EPUB, MOBI, FictionBook and
PowerPoint need a real package built as a fixture, which is its own work — a
known gap recorded rather than an unnoticed one.

ODT was on that list, with the reason *"needs a real package built"*. It did
not: `odf.Write` builds one, exactly as `ooxml.WriteWord` does for Word. ⛔ **A
stated gap is still a claim**, and that one had stopped being true before it was
written down.

The three OpenDocument bodies beyond text are exercised with packages built by
hand, because `odf.Write` always produces a *text* document — a spreadsheet
written by it and named `.ods` would be an ODT with a misleading name, and
would pass while proving only that the reader reads ODT, which was never in
doubt. Those three also assert that the **words reach the page**: a converter
that wrote a valid but empty PDF would otherwise pass every other check here.

## Licence

BSD-3-Clause.
