// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func streamOf(s string) func(io.Writer) (int64, error) {
	return func(w io.Writer) (int64, error) {
		n, err := io.WriteString(w, s)
		return int64(n), err
	}
}

func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// The stream goes into the temporary file and is published whole.
func TestWriteAtomicStreamPublishesWhatWasWritten(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "o.pdf")
	n, err := writeAtomicStream(dst, streamOf("whole"), false)
	if err != nil || n != 5 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "whole" {
		t.Errorf("dst holds %q", b)
	}
	if names := leftovers(t, dir); len(names) != 1 {
		t.Errorf("the temporary file stayed: %v", names)
	}
}

// Without force an existing file is refused and kept; with force it is replaced.
func TestWriteAtomicStreamExistingFile(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "o.pdf")
	if err := os.WriteFile(dst, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := writeAtomicStream(dst, streamOf("new"), false); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("an existing file: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "old" {
		t.Errorf("the old file became %q", b)
	}
	if _, err := writeAtomicStream(dst, streamOf("new"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "new" {
		t.Errorf("force left %q", b)
	}
	if names := leftovers(t, dir); len(names) != 1 {
		t.Errorf("leftovers: %v", names)
	}
}

// A writer that fails part-way leaves no file and no temporary one.
func TestWriteAtomicStreamFailureLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	boom := errors.New("disk full")
	_, err := writeAtomicStream(filepath.Join(dir, "o.pdf"), func(w io.Writer) (int64, error) {
		io.WriteString(w, "half")
		return 4, boom
	}, false)
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("leftovers: %v", names)
	}
}

// A directory that is not there is an error, not a panic.
func TestWriteAtomicStreamNoDirectory(t *testing.T) {
	if _, err := writeAtomicStream(filepath.Join(t.TempDir(), "missing", "o.pdf"), streamOf("x"), true); err == nil {
		t.Fatal("wrote into a directory that is not there")
	}
}

// A file system without hard links: the file is copied into a dst created
// exclusively, and an existing dst is still refused.
func TestWriteAtomicStreamWithoutHardLinks(t *testing.T) {
	old := linkFile
	t.Cleanup(func() { linkFile = old })
	linkFile = func(string, string) error { return errors.New("no links here") }
	dir := t.TempDir()
	dst := filepath.Join(dir, "o.pdf")
	if n, err := writeAtomicStream(dst, streamOf("copied"), false); err != nil || n != 6 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "copied" {
		t.Errorf("dst holds %q", b)
	}
	if _, err := writeAtomicStream(dst, streamOf("other"), false); !errors.Is(err, fs.ErrExist) {
		t.Errorf("an existing dst: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "copied" {
		t.Errorf("the file became %q", b)
	}
	if names := leftovers(t, dir); len(names) != 1 {
		t.Errorf("leftovers: %v", names)
	}
}

// copyExclusive removes a dst it made when the source cannot be read, and
// never one it did not make.
func TestCopyExclusiveFailures(t *testing.T) {
	dir := t.TempDir()
	if err := copyExclusive(filepath.Join(dir, "o.pdf"), filepath.Join(dir, "nothing")); err == nil {
		t.Error("copied a file that is not there")
	}
	if names := leftovers(t, dir); len(names) != 0 {
		t.Errorf("leftovers: %v", names)
	}
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.WriteFile(dst, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyExclusive(dst, src); !errors.Is(err, fs.ErrExist) {
		t.Errorf("an existing dst: %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "mine" {
		t.Errorf("somebody's file became %q", b)
	}
	// A source that is a directory fails to read, after dst was made: dst goes.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	made := filepath.Join(dir, "made")
	if err := copyExclusive(made, sub); err == nil {
		t.Error("copied a directory")
	}
	if _, err := os.Stat(made); err == nil {
		t.Error("a failed copy left its dst")
	}
}
