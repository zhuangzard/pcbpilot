package kicad

import (
	"strconv"
	"strings"
	"testing"
)

// dumpSexp and dumpSx print a tree in one neutral form so the table below
// can check that both readers agree: lists as (…), quoted atoms with Go
// quoting, bare atoms as is.
func dumpSexp(n *sexp) string {
	if n.isList {
		parts := make([]string, len(n.list))
		for i, c := range n.list {
			parts[i] = dumpSexp(c)
		}
		return "(" + strings.Join(parts, " ") + ")"
	}
	if n.quoted {
		return strconv.Quote(n.atom)
	}
	return n.atom
}

func dumpSx(n *sx) string {
	if n.isList {
		parts := make([]string, len(n.Kids))
		for i, c := range n.Kids {
			parts[i] = dumpSx(c)
		}
		return "(" + strings.Join(parts, " ") + ")"
	}
	if n.Quoted {
		return strconv.Quote(n.Atom)
	}
	return n.Atom
}

// sexprCases is the shared table for every KiCad S-expression reader. The
// expected escape handling was checked against KiCad 10.0.6 by writing each
// string into a sheet's title block and re-saving it with
// `kicad-cli sch upgrade --force`.
var sexprCases = []struct {
	name, in string
	want     string // neutral dump; "" when an error is expected
	err      string // substring of the error
}{
	{name: "empty", in: "", err: "empty input"},
	{name: "blank", in: " \t\r\n", err: "empty input"},
	{name: "eof in string", in: `(a "b`, err: "unterminated string"},
	{name: "eof in bare string", in: `"abc`, err: "unterminated string"},
	{name: "eof after backslash", in: `(a "b\`, err: "unterminated string"},
	{name: "eof in list", in: `(a (b c)`, err: "unclosed '('"},
	{name: "lone close", in: `)`, err: "unexpected ')'"},
	{name: "close first", in: ` ) (a)`, err: "unexpected ')'"},
	{name: "trailing close ignored like KiCad", in: `(a))`, want: `(a)`},
	{name: "trailing junk ignored like KiCad", in: "(a)\njunk (x)", want: `(a)`},
	{name: "raw newline in string", in: "(a \"b\nc\")", err: "newline in string"},
	{name: "kicad escapes", in: `(t "q\"b\\s\nn\tt\rr")`, want: `(t "q\"b\\s\nn\tt\rr")`},
	{name: "c control escapes", in: `(t "\a\b\f\v")`, want: `(t "\a\b\f\v")`},
	{name: "hex and octal", in: `(t "hex\x41oct\101\102z")`, want: `(t "hexAoctABz")`},
	{name: "hex one digit", in: `(t "\x4g")`, want: `(t "\x04g")`},
	{name: "hex no digit", in: `(t "\xZ")`, want: `(t "xZ")`},
	{name: "octal three digits max", in: `(t "\1234")`, want: `(t "S4")`},
	{name: "unknown escape keeps backslash", in: `(t "\q\8\u00e9")`, want: `(t "\\q\\8\\u00e9")`},
	{name: "escaped backslash at end", in: `(t "end\\")`, want: `(t "end\\")`},
	{name: "utf8", in: `(t "µΩ 中文 é" µ)`, want: `(t "µΩ 中文 é" µ)`},
	{name: "invalid utf8 kept", in: "(t \"\xff\xfe\")", want: `(t "\xff\xfe")`},
	{name: "quote inside bare atom", in: `(t ab"cd"ef)`, want: `(t ab"cd"ef)`},
	{name: "empty string vs empty list", in: `(t "" ())`, want: `(t "" ())`},
	{name: "nested", in: "(a (b (c d)) e)", want: "(a (b (c d)) e)"},
	{name: "bare atom root", in: "  abc  ", want: "abc"},
	{name: "comment line", in: "(a\n  # c ) (\n b)", want: "(a b)"},
	{name: "hash mid-line is an atom", in: "(a #b \"x#y\")", want: `(a #b "x#y")`},
	{name: "nul separates", in: "(a\x00b)", want: "(a b)"},
	{name: "too deep", in: strings.Repeat("(", maxSexprDepth+1) + strings.Repeat(")", maxSexprDepth+1), err: "nested deeper than"},
	{name: "deep at cap", in: strings.Repeat("(", maxSexprDepth) + strings.Repeat(")", maxSexprDepth), want: strings.Repeat("(", maxSexprDepth) + strings.Repeat(")", maxSexprDepth)},
}

func TestSexprReadersAgree(t *testing.T) {
	for _, tc := range sexprCases {
		t.Run(tc.name, func(t *testing.T) {
			check := func(reader, got string, err error) {
				t.Helper()
				if tc.err != "" {
					if err == nil || !strings.Contains(err.Error(), tc.err) {
						t.Errorf("%s: err = %v, want %q", reader, err, tc.err)
					}
					return
				}
				if err != nil {
					t.Fatalf("%s: %v", reader, err)
				}
				if got != tc.want {
					t.Errorf("%s:\n got %s\nwant %s", reader, got, tc.want)
				}
			}
			n, err := parseSx(tc.in)
			got := ""
			if err == nil {
				got = dumpSexp(n)
			}
			check("parseSx", got, err)
			m, err := parseSexpr([]byte(tc.in))
			got = ""
			if err == nil {
				got = dumpSx(m)
			}
			check("parseSexpr", got, err)
		})
	}
}

// A huge run of '(' must fail fast on the depth cap, not grow the stack.
func TestSexprHugeNestingFails(t *testing.T) {
	in := strings.Repeat("(", 5_000_000)
	if _, err := parseSx(in); err == nil || !strings.Contains(err.Error(), "nested deeper than") {
		t.Fatalf("parseSx: %v", err)
	}
	if _, err := parseSexpr([]byte(in)); err == nil || !strings.Contains(err.Error(), "nested deeper than") {
		t.Fatalf("parseSexpr: %v", err)
	}
}

func TestSexprListOffsets(t *testing.T) {
	src := "(a \"x)\" (b c) (d))"
	n, err := parseSx(src)
	if err != nil {
		t.Fatal(err)
	}
	if src[n.beg:n.end] != src {
		t.Errorf("root span %q", src[n.beg:n.end])
	}
	if got := src[n.list[2].beg:n.list[2].end]; got != "(b c)" {
		t.Errorf("child span %q", got)
	}
}

// Render must reject an edit that closes the root early (the rest of the
// sheet would become ignored trailing text) as well as one left open.
func TestRenderRejectsUnbalancedEdit(t *testing.T) {
	src := NewSchematicText("A4", NewUUID(), true) + "\n# trailing comment\n"
	for _, item := range []string{`(text "x"))`, `(text "x"`} {
		e, err := OpenSchematic(src)
		if err != nil {
			t.Fatal(err)
		}
		e.AddRaw(item)
		if _, err := e.Render(); err == nil || !strings.Contains(err.Error(), "does not re-parse") {
			t.Errorf("%s: err = %v", item, err)
		}
	}
	e, _ := OpenSchematic(src)
	if out, err := e.Render(); err != nil || out != src {
		t.Errorf("no-op render changed the sheet: %v", err)
	}
}

// Q writes what KiCad's own formatter writes: backslash, quote, LF and CR
// escaped, everything else (tab, UTF-8) raw; and it reads back unchanged.
func TestQMatchesKiCadQuoting(t *testing.T) {
	cases := map[string]string{
		`a"b`:    `"a\"b"`,
		`a\b`:    `"a\\b"`,
		"a\nb":   `"a\nb"`,
		"a\rb":   `"a\rb"`,
		"a\tb":   "\"a\tb\"",
		"µΩ 中文":  `"µΩ 中文"`,
		`\u00e9`: `"\\u00e9"`,
		"":       `""`,
	}
	for in, want := range cases {
		if got := Q(in); got != want {
			t.Errorf("Q(%q) = %s, want %s", in, got, want)
		}
		n, err := parseSx("(t " + Q(in) + ")")
		if err != nil || n.list[1].atom != in {
			t.Errorf("Q(%q) does not read back: %v", in, err)
		}
	}
}

func TestUnescapeKiCadString(t *testing.T) {
	if got := unescapeKiCad(`a\"b\\c\x41\q`); got != `a"b\cA\q` {
		t.Errorf("got %q", got)
	}
}

func TestRenameLibSymbolEscapedName(t *testing.T) {
	sym := `(symbol "R\\x" (symbol "R\\x_0_1" (pin passive line)))`
	got, err := RenameLibSymbol(sym, `Lib:R2`)
	if err != nil {
		t.Fatal(err)
	}
	if want := `(symbol "Lib:R2" (symbol "R2_0_1" (pin passive line)))`; got != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestApplyEdits(t *testing.T) {
	src := "0123456789"
	ok := []struct {
		edits []textEdit
		want  string
	}{
		{[]textEdit{{2, 4, "ab"}, {6, 6, "X"}, {0, 1, ""}}, "1ab45X6789"},
		// repeated replacement of one span: the last wins, shrinking or growing
		{[]textEdit{{2, 8, ""}, {2, 8, ""}}, "0189"},
		{[]textEdit{{2, 8, "LONGER TEXT"}, {2, 8, "x"}}, "01x89"},
		{[]textEdit{{2, 4, "x"}, {2, 4, "a much longer value"}, {6, 7, "Z"}}, "01a much longer value45Z789"},
		// insertions at one offset: later first, before a replacement there
		{[]textEdit{{5, 5, "A"}, {5, 7, "C"}, {5, 5, "B"}}, "01234BAC789"},
		{[]textEdit{{3, 5, "r"}, {5, 5, "i"}}, "012ri56789"},
		{[]textEdit{{10, 10, "end"}, {0, 0, "start"}}, "start0123456789end"},
	}
	for _, tc := range ok {
		got, err := applyEdits(src, tc.edits)
		if err != nil || got != tc.want {
			t.Errorf("%v: got %q, %v; want %q", tc.edits, got, err, tc.want)
		}
	}
	bad := [][]textEdit{
		{{3, 6, "x"}, {4, 5, "nested"}},
		{{2, 6, "x"}, {4, 8, "partial"}},
		{{2, 6, "x"}, {2, 8, "same start"}},
		{{2, 6, "x"}, {4, 4, "insert inside"}},
		{{8, 12, "past end"}},
	}
	for _, edits := range bad {
		if got, err := applyEdits(src, edits); err == nil {
			t.Errorf("%v: want an error, got %q", edits, got)
		}
	}
}

// Setting one field twice keeps the second value; Render reports an
// overlapping edit instead of writing a corrupt sheet.
func TestRenderRepeatedAndOverlappingEdits(t *testing.T) {
	sheet, _ := buildTestSheet(t)
	e, err := OpenSchematic(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SetField("R1", "Value", "a much longer value than before"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetField("R1", "Value", "4k7"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `(property "Value" "4k7"`) || strings.Contains(out, "longer value") || strings.Contains(out, `"10k"`) {
		t.Errorf("second SetField did not win")
	}
	e.repl = append(e.repl, textEdit{e.root.beg + 1, e.root.end - 1, ""}, textEdit{e.root.beg + 2, e.root.beg + 3, "x"})
	if _, err := e.Render(); err == nil || !strings.Contains(err.Error(), "overlapping sheet edits") {
		t.Errorf("overlap: err = %v", err)
	}
}
