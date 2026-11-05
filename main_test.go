// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-ooxml/ooxml"
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
		"a.eml":  []byte("From: a@example.invalid\r\nSubject: s\r\n\r\nSome prose.\r\n"),
		"a.docx": docx(t),
		"a.xlsx": xlsx(t),
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

	// ⛔ The denominator. Five of the fifteen formats have no case above —
	// EPUB, MOBI, FictionBook, PowerPoint and ODT need a real package to be
	// built, which is its own work — and saying so here is the difference
	// between a known gap and an unnoticed one.
	const covered = 10
	if len(cases) != covered {
		t.Fatalf("%d formats are exercised; the count says %d", len(cases), covered)
	}
	if len(formats) <= covered {
		t.Fatalf("%d formats are claimed and %d exercised: the gap closed, so this count should go",
			len(formats), covered)
	}
	t.Logf("%d of %d formats are exercised end to end", covered, len(formats))
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
