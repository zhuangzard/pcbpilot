package kicad

import (
	"os"
	"strings"
	"testing"
)

// erc-10.0.6.json is kicad-cli 10.0.6's ERC of testdata/auto.kicad_sch
// (positions 1/100 mm, KiCad #25582); erc-10.0.7.json is the same report
// as 10.0.7 writes it (mm).
func parseERCFixture(t *testing.T, name string, edit func(string) string) *ERCReport {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if edit != nil {
		s = edit(s)
	}
	r, err := ParseERCReport([]byte(s), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// ercItemAt is the position of the first item whose description has desc.
func ercItemAt(t *testing.T, r *ERCReport, desc string) Pt {
	t.Helper()
	for _, v := range r.Violations {
		for _, it := range v.Items {
			if strings.Contains(it.Description, desc) {
				return it.Pos
			}
		}
	}
	t.Fatalf("no ERC item %q", desc)
	return Pt{}
}

func TestParseERCReportScalesPositions(t *testing.T) {
	// auto.kicad_sch: #PWR0001 (GND, pin at its origin) at (50.8, 54.61),
	// R3 at (88.9, 88.9) with pin 1 at (88.9, 85.09)
	want := map[string]Pt{
		"Symbol #PWR0001 Hidden pin 1": {50.8, 54.61},
		"Symbol R3 Pin 1":              {88.9, 85.09},
		"Symbol R2 [R]":                {127, 50.8},
	}
	for _, name := range []string{"erc-10.0.6.json", "erc-10.0.7.json"} {
		r := parseERCFixture(t, name, nil)
		for d, p := range want {
			if got := ercItemAt(t, r, d); got != p {
				t.Errorf("%s: %s at %+v, want %+v", name, d, got, p)
			}
		}
		if r.Errors != 3 {
			t.Errorf("%s: %d errors", name, r.Errors)
		}
	}
	if r := parseERCFixture(t, "erc-10.0.6.json", nil); r.PosScale != 100 || r.KicadVersion != "10.0.6" || r.PosNote == "" {
		t.Errorf("10.0.6: scale %v version %q note %q", r.PosScale, r.KicadVersion, r.PosNote)
	}
	if r := parseERCFixture(t, "erc-10.0.7.json", nil); r.PosScale != 1 || r.PosNote != "" {
		t.Errorf("10.0.7: scale %v note %q", r.PosScale, r.PosNote)
	}
}

func TestParseERCReportAsksCLIVersion(t *testing.T) {
	drop := func(s string) string { return strings.Replace(s, `"kicad_version": "10.0.6",`, "", 1) }
	b, _ := os.ReadFile("testdata/erc-10.0.6.json")
	asked := false
	r, err := ParseERCReport([]byte(drop(string(b))), nil, func() string { asked = true; return "10.0.5" })
	if err != nil {
		t.Fatal(err)
	}
	if !asked || r.KicadVersion != "10.0.5" || r.PosScale != 100 {
		t.Fatalf("asked %v, version %q, scale %v", asked, r.KicadVersion, r.PosScale)
	}
	// a report naming its version is not second-guessed by the binary
	if _, err := ParseERCReport(b, nil, func() string { t.Fatal("asked"); return "" }); err != nil {
		t.Fatal(err)
	}
}

func TestERCPosScale(t *testing.T) {
	tiny := []Pt{{0.508, 0.5461}, {1.27, 0.889}}
	mm := []Pt{{50.8, 54.61}, {127, 88.9}}
	for _, c := range []struct {
		ver  string
		pos  []Pt
		want float64
	}{
		{"10.0.6", tiny, 100},
		{"10.0.0", tiny, 100},
		{"10.0.6-1.fc42", tiny, 100},
		{"10.0.6", nil, 100},
		{"10.0.6", []Pt{{40, 1}}, 1}, // 4000 mm scaled: beyond any page, a patched build
		{"10.0.7", tiny, 1},
		{"10.0.7", mm, 1},
		{"10.1.0", tiny, 1},
		{"11.0.0", tiny, 1},
		// unknown: 9.x, a nightly, none — decided from the positions
		{"9.0.4", tiny, 100},
		{"9.0.4", mm, 1},
		{"10.99.0-123-gdeadbee", tiny, 100},
		{"", mm, 1},
		{"", nil, 1},
		{"garbage", []Pt{{11.89, 8.41}}, 100},
		{"garbage", []Pt{{11.89, 12.5}}, 1},
	} {
		if got, _ := ercPosScale(c.ver, c.pos); got != c.want {
			t.Errorf("ercPosScale(%q, %v) = %v, want %v", c.ver, c.pos, got, c.want)
		}
	}
}
