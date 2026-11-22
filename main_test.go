// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-odf/odf"
	"github.com/go-ooxml/ooxml"
	"github.com/go-pdfkit/extract"
	"github.com/go-pdfkit/reader"
	"github.com/go-richdoc/markdown"
	"github.com/go-richdoc/richdoc"
)

// drive runs the command and returns its exit code and both outputs.
func drive(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	c := RootCmd()
	c.SetArgs(args)
	c.SetOut(&out)
	c.SetErr(&errOut)
	code := 0
	if err := c.Execute(); err != nil {
		if e, ok := err.(exitCode); ok {
			code = int(e)
		} else {
			code = 2
		}
	}
	return code, out.String(), errOut.String()
}

// write puts bytes in a temporary file with the suffix given.
func write(t *testing.T, name string, body []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// ⛔⛔ ONE REAL FILE OF EVERY FORMAT THIS CLAIMS TO READ. The claim is the
// table in formats.go, and a table is a promise nobody keeps unless something
// checks it: a reader that stops reading, or a suffix that was never wired to
// anything, is a test that stops passing rather than a defect somebody meets
// in a year.
//
// ⛔ And the count is asserted against the table, so a format ADDED without a
// case here fails rather than quietly going unchecked.
func TestEveryFormatItClaimsToReadBecomesAPDF(t *testing.T) {
	cases := map[string][]byte{
		"a.md":   []byte("# Title\n\nSome prose.\n"),
		"a.rst":  []byte("Title\n=====\n\nSome prose.\n"),
		"a.html": []byte("<h1>Title</h1><p>Some prose.</p>"),
		"a.txt":  []byte("Title\n\nSome prose.\n"),
		"a.csv":  []byte("one,two\n1,2\n"),
		"a.json": []byte(`{"one": 1, "two": "2"}`),
		"a.xml":  []byte(`<r><a>1</a><b>2</b></r>`),
		"a.rtf":  []byte(`{\rtf1\ansi{\fonttbl{\f0 Times;}}\pard\outlinelvl0 Title\par\pard Some {\b prose}.\par}`),
		"a.eml":  []byte("From: a@example.invalid\r\nSubject: s\r\n\r\nSome prose.\r\n"),
		"a.docx": docx(t),
		"a.xlsx": xlsx(t),
		"a.odt":  odt(t),
		"a.ods":  odPackage(t, "spreadsheet", odsBody),
		"a.odp":  odPackage(t, "presentation", odpBody),
		"a.odg":  odPackage(t, "graphics", odgBody),
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			in := write(t, name, body)
			code, out, errOut := drive(t, in)
			if code != 0 {
				t.Fatalf("code %d: %s%s", code, out, errOut)
			}
			dst := strings.TrimSuffix(in, filepath.Ext(in)) + ".pdf"
			pdf, err := os.ReadFile(dst)
			if err != nil {
				t.Fatalf("no PDF was written: %v", err)
			}
			if _, err := reader.Open(pdf); err != nil {
				t.Fatalf("what it wrote does not open as a PDF: %v", err)
			}
			// It says which library read the file, which is the thing a person
			// needs when one of thirteen is the one that went wrong.
			if !strings.Contains(out, "read by go-") {
				t.Errorf("it does not say what read the file: %q", out)
			}
		})
	}

	// ⛔ The denominator. Four of the eighteen formats have no case above —
	// EPUB, MOBI, FictionBook and PowerPoint — and saying so here is the
	// difference between a known gap and an unnoticed one.
	//
	// ODT was on that list with the reason "needs a real package to be built".
	// It did not: odf.Write builds one, exactly as ooxml.WriteWord does for
	// Word above. A stated gap is still a claim, and that one had stopped being
	// true before it was written down.
	const covered = 15
	if len(cases) != covered {
		t.Fatalf("%d formats are exercised; the count says %d", len(cases), covered)
	}
	if len(formats) <= covered {
		t.Fatalf("%d formats are claimed and %d exercised: the gap closed, so this count should go",
			len(formats), covered)
	}
	t.Logf("%d of %d formats are exercised end to end", covered, len(formats))
}

// ⛔ A hostile .rtf is REFUSED, not converted. rtf v0.1.2 bounds what a file can
// make Parse allocate (16 MiB input, 1 Mi nodes, \bin checked against the input
// that remains); v0.1.0 and v0.1.1 had no such bound. A converter that only
// proves a sample converts would pass on those too.
func TestHostileRTFIsRefused(t *testing.T) {
	cases := map[string][]byte{
		// A few bytes of input per node: 2 Mi paragraphs against a limit of 1 Mi.
		"nodes.rtf": []byte(`{\rtf1` + strings.Repeat(`a\par `, 2<<20) + `}`),
		// A six-byte control word naming two gigabytes that are not there.
		"bin.rtf": []byte(`{\rtf1{\pict\pngblip\bin2000000000 x}}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			in := write(t, name, body)
			code, out, errOut := drive(t, in)
			if code == 0 {
				t.Fatalf("a hostile file was converted: %s%s", out, errOut)
			}
			if !strings.Contains(errOut, "rtf could not read") && !strings.Contains(errOut, "go-richdoc/rtf could not read") ||
				!strings.Contains(errOut, "rtf: ") {
				t.Errorf("the refusal does not say rtf refused it: %q", errOut)
			}
			if _, err := os.Stat(strings.TrimSuffix(in, ".rtf") + ".pdf"); err == nil {
				t.Fatal("a PDF was written for a refused file")
			}
		})
	}
}

// ⛔ An oversized input is refused from its SIZE, before one byte is read: the
// file is sparse (Truncate writes nothing), so a reader that buffered it first
// would allocate 16 MiB here and, on a real 10 GB file, the whole of it.
func TestOversizedInputIsRefusedUnread(t *testing.T) {
	in := write(t, "big.rtf", nil)
	if err := os.Truncate(in, rtfDefaultMaxInput+1); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := drive(t, in)
	if code != 1 {
		t.Fatalf("code %d: %s%s", code, out, errOut)
	}
	for _, want := range []string{"16777217 bytes", "16777216-byte limit", "RTF", "-max-input"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("%q is missing from %q", want, errOut)
		}
	}
	if _, err := os.Stat(strings.TrimSuffix(in, ".rtf") + ".pdf"); err == nil {
		t.Fatal("a PDF was written for a refused file")
	}
}

// The flag lowers the limit for any format, and raises it for RTF (which then
// passes the larger limit on to the reader).
func TestMaxInputFlag(t *testing.T) {
	in := write(t, "a.md", []byte("# Title\n\nSome prose.\n"))
	code, _, errOut := drive(t, "--max-input", "5", in)
	if code != 1 || !strings.Contains(errOut, "5-byte limit") || !strings.Contains(errOut, "Markdown") {
		t.Fatalf("code %d: %q", code, errOut)
	}
	code, out, errOut := drive(t, "--max-input", "1000000", in)
	if code != 0 {
		t.Fatalf("code %d: %s%s", code, out, errOut)
	}

	// 17 MiB of RTF paragraph-free text: over rtf's default, under the flag.
	big := []byte(`{\rtf1 a}` + strings.Repeat("\n", 17<<20))
	rtfIn := write(t, "big.rtf", big)
	code, out, errOut = drive(t, "--max-input", "20000000", rtfIn)
	if code != 0 {
		t.Fatalf("a raised limit did not reach rtf: %d %s%s", code, out, errOut)
	}
}

// The refusals of readBounded and of the write, each by what it says.
func TestReadAndWriteFailuresAreReported(t *testing.T) {
	// Refused from the size: the message carries the size of the FILE, not the
	// limit plus one a read would have stopped at.
	big := write(t, "big.md", nil)
	if err := os.Truncate(big, 64<<20); err != nil {
		t.Fatal(err)
	}
	if code, _, e := drive(t, "--max-input", "1048576", big); code != 1 || !strings.Contains(e, "67108864 bytes") {
		t.Errorf("size refusal: %d %q", code, e)
	}

	// A missing file, and a directory (opens, cannot be read).
	if code, _, e := drive(t, filepath.Join(t.TempDir(), "none.md")); code != 1 || !strings.Contains(e, "no such file") {
		t.Errorf("missing: %d %q", code, e)
	}
	dir := filepath.Join(t.TempDir(), "d.md")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, e := drive(t, dir); code != 1 || !strings.Contains(e, "directory") {
		t.Errorf("directory: %d %q", code, e)
	}

	// Not a regular file, so no size to check: the bounded read refuses it.
	if _, err := os.Stat("/dev/zero"); err == nil {
		_, err := readBounded("/dev/zero", format{Name: "test"}, 10)
		if err == nil || !strings.Contains(err.Error(), "11 bytes") {
			t.Errorf("unbounded device: %v", err)
		}
	}

	// A write into a directory that is not there is reported, not dropped.
	in := write(t, "a.md", []byte("# T\n"))
	out := filepath.Join(t.TempDir(), "missing", "o.pdf")
	if code, _, e := drive(t, in, out); code != 1 || !strings.Contains(e, "doc2pdf:") {
		t.Errorf("write: %d %q", code, e)
	}
}

// The temporary file cannot be created: the command reports it, exits 1, and
// writes nothing. A read-only directory is how that happens; root ignores the
// mode, so the test cannot say anything there.
func TestUnwritableOutputDirectoryIsReported(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	ro := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(ro, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ro, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(ro, 0o755) })
	in := write(t, "a.md", []byte("# T\n"))
	code, out, errOut := drive(t, in, filepath.Join(ro, "o.pdf"))
	if code != 1 || !strings.Contains(errOut, "permission denied") || out != "" {
		t.Fatalf("code %d, out %q, err %q", code, out, errOut)
	}
	if ents, _ := os.ReadDir(ro); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
}

// A failed or forced write leaves no temporary file, and -f replaces the old.
func TestWriteAtomicLeavesNoTemporary(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "o.pdf")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAtomic(dst, []byte("new"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if err := writeAtomic(filepath.Join(dir, "missing", "o.pdf"), []byte("x"), true); err == nil {
		t.Fatal("a write into a missing directory succeeded")
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("temporary files left behind: %v", ents)
	}
}

// Without force the output is published by a hard link, which refuses a file
// that is already there (so one created after the caller looked is not
// clobbered), and leaves neither the temporary name nor a changed dst. With a
// free name it publishes.
func TestWriteAtomicWithoutForceNeverReplaces(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "o.pdf")
	if err := os.WriteFile(dst, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeAtomic(dst, []byte("new"), false)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "mine" {
		t.Fatalf("an existing file was replaced: %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temporary files left behind: %v", ents)
	}

	free := filepath.Join(dir, "free.pdf")
	if err := writeAtomic(free, []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(free); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("temporary files left behind: %v", ents)
	}
}

// ⛔ On a file system with no hard links (FAT, exFAT, some SMB) Link fails with
// something that is not ErrExist, and a plain conversion must still work,
// without a Stat and Rename that would reopen the race.
func TestNoHardLinksFallsBackToExclusiveCreate(t *testing.T) {
	oldLink, oldWrite := linkFile, writeTemp
	t.Cleanup(func() { linkFile, writeTemp = oldLink, oldWrite })
	linkFile = func(string, string) error { return os.ErrPermission }

	// It works, and leaves no temporary file.
	dir := t.TempDir()
	dst := filepath.Join(dir, "o.pdf")
	if err := writeAtomic(dst, []byte("new"), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temporary files left behind: %v", ents)
	}

	// An existing file is refused as ErrExist and left alone.
	err := writeAtomic(dst, []byte("other"), false)
	if !errors.Is(err, fs.ErrExist) {
		t.Fatalf("want ErrExist, got %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Fatalf("an existing file was changed: %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("temporary files left behind: %v", ents)
	}

	// The temporary write succeeds and the second one fails: dst is removed.
	boom := errors.New("disk full")
	calls := 0
	writeTemp = func(f *os.File, b []byte) error {
		calls++
		if calls == 2 {
			f.Write(b[:1])
			return boom
		}
		return oldWrite(f, b)
	}
	dir2 := t.TempDir()
	if err := writeAtomic(filepath.Join(dir2, "o.pdf"), []byte("whole"), false); !errors.Is(err, boom) {
		t.Fatalf("got %v", err)
	}
	if ents, _ := os.ReadDir(dir2); len(ents) != 0 {
		t.Fatalf("left behind: %v", ents)
	}
}

// A write that fails part-way (a full disk) publishes nothing, with or without
// force, and leaves no temporary file.
func TestPartialWriteIsNeverPublished(t *testing.T) {
	old := writeTemp
	t.Cleanup(func() { writeTemp = old })
	boom := errors.New("disk full")
	writeTemp = func(f *os.File, b []byte) error {
		f.Write(b[:1])
		return boom
	}
	for _, force := range []bool{false, true} {
		dir := t.TempDir()
		dst := filepath.Join(dir, "o.pdf")
		if err := writeAtomic(dst, []byte("whole"), force); !errors.Is(err, boom) {
			t.Fatalf("force=%v: got %v", force, err)
		}
		if ents, _ := os.ReadDir(dir); len(ents) != 0 {
			t.Fatalf("force=%v: left behind: %v", force, ents)
		}
	}
}

// ⛔ --max-input 9223372036854775807 made limit+1 wrap, so nothing was read and
// an empty PDF came out with exit 0. 0 and negatives are refused too, as usage
// errors that name the flag, and the file is not read or written.
func TestMaxInputOutOfRangeIsAUsageError(t *testing.T) {
	for _, v := range []string{"9223372036854775807", "0", "-1"} {
		in := write(t, "a.md", []byte("# Title\n"))
		code, out, errOut := drive(t, "--max-input="+v, in)
		if code != 2 || out != "" || !strings.Contains(errOut, "--max-input") {
			t.Errorf("%s: code %d, out %q, err %q", v, code, out, errOut)
		}
		if _, err := os.Stat(strings.TrimSuffix(in, ".md") + ".pdf"); err == nil {
			t.Errorf("%s: a PDF was written", v)
		}
	}
	// The largest accepted value reads the file in full.
	in := write(t, "b.md", []byte("# Title\n"))
	if code, out, errOut := drive(t, "--max-input=9223372036854775806", in); code != 0 {
		t.Errorf("code %d: %s%s", code, out, errOut)
	}
}

// docx is a Word package, written by the library that reads it back.
func docx(t *testing.T) []byte {
	t.Helper()
	b, err := ooxml.WriteWord(&richdoc.Document{Blocks: []richdoc.Block{
		richdoc.Heading{Level: 1, Inlines: []richdoc.Inline{richdoc.Text{Value: "Title"}}},
		richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Text{Value: "Some prose."}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// xlsx is a workbook, written the same way.
func xlsx(t *testing.T) []byte {
	t.Helper()
	b, err := ooxml.WriteWorkbook(&richdoc.Document{Blocks: []richdoc.Block{
		richdoc.Table{
			Header: []richdoc.Cell{{Inlines: []richdoc.Inline{richdoc.Text{Value: "a"}}}},
			Rows:   [][]richdoc.Cell{{{Inlines: []richdoc.Inline{richdoc.Text{Value: "1"}}}}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// odt is an OpenDocument text package, written by the library that reads it
// back — the same shape as docx above.
func odt(t *testing.T) []byte {
	t.Helper()
	b, err := odf.Write(&richdoc.Document{Blocks: []richdoc.Block{
		richdoc.Heading{Level: 1, Inlines: []richdoc.Inline{richdoc.Text{Value: "Title"}}},
		richdoc.Paragraph{Inlines: []richdoc.Inline{richdoc.Text{Value: "Some prose."}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// odPackage builds a minimal OpenDocument package of one of the other three
// bodies.
//
// ⛔ BUILT BY HAND, because odf.Write always produces a TEXT document — the
// package says so. A spreadsheet written as an .ods by this test would
// therefore be an ODT with a misleading name, and it would pass while proving
// that the reader can read ODT, which was never in doubt. The container is the
// one the OpenDocument packaging specification describes: the mimetype entry
// first and STORED rather than deflated.
func odPackage(t *testing.T, kind, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	mt, err := z.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mt.Write([]byte("application/vnd.oasis.opendocument." + kind)); err != nil {
		t.Fatal(err)
	}
	c, err := z.Create("content.xml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const odNS = `xmlns:office="urn:oasis:names:tc:opendocument:xmlns:office:1.0" ` +
	`xmlns:table="urn:oasis:names:tc:opendocument:xmlns:table:1.0" ` +
	`xmlns:text="urn:oasis:names:tc:opendocument:xmlns:text:1.0" ` +
	`xmlns:draw="urn:oasis:names:tc:opendocument:xmlns:drawing:1.0"`

const odsBody = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content ` + odNS + ` office:version="1.3"><office:body><office:spreadsheet>
<table:table table:name="Sheet1">
<table:table-row><table:table-cell><text:p>one</text:p></table:table-cell>
<table:table-cell><text:p>two</text:p></table:table-cell></table:table-row>
</table:table>
</office:spreadsheet></office:body></office:document-content>`

const odpBody = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content ` + odNS + ` office:version="1.3"><office:body><office:presentation>
<draw:page draw:name="Slide one">
<draw:frame><draw:text-box><text:p>Some prose on a slide.</text:p></draw:text-box></draw:frame>
</draw:page>
</office:presentation></office:body></office:document-content>`

const odgBody = `<?xml version="1.0" encoding="UTF-8"?>
<office:document-content ` + odNS + ` office:version="1.3"><office:body><office:drawing>
<draw:page draw:name="Page one">
<draw:frame><draw:text-box><text:p>Words in a shape.</text:p></draw:text-box></draw:frame>
</draw:page>
</office:drawing></office:body></office:document-content>`

// ⛔ And what comes out CARRIES THE WORDS. A converter that writes a valid but
// empty PDF passes every test above: the file opens, the command says which
// library read it, and nothing is in it. The spreadsheet's cells and the
// slide's text are the point of the conversion.
func TestTheOpenDocumentBodiesReachThePage(t *testing.T) {
	for _, c := range []struct {
		name, body, want string
	}{
		{"a.ods", odsBody, "one"},
		{"a.odp", odpBody, "Some prose on a slide."},
		{"a.odg", odgBody, "Words in a shape."},
	} {
		t.Run(c.name, func(t *testing.T) {
			kind := map[string]string{
				"a.ods": "spreadsheet", "a.odp": "presentation", "a.odg": "graphics",
			}[c.name]
			in := write(t, c.name, odPackage(t, kind, c.body))
			if code, out, errOut := drive(t, in); code != 0 {
				t.Fatalf("code %d: %s%s", code, out, errOut)
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
			// Compared with the spaces taken out of both: how a page breaks a
			// line into runs is the layout's business, and this is asking
			// whether the words got there at all.
			flat := strings.ReplaceAll(said.String(), " ", "")
			if !strings.Contains(flat, strings.ReplaceAll(c.want, " ", "")) {
				t.Errorf("the page does not carry %q; it reads %q", c.want, said.String())
			}
		})
	}
}

func TestTheOutputNameDefaultsToTheInputWithPDF(t *testing.T) {
	in := write(t, "report.md", []byte("# x\n"))
	if code, _, errOut := drive(t, in); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(in), "report.pdf")); err != nil {
		t.Errorf("report.pdf is not beside the input: %v", err)
	}
}

// ⛔ An existing file is REFUSED, not overwritten. A conversion is something
// somebody runs twice by habit, and the second run must not eat a file they
// edited between them.
func TestAnExistingOutputIsRefusedUnlessForced(t *testing.T) {
	in := write(t, "a.md", []byte("# x\n"))
	dst := filepath.Join(filepath.Dir(in), "out.pdf")
	if err := os.WriteFile(dst, []byte("do not lose me"), 0o644); err != nil {
		t.Fatal(err)
	}

	code, _, errOut := drive(t, in, dst)
	if code != 1 || !strings.Contains(errOut, "already there") {
		t.Fatalf("code %d, said %q", code, errOut)
	}
	if b, _ := os.ReadFile(dst); string(b) != "do not lose me" {
		t.Error("it overwrote the file it refused")
	}

	if code, _, errOut := drive(t, "-f", in, dst); code != 0 {
		t.Fatalf("-f did not overwrite: code %d, %s", code, errOut)
	}
	if b, _ := os.ReadFile(dst); strings.HasPrefix(string(b), "do not lose me") {
		t.Error("-f left the old file")
	}
}

// ⛔⛔ NOTHING IS FETCHED, AND WHAT WAS REFUSED IS SAID. A document arrives
// from something somebody was handed and its images carry URLs the author
// chose; a conversion that followed them would reach the network on the
// author's behalf, from this machine. A page with holes in it must not be
// silent either.
func TestAnImageURLIsRefusedAndReported(t *testing.T) {
	in := write(t, "a.md", []byte("![x](http://169.254.169.254/latest/meta-data/)\n"))
	code, out, errOut := drive(t, in)
	if code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
	if !strings.Contains(out, "not fetched") || !strings.Contains(out, "169.254.169.254") {
		t.Errorf("it did not say what it refused: %q", out)
	}
}

func TestListSaysWhatItReadsAndWhatReadsIt(t *testing.T) {
	code, out, _ := drive(t, "--list")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	for _, f := range formats {
		if !strings.Contains(out, f.Name) || !strings.Contains(out, f.From) {
			t.Errorf("%s or %s is missing from the listing:\n%s", f.Name, f.From, out)
		}
	}
}

// ⛔ A suffix nothing reads says WHICH suffixes are read, rather than "cannot
// open". The second sends somebody to look at their file; the first tells them
// what to do.
func TestASuffixNothingReadsSaysWhatIsRead(t *testing.T) {
	for _, name := range []string{"a.doc", "a.pages", "noSuffix"} {
		in := write(t, name, []byte("x"))
		code, _, errOut := drive(t, in)
		if code != 2 {
			t.Errorf("%s: code %d, want 2", name, code)
		}
		if !strings.Contains(errOut, "it reads ") {
			t.Errorf("%s: it did not say what it reads: %q", name, errOut)
		}
	}
}

// A reader that fails names ITSELF, because thirteen libraries stand behind
// this command and "cannot read it" sends somebody to the wrong one.
func TestAReaderThatFailsNamesItself(t *testing.T) {
	in := write(t, "a.docx", []byte("this is not a Word package"))
	code, _, errOut := drive(t, in)
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut, "go-ooxml/ooxml") || !strings.Contains(errOut, "Word") {
		t.Errorf("the failure does not name the reader: %q", errOut)
	}
}

func TestUsageErrors(t *testing.T) {
	in := write(t, "a.md", []byte("# x\n"))
	for _, args := range [][]string{{}, {in, "b.pdf", "c.pdf"}} {
		if code, _, _ := drive(t, args...); code != 2 {
			t.Errorf("%v: code %d, want 2", args, code)
		}
	}
	// A file that is not there is a failure, not a usage error: the command
	// was formed correctly and the world did not cooperate.
	if code, _, _ := drive(t, filepath.Join(t.TempDir(), "absent.md")); code != 1 {
		t.Errorf("an absent file gave %d, want 1", code)
	}
}

// markdownRoundTrip is here so the Markdown reader is exercised against the
// writer in its own package, which is the pair this command leans on hardest.
func TestTheMarkdownItReadsIsTheMarkdownThatPackageWrites(t *testing.T) {
	doc := &richdoc.Document{Blocks: []richdoc.Block{
		richdoc.Heading{Level: 1, Inlines: []richdoc.Inline{richdoc.Text{Value: "Title"}}},
	}}
	src, err := markdown.Write(doc)
	if err != nil {
		t.Fatal(err)
	}
	in := write(t, "a.md", src)
	if code, _, errOut := drive(t, in); code != 0 {
		t.Fatalf("code %d: %s", code, errOut)
	}
}

// ⛔ A zipOf helper was here, written for the EPUB and ODT fixtures that do
// not exist yet. It was removed: a helper with no caller is a guess about work
// nobody has started, and the count in the first test already records that
// five formats go unexercised.
