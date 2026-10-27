package kicad

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// auto.kicad_sch's root uuid path, the sheet ERC fixtures name
const autoRootPath = "/6f1c1d40-0000-4000-8000-000000000001"

// erc-10.0.6.json is kicad-cli 10.0.6's ERC of testdata/auto.kicad_sch
// (positions 1/100 mm, KiCad #25582); erc-10.0.7.json is the same report
// as 10.0.7 writes it (mm).
func parseERCFixture(t *testing.T, name string, edit func(string) string, opt ERCParseOptions) *ERCReport {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if edit != nil {
		s = edit(s)
	}
	r, err := ParseERCReport([]byte(s), opt)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func noVersion(s string) string {
	s = strings.Replace(s, `"kicad_version": "10.0.6",`, "", 1)
	return strings.Replace(s, `"kicad_version": "10.0.7",`, "", 1)
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
	geo := ERCParseOptions{Files: map[string]string{autoRootPath: "testdata/auto.kicad_sch"}, Anchors: SheetAnchors}
	for _, c := range []struct {
		name  string
		edit  func(string) string
		opt   ERCParseOptions
		scale float64
	}{
		{"erc-10.0.6.json", nil, ERCParseOptions{}, 100},
		{"erc-10.0.7.json", nil, ERCParseOptions{}, 1},
		// no version anywhere: the sheet geometry decides
		{"erc-10.0.6.json", noVersion, geo, 100},
		{"erc-10.0.7.json", noVersion, geo, 1},
	} {
		r := parseERCFixture(t, c.name, c.edit, c.opt)
		if r.PosScale != c.scale {
			t.Errorf("%s (version %q): scale %v, want %v (%s)", c.name, r.KicadVersion, r.PosScale, c.scale, r.PosNote)
			continue
		}
		for d, p := range want {
			if got := ercItemAt(t, r, d); got != p {
				t.Errorf("%s: %s at %+v, want %+v", c.name, d, got, p)
			}
		}
		if r.Errors != 3 {
			t.Errorf("%s: %d errors", c.name, r.Errors)
		}
	}
	if r := parseERCFixture(t, "erc-10.0.6.json", nil, ERCParseOptions{}); r.KicadVersion != "10.0.6" || r.PosNote == "" {
		t.Errorf("10.0.6: version %q note %q", r.KicadVersion, r.PosNote)
	}
	// no version and no geometry: unknown, positions as reported
	r := parseERCFixture(t, "erc-10.0.6.json", noVersion, ERCParseOptions{})
	if got := ercItemAt(t, r, "Symbol R3 Pin 1"); r.PosScale != 0 || got != (Pt{0.889, 0.8509}) {
		t.Errorf("unknown: scale %v, R3.1 at %+v", r.PosScale, got)
	}
}

// A known affected version is scaled whatever its geometry: an item far
// off the page (legal in KiCad) does not switch the report to mm.
func TestParseERCReportKnownVersionOffPageItem(t *testing.T) {
	far := func(s string) string {
		return strings.Replace(s, `"x": 1.27,`, `"x": 40.0,`, 1) // R2 [R] → 4000 mm
	}
	r := parseERCFixture(t, "erc-10.0.6.json", far, ERCParseOptions{})
	if r.PosScale != 100 {
		t.Fatalf("scale %v: %s", r.PosScale, r.PosNote)
	}
	if got := ercItemAt(t, r, "Symbol R2 [R]"); got != (Pt{4000, 50.8}) {
		t.Errorf("R2 at %+v", got)
	}
	if got := ercItemAt(t, r, "Symbol R3 Pin 1"); got != (Pt{88.9, 85.09}) {
		t.Errorf("R3.1 at %+v", got)
	}
}

func TestParseERCReportAsksCLIVersion(t *testing.T) {
	b, _ := os.ReadFile("testdata/erc-10.0.6.json")
	asked := false
	r, err := ParseERCReport([]byte(noVersion(string(b))), ERCParseOptions{CLIVersion: func() string { asked = true; return "10.0.5" }})
	if err != nil {
		t.Fatal(err)
	}
	if !asked || r.KicadVersion != "10.0.5" || r.PosScale != 100 {
		t.Fatalf("asked %v, version %q, scale %v", asked, r.KicadVersion, r.PosScale)
	}
	// a report naming its version is not second-guessed by the binary
	if _, err := ParseERCReport(b, ERCParseOptions{CLIVersion: func() string { t.Fatal("asked"); return "" }}); err != nil {
		t.Fatal(err)
	}
}

func TestERCPosScale(t *testing.T) {
	on := func(pts ...Pt) func(string) []Pt { return func(string) []Pt { return pts } }
	at := func(pts ...Pt) []ercPos {
		var out []ercPos
		for _, p := range pts {
			out = append(out, ercPos{"s.kicad_sch", p})
		}
		return out
	}
	tiny := at(Pt{0.508, 0.5461}, Pt{0.889, 0.8509})
	sheet := on(Pt{50.8, 54.61}, Pt{88.9, 85.09}, Pt{127, 50.8})
	for _, c := range []struct {
		name    string
		ver     string
		pos     []ercPos
		anchors func(string) []Pt
		want    float64
	}{
		{"10.0.6", "10.0.6", tiny, nil, 100},
		{"10.0.0", "10.0.0", tiny, nil, 100},
		{"distro build", "10.0.6-1.fc42", tiny, nil, 100},
		{"10.0.6 empty", "10.0.6", nil, nil, 100},
		{"10.0.6 mm geometry ignored", "10.0.6", at(Pt{50.8, 54.61}), sheet, 100},
		{"10.0.7", "10.0.7", tiny, sheet, 1},
		{"10.1", "10.1.0", tiny, sheet, 1},
		{"11", "11.0.0", tiny, sheet, 1},
		// unknown version: geometry
		{"9.x broken", "9.0.4", tiny, sheet, 100},
		{"9.x mm", "9.0.4", at(Pt{50.8, 54.61}, Pt{88.9, 85.09}), sheet, 1},
		{"nightly broken", "10.99.0-123-gdeadbee", tiny, sheet, 100},
		{"mm near the origin", "", at(Pt{10.16, 10.16}), on(Pt{10.16, 10.16}), 1},
		{"broken, large sheet", "", at(Pt{15, 12.7}), on(Pt{1500, 1270}), 100},
		{"off-0.01-grid pin", "", at(Pt{0.8890, 0.8509}), on(Pt{88.9025, 85.09}), 100},
		{"majority broken, one stray", "", at(Pt{0.508, 0.5461}, Pt{0.889, 0.8509}, Pt{0.2, 0.3}), sheet, 100},
		{"neither", "", at(Pt{3, 4}), sheet, 0},
		{"both (origin)", "", at(Pt{0, 0}), on(Pt{0, 0}), 0},
		{"no geometry", "garbage", tiny, nil, 0},
		{"no sheet file", "", []ercPos{{"", Pt{0.508, 0.5461}}}, sheet, 0},
		{"empty report", "", nil, nil, 1},
	} {
		if got, note := ercPosScale(c.ver, c.pos, c.anchors); got != c.want {
			t.Errorf("%s: ercPosScale(%q) = %v, want %v (%s)", c.name, c.ver, got, c.want, note)
		}
	}
}

// repeatedSheetFixture writes auto.kicad_sch with R3 nudged off the 0.01 mm
// grid (88.9025) and used by two instances of a repeated sheet, /r/a as
// R101 and /r/b as R201, its Reference field still R3. Pin 1 is then at
// (88.9025, 85.09), pin 2 at (88.9025, 92.71).
func repeatedSheetFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/auto.kicad_sch")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	i := strings.Index(s, `(property "Reference" "R3"`)
	at := strings.LastIndex(s[:i], "(at 88.9 88.9 0)")
	inst := "(path \"" + autoRootPath + "\"\n\t\t\t\t\t(reference \"R3\")"
	if i < 0 || at < 0 || !strings.Contains(s[i:], inst) {
		t.Fatal("fixture changed: R3 not found")
	}
	head := s[:at] + "(at 88.9025 88.9 0)" + s[at+len("(at 88.9 88.9 0)"):i]
	tail := strings.Replace(s[i:], inst, "(path \"/r/a\" (reference \"R101\") (unit 1))\n\t\t\t\t(path \"/r/b\"\n\t\t\t\t\t(reference \"R201\")", 1)
	p := filepath.Join(t.TempDir(), "rep.kicad_sch")
	if err := os.WriteFile(p, []byte(head+tail), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestERCPinPointRepeatedSheet(t *testing.T) {
	e, err := OpenSchematicFile(repeatedSheetFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	pin1 := Pt{88.9025, 85.09}
	rep := Pt{88.9, 85.09} // 10.0.6: 0.889 ×100, the 0.0025 lost
	for _, c := range []struct {
		name, uuid, path, ref, pin string
		want                       Pt
		by                         string
	}{
		{"pin uuid", "3335ca2c-6813-45e6-b4e6-8674a5772037", "/r/b", "R201", "1", pin1, "uuid"},
		{"pin uuid, upper case", "3335CA2C-6813-45E6-B4E6-8674A5772037", "/r/a", "R101", "1", pin1, "uuid"},
		{"instance reference", "", "/r/b", "R201", "1", pin1, "reference"},
		{"other instance", "", "/r/a", "R101", "2", Pt{88.9025, 92.71}, "reference"},
	} {
		got, err := e.ERCPinPoint(c.uuid, c.path, c.ref, c.pin, &rep)
		if err != nil || got.At != c.want || got.By != c.by {
			t.Errorf("%s: %+v %v, want %+v by %s", c.name, got, err, c.want, c.by)
		}
	}
	for _, c := range []struct{ name, path, ref, pin string }{
		{"field reference is not an instance's", "/r/b", "R3", "1"},
		{"reference of another instance", "/r/a", "R201", "1"},
		{"no such pin", "/r/b", "R201", "9"},
		{"no such symbol", "/r/b", "R999", "1"},
	} {
		if got, err := e.ERCPinPoint("", c.path, c.ref, c.pin, &rep); err == nil {
			t.Errorf("%s: resolved to %+v", c.name, got)
		}
	}
}
