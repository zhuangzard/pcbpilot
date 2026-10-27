package kicad

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSexpCanon writes a tree on one line, quoted atoms through Q, so a
// re-parse can be compared with the original tree.
func writeSexpCanon(b *strings.Builder, n *sexp) {
	if !n.isList {
		if n.quoted {
			b.WriteString(Q(n.atom))
		} else {
			b.WriteString(n.atom)
		}
		return
	}
	b.WriteByte('(')
	for i, c := range n.list {
		if i > 0 {
			b.WriteByte(' ')
		}
		writeSexpCanon(b, c)
	}
	b.WriteByte(')')
}

// checkSexprInput is the property every fuzz target and the demo corpus
// share: both readers agree (tree or error), list offsets point at the
// parentheses, and a list root survives write → parse unchanged.
func checkSexprInput(t *testing.T, src string) *sexp {
	t.Helper()
	n, err := parseSx(src)
	m, err2 := parseSexpr([]byte(src))
	if (err == nil) != (err2 == nil) {
		t.Fatalf("readers disagree on %q: parseSx %v, parseSexpr %v", clip(src), err, err2)
	}
	if err != nil {
		if "s-expression: "+err.Error() != err2.Error() {
			t.Fatalf("readers report differently: %v / %v", err, err2)
		}
		return nil
	}
	d := dumpSexp(n)
	if d2 := dumpSx(m); d != d2 {
		t.Fatalf("readers disagree on %q:\n%s\n%s", clip(src), clip(d), clip(d2))
	}
	var walk func(*sexp)
	walk = func(x *sexp) {
		if !x.isList {
			return
		}
		if x.beg < 0 || x.end > len(src) || x.beg >= x.end || src[x.beg] != '(' || src[x.end-1] != ')' {
			t.Fatalf("bad list span [%d,%d) in %q", x.beg, x.end, clip(src))
		}
		for _, c := range x.list {
			walk(c)
		}
	}
	walk(n)
	if n.isList {
		var b strings.Builder
		writeSexpCanon(&b, n)
		r, err := parseSx(b.String())
		if err != nil {
			t.Fatalf("written tree does not re-parse: %v\n%s", err, clip(b.String()))
		}
		if got := dumpSexp(r); got != d {
			t.Fatalf("write → parse changed the tree:\n%s\n%s", clip(d), clip(got))
		}
	}
	return n
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// addKiCadSeeds seeds f with the repository's KiCad fixtures and the shared
// table; while fuzzing (-fuzz) it also adds the smaller KiCad demo files.
func addKiCadSeeds(f *testing.F, exts ...string) {
	files, _ := filepath.Glob("testdata/*")
	more, _ := filepath.Glob("testdata/fab/*")
	files = append(files, more...)
	if fl := flag.Lookup("test.fuzz"); fl != nil && fl.Value.String() != "" {
		if dir := kicadDemosDir(); dir != "" {
			_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if info, err := d.Info(); err == nil && info.Size() < 32<<10 {
						files = append(files, p)
					}
				}
				return nil
			})
		}
	}
	for _, p := range files {
		ok := false
		for _, e := range exts {
			ok = ok || strings.HasSuffix(p, e)
		}
		if b, err := os.ReadFile(p); ok && err == nil {
			f.Add(string(b))
		}
	}
	for _, tc := range sexprCases {
		if len(tc.in) < 4096 {
			f.Add(tc.in)
		}
	}
}

func FuzzKiCadSexpr(f *testing.F) {
	addKiCadSeeds(f, ".kicad_sch", ".kicad_pcb", ".kicad_sym")
	f.Fuzz(func(t *testing.T, src string) { checkSexprInput(t, src) })
}

// FuzzParseBoard: the board reader built on parseSexpr never panics.
func FuzzParseBoard(f *testing.F) {
	addKiCadSeeds(f, ".kicad_pcb")
	f.Fuzz(func(t *testing.T, src string) { _, _ = ParseBoard([]byte(src)) })
}

// FuzzOpenSchematic: opening never panics, an unedited Render returns the
// input byte for byte, and the sheet-fit reader copes with whatever parses.
func FuzzOpenSchematic(f *testing.F) {
	addKiCadSeeds(f, ".kicad_sch")
	f.Fuzz(func(t *testing.T, src string) {
		e, err := OpenSchematic(src)
		if err != nil {
			return
		}
		out, err := e.Render()
		if err != nil || out != src {
			t.Fatalf("no-op render: %v", err)
		}
		_, _, _ = FitSheet(src)
	})
}
