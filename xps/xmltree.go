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

// parseXML builds the tree of one part.
//
// ⛔ BOUNDED THREE WAYS, because encoding/xml is bounded in none. Depth is
// the recursion every later walk of the tree would otherwise inherit, nodes
// is the allocation, tokens is the time (a part of nothing but comments and
// processing instructions allocates no node and still costs a token each).
// encoding/xml expands no DTD entity, so there is no billion-laughs here; a
// reference to one it does not know is a syntax error, which is what is wanted.
func parseXML(b []byte, maxDepth, maxNodes, maxTokens int) (*node, error) {
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
			if len(t.Attr) > 0 {
				n.attrs = make(map[string]string, len(t.Attr))
				for _, a := range t.Attr {
					// xmlns declarations are not data.
					if a.Name.Space == "xmlns" || (a.Name.Space == "" && a.Name.Local == "xmlns") {
						continue
					}
					if _, dup := n.attrs[a.Name.Local]; !dup {
						n.attrs[a.Name.Local] = a.Value
					}
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
