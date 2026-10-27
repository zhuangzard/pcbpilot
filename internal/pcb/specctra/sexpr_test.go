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
		out := writeSexpr(n)
		m, err := parseSexpr(out)
		if err != nil {
			t.Fatalf("re-parse of %q: %v", out, err)
		}
		if again := writeSexpr(m); again != out {
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
		if got := writeSexpr(n); got != in {
			t.Errorf("got %s, want %s", got, in)
		}
	}
}
