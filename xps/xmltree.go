// Copyright (c) 2026, the go-pdfkit authors
// All rights reserved.
//
// SPDX-License-Identifier: BSD-3-Clause

package xps

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
)

// A node is one element of a part, with the namespace thrown away.
//
// ⛔ LOCAL NAMES ONLY. An OpenXPS part (xmlns "http://schemas.openxps.org/
// oxps/v1.0", ECMA-388 §14.2.5 example) and an XPS 1.0 one (Microsoft's own
// namespace) name the same elements, so a reader that matched the namespace
// would read one and refuse the other for no reason a person could see. What
// is matched is the element's local name, and an element this does not know is
// skipped with its subtree, never read as something else.
type node struct {
	name  string
	attrs map[string]string
	kids  []*node
}

// attr is an attribute by local name, "" when absent.
func (n *node) attr(name string) string { return n.attrs[name] }

// has says whether the attribute is there at all, empty or not.
func (n *node) has(name string) bool { _, ok := n.attrs[name]; return ok }

// child is the first child element with the given name, or nil.
func (n *node) child(name string) *node {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// maxAttrs is the attributes one element may carry. No XPS element has more
// than a few dozen; the cap exists because encoding/xml materialises every
// attribute of a start tag before this package sees any of them.
const maxAttrs = 256

// checkAttrCounts refuses a start tag with more than max attributes, by
// scanning the raw bytes BEFORE the decoder allocates for them: a tag is read
// to its closing '>' outside quotes, and the '=' signs outside quotes are
// counted. Comments, CDATA, processing instructions and declarations are
// stepped over, so their contents are not counted as tags. What is malformed
// is left for the decoder to refuse.
func checkAttrCounts(b []byte, max int) error {
	skip := func(i int, end string) int {
		j := bytes.Index(b[i:], []byte(end))
		if j < 0 {
			return len(b)
		}
		return i + j + len(end)
	}
	for i := 0; i < len(b); {
		if b[i] != '<' {
			i++
			continue
		}
		rest := b[i:]
		switch {
		case bytes.HasPrefix(rest, []byte("<!--")):
			i = skip(i+4, "-->")
			continue
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			i = skip(i+9, "]]>")
			continue
		case bytes.HasPrefix(rest, []byte("<?")):
			i = skip(i+2, "?>")
			continue
		case bytes.HasPrefix(rest, []byte("<!")):
			// ⛔ No directive at all. Where a DOCTYPE ends is a matter of quotes
			// and nested brackets, which encoding/xml reads one way and a byte
			// scan another: `<!DOCTYPE a '>' "<!--" >` hid the root element from
			// the scan and let six million attributes through. XPS has no use for
			// a DOCTYPE, so it is refused instead of stepped over.
			return fmt.Errorf("%w: a <! declaration (DOCTYPE) is not allowed", ErrBadXML)
		}
		n, quote := 0, byte(0)
		i++
		for ; i < len(b); i++ {
			c := b[i]
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				continue
			}
			if c == '"' || c == '\'' {
				quote = c
			} else if c == '=' {
				if n++; n > max {
					return limit("attributes on one XML element", max)
				}
			} else if c == '>' {
				i++
				break
			}
		}
	}
	return nil
}

// parseXML builds the tree of one part.
//
// ⛔ BOUNDED THREE WAYS, because encoding/xml is bounded in none. Depth is
// the recursion every later walk of the tree would otherwise inherit, nodes
// is the allocation, tokens is the time (a part of nothing but comments and
// processing instructions allocates no node and still costs a token each).
// encoding/xml expands no DTD entity, so there is no billion-laughs here; a
// reference to one it does not know is a syntax error, which is what is wanted.
func parseXML(b []byte, maxDepth, maxNodes, maxTokens int) (*node, error) {
	return parseXMLWithin(b, maxDepth, maxNodes, maxTokens, 0)
}

// What the tree of a part costs in memory, measured on a page of 17,000
// elements with 256 attributes each (489 MiB peak for 4.4 million attributes,
// TestSecPeakXMLTree): a node and its slices, and a map entry with the strings
// the decoder allocated for it.
const (
	nodeBytes = 128
	attrBytes = 96
)

// parseXMLWithin is parseXML that also refuses a tree whose estimated size
// (nodeBytes an element, attrBytes an attribute and the bytes of their names
// and values) passes budget bytes; zero is no budget.
func parseXMLWithin(b []byte, maxDepth, maxNodes, maxTokens int, budget int64) (*node, error) {
	var cost int64
	if err := checkAttrCounts(b, maxAttrs); err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(b))
	var (
		root   *node
		stack  []*node
		nodes  int
		tokens int
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadXML, err)
		}
		if tokens++; tokens > maxTokens {
			return nil, limit("XML tokens in one part", maxTokens)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= maxDepth {
				return nil, limit("XML nesting depth", maxDepth)
			}
			if nodes++; nodes > maxNodes {
				return nil, limit("XML elements in one part", maxNodes)
			}
			n := &node{name: t.Name.Local}
			cost += nodeBytes + int64(len(t.Name.Local)) + attrBytes*int64(len(t.Attr))
			for _, a := range t.Attr {
				cost += int64(len(a.Name.Local) + len(a.Value))
			}
			if budget > 0 && cost > budget {
				return nil, limit("bytes of memory held", int(min(budget, math.MaxInt32)))
			}
			if len(t.Attr) > 0 {
				n.attrs = make(map[string]string, len(t.Attr))
				for _, a := range t.Attr {
					// xmlns declarations are not data.
					if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
						continue
					}
					if _, dup := n.attrs[a.Name.Local]; dup {
						return nil, fmt.Errorf("%w: attribute %q given twice on <%s>", ErrBadXML, a.Name.Local, t.Name.Local)
					}
					n.attrs[a.Name.Local] = a.Value
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, fmt.Errorf("%w: more than one root element", ErrBadXML)
				}
				root = n
			} else {
				p := stack[len(stack)-1]
				p.kids = append(p.kids, n)
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("%w: no root element", ErrBadXML)
	}
	return root, nil
}
