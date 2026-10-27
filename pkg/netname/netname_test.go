package netname

import "testing"

func TestCanonicalKeepsSlashInLabels(t *testing.T) {
	sheets := []string{"/", "/pic_sockets/", "/pic_sockets/inner/"}
	for in, want := range map[string]string{
		"/VPP/MCLR":                  "/VPP{slash}MCLR", // root-sheet local label "VPP/MCLR"
		"/pic_sockets/VCC_PIC":       "/pic_sockets/VCC_PIC",
		"/pic_sockets/A/B":           "/pic_sockets/A{slash}B",
		"/pic_sockets/inner/X":       "/pic_sockets/inner/X",
		"GND":                        "GND",
		"Net-(U5-OSC1/CLKI)":         "Net-(U5-OSC1{slash}CLKI)",
		"unconnected-(U4-S/S-Pad4)":  "unconnected-(U4-S{slash}S-Pad4)",
		"/VPP{slash}MCLR":            "/VPP{slash}MCLR", // idempotent
		"/pic_sockets/VCC_PIC/extra": "/pic_sockets/VCC_PIC{slash}extra",
	} {
		if got := Canonical(in, sheets); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Canonical("/A/B", nil); got != "/A{slash}B" {
		t.Errorf("no sheets known: %q", got)
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
		if got, ok := r.Resolve(in); !ok || got != want {
			t.Errorf("Resolve(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"SDA", "MCLR", "unconnected-(J1-Pad1)", "nope"} {
		if got, ok := r.Resolve(in); ok {
			t.Errorf("Resolve(%q) = %q: must not resolve (ambiguous / not a net / cut at '/')", in, got)
		}
	}
}
