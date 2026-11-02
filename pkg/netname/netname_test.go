package netname

import "testing"

func TestCandidatesNeverGuessTheSheetBoundary(t *testing.T) {
	sheets := []string{"/", "/pic_sockets/", "/pic_sockets/inner/"}
	eq := func(a, b []string) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}
	for in, want := range map[string][]string{
		"/VPP/MCLR":            {"/"}, // only the root: certain
		"/pic_sockets/VCC_PIC": {"/", "/pic_sockets/"},
		"/pic_sockets/inner/X": {"/", "/pic_sockets/", "/pic_sockets/inner/"},
		"GND":                  nil,
		"Net-(U5-OSC1/CLKI)":   nil,
	} {
		if got := Candidates(in, sheets); !eq(got, want) {
			t.Errorf("Candidates(%q) = %v, want %v", in, got, want)
		}
	}
	for _, c := range []struct{ prefix, name, want string }{
		{"/", "/VPP/MCLR", "/VPP{slash}MCLR"},
		{"/", "/pic_sockets/VCC_PIC", "/pic_sockets{slash}VCC_PIC"}, // root label "pic_sockets/VCC_PIC"
		{"/pic_sockets/", "/pic_sockets/VCC_PIC", "/pic_sockets/VCC_PIC"},
		{"", "Net-(U5-OSC1/CLKI)", "Net-(U5-OSC1{slash}CLKI)"},
		{"", "unconnected-(U4-S/S-Pad4)", "unconnected-(U4-S{slash}S-Pad4)"},
	} {
		if got := Join(c.prefix, c.name); got != c.want {
			t.Errorf("Join(%q, %q) = %q, want %q", c.prefix, c.name, got, c.want)
		}
	}
}

func TestLocalAndDisplay(t *testing.T) {
	if got := Local("/VPP{slash}MCLR"); got != "VPP/MCLR" {
		t.Errorf("Local = %q", got)
	}
	if got := Local("/pic_sockets/VCC_PIC"); got != "VCC_PIC" {
		t.Errorf("Local = %q", got)
	}
	d := Display([]string{"/a/SDA", "/b/SDA", "/c/SCL", "GND"})
	if d["/a/SDA"] != "/a/SDA" || d["/b/SDA"] != "/b/SDA" || d["/c/SCL"] != "SCL" || d["GND"] != "GND" {
		t.Errorf("Display = %v", d)
	}
}

func TestResolver(t *testing.T) {
	r := NewResolver([]string{"/VPP{slash}MCLR", "/pic_sockets/VCC_PIC", "VCC", "/s/VCC", "/a/SDA", "/b/SDA", "unconnected-(J1-Pad1)", ""})
	for in, want := range map[string]string{
		"VPP/MCLR":             "/VPP{slash}MCLR",
		"/VPP/MCLR":            "/VPP{slash}MCLR", // unescaped full name
		"/VPP{slash}MCLR":      "/VPP{slash}MCLR",
		"VCC_PIC":              "/pic_sockets/VCC_PIC",
		"VCC":                  "VCC", // the exact identity wins over /s/VCC's alias
		"/pic_sockets/VCC_PIC": "/pic_sockets/VCC_PIC",
	} {
		if got, m := r.Resolve(in); m != Found || got != want {
			t.Errorf("Resolve(%q) = %q %v, want %q", in, got, m, want)
		}
	}
	for in, want := range map[string]Match{"SDA": Ambiguous, "MCLR": Unknown, "unconnected-(J1-Pad1)": Unknown, "nope": Unknown} {
		if got, m := r.Resolve(in); m != want || got != in {
			t.Errorf("Resolve(%q) = %q %v, want %v (never a pick, never cut at '/')", in, got, m, want)
		}
	}
	if c := r.Candidates("SDA"); len(c) != 2 || c[0] != "/a/SDA" || c[1] != "/b/SDA" {
		t.Errorf("Candidates(SDA) = %v", c)
	}
}

// /a/X, /b/X and the root label "/a/X" (identity "/{slash}a{slash}X",
// Local "/a/X" = another net's identity): no display name may equal
// another net's identity or display name, and each display name resolves
// back to its own net.
func TestDisplayNeverShadowsAnotherNet(t *testing.T) {
	ids := []string{"/a/X", "/b/X", "/{slash}a{slash}X", "/c/Y",
		"/p/q{slash}r", "/{slash}p{slash}q{slash}r"} // Local of the second = unescaped full name of the first
	d := Display(ids)
	if d["/{slash}a{slash}X"] != "/{slash}a{slash}X" || d["/a/X"] != "/a/X" || d["/b/X"] != "/b/X" || d["/c/Y"] != "Y" ||
		d["/{slash}p{slash}q{slash}r"] != "/{slash}p{slash}q{slash}r" {
		t.Fatalf("Display = %v", d)
	}
	seen := map[string]string{}
	r := NewResolver(ids)
	for _, id := range ids {
		if prev, dup := seen[d[id]]; dup {
			t.Errorf("display %q for both %s and %s", d[id], prev, id)
		}
		seen[d[id]] = id
		if got, m := r.Resolve(d[id]); m != Found || got != id {
			t.Errorf("display %q of %s resolves to %q (%v)", d[id], id, got, m)
		}
	}
}

func TestEscapedSheetPaths(t *testing.T) {
	got := EscapedSheetPaths([]Sheet{
		{"/", "/"},
		{"/Power/", "/p/"},
		{"/I/O (1/2)/", "/a/"},
		{"/I/O (1/2)/A/B/", "/a/b/"},
		{"/Orphan/x/", "/q/r/"}, // parent not listed: kept as written
		{"/S/T/", "/s/"},        // root sheet "S/T" …
		{"/S/T/", "/t/u/"},      // … and (parent missing) another one written alike
	})
	for in, want := range map[string]string{
		"/":               "/",
		"/Power/":         "/Power/",
		"/I/O (1/2)/":     "/I{slash}O (1{slash}2)/",
		"/I/O (1/2)/A/B/": "/I{slash}O (1{slash}2)/A{slash}B/",
		"/Orphan/x/":      "/Orphan/x/",
		"/S/T/":           "", // ambiguous
	} {
		if got[in] != want {
			t.Errorf("EscapedSheetPaths[%q] = %q, want %q", in, got[in], want)
		}
	}
	if id := JoinEscaped("/I/O (1/2)/", got["/I/O (1/2)/"], "/I/O (1/2)/a/b"); id != "/I{slash}O (1{slash}2)/a{slash}b" {
		t.Errorf("JoinEscaped = %q", id)
	}
}
