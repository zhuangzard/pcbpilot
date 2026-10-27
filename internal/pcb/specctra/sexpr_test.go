package specctra

import (
	"os"
	"strings"
	"testing"
)

// DSN strings have no escapes (a quote ends the string), so this reader
// keeps its own rules; it must still fail cleanly on broken input.
func TestParseSexprMalformed(t *testing.T) {
	cases := map[string]string{
		"":                             "unexpected end of input",
		`(a "b`:                        "unterminated string",
		`"abc`:                         "unterminated string",
		`(a (b c)`:                     "unclosed '('",
		`)`:                            "unexpected ')'",
		strings.Repeat("(", 5_000_000): "nested deeper than",
	}
	for in, want := range cases {
		if _, err := parseSexpr(in); err == nil || !strings.Contains(err.Error(), want) {
			name := in
			if len(name) > 20 {
				name = name[:20] + "…"
			}
			t.Errorf("%q: err = %v, want %q", name, err, want)
		}
	}
	n, err := parseSexpr(`(pcb "a\b" (x "µΩ"))`)
	if err != nil || n.List[1].Atom != `a\b` || n.List[2].List[1].Atom != "µΩ" {
		t.Errorf("got %v %+v", err, n)
	}
	in := strings.Repeat("(", maxSexprDepth) + strings.Repeat(")", maxSexprDepth)
	if _, err := parseSexpr(in); err != nil {
		t.Errorf("depth at cap: %v", err)
	}
}

// FuzzParseSexpr: the reader never panics, and a parsed tree written back
// with writeSexpr reads back to the same text.
func FuzzParseSexpr(f *testing.F) {
	for _, p := range []string{"testdata/easyeda-export.dsn", "testdata/fastroute-excerpt.ses"} {
		if b, err := os.ReadFile(p); err == nil {
			f.Add(string(b))
		}
	}
	for _, s := range []string{"", "(", ")", `(a "b`, `(a "" ())`, "(a (b c) d)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		n, err := parseSexpr(src)
		if err != nil {
			return
		}
		if !n.isList {
			return // writeSexpr round-trips lists; a bare root atom is not a DSN
		}
		out, err := writeSexpr(n)
		if err != nil {
			t.Fatalf("a parsed tree must be writable: %v", err)
		}
		m, err := parseSexpr(out)
		if err != nil {
			t.Fatalf("re-parse of %q: %v", out, err)
		}
		if again, _ := writeSexpr(m); again != out {
			t.Fatalf("round trip changed:\n%s\n%s", out, again)
		}
	})
}

// A net name with a backslash or a raw newline is written back as read.
func TestWriteSexprKeepsStringBytes(t *testing.T) {
	for _, in := range []string{`(net "A\B" (pins U1-1))`, "(net \"a\nb\")", `(net "µΩ")`} {
		n, err := parseSexpr(in)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := writeSexpr(n); err != nil || got != in {
			t.Errorf("got %s (%v), want %s", got, err, in)
		}
	}
}

// Constructed nodes DSN cannot spell are refused instead of written as a
// file the router would split differently.
func TestWriteSexprRejectsUnrepresentable(t *testing.T) {
	bad := []*node{
		{isList: true, List: []*node{{Atom: "class"}, {Atom: `a"b`, Quoted: true}}},
		{isList: true, List: []*node{{Atom: "net"}, {Atom: "a b"}}},
		{isList: true, List: []*node{{Atom: "net"}, {Atom: "x)"}}},
		{isList: true, List: []*node{{Atom: ""}}},
	}
	for _, n := range bad {
		if got, err := writeSexpr(n); err == nil {
			t.Errorf("wrote %s, want an error", got)
		}
	}
	ok := &node{isList: true, List: []*node{{Atom: "class"}, {Atom: "c1"}, {Atom: `A\B (x)`, Quoted: true}, {isList: true}}}
	if got, err := writeSexpr(ok); err != nil || got != `(class c1 "A\B (x)" ())` {
		t.Errorf("got %s, %v", got, err)
	}
}

// A requirement on a net whose name DSN cannot quote fails the rewrite.
func TestApplyNetRequirementsRejectsQuoteInName(t *testing.T) {
	b, err := os.ReadFile("testdata/easyeda-export.dsn")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = ApplyNetRequirements(string(b), map[string]NetRequirement{`BAD"NET`: {OuterMil: 10}})
	if err == nil || !strings.Contains(err.Error(), "cannot quote") {
		t.Errorf("err = %v", err)
	}
}
