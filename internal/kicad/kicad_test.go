package kicad

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDRC(t *testing.T) {
	data, err := os.ReadFile("testdata/drc.json")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ParseDRC(data)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Total != 3 || rep.Counts["clearance"] != 1 || rep.Counts["track_width"] != 1 || rep.Counts["unconnected_items"] != 1 {
		t.Fatalf("counts: total %d %v", rep.Total, rep.Counts)
	}
	v := rep.Violations[0]
	if v.Rule != "clearance" || v.ObjType != "Track to Via" || v.Net != "VCC / GND" || v.Layer != "F.Cu" {
		t.Fatalf("first violation: %+v", v)
	}
	// 25.4 mm, 50.8 mm (y-down) → 1000 mil, -2000 mil (y-up).
	if v.X == nil || math.Abs(*v.X-1000) > 1e-6 || math.Abs(*v.Y+2000) > 1e-6 {
		t.Fatalf("position: %v %v", *v.X, *v.Y)
	}
	if len(v.Objs) != 2 || v.Objs[1] != "u-via-1" || !strings.Contains(v.Message, "Via [GND]") {
		t.Fatalf("objs/message: %+v", v)
	}
	u := rep.Violations[2]
	if u.Section != "unconnected_items" || u.ObjType != "Pad 1 to Pad 2" || u.Net != "VCC" {
		t.Fatalf("unconnected: %+v", u)
	}
	if got := rep.SortedCounts(); len(got) != 3 || got[0] != "clearance: 1" {
		t.Fatalf("sorted counts %v", got)
	}
	if _, err := ParseDRC([]byte("{")); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestPrepareDSN(t *testing.T) {
	data, err := os.ReadFile("testdata/classes.dsn")
	if err != nil {
		t.Fatal(err)
	}
	classes := []NetClass{
		{Name: "PP1_W20", Nets: []string{"VCC", "/sub/V-"}, TrackWidthMil: 20, InnerWidthMil: 30, MinMil: 20, ClearanceMil: 6},
		{Name: "PP2_W10", Nets: []string{"GND"}, TrackWidthMil: 10, InnerWidthMil: 10, MinMil: 6, ClearanceMil: 4},
	}
	reqs := map[string]NetRequirement{
		"VCC":     {OuterMil: 20, InnerMil: 30, MinMil: 20, ClearanceMil: 6},
		"/sub/V-": {OuterMil: 20, InnerMil: 30, MinMil: 20, ClearanceMil: 6},
		"GND":     {OuterMil: 10, InnerMil: 10, MinMil: 6, ClearanceMil: 4},
	}
	out, prep, err := PrepareDSN(string(data), classes, reqs)
	if err != nil {
		t.Fatal(err)
	}
	if len(prep.Short) != 0 {
		t.Fatalf("short: %v", prep.Short)
	}
	if prep.Renamed["PP1_W20,Power"] != "PP1_W20+Power" || strings.Contains(out, "PP1_W20,Power") {
		t.Fatalf("rename: %v", prep.Renamed)
	}
	if len(prep.NoNeckdown) != 1 || prep.NoNeckdown[0] != "PP1_W20+Power" || prep.MinTraceMil != 6 {
		t.Fatalf("no-neckdown %v floor %v", prep.NoNeckdown, prep.MinTraceMil)
	}
	if !strings.Contains(out, "(layer_rule In1.Cu In2.Cu (rule (width 762)))") || len(prep.InnerRules) != 1 {
		t.Fatalf("inner rule missing: %v\n%s", prep.InnerRules, out)
	}
	if !strings.Contains(out, `(class PP1_W20+Power VCC "/sub/V-"`) || !strings.Contains(out, `(class kicad_default "" SIG`) {
		t.Fatalf("class headers not preserved:\n%s", out)
	}
	// 0.2 mil clearance margin in DSN units (um): 152.4 + 5.08.
	if !strings.Contains(out, "(clearance 157.48)") || prep.ClearanceMarginMil != 0.2 {
		t.Fatalf("clearance margin missing")
	}
	if math.Abs(prep.NarrowestClassMil-200/25.4) > 1e-6 {
		t.Fatalf("narrowest class %v", prep.NarrowestClassMil)
	}
	if strings.Count(out, "(") != strings.Count(out, ")") {
		t.Fatal("unbalanced output")
	}
	// The prepared DSN passes its own check again (idempotent shape).
	if _, p2, err := PrepareDSN(out, classes, reqs); err != nil || len(p2.Short) != 0 {
		t.Fatalf("re-check: %v %v", err, p2.Short)
	}

	// A requirement the class does not meet stops routing.
	reqs["GND"] = NetRequirement{OuterMil: 12, InnerMil: 12, ClearanceMil: 5}
	reqs["NC"] = NetRequirement{OuterMil: 8}
	_, prep, err = PrepareDSN(string(data), classes, reqs)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"GND: outer width 10 < 12 mil", "GND: inner width 10 < 12 mil", "GND: clearance 4.2 < 5 mil", "NC: no net class in the DSN"}
	for _, w := range want {
		if !contains(prep.Short, w) {
			t.Errorf("missing %q in %v", w, prep.Short)
		}
	}
}

func TestParseBridgeOutput(t *testing.T) {
	out := []byte("noise\nswig/python detected a memory leak PCBPILOT_JSON:{\"ok\":true,\"n\":1}\nmore noise\n")
	raw, err := parseBridgeOutput(out)
	if err != nil || string(raw) != `{"ok":true,"n":1}` {
		t.Fatalf("%s %v", raw, err)
	}
	if _, err := parseBridgeOutput([]byte("nothing")); err == nil {
		t.Fatal("missing marker accepted")
	}
	if _, err := parseBridgeOutput([]byte("PCBPILOT_JSON:{oops")); err == nil {
		t.Fatal("bad JSON accepted")
	}
}

func TestToolPaths(t *testing.T) {
	app := t.TempDir()
	for _, v := range []string{"3.9", "3.11"} {
		p := filepath.Join(app, "Contents/Frameworks/Python.framework/Versions", v, "bin")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "python3"), nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	py, cli := toolPaths("darwin", app)
	// Lexical order: "3.9" sorts after "3.11"; any existing interpreter is fine.
	if !strings.HasSuffix(py, "/bin/python3") || !strings.HasSuffix(cli, "Contents/MacOS/kicad-cli") {
		t.Fatalf("%s %s", py, cli)
	}
	if py, cli := toolPaths("linux", "/usr"); py != "/usr/bin/python3" || cli != "/usr/bin/kicad-cli" {
		t.Fatalf("linux %s %s", py, cli)
	}
	t.Setenv(EnvApp, app)
	t.Setenv(EnvPython, filepath.Join(app, "nope"))
	t.Setenv(EnvCLI, filepath.Join(app, "nope-cli"))
	if _, err := Locate(); err == nil || !strings.Contains(err.Error(), EnvPython) {
		t.Fatalf("Locate with missing tools: %v", err)
	}
}

func TestArcAngleInBridge(t *testing.T) {
	// The bridge is embedded; make sure what ships is the file in the tree.
	src, err := os.ReadFile("bridge.py")
	if err != nil {
		t.Fatal(err)
	}
	if string(src) != string(bridgeSource) {
		t.Fatal("embedded bridge differs from bridge.py")
	}
	for _, sub := range []string{"def snapshot(", "def netclasses(", "def dsn(", "def ses(", "def fill(", resultPrefix} {
		if !strings.Contains(string(src), sub) {
			t.Errorf("bridge.py lacks %q", sub)
		}
	}
}

func TestPrepareDSNEdgeKeepouts(t *testing.T) {
	dsn := `(pcb board.dsn
  (parser
    (string_quote ")
  )
  (resolution um 10)
  (unit um)
  (structure
    (layer F.Cu (type signal))
    (layer In1.Cu (type signal))
    (layer In2.Cu (type signal))
    (layer B.Cu (type signal))
    (boundary
      (path pcb 0  0 0  100000 0  100000 50000  0 50000  0 0)
    )
    (keepout "" (polygon signal 0  40000 20000  42000 20000  42000 22000  40000 22000  40000 20000))
    (via "Via[0-3]_600:300_um")
    (rule (width 200) (clearance 150))
  )
  (library
    (image MH
      (keepout "" (circle F.Cu 3200))
      (keepout "" (circle B.Cu 3200 0 0))
    )
  )
  (network
    (net A (pins R1-1))
  )
  (wiring
  )
)`
	out, prep, err := PrepareDSN(dsn, nil, nil, EdgeKeepout{OuterMil: 20, InnerMil: 30})
	if err != nil {
		t.Fatal(err)
	}
	// 4 layers × (4 outline bands + 4 cut-out bands) + 2 grown circles.
	if prep.EdgeKeepouts != 34 {
		t.Fatalf("keepouts %d\n%s", prep.EdgeKeepouts, out)
	}
	// Outer band 20 mil = 508 um inward from the bottom edge (CCW outline).
	if !strings.Contains(out, `(keepout "pcbpilot_edge_F.Cu_0" (polygon F.Cu 0 0 0 100000 0 100000 508 0 508 0 0))`) {
		t.Fatalf("outer band missing:\n%s", out)
	}
	if !strings.Contains(out, `(keepout "pcbpilot_edge_In1.Cu_0" (polygon In1.Cu 0 0 0 100000 0 100000 762 0 762 0 0))`) {
		t.Fatalf("inner band missing")
	}
	// Cut-out band outside the cut-out (y below 20000).
	if !strings.Contains(out, `(keepout "pcbpilot_cutout0_F.Cu_0" (polygon F.Cu 0 40000 20000 42000 20000 42000 19492 40000 19492 40000 20000))`) {
		t.Fatalf("cut-out band missing:\n%s", out)
	}
	// Mounting hole circles grow by 2 × 20 mil = 1016 um.
	if !strings.Contains(out, `(circle F.Cu 4216)`) || !strings.Contains(out, `(circle B.Cu 4216 0 0)`) {
		t.Fatalf("hole circles not grown:\n%s", out)
	}
	if strings.Count(out, "(") != strings.Count(out, ")") {
		t.Fatal("unbalanced")
	}
}

func TestDRCSplitErrors(t *testing.T) {
	rep := &DRCReport{Violations: []Violation{{Rule: "clearance", Severity: "error"}, {Rule: "silk_overlap", Severity: "warning"},
		{Rule: "silk_overlap", Severity: "warning"}, {Rule: "unconnected_items", Severity: "error"}}}
	errs, warn, n := rep.SplitErrors()
	if errs.Total != 2 || errs.Counts["clearance"] != 1 || errs.Counts["unconnected_items"] != 1 || n != 2 || warn["silk_overlap"] != 2 {
		t.Fatalf("%+v %v %d", errs, warn, n)
	}
}
