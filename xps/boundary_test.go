// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

// Each ceiling is tested on both sides: the value that is allowed and the one
// that is not. `mutsweep -mutate boundary` moved these comparisons by one and
// nothing failed.

import (
	"errors"
	"strings"
	"testing"
)

func TestBoundaryXMLCeilings(t *testing.T) {
	nest := func(n int) []byte {
		return []byte(strings.Repeat("<a>", n) + strings.Repeat("</a>", n))
	}
	if _, err := parseXML(nest(5), 5, 100, 1000); err != nil {
		t.Errorf("depth 5 at ceiling 5: %v", err)
	}
	if _, err := parseXML(nest(6), 5, 100, 1000); !errors.Is(err, ErrLimit) {
		t.Errorf("depth 6 at ceiling 5: %v", err)
	}
	flat := []byte("<r>" + strings.Repeat("<a/>", 6) + "</r>") // 7 elements
	if _, err := parseXML(flat, 5, 7, 1000); err != nil {
		t.Errorf("7 elements at ceiling 7: %v", err)
	}
	if _, err := parseXML(flat, 5, 6, 1000); !errors.Is(err, ErrLimit) {
		t.Errorf("7 elements at ceiling 6: %v", err)
	}
	// <r><a/></r>: start r, start a, end a, end r = 4 tokens.
	tok := []byte("<r><a/></r>")
	if _, err := parseXML(tok, 5, 10, 4); err != nil {
		t.Errorf("4 tokens at ceiling 4: %v", err)
	}
	if _, err := parseXML(tok, 5, 10, 3); !errors.Is(err, ErrLimit) {
		t.Errorf("4 tokens at ceiling 3: %v", err)
	}
}

func TestBoundaryRatioBudget(t *testing.T) {
	p := newPkg(page(""))
	p.extra["a.bin"] = make([]byte, 10)
	c := newConv(t, p.bytes(t), Limits{})
	c.budget = 10
	if _, err := c.part("/a.bin"); err != nil {
		t.Fatalf("10 bytes against a budget of 10: %v", err)
	}
	c = newConv(t, p.bytes(t), Limits{})
	c.budget = 9
	if _, err := c.part("/a.bin"); !errors.Is(err, ErrLimit) {
		t.Errorf("10 bytes against a budget of 9: %v", err)
	}
}

func TestBoundaryPageSide(t *testing.T) {
	const side = 1000
	ok := newPkg(pageSized(side, side, ""))
	if _, err := Convert(ok.bytes(t), Limits{MaxPageUnits: side}); err != nil {
		t.Errorf("a page exactly at MaxPageUnits: %v", err)
	}
	if _, err := Convert(ok.bytes(t), Limits{MaxPageUnits: side - 1}); !errors.Is(err, ErrLimit) {
		t.Errorf("a page over MaxPageUnits: %v", err)
	}
}
