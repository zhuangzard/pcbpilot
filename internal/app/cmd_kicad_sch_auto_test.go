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

// autoFixture copies internal/kicad/testdata/auto.kicad_sch (R1–R2 on SIG
// and GND, R3 unconnected) into a temp dir.
func autoFixture(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile("../kicad/testdata/auto.kicad_sch")
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "auto.kicad_sch")
	if err := os.WriteFile(p, src, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func runSch(t *testing.T, sheet string, args ...string) (string, string, int) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(append(append([]string{"sch"}, args...), "--backend", "kicad", "--kicad-sch", sheet), &out, &errb)
	return out.String(), errb.String(), code
}

func pinNets(t *testing.T, sheet string) map[string]string {
	t.Helper()
	nl, err := kicad.ExportSchNetlist(sheet)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]string{}
	for k, v := range nl.PinNets() {
		m[k] = stripSheetPath(v)
	}
	return m
}

const autoSpec = `{"connections":[{"pin":"R3:1","kind":"power","net":"+3V3"},{"pin":"R3:2","kind":"gnd","net":"GND"},{"pin":"R2:1","kind":"net_label","net":"SIG"}]}`

func TestKicadAutoconnectPlanFromSheet(t *testing.T) {
	sheet := autoFixture(t)
	spec := filepath.Join(filepath.Dir(sheet), "spec.json")
	_ = os.WriteFile(spec, []byte(autoSpec), 0o644)
	before, _ := os.ReadFile(sheet)
	out, errs, code := runSch(t, sheet, "autoconnect", "--spec", spec, "--dry-run", "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	var r acReport
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	dirs := map[string]string{}
	for _, c := range r.Connections {
		if c.Selected != nil {
			dirs[c.Pin] = c.Selected.Direction
		}
	}
	// pins leave along their outward axis: R3 is vertical, pin 1 on top
	if dirs["R3:1"] != "up" || dirs["R3:2"] != "down" {
		t.Fatalf("directions %v", dirs)
	}
	if r.Connections[0].PinX != 350 || r.Connections[0].PinY != -335 {
		t.Errorf("R3:1 in planner units: %v,%v", r.Connections[0].PinX, r.Connections[0].PinY)
	}
	after, _ := os.ReadFile(sheet)
	if !bytes.Equal(before, after) {
		t.Fatal("--dry-run wrote the sheet")
	}
}

func TestKicadExpectNets(t *testing.T) {
	before := map[string]string{"R1.1": "/SIG", "R2.1": "/SIG", "R1.2": "GND", "R3.1": "unconnected-(R3-Pad1)", "R3.2": "unconnected-(R3-Pad2)"}
	planned := map[string]string{"R3.1": "+3V3", "R3.2": "GND"}
	check := kicadExpectNets(before, planned)
	good := map[string]string{"R1.1": "/SIG", "R2.1": "/SIG", "R1.2": "GND", "R3.1": "+3V3", "R3.2": "GND"}
	if err := check(good); err != nil {
		t.Fatal(err)
	}
	short := map[string]string{"R1.1": "/SIG", "R2.1": "/SIG", "R1.2": "GND", "R3.1": "GND", "R3.2": "GND"}
	if err := check(short); err == nil || !strings.Contains(err.Error(), "R3.1") {
		t.Fatalf("short accepted: %v", err)
	}
	split := map[string]string{"R1.1": "/SIG", "R2.1": "Net-(R2-Pad1)", "R1.2": "GND", "R3.1": "+3V3", "R3.2": "GND"}
	if err := check(split); err == nil {
		t.Fatal("split net accepted")
	}
}

func TestKicadAutoconnectWritesVerifiedNets(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	sheet := autoFixture(t)
	spec := filepath.Join(filepath.Dir(sheet), "spec.json")
	_ = os.WriteFile(spec, []byte(autoSpec), 0o644)
	if out, errs, code := runSch(t, sheet, "autoconnect", "--spec", spec); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	n := pinNets(t, sheet)
	want := map[string]string{"R1.1": "SIG", "R2.1": "SIG", "R1.2": "GND", "R2.2": "GND", "R3.2": "GND", "R3.1": "+3V3"}
	for p, w := range want {
		if n[p] != w {
			t.Errorf("%s on %q, want %q", p, n[p], w)
		}
	}
	// idempotent: every pin is already on its net, nothing written
	written, _ := os.ReadFile(sheet)
	out, errs, code := runSch(t, sheet, "autoconnect", "--spec", spec, "--json")
	if code != 0 || strings.Contains(out, `"state": "new"`) {
		t.Fatalf("second run: exit %d %s %s", code, out, errs)
	}
	again, _ := os.ReadFile(sheet)
	if !bytes.Equal(written, again) {
		t.Fatal("second run changed the sheet")
	}
	// a pin on another net is a conflict and --replace is refused
	if _, _, code := runSch(t, sheet, "autoconnect", "--pin", "R3:1", "--kind", "gnd", "--net", "GND", "--replace"); code == 0 {
		t.Fatal("--replace accepted on the kicad backend")
	}
}

func TestKicadSchConnect(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	sheet := autoFixture(t)
	out, errs, code := runSch(t, sheet, "connect", "--pin", "R3:1", "--kind", "netport", "--net", "OUT", "--fit")
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, `"direction": "right"`) || !strings.Contains(out, `"verified": true`) {
		t.Fatalf("report %s", out)
	}
	if n := pinNets(t, sheet); n["R3.1"] != "OUT" || n["R1.1"] != "SIG" {
		t.Fatalf("nets %v", n)
	}
}

func TestKicadCommitVerifiedLeavesFileOnMismatch(t *testing.T) {
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	sheet := autoFixture(t)
	orig, _ := os.ReadFile(sheet)
	e, err := kicad.OpenSchematicFile(sheet)
	if err != nil {
		t.Fatal(err)
	}
	// a wire that shorts SIG to GND (R1 pin 1 to pin 2)
	e.AddWire(kicad.Pt{X: 50.8, Y: 46.99}, kicad.Pt{X: 45.72, Y: 46.99}, kicad.Pt{X: 45.72, Y: 54.61}, kicad.Pt{X: 50.8, Y: 54.61})
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	before, err := kicadBeforeNets(sheet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kicadSchCommitVerified(sheet, text, false, kicadExpectNets(before, nil)); err == nil || !strings.Contains(err.Error(), "left unchanged") {
		t.Fatalf("short written: %v", err)
	}
	now, _ := os.ReadFile(sheet)
	if !bytes.Equal(orig, now) {
		t.Fatal("original changed")
	}
	if left, _ := filepath.Glob(sheet + ".*"); len(left) > 0 {
		t.Fatalf("temp files left: %v", left)
	}
}

const autoLayout = `{"schemaVersion":1,"coreComponentId":"r1","netPolicies":{"SIG":"direct","GND":"local_ground"},"components":[
{"id":"r1","measurement":{"designator":"R1","x":0,"y":0,"rotation":0,"mirror":false,"bbox":{"minX":0,"minY":0,"maxX":0,"maxY":0},"pins":[{"number":"1","net":"SIG","x":0,"y":0},{"number":"2","net":"GND","x":0,"y":0}]}},
{"id":"r2","measurement":{"designator":"R2","x":0,"y":0,"rotation":0,"mirror":false,"bbox":{"minX":0,"minY":0,"maxX":0,"maxY":0},"pins":[{"number":"1","net":"SIG","x":0,"y":0},{"number":"2","net":"GND","x":0,"y":0}]},"allowedRotations":[0,90,180,270]}]}`

func TestKicadLayoutPlanApplyKeepsNetlist(t *testing.T) {
	sheet := autoFixture(t)
	from := filepath.Join(filepath.Dir(sheet), "set.json")
	_ = os.WriteFile(from, []byte(autoLayout), 0o644)
	if _, _, code := runSch(t, sheet, "layout-plan", "--from", from, "--zones"); code == 0 {
		t.Fatal("--zones accepted on the kicad backend")
	}
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	before := pinNets(t, sheet)
	layout := filepath.Join(filepath.Dir(sheet), "layout.json")
	out, errs, code := runSch(t, sheet, "layout-plan", "--from", from, "--out", layout)
	if code != 0 {
		t.Fatalf("exit %d: %s %s", code, out, errs)
	}
	if !strings.Contains(out, `"verified": true`) {
		t.Fatalf("report %s", out)
	}
	if _, err := os.Stat(layout); err != nil {
		t.Fatal("layout not written to --out")
	}
	e, _ := kicad.OpenSchematicFile(sheet)
	if x, y, _, _ := e.SymbolAt("R1"); x != 50.8 || y != 50.8 {
		t.Errorf("core moved to %v,%v", x, y)
	}
	if x, _, _, _ := e.SymbolAt("R2"); x == 127 {
		t.Error("R2 not moved")
	}
	after := pinNets(t, sheet)
	if cmp := kicad.ComparePinNets(before, after, nil); !cmp.Equal || cmp.NamesEqual != cmp.NetsA {
		t.Fatalf("netlist changed: %+v", cmp)
	}
}
