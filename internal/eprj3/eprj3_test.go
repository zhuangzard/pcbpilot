package eprj3

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	upstreamProject  = "testdata/upstream-example/easyeda-eprj3-project"
	syntheticProject = "testdata/synthetic-minimal"
)

func mainCounts(t *testing.T, r *Report, rel string) Counts {
	t.Helper()
	for _, f := range r.Files {
		if f.Path == rel {
			if f.Main == nil || f.Main.Counts == nil {
				t.Fatalf("%s: no main document counts", rel)
			}
			return *f.Main.Counts
		}
	}
	t.Fatalf("%s not in report", rel)
	return Counts{}
}

func hasFinding(r *Report, sev, code string) *Finding {
	for i := range r.Findings {
		if r.Findings[i].Severity == sev && r.Findings[i].Code == code {
			return &r.Findings[i]
		}
	}
	return nil
}

// The real editor-written sample (kicad-to-easyeda-eprj3, Apache-2.0,
// editVersion 4.1.36) must parse clean with exact live counts.
func TestInspectUpstreamExample(t *testing.T) {
	r, err := Inspect(upstreamProject)
	if err != nil {
		t.Fatal(err)
	}
	if r.Errors != 0 || r.Warnings != 0 {
		t.Fatalf("errors=%d warnings=%d findings=%+v", r.Errors, r.Warnings, r.Findings)
	}
	inv := r.Inventory
	if inv == nil || inv.Name != "easyeda-eprj3-project" || inv.Format != "folder" ||
		strings.Join(inv.Sheets, ",") != "Schematic1/P1,Schematic1/P2" ||
		strings.Join(inv.PCBs, ",") != "PCB1" || strings.Join(inv.Panels, ",") != "Panel1" {
		t.Fatalf("inventory = %+v", inv)
	}
	// P1: A4 frame + R1 + R2 + C1 + 2 net flags; 4 WIRE rows, one deleted.
	if got, want := mainCounts(t, r, "sch/Schematic1/P1.esch2"), (Counts{Components: 6, Wires: 3, Nets: 2}); got != want {
		t.Errorf("P1 counts = %+v, want %+v", got, want)
	}
	// PCB: 5 NET rows, one deleted (the empty-name net); copper LINE winners only.
	if got, want := mainCounts(t, r, "pcb/PCB1.epcb2"), (Counts{Components: 6, Nets: 4, Tracks: 18, Pours: 1}); got != want {
		t.Errorf("PCB1 counts = %+v, want %+v", got, want)
	}
	if r.VersionMarkers.Generation != "V4" || strings.Join(r.VersionMarkers.EditVersions, ",") != "4.1.36" {
		t.Errorf("version markers = %+v", r.VersionMarkers)
	}
	for _, f := range r.Files {
		if f.Path == "pcb/PCB1.epcb2" {
			if f.LibraryDocTypes["FOOTPRINT"] != 2 || f.Main.Coordinates == nil || f.Main.Coordinates.Unit != "mil" || f.Main.Coordinates.CanvasUnit != "mil" {
				t.Errorf("pcb file report = %+v / %+v", f.LibraryDocTypes, f.Main.Coordinates)
			}
		}
	}
	// Editor output deviates from the generator schemas: advisory findings,
	// never errors or warnings.
	if r.Schema.Validated == 0 || len(r.Schema.Findings) == 0 || r.Schema.DocumentedDeviation == 0 {
		t.Errorf("schema summary = %+v", r.Schema)
	}
}

func TestInspectSyntheticMinimal(t *testing.T) {
	r, err := Inspect(syntheticProject)
	if err != nil {
		t.Fatal(err)
	}
	if !r.OK() || r.Warnings != 0 {
		t.Fatalf("findings = %+v", r.Findings)
	}
	if want := (Counts{Components: 4, Wires: 2, Nets: 4, Tracks: 2, Vias: 1, Pours: 1}); r.Totals != want {
		t.Errorf("totals = %+v, want %+v", r.Totals, want)
	}
	for _, f := range r.Files {
		if f.Path == "sch/Main/P1.esch2" {
			// WIRE ...03 deleted by an empty payload; COMPONENT ...02 superseded
			// by a later ticket (x 500 → 520) and the deletion supersedes the wire.
			if f.Main.Deleted != 1 || f.Main.Superseded != 2 {
				t.Errorf("deleted=%d superseded=%d", f.Main.Deleted, f.Main.Superseded)
			}
		}
	}
	if r.Schema.Violations != 0 || r.Schema.DocumentedDeviation != 2 {
		t.Errorf("schema = %+v", r.Schema)
	}
}

func TestInspectSingleFile(t *testing.T) {
	r, err := Inspect(filepath.Join(syntheticProject, "pcb", "PCB1.epcb2"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != "file" || r.Inventory != nil || r.Totals.Vias != 1 || !r.OK() {
		t.Fatalf("report = %+v", r)
	}
	if _, err := Inspect(filepath.Join(syntheticProject, "synthetic-minimal.txt")); err == nil {
		t.Error("missing path should error")
	}
}

func TestParseLine(t *testing.T) {
	cases := []struct {
		in      string
		typ, id string
		deleted bool
		ticket  int64
	}{
		{`{"type":"DOCHEAD"}||{"docType":"PCB"}|`, "DOCHEAD", "", false, 0},
		{`{"type":"WIRE","ticket":222,"id":"1cb9"}|||`, "WIRE", "1cb9", true, 222},
		{`{"type":"WIRE","ticket":3,"id":"x"}||`, "WIRE", "x", true, 3},
		{`{"type":"META","ticket":1,"id":"META"}||{"source":"a|b||c"}`, "META", "META", false, 1}, // last line, no terminator, pipes inside strings
	}
	for _, c := range cases {
		rec, err := ParseLine([]byte(c.in), 1)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if rec.Type != c.typ || rec.ID != c.id || rec.Deleted() != c.deleted || rec.Ticket != c.ticket {
			t.Errorf("%s → %+v", c.in, rec)
		}
	}
	for _, bad := range []string{
		`not json`,
		`{"type":"X"}{"a":1}|`,
		`{"type":"X","id":"a"}||{bad}|`,
		`{"id":"a"}||{}|`,
		`{"type":"X","id":7}||{}|`,
		`{"type":"X","ticket":"7"}||{}|`,
		`{"type":"X"`,
	} {
		if _, err := ParseLine([]byte(bad), 9); err == nil {
			t.Errorf("%q should fail", bad)
		} else if !strings.HasPrefix(err.Error(), "line 9: ") {
			t.Errorf("%q error lacks line number: %v", bad, err)
		}
	}
}

// copyProject clones the synthetic project into a temp dir for mutation.
func copyProject(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	err := filepath.Walk(syntheticProject, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(syntheticProject, p)
		if info.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return dst
}

func TestInspectMalformedInputs(t *testing.T) {
	sch := filepath.Join("sch", "Main", "P1.esch2")
	pcb := filepath.Join("pcb", "PCB1.epcb2")
	cases := []struct {
		name   string
		mutate func(t *testing.T, dir string)
		sev    string
		code   string
		line   int
	}{
		{"broken outer json", func(t *testing.T, d string) { appendLine(t, d, pcb, `{"type":"VIA",`) }, SevError, "malformed-line", 14},
		{"missing separator", func(t *testing.T, d string) { appendLine(t, d, pcb, `{"type":"VIA","id":"v","ticket":99}{"a":1}|`) }, SevError, "malformed-line", 14},
		{"record before DOCHEAD", func(t *testing.T, d string) {
			writeFile(t, d, pcb, `{"type":"VIA","id":"v","ticket":1}||{}|`+"\n")
		}, SevError, "malformed-line", 1},
		{"empty file", func(t *testing.T, d string) { writeFile(t, d, pcb, "\n\n") }, SevError, "empty-file", 0},
		{"index not json", func(t *testing.T, d string) { writeFile(t, d, "synthetic-minimal.eprj3", "{nope") }, SevError, "index-not-json", 0},
		{"index without profile", func(t *testing.T, d string) {
			writeFile(t, d, "synthetic-minimal.eprj3", `{"name":"x","format":"folder"}`)
		}, SevError, "index-no-profile", 0},
		{"missing document", func(t *testing.T, d string) { _ = os.Remove(filepath.Join(d, pcb)) }, SevWarning, "missing-document", 0},
		{"unlisted document", func(t *testing.T, d string) {
			b, _ := os.ReadFile(filepath.Join(d, pcb))
			writeFile(t, d, filepath.Join("pcb", "PCB2.epcb2"), string(b))
		}, SevWarning, "unlisted-document", 0},
		{"uuid mismatch", func(t *testing.T, d string) {
			replaceIn(t, d, pcb, `"uuid":"c000000000000001"`, `"uuid":"c00000000000ffff"`)
		}, SevWarning, "uuid-mismatch", 0},
		{"crlf", func(t *testing.T, d string) { replaceIn(t, d, pcb, "|\n", "|\r\n") }, SevWarning, "crlf", 0},
		{"dangling partId", func(t *testing.T, d string) { replaceIn(t, d, sch, `"partId":"R.1","x":300`, `"partId":"C.1","x":300`) }, SevWarning, "dangling-partId", 7},
		{"dangling attr parent", func(t *testing.T, d string) {
			replaceIn(t, d, sch, `"parentId":"2000000000000001"`, `"parentId":"ffffffffffffffff"`)
		}, SevWarning, "dangling-parentId", 12},
		{"dangling pad net", func(t *testing.T, d string) {
			replaceIn(t, d, pcb, `\"PAD_NET\",\"4000000000000001\"`, `\"PAD_NET\",\"4000000000000009\"`)
		}, SevWarning, "dangling-pad-net", 7},
		{"ticket collision", func(t *testing.T, d string) {
			appendLine(t, d, pcb, `{"type":"VIA","ticket":10,"id":"6000000000000001"}||{"netName":"GND"}|`)
		}, SevWarning, "ticket-collision", 0},
		{"mixed generations", func(t *testing.T, d string) {
			appendLine(t, d, pcb, `{"type":"DOCHEAD","ticket":1}||{"docType":"PROJECT_CONFIG","client":"0123456789abcdef","uuid":"d000000000000001"}|`)
		}, SevWarning, "mixed-generation", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := copyProject(t)
			c.mutate(t, dir)
			r, err := Inspect(dir)
			if err != nil {
				t.Fatal(err)
			}
			f := hasFinding(r, c.sev, c.code)
			if f == nil {
				t.Fatalf("no %s/%s in %+v", c.sev, c.code, r.Findings)
			}
			if c.line != 0 && f.Line != c.line {
				t.Errorf("line = %d, want %d (%s)", f.Line, c.line, f.Message)
			}
			if c.sev == SevError && r.OK() {
				t.Error("report OK despite an error finding")
			}
		})
	}
}

func TestSchemaViolationsAggregate(t *testing.T) {
	dir := copyProject(t)
	replaceIn(t, dir, filepath.Join("pcb", "PCB1.epcb2"), `"viaType":"NORMAL"`, `"viaType":"WORMHOLE"`)
	r, err := Inspect(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Schema.Violations != 1 || len(r.Schema.Findings) != 1 {
		t.Fatalf("schema = %+v", r.Schema)
	}
	f := r.Schema.Findings[0]
	if f.Schema != "t-pcb-via" || f.Path != "viaType" || f.Rule != "enum" || f.FirstLine != 11 {
		t.Errorf("finding = %+v", f)
	}
	if !r.OK() || r.Warnings != 0 {
		t.Error("schema violations are advisory and must not become errors/warnings")
	}
}

func TestDirectoryWithoutIndex(t *testing.T) {
	if _, err := Inspect(t.TempDir()); err == nil || !strings.Contains(err.Error(), "no .eprj3 index") {
		t.Fatalf("err = %v", err)
	}
}

func TestVendoredSchemasLoad(t *testing.T) {
	s, err := loadSchemas()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"t-doc-head", "tm-sch-component", "t-wire", "t-pcb-line", "t-pcb-via", "t-pcb-pour", "t-net"} {
		if s[name] == nil {
			t.Errorf("schema %s not embedded", name)
		}
	}
	if vendoredCommit() == "unknown" || vendoredCommit() == "" {
		t.Error("SOURCE.json commit missing")
	}
}

func writeFile(t *testing.T, dir, rel, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendLine(t *testing.T, dir, rel, line string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, rel, string(b)+line+"\n")
}

func replaceIn(t *testing.T, dir, rel, old, new string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), old) {
		t.Fatalf("%s does not contain %q", rel, old)
	}
	writeFile(t, dir, rel, strings.ReplaceAll(string(b), old, new))
}
