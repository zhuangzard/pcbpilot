package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// richFixture is auto.kicad_sch plus R4/R5 joined pin 2 to pin 2 by a bent
// wire labelled OUT, R4 pin 1 on a +3V3 stub, R5 pin 1 on a VREF global
// label stub.
func richFixture(t *testing.T) string {
	t.Helper()
	sheet := autoFixture(t)
	e, err := kicad.OpenSchematicFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct {
		ref string
		at  kicad.Pt
	}{{"R4", kicad.Pt{X: 63.5, Y: 101.6}}, {"R5", kicad.Pt{X: 101.6, Y: 101.6}}} {
		if _, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: "my:R", Ref: r.ref, Value: "1k", At: r.at}); err != nil {
			t.Fatal(err)
		}
	}
	e.AddWire(kicad.Pt{X: 63.5, Y: 105.41}, kicad.Pt{X: 63.5, Y: 111.76}, kicad.Pt{X: 101.6, Y: 111.76}, kicad.Pt{X: 101.6, Y: 105.41})
	if err := e.AddLabel(kicad.LabelLocal, "OUT", kicad.Pt{X: 76.2, Y: 111.76}, 0, ""); err != nil {
		t.Fatal(err)
	}
	e.AddWire(kicad.Pt{X: 63.5, Y: 97.79}, kicad.Pt{X: 63.5, Y: 92.71})
	if _, err := e.AddPower("+3V3", kicad.Pt{X: 63.5, Y: 92.71}, 0, false); err != nil {
		t.Fatal(err)
	}
	e.AddWire(kicad.Pt{X: 101.6, Y: 97.79}, kicad.Pt{X: 101.6, Y: 92.71})
	if err := e.AddLabel(kicad.LabelGlobal, "VREF", kicad.Pt{X: 101.6, Y: 92.71}, 90, "input"); err != nil {
		t.Fatal(err)
	}
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sheet, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return sheet
}

func assertCleanSheet(t *testing.T, sheet string) {
	t.Helper()
	b, _ := os.ReadFile(sheet)
	if fs := kicad.CheckSchematic(string(b), kicad.CheckOptions{}); len(fs) > 0 {
		t.Fatalf("quality findings: %s", kicad.Summary(fs, 10))
	}
}

func TestKicadRichFixtureIsClean(t *testing.T) {
	assertCleanSheet(t, richFixture(t))
}

func TestKicadGroupMoveRigidAndRerouted(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	sheet := richFixture(t)
	before := pinNets(t, sheet)
	// both ends of SIG move: the SIG wire, label and GND symbols go rigidly
	out, errs, code := runSch(t, sheet, "group-move", "--refs", "R1,R2", "--dx", "25.4", "--dy", "12.7")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	var r struct {
		Moved kicad.DragResult `json:"moved"`
	}
	_ = json.Unmarshal([]byte(out), &r)
	if r.Moved.RigidNets != 1 || r.Moved.Rerouted != 0 {
		t.Fatalf("report %s", out)
	}
	e, _ := kicad.OpenSchematicFile(sheet)
	if x, y, _, _ := e.SymbolAt("R1"); x != 76.2 || y != 63.5 {
		t.Errorf("R1 at %v,%v", x, y)
	}
	assertCleanSheet(t, sheet)
	// R5 alone: OUT is re-routed between R4 (stays) and R5
	out, errs, code = runSch(t, sheet, "group-move", "--refs", "R5", "--dx", "25.4", "--dy", "-7.62")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	_ = json.Unmarshal([]byte(out), &r)
	if r.Moved.Rerouted != 1 || r.Moved.RigidNets != 1 {
		t.Fatalf("report %s", out)
	}
	assertCleanSheet(t, sheet)
	after := pinNets(t, sheet)
	if cmp := kicad.ComparePinNets(before, after, nil); !cmp.Equal || cmp.NamesEqual != cmp.NetsA {
		t.Fatalf("netlist changed: %+v", cmp)
	}
	// onto R3: refused by the gate, file untouched
	orig, _ := os.ReadFile(sheet)
	if _, errs, code := runSch(t, sheet, "group-move", "--refs", "R4", "--dx", "25.4", "--dy", "-12.7"); code == 0 || !strings.Contains(errs, "strict gate") {
		t.Fatalf("overlap accepted: %s", errs)
	}
	now, _ := os.ReadFile(sheet)
	if !bytes.Equal(orig, now) {
		t.Fatal("refused move changed the file")
	}
}

func TestKicadLayoutPlanApplyIsOrthogonal(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	sheet := autoFixture(t)
	from := filepath.Join(filepath.Dir(sheet), "set.json")
	_ = os.WriteFile(from, []byte(autoLayout), 0o644)
	if out, errs, code := runSch(t, sheet, "layout-plan", "--from", from); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	assertCleanSheet(t, sheet)
}

func zoneComp(id, ref, n1, n2 string, rot bool) string {
	ar := ""
	if rot {
		ar = `,"allowedRotations":[0,90,180,270]`
	}
	return `{"id":"` + id + `","measurement":{"designator":"` + ref + `","x":0,"y":0,"rotation":0,"mirror":false,"bbox":{"minX":0,"minY":0,"maxX":0,"maxY":0},"pins":[{"number":"1","net":"` + n1 + `","x":0,"y":0},{"number":"2","net":"` + n2 + `","x":0,"y":0}]}` + ar + `}`
}

func TestKicadLayoutPlanZonesApply(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	sheet := richFixture(t)
	before := pinNets(t, sheet)
	in := `{"schemaVersion":1,"netPolicies":{"SIG":"direct","GND":"local_ground","OUT":"direct","+3V3":"local_power","VREF":"module_port"},
"zones":[{"id":"a","title":"DIVIDER","coreComponentId":"r1","componentIds":["r1","r2"]},{"id":"b","title":"OUTPUT","coreComponentId":"r4","componentIds":["r4","r5"]}],
"components":[` + zoneComp("r1", "R1", "SIG", "GND", false) + `,` + zoneComp("r2", "R2", "SIG", "GND", true) + `,` +
		zoneComp("r4", "R4", "+3V3", "OUT", false) + `,` + zoneComp("r5", "R5", "VREF", "OUT", true) + `]}`
	from := filepath.Join(filepath.Dir(sheet), "zones.json")
	_ = os.WriteFile(from, []byte(in), 0o644)
	out, errs, code := runSch(t, sheet, "layout-plan", "--zones", "--from", from, "--fit")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, `"verified": true`) || !strings.Contains(out, `"DIVIDER"`) {
		t.Fatalf("report %s", out)
	}
	assertCleanSheet(t, sheet)
	after := pinNets(t, sheet)
	if cmp := kicad.ComparePinNets(before, after, nil); !cmp.Equal {
		t.Fatalf("netlist changed: %+v", cmp)
	}
	// again: same frames, the old ones replaced (not duplicated)
	if out, errs, code := runSch(t, sheet, "layout-plan", "--zones", "--from", from, "--fit"); code != 0 {
		t.Fatalf("second run exit %d: %s %s", code, out, errs)
	}
	b, _ := os.ReadFile(sheet)
	if n := strings.Count(string(b), `(text "DIVIDER"`); n != 1 {
		t.Fatalf("%d DIVIDER titles after two runs", n)
	}
}
