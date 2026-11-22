// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

// Command doc2pdf turns a document into a PDF:
//
//	doc2pdf report.docx            # writes report.pdf
//	doc2pdf notes.md out.pdf
//	doc2pdf -list
//
// Word, Excel, PowerPoint, OpenDocument Text, Markdown, reStructuredText,
// HTML, plain text, CSV, JSON, XML, EPUB, MOBI, FictionBook and email, through
// the richdoc document model and go-pdfkit/document.
//
// ⛔ IT EXISTS TO MAKE THIRTEEN LIBRARIES USABLE AND TO HOLD THEM TO THEIR
// WORD. Measured on 2026-11-05 across thirty repositories: four of them
// shipped a command and twenty-five did not, so a reader that was finished,
// tested and released was still something only a Go programme could reach.
// That is a different state from unfinished and is easy to mistake for it —
// an inventory of this fleet's capabilities was revised five times that
// morning, largely over this distinction.
//
// ⛔ AND NOTHING IS FETCHED. A document arrives from something somebody was
// handed, and its images carry URLs that whoever wrote the file chose:
// fetching them would make every conversion a request to an address of the
// author's choosing, from this machine and with its network position.
// go-pdfkit/document refuses by default and this does not override it; what
// was refused is reported, because a page with holes in it must not be silent.
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-pdfkit/document"
	"github.com/spf13/cobra"
)

func main() {
	if err := RootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// RootCmd builds the command. Tests drive it through run, below.
func RootCmd() *cobra.Command {
	var o options
	c := &cobra.Command{
		Use:   "doc2pdf [flags] <in> [out.pdf]",
		Short: "Turn a document into a PDF",
		Long: `Reads a document and writes it as a PDF.

    doc2pdf report.docx            # writes report.pdf beside it
    doc2pdf notes.md out.pdf
    doc2pdf -list

The output name defaults to the input with its suffix replaced by .pdf; it is
refused rather than overwritten unless -f is given.

Nothing is fetched. An <img src="https://…"> in the document is refused and
counted, because a document says what it wants fetched and a conversion that
obeyed it would reach the network on the author's behalf.

Exit status: 0 it was written, 1 it was not, 2 a usage error.`,
		Args:          cobra.ArbitraryArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fromStatus(run(cmd.OutOrStdout(), cmd.ErrOrStderr(), o, args))
		},
	}
	f := c.Flags()
	f.BoolVar(&o.list, "list", false, "print the formats this reads, and what reads them")
	f.BoolVarP(&o.force, "force", "f", false, "overwrite the output if it is already there")
	f.Float64Var(&o.marginMm, "margin", 20, "the margin on every side, in millimetres")
	f.Int64Var(&o.maxInput, "max-input", 0, "the largest input to read, in bytes (default: 16 MiB for RTF, 256 MiB otherwise)")
	return c
}

type options struct {
	maxInput int64
	list     bool
	force    bool
	marginMm float64
}

// fromStatus turns an exit code into the error cobra wants, so that main has
// one way out.
func fromStatus(code int) error {
	if code == 0 {
		return nil
	}
	return exitCode(code)
}

type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

// run is the command's work.
func run(out, errOut interface {
	Write([]byte) (int, error)
}, o options, args []string) int {
	if o.list {
		listFormats(out)
		return 0
	}
	if len(args) < 1 || len(args) > 2 {
		fmt.Fprintln(errOut, "doc2pdf: give one file to read and, if you like, one to write")
		return 2
	}

	in := args[0]
	f, err := formatFor(in)
	if err != nil {
		fmt.Fprintln(errOut, err)
		return 2
	}
	src, err := readBounded(in, f, o.maxInput)
	if err != nil {
		fmt.Fprintf(errOut, "doc2pdf: %v\n", err)
		return 1
	}

	doc, err := f.Read(src)
	if err != nil {
		// ⛔ Which READER failed, not just that something did. Thirteen
		// libraries stand behind this command, and "cannot read it" sends
		// somebody to the wrong one.
		fmt.Fprintf(errOut, "doc2pdf: %s could not read this %s: %v\n", f.From, f.Name, err)
		return 1
	}

	var refused []string
	pdf, err := document.Write(doc, document.WriteOptions{
		MarginMm:  o.marginMm,
		OnRefused: func(u string) { refused = append(refused, u) },
	})
	if err != nil {
		fmt.Fprintf(errOut, "doc2pdf: %v\n", err)
		return 1
	}

	dst := outputName(in, args)
	if !o.force {
		if _, err := os.Stat(dst); err == nil {
			fmt.Fprintf(errOut, "doc2pdf: %s is already there; -f overwrites it\n", dst)
			return 1
		}
	}
	if err := writeAtomic(dst, pdf); err != nil {
		fmt.Fprintf(errOut, "doc2pdf: %v\n", err)
		return 1
	}

	fmt.Fprintf(out, "%s → %s (%s, read by %s, %d bytes)\n",
		in, dst, f.Name, f.From, len(pdf))
	// ⛔ Said on the OUTPUT, not buried in a log. A document that asked for
	// pictures it did not get is a page with holes in it, and the person who
	// converted it is the only one who can decide whether that matters.
	for _, u := range refused {
		fmt.Fprintf(out, "  not fetched: %s\n", u)
	}
	return 0
}

// readBounded reads a file of at most the format's limit (or override, when
// positive), refusing BEFORE reading when the file is already larger, and
// never reading more than the limit plus one byte whatever the file turns into.
func readBounded(path string, f format, override int64) ([]byte, error) {
	limit := f.MaxInput()
	if override > 0 {
		limit = override
	}
	refuse := func(size int64) error {
		return fmt.Errorf("%s is %d bytes, over the %d-byte limit for %s; -max-input raises it",
			path, size, limit, f.Name)
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	// A Stat that fails is not a reason to refuse: the read below is bounded
	// on its own.
	if st, err := fh.Stat(); err == nil && st.Mode().IsRegular() && st.Size() > limit {
		return nil, refuse(st.Size())
	}
	b, err := io.ReadAll(io.LimitReader(fh, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, refuse(int64(len(b)))
	}
	return b, nil
}

// writeAtomic writes beside dst and renames, so a failed write leaves either
// the old file or none, never a partial PDF.
func writeAtomic(dst string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".doc2pdf-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(b)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(name, 0o644)
	}
	if err == nil {
		err = os.Rename(name, dst)
	}
	if err != nil {
		os.Remove(name)
	}
	return err
}

// outputName is the second argument, or the input with a .pdf suffix.
func outputName(in string, args []string) string {
	if len(args) == 2 {
		return args[1]
	}
	return strings.TrimSuffix(in, filepath.Ext(in)) + ".pdf"
}

// listFormats prints what this reads and what reads it.
//
// ⛔ A command that converts eighteen kinds of file has to be able to SAY which
// eighteen. The alternative is a person trying one to find out, which is how a
// capability that exists goes unused.
//
// ⛔ The columns are MEASURED rather than guessed at twenty characters. They
// were guessed, and "OpenDocument Presentation" is twenty-five: the three
// formats added last ran their name into the next column and the table stopped
// lining up. A width taken from the widest row cannot go out of date when a
// row is added, which is the only kind of layout worth having in a list that
// grows.
func listFormats(out interface{ Write([]byte) (int, error) }) {
	name, suffix := 0, 0
	for _, f := range formats {
		name = max(name, len(f.Name))
		suffix = max(suffix, len(strings.Join(f.Suffixes, " ")))
	}
	for _, f := range formats {
		fmt.Fprintf(out, "%-*s  %-*s  %s\n",
			name, f.Name, suffix, strings.Join(f.Suffixes, " "), f.From)
	}
}
