// Package specctra reads and patches the Specctra DSN / SES files that
// EasyEDA Pro exchanges with external autorouters (Freerouting, fastroute).
//
// Everything here is pure text/geometry work with no EasyEDA dependency, so
// the export fixes and the session-import repair plan are unit-testable from
// recorded files. The live steps (listing, deleting and creating tracks) stay
// in internal/app.
package specctra

import (
	"fmt"
	"strings"
)

// node is one S-expression: an atom (Atom set, List nil) or a list.
type node struct {
	Atom   string
	Quoted bool
	List   []*node
	isList bool
}

// head returns the first atom of a list ("" for atoms and empty lists).
func (n *node) head() string {
	if !n.isList || len(n.List) == 0 || n.List[0].isList {
		return ""
	}
	return n.List[0].Atom
}

// children returns the sub-lists whose head is name.
func (n *node) children(name string) []*node {
	var out []*node
	for _, c := range n.List {
		if c.isList && c.head() == name {
			out = append(out, c)
		}
	}
	return out
}

func (n *node) child(name string) *node {
	if c := n.children(name); len(c) > 0 {
		return c[0]
	}
	return nil
}

// atoms returns the atom values after the head, stopping at the first list.
func (n *node) atoms() []string {
	var out []string
	for _, c := range n.List[1:] {
		if c.isList {
			break
		}
		out = append(out, c.Atom)
	}
	return out
}

// parseSexpr parses one top-level S-expression. Double-quoted strings are
// atoms with the quotes removed. Specctra's (string_quote ...) directive is
// not interpreted: EasyEDA and fastroute both use '"'.
func parseSexpr(src string) (*node, error) {
	p := &sexprParser{src: src}
	p.skipSpace()
	n, err := p.parse()
	if err != nil {
		return nil, err
	}
	return n, nil
}

type sexprParser struct {
	src string
	pos int
}

func (p *sexprParser) skipSpace() {
	for p.pos < len(p.src) && strings.ContainsRune(" \t\r\n", rune(p.src[p.pos])) {
		p.pos++
	}
}

func (p *sexprParser) parse() (*node, error) {
	if p.pos >= len(p.src) {
		return nil, fmt.Errorf("unexpected end of input")
	}
	switch p.src[p.pos] {
	case '(':
		p.pos++
		n := &node{isList: true}
		for {
			p.skipSpace()
			if p.pos >= len(p.src) {
				return nil, fmt.Errorf("unclosed '(' at end of input")
			}
			if p.src[p.pos] == ')' {
				p.pos++
				return n, nil
			}
			c, err := p.parse()
			if err != nil {
				return nil, err
			}
			n.List = append(n.List, c)
		}
	case ')':
		return nil, fmt.Errorf("unexpected ')' at offset %d", p.pos)
	case '"':
		end := strings.IndexByte(p.src[p.pos+1:], '"')
		if end < 0 {
			return nil, fmt.Errorf("unterminated string at offset %d", p.pos)
		}
		s := p.src[p.pos+1 : p.pos+1+end]
		p.pos += end + 2
		return &node{Atom: s, Quoted: true}, nil
	default:
		start := p.pos
		for p.pos < len(p.src) && !strings.ContainsRune(" \t\r\n()", rune(p.src[p.pos])) {
			p.pos++
		}
		return &node{Atom: p.src[start:p.pos]}, nil
	}
}

// Expr is the exported form of one S-expression, for readers outside this
// package (pkg/pcbroute/dsn, PLAN.md Q12). An atom has IsList false.
type Expr struct {
	Atom   string
	Quoted bool
	List   []*Expr
	IsList bool
}

// ParseExpr parses one top-level S-expression with the same rules as the
// package's own DSN/SES readers.
func ParseExpr(src string) (*Expr, error) {
	n, err := parseSexpr(src)
	if err != nil {
		return nil, err
	}
	return exportNode(n), nil
}

func exportNode(n *node) *Expr {
	e := &Expr{Atom: n.Atom, Quoted: n.Quoted, IsList: n.isList}
	if n.isList {
		e.List = make([]*Expr, len(n.List))
		for i, c := range n.List {
			e.List[i] = exportNode(c)
		}
	}
	return e
}

// Head returns the first atom of a list ("" for atoms and empty lists).
func (e *Expr) Head() string {
	if !e.IsList || len(e.List) == 0 || e.List[0].IsList {
		return ""
	}
	return e.List[0].Atom
}

// Children returns the sub-lists whose head is name.
func (e *Expr) Children(name string) []*Expr {
	var out []*Expr
	for _, c := range e.List {
		if c.IsList && c.Head() == name {
			out = append(out, c)
		}
	}
	return out
}

// Child returns the first sub-list whose head is name, or nil.
func (e *Expr) Child(name string) *Expr {
	for _, c := range e.List {
		if c.IsList && c.Head() == name {
			return c
		}
	}
	return nil
}

// Atoms returns every atom after the head, skipping sub-lists (unlike the
// internal atoms helper, which stops at the first list).
func (e *Expr) Atoms() []string {
	if len(e.List) < 2 {
		return nil
	}
	var out []string
	for _, c := range e.List[1:] {
		if !c.IsList {
			out = append(out, c.Atom)
		}
	}
	return out
}
