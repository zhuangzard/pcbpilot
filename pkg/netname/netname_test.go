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
