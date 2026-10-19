package app

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	for _, g := range []string{"netlist", "erc", "quality", "engineer-grade", "connectivity", "intent", "page-fit"} {
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

// TestSchBuildHardGateTransactional: a failing hard gate (here the quality
// gate, injected) leaves --out untouched and exits non-zero.
func TestSchBuildHardGateTransactional(t *testing.T) {
	sbNeedKicad(t)
	out := filepath.Join(t.TempDir(), "p")
	spec := func() *sbSpec {
		return &sbSpec{Parts: []sbPart{{Ref: "R1", Value: "1k", Symbol: "Device:R"}, {Ref: "R2", Value: "2k", Symbol: "Device:R"}},
			Nets: []sbNet{{Name: "A", Pins: []string{"R1:1", "R2:1"}}, {Name: "GND", Pins: []string{"R1:2", "R2:2"}}}}
	}
	if _, err := runSchBuild(spec(), sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(out, "p.kicad_sch"))
	sbTestGateHook = func(rep *sbReport) { rep.gate("quality", "fail", "injected finding", nil) }
	defer func() { sbTestGateHook = nil }()
	s2 := spec()
	s2.Parts[1].Value = "4k7"
	rep, err := runSchBuild(s2, sbOptions{OutDir: out, NoIntent: true, PlannerBudget: 20000}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK || rep.Error == nil || rep.Error.Code != "GATE_FAILED" {
		t.Fatalf("report ok=%v error=%+v", rep.OK, rep.Error)
	}
	if after, _ := os.ReadFile(filepath.Join(out, "p.kicad_sch")); !bytes.Equal(before, after) {
		t.Fatal("a failed gate changed the committed sheet")
	}
	var so, se bytes.Buffer
	if err := finishSbRun(rep, nil, "", &so, &se); err == nil {
		t.Fatal("a failed hard gate must exit non-zero")
	}
}

// field report 2026-10-15: page id "aux" produced aux.kicad_sch, a Windows reserved
// device name that can be neither created nor added to git.
func TestSbSheetFileAvoidsWindowsReservedNames(t *testing.T) {
	for page, want := range map[string]string{
		"aux": "aux_.kicad_sch", "AUX": "AUX_.kicad_sch", "con": "con_.kicad_sch", "nul": "nul_.kicad_sch",
		"prn": "prn_.kicad_sch", "com1": "com1_.kicad_sch", "LPT9": "LPT9_.kicad_sch",
		"auxiliary": "auxiliary.kicad_sch", "com10": "com10.kicad_sch", "power": "power.kicad_sch",
	} {
		if got := sbSheetFile(page); got != want {
			t.Errorf("sbSheetFile(%q) = %q, want %q", page, got, want)
		}
	}
}

// A net renamed in the SPEC keeps the canonical zone hash, so the cached
// layout must be rejected by its stale marker names (field report 2026-10-15: the
// netlist gate failed until --fresh).
func TestSbMarkersCurrent(t *testing.T) {
	d := &sbDesign{
		PinNet:  map[string]string{"U1.1": "+3V3", "U1.2": "GND", "U1.3": "LED_X"},
		NetKind: map[string]string{"+3V3": "power", "GND": "ground"},
	}
	zi := &sbZoneIn{Parts: []*sbRPart{{LPins: []kicad.LibPin{{Number: "1"}, {Number: "2"}, {Number: "3"}}}}}
	zi.Parts[0].Ref = "U1"
	cached := func(ms ...sbMarker) *sbZoneLayout { return &sbZoneLayout{Markers: ms} }
	if !zi.markersCurrent(d, cached(sbMarker{Kind: "power", Net: "+3V3"}, sbMarker{Kind: "ground", Net: "GND"}, sbMarker{Kind: "label", Net: "LED_X"})) {
		t.Fatal("an up-to-date layout was rejected")
	}
	if zi.markersCurrent(d, cached(sbMarker{Kind: "label", Net: "LED_A"})) {
		t.Fatal("a layout drawing a renamed-away net was reused")
	}
	if zi.markersCurrent(d, cached(sbMarker{Kind: "power", Net: "LED_X"})) {
		t.Fatal("a power symbol on a net that is no longer a power net was reused")
	}
}

// End to end: rename a net in the spec and rebuild into the same --out
// without --fresh — the netlist gate must pass and the zone must re-plan.
func TestSchBuildSpecNetRenameInvalidatesCache(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	out := filepath.Join(t.TempDir(), "small")
	spec, err := loadSbSpec("testdata/sch-build/small.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, Jobs: 4, PlannerBudget: 20000}, nil); err != nil {
		t.Fatalf("first build: %v", err)
	}
	// Only a zone whose cached drawing names LED_A must re-plan; a net drawn
	// as a bare wire carries no name and may be reused.
	st, _ := loadSbState(out)
	if st == nil {
		t.Fatal("first build left no state")
	}
	drawsLEDA := map[string]bool{}
	for id, z := range st.Zones {
		if z == nil || z.Layout == nil {
			continue
		}
		for _, mk := range z.Layout.Markers {
			if mk.Net == "LED_A" {
				drawsLEDA[id] = true
			}
		}
	}
	renamed := false
	for i := range spec.Nets {
		if spec.Nets[i].Name == "LED_A" {
			spec.Nets[i].Name, renamed = "LED_X", true
		}
	}
	if !renamed {
		t.Fatal("fixture has no LED_A net")
	}
	res, err := runSchBuild(spec, sbOptions{OutDir: out, NoIntent: true, Jobs: 4, PlannerBudget: 20000}, nil)
	if err != nil || sbGateStatus(res, "netlist") != "pass" {
		t.Fatalf("rebuild after spec rename: %v %+v", err, res)
	}
	for _, z := range res.Zones {
		if drawsLEDA[z.ID] && z.Reused {
			t.Errorf("zone %s reused a layout that still draws LED_A", z.ID)
		}
	}
}

// Decoupling caps go through sidePlace, which used to anchor every cap of a
// rail at the topmost core pin of that rail and ignore `near` (a field project
// 2026-10-15). Two caps hinted at two different VDD pins must not end up in
// one row beside one pin; a hint must also change the zone hash so an edited
// hint redraws the zone.
func TestSbGridHonorsNearForDecaps(t *testing.T) {
	gen2 := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	spec := func(withNear bool) *sbSpec {
		c1, c2 := sbPart{Ref: "C1", Value: "100nF", Zone: "mcu", Pins: gen2}, sbPart{Ref: "C2", Value: "100nF", Zone: "mcu", Pins: gen2}
		if withNear {
			c1.Near, c2.Near = "U1:VDD1", "U1:VDD2"
		}
		return &sbSpec{
			Name:  "near",
			Zones: []sbZone{{ID: "mcu", Title: "MCU"}},
			Parts: []sbPart{{Ref: "U1", Value: "MCU", Zone: "mcu", Pins: []sbPin{
				{Number: "1", Name: "VDD1", Type: "power_in"}, {Number: "2", Name: "PA0", Type: "bidirectional"},
				{Number: "3", Name: "PA1", Type: "bidirectional"}, {Number: "4", Name: "PA2", Type: "bidirectional"},
				{Number: "5", Name: "PA3", Type: "bidirectional"}, {Number: "6", Name: "PA4", Type: "bidirectional"},
				{Number: "7", Name: "VDD2", Type: "power_in"}, {Number: "8", Name: "VSS", Type: "power_in"}}}, c1, c2},
			Nets: []sbNet{
				{Name: "+3V3", Pins: []string{"U1:VDD1", "U1:VDD2", "C1:1", "C2:1"}},
				{Name: "GND", Pins: []string{"U1:VSS", "C1:2", "C2:2"}},
			},
		}
	}
	layout := func(withNear bool) (*sbZoneLayout, string) {
		d, _, err := buildDesign(spec(withNear), sbResolveOpts{Offline: true})
		if err != nil {
			t.Fatalf("design: %v", err)
		}
		zi := d.zoneInputs()[0]
		zl, err := gridZone(d, zi, &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, sbGridVariants[0])
		if err != nil {
			t.Fatalf("grid: %v", err)
		}
		return zl, zi.hash(d, "grid-autoconnect")
	}
	hinted, hHash := layout(true)
	_, plainHash := layout(false)
	if hHash == plainHash {
		t.Error("a near hint must change the zone hash")
	}
	p1, p2 := hinted.Parts["C1"], hinted.Parts["C2"]
	if len(p1) == 0 || len(p2) == 0 {
		t.Fatalf("caps not placed: %+v", hinted.Parts)
	}
	if p1[0].At.Y == p2[0].At.Y {
		t.Errorf("C1 (near VDD1) and C2 (near VDD2) share one row at y=%v — the hints were ignored", p1[0].At.Y)
	}
}

// field report 2026-10-15: MPN "Diode X (VR >= 200 V)" gave a
// generated symbol name with '>', KiCad could not load the sheet, and the
// root netlist export silently left the whole page out (94 "missing" pins).
func TestSbGenNameIsKicadSafe(t *testing.T) {
	for _, mpn := range []string{"Diode X (VR >= 200 V)", "A<B", "tab\there", "nl\nx"} {
		p := &sbRPart{}
		p.Ref, p.MPN = "D1", mpn
		p.Pins = []sbPin{{Number: "1"}, {Number: "2"}}
		name := sbGenName(p)
		if strings.ContainsAny(name, `<>:"/\ `+"\t\r\n") {
			t.Errorf("sbGenName(%q) = %q contains a character KiCad cannot load", mpn, name)
		}
	}
	a, b := &sbRPart{}, &sbRPart{}
	a.Ref, a.MPN, b.Ref, b.MPN = "D1", "X>=1", "D2", "X<=1"
	if sbGenName(a) == sbGenName(b) {
		t.Error("'>' and '<' must not map to the same name")
	}
}

func TestSbDroppedPages(t *testing.T) {
	mk := func(ref, page string) *sbRPart { p := &sbRPart{PageID: page}; p.Ref = ref; return p }
	d := &sbDesign{
		Pages: []sbPage{{ID: "P01"}, {ID: "P02"}},
		Parts: []*sbRPart{mk("R1", "P01"), mk("C1", "P02"), mk("C2", "P02"), mk("#PWR01", "P02")},
	}
	nl := &kicad.SchNetlist{Components: []kicad.SchNetComp{{Ref: "R1"}}}
	if got := sbDroppedPages(d, nl); len(got) != 1 || got[0] != "P02" {
		t.Fatalf("dropped = %v, want [P02]", got)
	}
	nl.Components = append(nl.Components, kicad.SchNetComp{Ref: "C2"})
	if got := sbDroppedPages(d, nl); len(got) != 0 {
		t.Fatalf("a page with any part present is not dropped: %v", got)
	}
}

// field report 2026-10-18: a rebuild without --fresh rejected the fresh build's
// own zone positions (packed closer than the old keep check allowed), placed
// those zones past A0 and the sheets came back as A4. Packing must be
// idempotent: re-packing with the previous translations keeps every one.
func TestPackZonesKeepsItsOwnFreshPositions(t *testing.T) {
	for seed := 1; seed <= 40; seed++ {
		layouts := map[string]*sbZoneLayout{}
		var ids []string
		n := 4 + seed%9
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("z%02d", i)
			w := 30 + float64((seed*7+i*13)%90) + 0.37*float64(i)
			h := 20 + float64((seed*11+i*5)%70) + 0.61*float64(seed%3)
			layouts[id] = &sbZoneLayout{Box: kicad.Box{MinX: -w / 3, MinY: -h / 2, MaxX: 2 * w / 3, MaxY: h / 2}}
			ids = append(ids, id)
		}
		t0 := packZones(ids, layouts, nil)
		if len(t0) != n {
			t.Fatalf("seed %d: fresh pack placed %d of %d zones", seed, len(t0), n)
		}
		t1 := packZones(ids, layouts, t0)
		for _, id := range ids {
			if t1[id] != t0[id] {
				t.Fatalf("seed %d: zone %s moved on an unchanged rebuild: %v → %v", seed, id, t0[id], t1[id])
			}
		}
	}
}

// Kept positions that no longer fit the largest sheet are repacked instead of
// being shipped off-page.
func TestPackZonesRepacksWhenKeptLayoutOverflows(t *testing.T) {
	layouts := map[string]*sbZoneLayout{
		"a": {Box: kicad.Box{MaxX: 100, MaxY: 80}},
		"b": {Box: kicad.Box{MaxX: 100, MaxY: 80}},
	}
	keep := map[string]kicad.Pt{"a": {X: 20, Y: 30}, "b": {X: 1500, Y: 30}} // b far past A0
	out := packZones([]string{"a", "b"}, layouts, keep)
	var frames []kicad.Box
	for id, tr := range out {
		frames = append(frames, sbFootprint(layouts[id].Box, tr))
	}
	if len(out) != 2 || !sbFitsLargestPaper(frames) {
		t.Fatalf("overflowing kept layout not repacked: %v", out)
	}
}

// Identical input must draw identical schematics: the decap anchor used to be
// chosen by ranging over a map, so two same-height supply pins let Go's random
// map order pick the side (field report 2026-10-19, EG-08 flipping run to run).
func TestSbGridZoneIsDeterministic(t *testing.T) {
	gen2 := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	spec := &sbSpec{
		Name:  "det",
		Zones: []sbZone{{ID: "mcu", Title: "MCU"}},
		Parts: []sbPart{{Ref: "U1", Value: "MCU", Zone: "mcu", Pins: []sbPin{
			{Number: "1", Name: "VDDL", Type: "power_in"}, {Number: "2", Name: "PA0", Type: "bidirectional"},
			{Number: "3", Name: "PA1", Type: "bidirectional"}, {Number: "4", Name: "VSSL", Type: "power_in"},
			{Number: "5", Name: "VSSR", Type: "power_in"}, {Number: "6", Name: "PB0", Type: "bidirectional"},
			{Number: "7", Name: "PB1", Type: "bidirectional"}, {Number: "8", Name: "VDDR", Type: "power_in"}}},
			{Ref: "C1", Value: "100nF", Zone: "mcu", Pins: gen2}, {Ref: "C2", Value: "100nF", Zone: "mcu", Pins: gen2}},
		Nets: []sbNet{
			{Name: "+3V3", Pins: []string{"U1:VDDL", "U1:VDDR", "C1:1", "C2:1"}},
			{Name: "GND", Pins: []string{"U1:VSSL", "U1:VSSR", "C1:2", "C2:2"}},
		},
	}
	var first string
	for i := 0; i < 40; i++ {
		d, _, err := buildDesign(spec, sbResolveOpts{Offline: true})
		if err != nil {
			t.Fatalf("design: %v", err)
		}
		zl, err := gridZone(d, d.zoneInputs()[0], &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, sbGridVariants[0])
		if err != nil {
			t.Fatalf("grid: %v", err)
		}
		got, _ := json.Marshal(struct {
			P any
			W any
		}{zl.Parts, zl.Wires})
		if i == 0 {
			first = string(got)
		} else if string(got) != first {
			t.Fatalf("run %d drew a different zone for identical input", i)
		}
	}
}
