package app

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func sbNeedKicad(t *testing.T) {
	t.Helper()
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip("kicad-cli not installed")
	}
	if kicad.StockSymbolDir() == "" {
		t.Skip("KiCad stock symbol library not found")
	}
}

func sbGateStatus(r *sbReport, name string) string {
	for _, g := range r.Gates {
		if g.Name == name {
			return g.Status
		}
	}
	return ""
}

func TestSbSafeJoin(t *testing.T) {
	base := t.TempDir()
	for _, bad := range []string{"../x", "a/../../x", "/etc/passwd", "..", "a\\..\\b", "", "x/..", "lcsc.pretty/../../y.kicad_mod"} {
		if _, err := sbSafeJoin(base, bad); err == nil {
			t.Errorf("sbSafeJoin(%q) accepted", bad)
		}
	}
	for _, ok := range []string{"a.kicad_sch", "lcsc.pretty/SOT-223-3_L6.5-W3.4.kicad_mod", "TYPE-C 16PIN 2MD(073).kicad_mod"} {
		if _, err := sbSafeJoin(base, ok); err != nil {
			t.Errorf("sbSafeJoin(%q): %v", ok, err)
		}
	}
	if got := sbSheetFile("../../evil"); strings.Contains(got, "/") || strings.Contains(got, "..") {
		t.Errorf("sheet file %q", got)
	}
}

// TestSchBuildMaliciousSpec: names from the spec never escape --out.
func TestSchBuildMaliciousSpec(t *testing.T) {
	sbNeedKicad(t)
	root := t.TempDir()
	out := filepath.Join(root, "proj")
	spec := &sbSpec{Name: "../../escape", Pages: []sbPage{{ID: "../../p1"}, {ID: "/abs/p2"}},
		Parts: []sbPart{
			{Ref: "R1", Value: "1k", Symbol: "Device:R", Page: "../../p1"},
			{Ref: "R2", Value: "1k", Symbol: "Device:R", Page: "/abs/p2"},
		},
		Nets: []sbNet{{Name: "A", Pins: []string{"R1:1", "R2:1"}}, {Name: "GND", Pins: []string{"R1:2", "R2:2"}}}}
	res, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, Jobs: 2, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if sbGateStatus(res, "netlist") != "pass" {
		t.Fatalf("netlist gate: %+v", res.Gates)
	}
	ents, _ := os.ReadDir(root)
	if len(ents) != 1 || ents[0].Name() != "proj" {
		var n []string
		for _, e := range ents {
			n = append(n, e.Name())
		}
		t.Fatalf("files outside --out: %v", n)
	}
	filepath.Walk(out, func(p string, info os.FileInfo, err error) error {
		if rel, _ := filepath.Rel(out, p); strings.HasPrefix(rel, "..") {
			t.Errorf("%s outside %s", p, out)
		}
		return nil
	})
	// an unsafe library nickname from a symbol path is refused
	lib := filepath.Join(root, "x y.kicad_sym")
	_ = os.WriteFile(lib, []byte(testSymLib), 0o644)
	bad := &sbSpec{Parts: []sbPart{{Ref: "R1", Symbol: lib + ":R"}}, Nets: []sbNet{{Name: "A", Pins: []string{"R1:1"}}}}
	if _, err := runSchBuild(bad, sbOptions{OutDir: filepath.Join(root, "p2"), NoIntent: true}, nil); err == nil || !strings.Contains(err.Error(), "safe") {
		t.Fatalf("unsafe library nickname accepted: %v", err)
	}
}

// TestSchBuildSmall builds the small spec (regulator + MCU-like part with
// generated symbols, stock passives): netlist == spec, ERC and quality
// clean; then sch-read round trip, sch-edit, dry-run and checkpoint restore.
func TestSchBuildSmall(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	out := filepath.Join(t.TempDir(), "small")
	spec, err := loadSbSpec("testdata/sch-build/small.json")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	res, err := runSchBuild(spec, sbOptions{OutDir: out, Jobs: 4, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	t.Logf("small build: %v, timings %+v", time.Since(t0), res.Timings)
	for _, g := range []string{"netlist", "erc", "quality", "connectivity", "intent", "page-fit"} {
		if s := sbGateStatus(res, g); s != "pass" {
			t.Errorf("gate %s = %s: %+v", g, s, res.Gates)
		}
	}
	if !res.OK || res.Checkpoint != 1 {
		t.Fatalf("ok=%v checkpoint=%d", res.OK, res.Checkpoint)
	}
	for _, f := range []string{"small.kicad_pro", "small.kicad_sch", "sym-lib-table", "connectivity.json", "intent.json", "erc.json"} {
		if !fileExists(filepath.Join(out, f)) {
			t.Errorf("missing %s", f)
		}
	}
	// connectivity.json carries the spec nets for intent derive / PCB
	var conn struct {
		Nets []struct{ Name string } `json:"nets"`
	}
	b, _ := os.ReadFile(filepath.Join(out, "connectivity.json"))
	_ = json.Unmarshal(b, &conn)
	if len(conn.Nets) != 6 {
		t.Errorf("connectivity nets = %d, want 6", len(conn.Nets))
	}

	// sch-read → sch-build elsewhere gives the same netlist (gate) again
	rd, err := readSbSpec(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rd.Parts) != 9 || len(rd.Nets) != 6 {
		t.Fatalf("sch-read: %d parts %d nets", len(rd.Parts), len(rd.Nets))
	}
	res2, err := runSchBuild(rd, sbOptions{OutDir: filepath.Join(t.TempDir(), "copy"), NoIntent: true, Jobs: 4, PlannerBudget: 20000}, nil)
	if err != nil || sbGateStatus(res2, "netlist") != "pass" {
		t.Fatalf("rebuild from sch-read: %v %+v", err, res2)
	}

	// dry-run edit writes nothing
	ops := []sbOp{
		{Op: "add_part", Part: &sbPart{Ref: "R3", Value: "4k7", Symbol: "Device:R", Zone: "mcu"}},
		{Op: "connect", Net: "SWDIO", Pins: []string{"U2:SWDIO", "R3:1"}},
		{Op: "connect", Net: "+3V3", Pins: []string{"R3:2"}},
		{Op: "rename_net", From: "LED_A", To: "LED_ANODE"},
	}
	before, _ := os.ReadFile(filepath.Join(out, "small.kicad_sch"))
	dr, err := runSchEdit(sbOptions{OutDir: out, DryRun: true, Jobs: 4, PlannerBudget: 20000}, ops)
	if err != nil || dr.Plan == nil {
		t.Fatalf("dry-run: %v", err)
	}
	if after, _ := os.ReadFile(filepath.Join(out, "small.kicad_sch")); !bytes.Equal(before, after) {
		t.Fatal("dry-run changed the sheet")
	}
	// the real edit: LED zone is untouched (rename keeps its layout)
	er, err := runSchEdit(sbOptions{OutDir: out, NoIntent: true, Jobs: 4, PlannerBudget: 20000}, ops)
	if err != nil || sbGateStatus(er, "netlist") != "pass" {
		t.Fatalf("edit: %v %+v", err, er)
	}
	for _, z := range er.Zones {
		if z.ID == "led" && !z.Reused {
			t.Errorf("led zone was re-planned by an edit that does not touch it")
		}
	}
	sch, _ := os.ReadFile(filepath.Join(out, "small.kicad_sch"))
	if !strings.Contains(string(sch), `"LED_ANODE"`) || !strings.Contains(string(sch), `(reference "R3")`) {
		t.Error("edit not in the sheet")
	}
	// restore checkpoint 1
	if _, err := restoreSbCheckpoint(out, 1); err != nil {
		t.Fatal(err)
	}
	if sch, _ := os.ReadFile(filepath.Join(out, "small.kicad_sch")); !bytes.Equal(sch, before) {
		t.Error("restore did not bring back checkpoint 1")
	}
	_, sp := loadSbState(out)
	for _, p := range sp.Parts {
		if p.Ref == "R3" {
			t.Error("restored spec still has R3")
		}
	}
}

// TestSchBuildFromConnectivity regenerates an EasyEDA connectivity IR
// document (no LCSC numbers → generated symbols) offline.
func TestSchBuildFromConnectivity(t *testing.T) {
	sbNeedKicad(t)
	dir := t.TempDir()
	doc := `{"schemaVersion":"1.4","projectId":"demo","components":[
	 {"id":"c1","ref":"U1","device":{"deviceUuid":"x","name":"REG"},"pins":[{"number":"1","name":"IN"},{"number":"2","name":"GND"},{"number":"3","name":"OUT"},{"number":"4","name":"NC","noConnected":true}]},
	 {"id":"c2","ref":"C1","device":{"deviceUuid":"y","name":"10u"},"pins":[{"number":"1","name":"1"},{"number":"2","name":"2"}]}],
	 "nets":[{"id":"n1","name":"VIN"},{"id":"n2","name":"GND"},{"id":"n3","name":"VOUT"}],
	 "connections":[{"componentId":"c1","pinNumber":"1","netId":"n1"},{"componentId":"c1","pinNumber":"2","netId":"n2"},
	  {"componentId":"c1","pinNumber":"3","netId":"n3"},{"componentId":"c2","pinNumber":"1","netId":"n3"},{"componentId":"c2","pinNumber":"2","netId":"n2"}]}`
	p := filepath.Join(dir, "conn.json")
	_ = os.WriteFile(p, []byte(doc), 0o644)
	spec, err := specFromConnectivity([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	res, err := runSchBuild(spec, sbOptions{OutDir: filepath.Join(dir, "out"), Offline: true, NoIntent: true, Jobs: 2, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sbGateStatus(res, "netlist") != "pass" || res.Resolve.Generated != 2 {
		t.Fatalf("gates %+v symbols %+v", res.Gates, res.Resolve)
	}
}

func TestCompareSbNetlist(t *testing.T) {
	d := &sbDesign{Nets: []sbNet{{Name: "A"}, {Name: "B"}}, PinNet: map[string]string{"R1.1": "A", "R2.1": "A", "R1.2": "B"}}
	nl := &kicad.SchNetlist{Nets: []kicad.SchNet{
		{Name: "/A", Nodes: []kicad.SchNetNode{{Ref: "R1", Pin: "1"}, {Ref: "R2", Pin: "1"}, {Ref: "R1", Pin: "2"}}},
		{Name: "unconnected-(R2-Pad2)", Nodes: []kicad.SchNetNode{{Ref: "R2", Pin: "2"}}}}}
	diff := compareSbNetlist(d, nl)
	if diff.Equal || len(diff.WrongNet) != 1 || len(diff.MergedNets) != 1 {
		t.Fatalf("diff = %+v", diff)
	}
	nl.Nets[0].Nodes = nl.Nets[0].Nodes[:2]
	nl.Nets = append(nl.Nets, kicad.SchNet{Name: "B", Nodes: []kicad.SchNetNode{{Ref: "R1", Pin: "2"}, {Ref: "#PWR01", Pin: "1"}}})
	if diff := compareSbNetlist(d, nl); !diff.Equal {
		t.Fatalf("diff = %+v", diff)
	}
}

// TestSchBuildESP32Bench builds examples/kicad-sch-build/esp32-mini.json
// (31 parts from blocks, LCSC symbols) and reports the timing. Needs the
// LCSC parts in the cache or the network.
func TestSchBuildESP32Bench(t *testing.T) {
	if testing.Short() {
		t.Skip("benchmark build")
	}
	sbNeedKicad(t)
	if _, err := os.Stat(filepath.Join(kicad.LCSCCacheDir("C2913202"), "part.json")); err != nil {
		c := http.Client{Timeout: 5 * time.Second}
		if _, err := c.Get(kicad.EasyEDAComponentURL("https://lceda.cn", "C6186")); err != nil {
			t.Skipf("LCSC parts not cached and no network (%v)", err)
		}
	}
	spec, err := loadSbSpec("../../examples/kicad-sch-build/esp32-mini.json")
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "esp32")
	t0 := time.Now()
	res, err := runSchBuild(spec, sbOptions{OutDir: out, Jobs: 8, PlannerBudget: 20000}, nil)
	wall := time.Since(t0)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("ESP32-mini: %d parts, %d nets, wall %v; symbols %+v; timings %+v", res.Parts, res.Nets, wall, res.Resolve, res.Timings)
	for _, g := range res.Gates {
		t.Logf("gate %-12s %-4s %s", g.Name, g.Status, g.Detail)
	}
	if res.Parts < 30 || sbGateStatus(res, "netlist") != "pass" || sbGateStatus(res, "erc") == "fail" {
		t.Fatalf("gates: %+v", res.Gates)
	}
	if wall > 30*time.Second {
		t.Errorf("wall %v > 30 s target", wall)
	}
}
