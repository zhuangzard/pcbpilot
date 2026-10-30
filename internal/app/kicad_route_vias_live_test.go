package app

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// The added-padstack path end to end with KiCad and the real fastroute:
// two classes whose componentwise-maximum via exists in neither (A 0.8/0.2,
// GND 0.7/0.4 — both nets need vias on the 2-layer test board), KiCad's DSN
// export, dsnRouterVia, fastroute, KiCad's SES import (router vias come in
// as 0.8/0.4: the drill is read from the padstack name), the shrink to each
// class via, and a KiCad DRC without annular-ring / hole errors. Needs
// PCBPILOT_KICAD_LIVE=1 and PCBPILOT_LIVE_FASTROUTE.
func TestRouterViaPadstackKiCadLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" || os.Getenv("PCBPILOT_LIVE_FASTROUTE") == "" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 and PCBPILOT_LIVE_FASTROUTE (a fastroute executable)")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Skip(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "tiny.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if k := os.Getenv("PCBPILOT_LIVE_KEEP_DIR"); k != "" { // keep the files for a look
		dir = k
		_ = os.MkdirAll(dir, 0o755)
	}
	pcb := filepath.Join(dir, "vias.kicad_pcb")
	if err := os.WriteFile(pcb, src, 0o644); err != nil {
		t.Fatal(err)
	}
	pro := `{"board":{"design_settings":{"rules":{"min_via_annular_width":0.05,"min_through_hole_diameter":0.15,"min_via_diameter":0.4,"min_clearance":0.15,"min_track_width":0.15}}},
 "net_settings":{"classes":[
  {"name":"Default","clearance":0.2,"track_width":0.25,"via_diameter":0.6,"via_drill":0.3,"priority":2147483647},
  {"name":"C_A","clearance":0.2,"track_width":0.25,"via_diameter":0.8,"via_drill":0.2,"priority":0},
  {"name":"C_GND","clearance":0.2,"track_width":0.25,"via_diameter":0.7,"via_drill":0.4,"priority":1}],
  "netclass_patterns":[{"netclass":"C_A","pattern":"A"},{"netclass":"C_GND","pattern":"GND"}]},
 "meta":{"filename":"vias.kicad_pro","version":3}}`
	if err := os.WriteFile(boardSibling(pcb, ".kicad_pro"), []byte(pro), 0o644); err != nil {
		t.Fatal(err)
	}
	rb0, err := kt.RulesRead(pcb)
	if err != nil {
		t.Fatal(err)
	}
	if v := rb0.Nets["A"]; v.ViaDiameter != 800000 || v.ViaDrill != 200000 {
		t.Fatalf("class C_A not applied: %+v", v)
	}
	if v := rb0.Nets["GND"]; v.ViaDiameter != 700000 || v.ViaDrill != 400000 {
		t.Fatalf("class C_GND not applied: %+v", v)
	}
	dsn := filepath.Join(dir, "route.dsn")
	if _, err := kt.ExportDSN(pcb, dsn); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(dsn)
	routed, order, top := dsnRouterVia(string(text))
	if top == nil || top.dia != 800 || top.drill != 400 || !strings.Contains(routed, "(padstack "+top.name) || !strings.HasSuffix(top.name, `_800:400_um"`) {
		t.Fatalf("router via %+v order %v", top, order)
	}
	// Only the classes' vias: the router via must be one the DSN did not have.
	if strings.Contains(string(text), "800:400") {
		t.Fatal("the test needs a maximum via no class has")
	}
	// fastroute's autorouter takes each class's use_via (A, GND above); the
	// first via of the list is what its fanout / default path takes. To put
	// the added padstack on real router vias, the Default class uses it.
	defUse := regexp.MustCompile(`(\(class kicad_default[^()]*\(circuit\s*\(use_via )"[^"]*"`)
	if !defUse.MatchString(routed) {
		t.Fatalf("no Default class use_via in the DSN: %s", routed)
	}
	routed = defUse.ReplaceAllString(routed, "${1}"+top.name)
	if err := os.WriteFile(dsn, []byte(routed), 0o644); err != nil {
		t.Fatal(err)
	}
	ses, runs, err := runFastroute(fastrouteOpts{bin: os.Getenv("PCBPILOT_LIVE_FASTROUTE"), maxTime: 30 * time.Second, timeout: 3 * time.Minute}, dsn, filepath.Join(dir, "route"), os.Stderr)
	if err != nil {
		t.Fatalf("%v %+v", err, runs)
	}
	imported := filepath.Join(dir, "imported.kicad_pcb")
	if _, err := kt.ImportSES(pcb, ses, imported); err != nil {
		t.Fatal(err)
	}
	r := &kicadRun{kt: kt, work: dir, summary: map[string]any{}, stderr: os.Stderr}
	before, _, err := r.snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	after, _, err := r.snapshot(imported)
	if err != nil {
		t.Fatal(err)
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.01 }
	um := func(v float64) float64 { return v * 25.4 }
	class := map[string][2]float64{"A": {800, 200}, "GND": {700, 400}}
	nets := map[string]int{}
	for _, v := range snapshotVias(after) {
		nets[v.Net]++
		want, ok := class[v.Net]
		if !ok {
			want = [2]float64{800, 400} // the added padstack: drill read from its name
		}
		if !near(um(v.Dia), want[0]) || !near(um(v.Drill), want[1]) {
			t.Fatalf("router via %s on %s imported as %.1f/%.1f µm, want %v", v.ID, v.Net, um(v.Dia), um(v.Drill), want)
		}
	}
	if nets["A"] == 0 || nets["GND"] == 0 || len(nets) < 3 {
		t.Fatalf("the routed test board needs vias on A, GND and a Default net: %v", nets)
	}
	out, gates, err := r.routerViasToClass(before, imported)
	if err != nil || len(gates) != 0 || out == imported || r.summary["routerVias"].(map[string]any)["shrunkToClass"].(int) != nets["VCC"]+nets["B"]+nets["C"]+nets["D"] {
		t.Fatalf("shrink: %v gates %+v", err, gates)
	}
	final, raw, err := r.snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range snapshotVias(final) {
		want, ok := class[v.Net]
		if !ok {
			want = [2]float64{600, 300} // Default
		}
		if !near(um(v.Dia), want[0]) || !near(um(v.Drill), want[1]) {
			t.Fatalf("via on %s %.1f/%.1f µm, want its class via %v", v.Net, um(v.Dia), um(v.Drill), want)
		}
	}
	rep, err := kt.DRC(out, filepath.Join(dir, "drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rep.Violations {
		switch v.Rule {
		case "annular_width", "drill_out_of_range", "hole_size", "via_diameter", "hole_clearance", "hole_to_hole":
			t.Fatalf("DRC %s: %s", v.Rule, v.Message)
		}
	}
	var dump struct{}
	_ = json.Unmarshal(raw, &dump)
}
