// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bufio"
	"crypto/rand"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// writeAtomicStream is writeAtomic for a converter that writes as it goes: the
// PDF goes straight into the temporary file, never into a buffer, so a large
// file costs the converter's document and nothing more. It returns the size.
func writeAtomicStream(dst string, write func(io.Writer) (int64, error), force bool) (int64, error) {
	name := filepath.Join(filepath.Dir(dst), ".doc2pdf-"+rand.Text()+".tmp")
	tmp, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	defer os.Remove(name)
	// The writer makes many small writes; a buffer turns them into few.
	bw := bufio.NewWriterSize(tmp, 256<<10)
	n, err := write(bw)
	if err == nil {
		err = bw.Flush()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if force {
		return n, os.Rename(name, dst)
	}
	err = linkFile(name, dst)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return n, err
	}
	// No hard links here: create dst with O_EXCL and copy the temporary file
	// into it, as createExclusive does for bytes.
	return n, copyExclusive(dst, name)
}

// copyExclusive copies src into a dst it creates, refusing one that exists; a
// failure removes dst again.
func copyExclusive(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(f, in)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
	}
	return err
}
