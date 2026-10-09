package kicad

// schtitle.go — the sheet's title block (title, date, rev, company,
// comments 1–9), merged into what the sheet already has.

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// TitleBlock holds title-block fields; empty strings leave a field as it is.
// Comments are keyed 1–9.
type TitleBlock struct {
	Title, Date, Rev, Company string
	Comments                  map[int]string
}

// TitleBlock reads the sheet's title block.
func (e *SchEditor) TitleBlock() TitleBlock {
	tb := TitleBlock{Comments: map[int]string{}}
	n := e.root.child("title_block")
	if n == nil {
		return tb
	}
	for _, c := range n.list {
		switch c.head() {
		case "title":
			tb.Title = atomAt(c, 1)
		case "date":
			tb.Date = atomAt(c, 1)
		case "rev":
			tb.Rev = atomAt(c, 1)
		case "company":
			tb.Company = atomAt(c, 1)
		case "comment":
			if k, err := strconv.Atoi(atomAt(c, 1)); err == nil {
				tb.Comments[k] = atomAt(c, 2)
			}
		}
	}
	return tb
}

func atomAt(n *sexp, i int) string {
	if i < len(n.list) {
		return n.list[i].atom
	}
	return ""
}

// SetTitleBlock merges set into the sheet's title block (KiCad field order).
func (e *SchEditor) SetTitleBlock(set TitleBlock) error {
	tb := e.TitleBlock()
	for k := range set.Comments {
		if k < 1 || k > 9 {
			return fmt.Errorf("title block comment %d: KiCad has comments 1–9", k)
		}
	}
	if set.Title != "" {
		tb.Title = set.Title
	}
	if set.Date != "" {
		tb.Date = set.Date
	}
	if set.Rev != "" {
		tb.Rev = set.Rev
	}
	if set.Company != "" {
		tb.Company = set.Company
	}
	for k, v := range set.Comments {
		tb.Comments[k] = v
	}
	var b strings.Builder
	b.WriteString("(title_block")
	for _, f := range [][2]string{{"title", tb.Title}, {"date", tb.Date}, {"rev", tb.Rev}, {"company", tb.Company}} {
		if f[1] != "" {
			fmt.Fprintf(&b, "\n\t\t(%s %s)", f[0], Q(f[1]))
		}
	}
	keys := make([]int, 0, len(tb.Comments))
	for k := range tb.Comments {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		if tb.Comments[k] != "" {
			fmt.Fprintf(&b, "\n\t\t(comment %d %s)", k, Q(tb.Comments[k]))
		}
	}
	b.WriteString("\n\t)")
	if n := e.root.child("title_block"); n != nil {
		e.repl = append(e.repl, textEdit{n.beg, n.end, b.String()})
		return nil
	}
	p := e.root.child("paper")
	if p == nil {
		return fmt.Errorf("sheet has no (paper …) to put the title block after")
	}
	e.repl = append(e.repl, textEdit{p.end, p.end, "\n\t" + b.String()})
	return nil
}
