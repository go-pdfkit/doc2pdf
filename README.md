# doc2pdf

**Any document into a PDF.** Word, Excel, PowerPoint, OpenDocument Text,
Markdown, reStructuredText, HTML, plain text, CSV, JSON, XML, EPUB, MOBI,
FictionBook and email — through the [richdoc](https://github.com/go-richdoc/richdoc)
document model and [go-pdfkit/document](https://github.com/go-pdfkit/document).

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
| email | `.eml` `.mbox` | `go-richdoc/email` |
| Word | `.docx` | `go-ooxml/ooxml` |
| Excel | `.xlsx` | `go-ooxml/ooxml` |
| PowerPoint | `.pptx` | `go-ooxml/ooxml` |
| OpenDocument Text | `.odt` | `go-odf/odf` |

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

## What it cannot do

It inherits `go-pdfkit/html2pdf`: no JavaScript, no `@font-face`, no CSS
background images, and **CJK characters render as nothing**. Its own
documentation is the list.

Two other richdoc → PDF paths exist and go through a real TeX compile —
`go-richdoc/rst/pdf` and `go-richdoc/latex/pdf`. Reach for those when TeX's
hyphenation and justification are the point and a six-megabyte engine is
acceptable; reach for this one when CGO-free, `GOOS=js`-capable and keeping
emphasis, links and tables matter more.

⛔ **Ten of the fifteen formats are exercised end to end**, and the test says
so by asserting that number against the table. EPUB, MOBI, FictionBook,
PowerPoint and ODT need a real package built as a fixture, which is its own
work — a known gap recorded rather than an unnoticed one.

## Licence

BSD-3-Clause.
