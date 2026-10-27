package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
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
		t0 := packZones(ids, layouts, nil, 0)
		if len(t0) != n {
			t.Fatalf("seed %d: fresh pack placed %d of %d zones", seed, len(t0), n)
		}
		t1 := packZones(ids, layouts, t0, 0)
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
	out := packZones([]string{"a", "b"}, layouts, keep, 0)
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

// field report 2026-10-26: a zone "could not be laid out" — a load switch
// pins 5+6 and the diode share VSW, and every stub of one VSW pin was
// rejected for touching its sibling pin / same-net wire as if it were foreign.
func TestSbGridZoneMultiPinSameNet(t *testing.T) {
	two := func(a, b string) []sbPin {
		return []sbPin{{Number: "1", Name: a, Type: "passive"}, {Number: "2", Name: b, Type: "passive"}}
	}
	spec := &sbSpec{
		Name:  "supply",
		Zones: []sbZone{{ID: "supply", Title: "Primary supply"}},
		Parts: []sbPart{
			{Ref: "U2", Value: "a load switch", Zone: "supply", Pins: []sbPin{
				{Number: "1", Name: "VIN", Type: "power_in"}, {Number: "2", Name: "GND", Type: "power_in"},
				{Number: "3", Name: "EN", Type: "input"}, {Number: "4", Name: "CT", Type: "passive"},
				{Number: "5", Name: "VOUT", Type: "power_out"}, {Number: "6", Name: "VOUT", Type: "power_out"}}},
			{Ref: "D1", Value: "BAT54", Zone: "supply", Pins: two("A", "K")},
			{Ref: "C1", Value: "1uF", Zone: "supply", Pins: two("1", "2")},
			{Ref: "C2", Value: "10uF", Zone: "supply", Pins: two("1", "2")},
			{Ref: "C3", Value: "1nF", Zone: "supply", Pins: two("1", "2")},
			{Ref: "R1", Value: "100k", Zone: "supply", Pins: two("1", "2")},
		},
		Nets: []sbNet{
			{Name: "+12V", Pins: []string{"U2:VIN", "C1:1", "R1:1"}, Kind: "power"},
			{Name: "VSW", Pins: []string{"U2:5", "U2:6", "D1:K", "C2:1"}, Kind: "power"},
			{Name: "EN", Pins: []string{"U2:EN", "R1:2"}},
			{Name: "CT", Pins: []string{"U2:CT", "C3:1"}},
			{Name: "AUX", Pins: []string{"D1:A"}},
			{Name: "GND", Pins: []string{"U2:GND", "C1:2", "C2:2", "C3:2"}},
		},
	}
	d, _, err := buildDesign(spec, sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	var errs []string
	for _, v := range sbGridVariants {
		if _, err := gridZone(d, zi, &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, v); err == nil {
			return
		} else {
			errs = append(errs, err.Error())
		}
	}
	t.Fatalf("no grid variant could lay out a zone with a multi-pin same-net IC side:\n%s", strings.Join(errs, "\n"))
}

// A zone of nothing but decoupling caps on several rails (no IC): the caps
// stack in columns, and the ground symbol of one cap and the supply symbol of
// the cap below used to meet on one vertical line, so every grid variant
// failed with "N stub(s) would touch a foreign wire or pin" (field board
// 2026-10-27: 33 caps on four rails, all stubs at one y).
func TestSbGridZoneDecapColumns(t *testing.T) {
	sbNeedKicad(t)
	spec := &sbSpec{Name: "decaps", Zones: []sbZone{{ID: "dec", Title: "FPGA decoupling"}}}
	rails := []string{"+1V2", "+2V5", "+3V3"}
	gnd := sbNet{Name: "GND", Kind: "ground"}
	nets := map[string]*sbNet{}
	// runs of one rail, then the next, then a bulk cap per rail
	var railOf []string
	for _, run := range []struct {
		rail string
		n    int
	}{{"+1V2", 8}, {"+3V3", 16}, {"+2V5", 5}, {"+1V2", 1}, {"+3V3", 2}, {"+2V5", 1}} {
		for i := 0; i < run.n; i++ {
			railOf = append(railOf, run.rail)
		}
	}
	for i, r := range railOf {
		ref := fmt.Sprintf("C%d", i+1)
		spec.Parts = append(spec.Parts, sbPart{Ref: ref, Value: "100n", Symbol: "Device:C", Zone: "dec"})
		if nets[r] == nil {
			nets[r] = &sbNet{Name: r, Kind: "power"}
		}
		nets[r].Pins = append(nets[r].Pins, ref+":1")
		gnd.Pins = append(gnd.Pins, ref+":2")
	}
	for _, r := range rails {
		spec.Nets = append(spec.Nets, *nets[r])
	}
	spec.Nets = append(spec.Nets, gnd)
	d, _, err := buildDesign(spec, sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	for i, v := range sbGridVariants {
		zl, err := gridZone(d, zi, &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, v)
		if err != nil {
			t.Errorf("grid variant %d: %v", i, err)
			continue
		}
		if f := sbZoneFindings(d, zi, zl); sbHasShort(f) {
			t.Errorf("grid variant %d shorts nets: %v", i, f)
		}
	}
}

// An IC whose side carries runs of same-net pins (three OUT, three VS) with a
// decoupling cap on the first of them: each pin used to get its own stub and
// label, and the cap's attach wire, bending past the next pins, crossed their
// stubs (EG-08) on every grid variant (field board 2026-10-27, a high-side
// switch zone that v0.10.0 drew clean). The runs are now tied along the pin
// ends and carry one connection.
// A pin of a same-net run (tied, no marker of its own) must never be the
// fallback tie target of its run's first pin: that pin dropped its marker for
// it and the whole run lost its net name (field board 2026-10-27, a KiCad
// netlist mismatch on an isolator's GND2 pins). Every stub of S is blocked;
// its only same-net sibling T is tied to S already.
func TestSbFixStubsNeverTiesToATiedPin(t *testing.T) {
	s, tp := kicad.Pt{X: 0, Y: 0}, kicad.Pt{X: 0, Y: 2.54}
	zl := &sbZoneLayout{
		Wires: [][]kicad.Pt{
			{s, tp},                                 // the run tie
			{s, {X: 5.08, Y: 0}},                    // S's stub
			{{X: 2.54, Y: -20}, {X: 2.54, Y: 20}},   // foreign wires all around
			{{X: -2.54, Y: -20}, {X: -2.54, Y: 20}}, //
			{{X: -20, Y: -2.54}, {X: 20, Y: -2.54}}, //
		},
		Markers: []sbMarker{{Kind: "label", Net: "GND2", At: kicad.Pt{X: 5.08, Y: 0}, Dir: "right"}},
	}
	pinNet := map[kicad.Pt]string{
		s: "GND2#U1.17", tp: "GND2#U1.18",
		{X: 2.54, Y: -20}: "A#R1.1", {X: 2.54, Y: 20}: "A#R2.1", {X: -2.54, Y: -20}: "B#R3.1", {X: -2.54, Y: 20}: "B#R4.1",
		{X: -20, Y: -2.54}: "C#R5.1", {X: 20, Y: -2.54}: "C#R6.1",
	}
	bad := sbFixStubs(zl, pinNet, nil, map[kicad.Pt]kicad.Pt{tp: s})
	if len(zl.Markers) != 1 {
		t.Fatalf("S dropped its marker for a pin of its own run: markers %v", zl.Markers)
	}
	if len(bad) == 0 {
		t.Fatal("a stub with no clear way out must be reported, not hidden")
	}
}

func TestSbGridZoneSameNetPinRuns(t *testing.T) {
	two := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	pin := func(n, name, typ string) sbPin { return sbPin{Number: n, Name: name, Type: typ} }
	spec := &sbSpec{
		Name:  "hss",
		Zones: []sbZone{{ID: "sw", Title: "High-side switch"}},
		Parts: []sbPart{
			{Ref: "U1", Value: "HSS", Zone: "sw", Pins: []sbPin{
				pin("1", "NC", "no_connect"), pin("2", "GND", "power_in"), pin("3", "IN", "input"), pin("4", "NC", "no_connect"),
				pin("5", "OUT", "output"), pin("6", "OUT", "output"), pin("7", "OUT", "output"), pin("8", "VS", "power_in"),
				pin("9", "VS", "power_in"), pin("10", "VS", "power_in"), pin("11", "NC", "no_connect"), pin("12", "DIAG_EN", "input"),
				pin("13", "CL", "passive"), pin("14", "CS", "output"), pin("15", "EP", "power_in")}},
			{Ref: "R1", Value: "10k", Zone: "sw", Pins: two},
			{Ref: "C1", Value: "100n", Zone: "sw", Pins: two, Near: "U1:8"},
			{Ref: "C2", Value: "10n", Zone: "sw", Pins: two},
		},
		Nets: []sbNet{
			{Name: "GND", Kind: "ground", Pins: []string{"U1:2", "U1:15", "C1:2", "C2:2"}},
			{Name: "+24V", Kind: "power", Pins: []string{"U1:8", "U1:9", "U1:10", "C1:1"}},
			{Name: "LOAD_PWR", Pins: []string{"U1:5", "U1:6", "U1:7", "C2:1"}},
			{Name: "LOAD_CS", Pins: []string{"U1:14"}},
			{Name: "LOAD_EN", Pins: []string{"R1:1"}},
			{Name: "LOAD_IN", Pins: []string{"R1:2", "U1:3"}},
			{Name: "LOAD_CL", Pins: []string{"U1:13"}},
			{Name: "LOAD_DIAG", Pins: []string{"U1:12"}},
		},
	}
	d, _, err := buildDesign(spec, sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	clean := 0
	var bad []string
	for i, v := range sbGridVariants {
		zl, err := gridZone(d, zi, &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, v)
		if err != nil {
			continue // a variant may fail; layoutZones takes the clean one
		}
		f := sbZoneFindings(d, zi, zl)
		if len(f) == 0 {
			clean++
		}
		for _, s := range f {
			if strings.HasPrefix(s, "EG-08") || strings.HasPrefix(s, "short: ") {
				bad = append(bad, fmt.Sprintf("variant %d: %s", i, s))
			}
		}
	}
	if len(bad) > 0 || clean == 0 {
		t.Fatalf("%d clean variant(s); crossings / shorts:\n%s", clean, strings.Join(bad, "\n"))
	}
}

func TestFinishSbRunFailureIsNotOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "r.json")
	err := finishSbRun(&sbReport{OK: true}, fmt.Errorf("zone z could not be laid out"), path, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("error swallowed")
	}
	var got struct {
		OK    bool       `json:"ok"`
		Error *sbFailure `json:"error"`
	}
	b, _ := os.ReadFile(path)
	if json.Unmarshal(b, &got) != nil || got.OK || got.Error == nil || !strings.Contains(got.Error.Message, "could not be laid out") {
		t.Fatalf("a failed run must report ok:false with the error, got %s", b)
	}
}

// Auto-split clustering (a field board's dense supply zone): every passive lands with the
// IC it belongs to — near hint first, then shared signal nets, then a shared
// supply (decoupling), then a signal-net neighbour; the rest go to the core.
func TestSbClusterZone(t *testing.T) {
	two := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	ic := func(ref string, n int) sbPart {
		var ps []sbPin
		for i := 1; i <= n; i++ {
			ps = append(ps, sbPin{Number: fmt.Sprint(i), Name: fmt.Sprintf("P%d", i), Type: "passive"})
		}
		return sbPart{Ref: ref, Value: ref, Zone: "z", Pins: ps}
	}
	cp := func(ref string) sbPart { return sbPart{Ref: ref, Value: "1u", Zone: "z", Pins: two} }
	c4 := cp("C4")
	c4.Near = "U2:P1"
	spec := &sbSpec{
		Name:  "cluster",
		Zones: []sbZone{{ID: "z", Title: "Z"}},
		Parts: []sbPart{ic("U1", 6), ic("U2", 4), cp("R1"), cp("R2"), cp("C3"), c4, cp("R5"), cp("R6")},
		Nets: []sbNet{
			{Name: "SIG1", Pins: []string{"U1:1", "R1:1"}}, // R1 → U1 (signal)
			{Name: "SIG2", Pins: []string{"U2:2", "R2:1"}}, // R2 → U2 (signal)
			{Name: "+5V", Pins: []string{"U1:3", "U2:1", "C3:1", "C4:1"}, Kind: "power"},
			{Name: "GND", Pins: []string{"U1:4", "U2:3", "C3:2", "C4:2", "R6:2"}, Kind: "ground"},
			{Name: "MID", Pins: []string{"R2:2", "R5:1"}},  // R5 → R2 → U2
			{Name: "LONE", Pins: []string{"R6:1", "R5:2"}}, // R6 → R5 → U2
			{Name: "S3", Pins: []string{"U1:2", "U2:4"}},
		},
	}
	d, _, err := buildDesign(spec, sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	got := sbClusterZone(d, zi)
	want := map[string]string{"U1": "U1", "U2": "U2", "R1": "U1", "R2": "U2", "C4": "U2", "R5": "U2", "R6": "U2"}
	for ref, ic := range want {
		if got[ref] != ic {
			t.Errorf("%s → %s, want %s (all: %v)", ref, got[ref], ic, got)
		}
	}
	if got["C3"] == "" {
		t.Errorf("C3 (decap on a shared rail) unassigned: %v", got)
	}
	// a zone with one IC is never split
	one := &sbSpec{Name: "one", Zones: []sbZone{{ID: "z"}}, Parts: []sbPart{ic("U1", 6), cp("R1")},
		Nets: []sbNet{{Name: "A", Pins: []string{"U1:1", "R1:1"}}}}
	d1, _, err := buildDesign(one, sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	if a := sbClusterZone(d1, d1.zoneInputs()[0]); a != nil {
		t.Errorf("single-IC zone must not be split: %v", a)
	}
}

// Review findings (Codex / Kimi 2026-10-26): supply evidence must not win
// before signal propagation (LDO feedback divider reached through another
// resistor), a long passive chain must follow its IC whatever the part order,
// and generated sub-zone ids must never collide with existing zones.
func TestSbClusterZoneStagedEvidence(t *testing.T) {
	two := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	ic := func(ref string, n int) sbPart {
		var ps []sbPin
		for i := 1; i <= n; i++ {
			ps = append(ps, sbPin{Number: fmt.Sprint(i), Name: fmt.Sprintf("P%d", i), Type: "passive"})
		}
		return sbPart{Ref: ref, Value: ref, Zone: "z", Pins: ps}
	}
	r := func(ref string) sbPart { return sbPart{Ref: ref, Value: "10k", Zone: "z", Pins: two} }
	// U2 has more pins (core, first in order); U1 is the LDO whose FB is
	// reached through RSER. RTOP touches +3V3 (both ICs) and FBX.
	parts := []sbPart{ic("U1", 4), ic("U2", 8), r("RTOP"), r("RSER")}
	nets := []sbNet{
		{Name: "+3V3", Pins: []string{"U1:1", "U2:1", "RTOP:1"}, Kind: "power"},
		{Name: "GND", Pins: []string{"U1:2", "U2:2"}, Kind: "ground"},
		{Name: "FB", Pins: []string{"U1:3", "RSER:2"}},
		{Name: "FBX", Pins: []string{"RSER:1", "RTOP:2"}},
	}
	// a 6-resistor chain hanging off U1, listed in reverse order
	prev := "U1:4"
	var chain []string
	for i := 6; i >= 1; i-- {
		chain = append(chain, fmt.Sprintf("RC%d", i))
	}
	for i := len(chain) - 1; i >= 0; i-- {
		parts = append(parts, r(chain[i]))
	}
	for i := len(chain) - 1; i >= 0; i-- {
		nets = append(nets, sbNet{Name: fmt.Sprintf("CH%d", i), Pins: []string{prev, chain[i] + ":1"}})
		prev = chain[i] + ":2"
	}
	spec := &sbSpec{Name: "staged", Zones: []sbZone{{ID: "z"}}, Parts: parts, Nets: nets}
	d, _, err := buildDesign(spec, sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	if zi.Parts[0].Ref != "U2" {
		t.Fatalf("test assumes U2 is the core, got %s", zi.Parts[0].Ref)
	}
	got := sbClusterZone(d, zi)
	for _, ref := range append([]string{"RTOP", "RSER"}, chain...) {
		if got[ref] != "U1" {
			t.Errorf("%s → %s, want U1 (all: %v)", ref, got[ref], got)
		}
	}

	ics := []*sbRPart{d.ByRef["U1"], d.ByRef["U2"]}
	ids := sbSubZoneIDs([]sbZone{{ID: "z"}, {ID: "z_U1"}, {ID: "z_U1_2"}}, "z", ics)
	if ids["U1"] == "z_U1" || ids["U1"] == "z_U1_2" || ids["U2"] != "z_U2" || ids["U1"] == ids["U2"] {
		t.Errorf("sub-zone ids collide with existing zones: %v", ids)
	}
}

// A stub's own power symbol may sit on the end of a FOREIGN wire. Reading the
// wire's net from that symbol made the foreign wire "same net" and merged +5V
// into a ground net on a full-board run (2026-10-26). Only pins are evidence.
func TestSbWireNetUsesPinsOnly(t *testing.T) {
	pinNet := map[kicad.Pt]string{{X: 0, Y: 0}: "GNDX#C1.2", {X: 20, Y: 0}: "+5V#C2.1"}
	markers := []sbMarker{{Kind: "power", Net: "+5V", At: kicad.Pt{X: 10, Y: 0}}}
	if n := sbWireNet([]kicad.Pt{{X: 0, Y: 0}, {X: 10, Y: 0}}, pinNet, markers); n != "GNDX" {
		t.Errorf("wire from a GNDX pin to a +5V symbol is %q, want GNDX", n)
	}
	if n := sbWireNet([]kicad.Pt{{X: 5, Y: 5}, {X: 10, Y: 0}}, pinNet, markers); n != "" {
		t.Errorf("a wire known only from a marker must stay unknown (foreign), got %q", n)
	}
	if n := sbWireNet([]kicad.Pt{{X: 0, Y: 0}, {X: 20, Y: 0}}, pinNet, markers); n != "" {
		t.Errorf("a wire between two different nets must be unknown, got %q", n)
	}
}

// sbSheetShorts flags a wire island that carries two different nets (a
// supply symbol and a ground symbol on one wire) and stays quiet for a clean
// island — the layout stage rejects a shorted zone drawing with it.
func TestSbSheetShorts(t *testing.T) {
	sheet := func(short bool) string {
		e, err := kicad.OpenSchematic(kicad.NewSchematicText("A4", kicad.NewUUID(), true))
		if err != nil {
			t.Fatal(err)
		}
		e.AddWire(kicad.Pt{X: 50.8, Y: 50.8}, kicad.Pt{X: 76.2, Y: 50.8})
		if _, err := e.AddPower("+5V", kicad.Pt{X: 50.8, Y: 50.8}, 0, false); err != nil {
			t.Fatal(err)
		}
		other := kicad.Pt{X: 101.6, Y: 50.8} // off the wire
		if short {
			other = kicad.Pt{X: 76.2, Y: 50.8}
		}
		if _, err := e.AddPower("GND", other, 0, true); err != nil {
			t.Fatal(err)
		}
		txt, err := e.Render()
		if err != nil {
			t.Fatal(err)
		}
		return txt
	}
	d := &sbDesign{PinNet: map[string]string{}}
	if got := sbSheetShorts(sheet(true), d); len(got) != 1 || !strings.Contains(got[0], "+5V") || !strings.Contains(got[0], "GND") {
		t.Fatalf("supply and ground on one wire not reported: %v", got)
	}
	if got := sbSheetShorts(sheet(false), d); len(got) != 0 {
		t.Fatalf("clean sheet reported a short: %v", got)
	}
	if !sbHasShort([]string{"text-overlap: x", "short: one wire island joins A + B"}) || sbHasShort([]string{"label-on-wire: y"}) {
		t.Fatal("sbHasShort misclassifies findings")
	}
}

// Automatic pagination: a page whose zones do not fit A0 is split next-fit
// in reading order, zones kept whole; new ids never collide with spec pages;
// a second run is a fixed point (no "-2-2" ids, no stacked "(1/2)" titles).
func TestSbPaginate(t *testing.T) {
	big := func() *sbZoneLayout { return &sbZoneLayout{Box: kicad.Box{MaxX: 700, MaxY: 500}} } // one per A0
	d := &sbDesign{
		Pages: []sbPage{{ID: "P", Title: "Power"}, {ID: "P-2", Title: "Taken"}},
		Zones: []sbZone{{ID: "z1", Page: "P"}, {ID: "z2", Page: "P"}, {ID: "z3", Page: "P"}, {ID: "q", Page: "P-2"}},
		ByRef: map[string]*sbRPart{},
	}
	for i, z := range []string{"z1", "z2", "z3", "q"} {
		p := &sbRPart{ZoneID: z, PageID: d.Zones[i].Page}
		p.Ref = fmt.Sprintf("R%d", i+1)
		d.Parts = append(d.Parts, p)
		d.ByRef[p.Ref] = p
	}
	d.Nets = []sbNet{{Name: "S", Pins: []string{"R1.1", "R3.1"}}}
	layouts := map[string]*sbZoneLayout{"z1": big(), "z2": big(), "z3": big(), "q": {Box: kicad.Box{MaxX: 50, MaxY: 50}}}
	var zis []*sbZoneIn
	for _, z := range d.Zones {
		zis = append(zis, &sbZoneIn{Zone: z})
	}
	b := newSbPageBases(d, zis, "P-3.kicad_sch") // the root sheet already uses P-3's file name
	changed, _ := sbPaginate(d, layouts, b)
	if !changed {
		t.Fatal("an overfull page was not split")
	}
	var ids, titles []string
	for _, pg := range d.Pages {
		ids, titles = append(ids, pg.ID), append(titles, pg.Title)
	}
	if strings.Join(ids, ",") != "P,P-2_2,P-3_2,P-2" {
		t.Errorf("pages %v, want P,P-2_2,P-3_2,P-2 (the spec uses P-2, the root file is P-3.kicad_sch)", ids)
	}
	if titles[0] != "Power (1/3)" || titles[2] != "Power (3/3)" || titles[3] != "Taken" {
		t.Errorf("titles %v", titles)
	}
	if d.ByRef["R2"].PageID != "P-2_2" || d.ByRef["R4"].PageID != "P-2" {
		t.Errorf("part pages not updated: R2=%s R4=%s", d.ByRef["R2"].PageID, d.ByRef["R4"].PageID)
	}
	if len(d.NetPages["S"]) != 2 {
		t.Errorf("NetPages not recomputed: %v", d.NetPages["S"])
	}
	if changed, _ := sbPaginate(d, layouts, b); changed {
		var again []string
		for _, pg := range d.Pages {
			again = append(again, pg.ID+"="+pg.Title)
		}
		t.Errorf("second pagination is not a fixed point: %v", again)
	}
	// the saved spec keeps the author's pages (sch-edit reads it back)
	d.Spec = &sbSpec{}
	ns := d.normalizedSpec()
	var sp []string
	for _, pg := range ns.Pages {
		sp = append(sp, pg.ID+"="+pg.Title)
	}
	if strings.Join(sp, ",") != "P=Power,P-2=Taken" {
		t.Errorf("saved spec pages %v, want the spec's P=Power,P-2=Taken", sp)
	}
	for _, z := range ns.Zones {
		if (z.ID == "z2" || z.ID == "z3") && z.Page != "P" {
			t.Errorf("saved spec moved zone %s to generated page %s", z.ID, z.Page)
		}
	}
}

// A zone re-laid out for its cross-page labels can come back SMALLER (another
// shape). Plain next-fit then merged the split page again, the net went back
// to local, the zone grew back, the page split again: a two-round cycle until
// "pagination did not settle" (field board 2026-10-27). Fragments now only
// grow: the loop must settle, keep the split, and never touch a page that fit.
func TestSbPaginateSettlesWhenRelayoutShrinks(t *testing.T) {
	d := &sbDesign{
		Pages:   []sbPage{{ID: "P", Title: "Main"}, {ID: "Q", Title: "Small"}},
		Zones:   []sbZone{{ID: "z1", Page: "P"}, {ID: "z2", Page: "P"}, {ID: "z3", Page: "P"}, {ID: "q", Page: "Q"}},
		ByRef:   map[string]*sbRPart{},
		PinNet:  map[string]string{},
		NetKind: map[string]string{},
	}
	for i, z := range d.Zones {
		p := &sbRPart{ZoneID: z.ID, PageID: z.Page, LPins: []kicad.LibPin{{Number: "1"}, {Number: "2"}}}
		p.Ref = fmt.Sprintf("R%d", i+1)
		d.Parts = append(d.Parts, p)
		d.ByRef[p.Ref] = p
	}
	// S joins z1 and z3 (the zone the split moves); T stays inside page Q
	d.Nets = []sbNet{{Name: "S", Pins: []string{"R1.1", "R3.1"}}, {Name: "T", Pins: []string{"R4.1", "R4.2"}}}
	for _, n := range d.Nets {
		for _, k := range n.Pins {
			d.PinNet[k] = n.Name
		}
	}
	d.recomputeNetPages()
	box := func(w, h float64) *sbZoneLayout { return &sbZoneLayout{Box: kicad.Box{MaxX: w, MaxY: h}} }
	// S local: z1 is big and P overflows A0; S cross-page: z1 is small and
	// all three zones would fit again
	size := func(id string) *sbZoneLayout {
		switch {
		case id == "z1" && len(d.NetPages["S"]) > 1:
			return box(400, 200)
		case id == "z1":
			return box(700, 500)
		case id == "q":
			return box(100, 100)
		}
		return box(300, 300)
	}
	layouts := map[string]*sbZoneLayout{}
	for _, z := range d.Zones {
		layouts[z.ID] = size(z.ID)
	}
	if sbPackFresh([]string{"z1", "z2", "z3"}, layouts, 0) != nil {
		t.Fatal("fixture: page P must overflow A0 with S local")
	}
	var zis []*sbZoneIn
	for _, z := range d.Zones {
		zis = append(zis, &sbZoneIn{Zone: z})
	}
	calls := 0
	relaid := map[string]int{}
	_, _, err := sbPaginateLoop(d, zis, layouts, "root.kicad_sch", func(redo []*sbZoneIn) (map[string]*sbZoneLayout, []string) {
		calls++
		out := map[string]*sbZoneLayout{}
		for _, zi := range redo {
			relaid[zi.Zone.ID]++
			out[zi.Zone.ID] = size(zi.Zone.ID)
		}
		return out, nil
	})
	if err != nil {
		t.Fatalf("pagination did not settle: %v (re-layout rounds %d, zones %v)", err, calls, relaid)
	}
	zp := map[string]string{}
	for _, z := range d.Zones {
		zp[z.ID] = z.Page
	}
	if zp["z1"] != "P" || zp["z2"] != "P" || zp["z3"] == "P" || zp["q"] != "Q" {
		t.Errorf("zone pages %v: want z1, z2 on P, z3 on a new page, q on Q", zp)
	}
	if relaid["q"] != 0 || relaid["z2"] != 0 {
		t.Errorf("zones whose cross-page nets never changed were re-laid out: %v", relaid)
	}
	if calls != 1 {
		t.Errorf("%d re-layout rounds, want 1 (the split, then a fixed point)", calls)
	}
}

// A narrow zone with a long title: the frame the packer sees must hold the
// title text, or it runs past the right edge of A0 on a page pagination
// accepted (field board 2026-10-27: page-fit failed on a split page).
func TestSbFitTitleWidensNarrowZone(t *testing.T) {
	title := "pull-down column / readback"
	b := sbFitTitle(kicad.Box{MinX: 0, MaxX: 9.5, MaxY: 50}, title)
	fp := sbFootprint(b, kicad.Pt{})
	if need := fp.MinX + 1.27 + 1 + float64(len(title)); fp.MaxX < need {
		t.Fatalf("frame right edge %.2f < title end %.2f (sch-fit's estimate)", fp.MaxX, need)
	}
	if wide := (kicad.Box{MaxX: 300, MaxY: 50}); sbFitTitle(wide, title) != wide {
		t.Fatal("a zone wider than its title must keep its box")
	}
}

// Two zones that fill A0 to the bottom: with an undriven supply on the page,
// its PWR_FLAG row goes under them and overflows, so pagination must split
// the page; without one the page fits and stays whole (field board
// 2026-10-27: page-fit failed on a page pagination had kept).
func TestSbPaginateCountsPwrFlagRow(t *testing.T) {
	for _, flags := range []bool{false, true} {
		d := &sbDesign{
			Pages:   []sbPage{{ID: "P", Title: "Main"}},
			Zones:   []sbZone{{ID: "a", Page: "P"}, {ID: "b", Page: "P"}},
			ByRef:   map[string]*sbRPart{},
			PinNet:  map[string]string{},
			NetKind: map[string]string{},
		}
		for i, z := range d.Zones {
			p := &sbRPart{ZoneID: z.ID, PageID: z.Page, LPins: []kicad.LibPin{{Number: "1"}}}
			p.Ref = fmt.Sprintf("C%d", i+1)
			d.Parts = append(d.Parts, p)
			d.ByRef[p.Ref] = p
		}
		d.Nets = []sbNet{{Name: "VBUS", Pins: []string{"C1.1", "C2.1"}}}
		d.PinNet["C1.1"], d.PinNet["C2.1"] = "VBUS", "VBUS"
		if flags {
			d.NetKind["VBUS"] = "power" // no power_out pin: one PWR_FLAG
		}
		d.recomputeNetPages()
		layouts := map[string]*sbZoneLayout{"a": {Box: kicad.Box{MaxX: 1000, MaxY: 380}}, "b": {Box: kicad.Box{MaxX: 1000, MaxY: 380}}}
		if sbPackFresh([]string{"a", "b"}, layouts, 0) == nil {
			t.Fatal("fixture: the two zones alone must fit A0")
		}
		var zis []*sbZoneIn
		for _, z := range d.Zones {
			zis = append(zis, &sbZoneIn{Zone: z})
		}
		b := newSbPageBases(d, zis, "root.kicad_sch")
		sbPaginate(d, layouts, b)
		if want := map[bool]int{false: 1, true: 2}[flags]; len(d.Pages) != want {
			t.Errorf("flags=%v: %d page(s), want %d", flags, len(d.Pages), want)
		}
	}
}

// A wide (CJK) title is drawn about twice as wide per glyph as ASCII, so it
// must widen the box twice as much (Kimi review).
func TestSbFitTitleCountsWideGlyphs(t *testing.T) {
	narrow := kicad.Box{MaxY: 50}
	ascii, cjk := sbFitTitle(narrow, "abcdefghijkl"), sbFitTitle(narrow, "电源去耦电容电源去耦电容")
	if got, want := cjk.MaxX, ascii.MaxX+1.2*12; ascii.MaxX <= 0 || got < want-1e-9 {
		t.Fatalf("CJK title box %.2f mm wide, want at least %.2f", got, want)
	}
}

// A single zone that fits A0 alone but not with the PWR_FLAG row of its
// undriven rail cannot be fixed by splitting pages: pagination must say so
// instead of accepting the page silently (Kimi review).
func TestSbPaginateNotesSingleZoneFlagOverflow(t *testing.T) {
	d := &sbDesign{
		Pages:   []sbPage{{ID: "P", Title: "Main"}},
		Zones:   []sbZone{{ID: "a", Page: "P"}},
		ByRef:   map[string]*sbRPart{},
		PinNet:  map[string]string{"C1.1": "VBUS"},
		NetKind: map[string]string{"VBUS": "power"},
	}
	p := &sbRPart{ZoneID: "a", PageID: "P", LPins: []kicad.LibPin{{Number: "1"}}}
	p.Ref = "C1"
	d.Parts = append(d.Parts, p)
	d.ByRef[p.Ref] = p
	d.Nets = []sbNet{{Name: "VBUS", Pins: []string{"C1.1"}}}
	d.recomputeNetPages()
	var layouts map[string]*sbZoneLayout
	for h := 700.0; h < 841; h += 2 {
		l := map[string]*sbZoneLayout{"a": {Box: kicad.Box{MaxX: 1000, MaxY: h}}}
		if sbPackFresh([]string{"a"}, l, 0) != nil && sbPackFresh([]string{"a"}, l, sbFlagRowW([]string{"VBUS"})) == nil {
			layouts = l
			break
		}
	}
	if layouts == nil {
		t.Fatal("fixture: no zone height fits A0 alone but not with the flag row")
	}
	b := newSbPageBases(d, []*sbZoneIn{{Zone: d.Zones[0]}}, "root.kicad_sch")
	_, notes := sbPaginate(d, layouts, b)
	if !strings.Contains(strings.Join(notes, "\n"), "PWR_FLAG row") {
		t.Fatalf("notes %q do not report the zone-plus-flags overflow", notes)
	}
}

// Three pages sharing one signal net: every root occurrence (labelled stub
// or direct wire between same-row neighbours) must join one net — KiCad's
// netlist is the judge (Codex review: an unlabelled direct wire was an
// island when a third sheet carried the net too).
func TestSchBuildThreePagesShareNet(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	two := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	r := func(ref, zone string) sbPart { return sbPart{Ref: ref, Value: "10k", Zone: zone, Pins: two} }
	spec := &sbSpec{
		Name:  "threepg",
		Pages: []sbPage{{ID: "A"}, {ID: "B"}, {ID: "C"}},
		Zones: []sbZone{{ID: "za", Page: "A"}, {ID: "zb", Page: "B"}, {ID: "zc", Page: "C"}},
		Parts: []sbPart{r("R1", "za"), r("R2", "zb"), r("R3", "zc"), r("R4", "zb")},
		Nets: []sbNet{
			{Name: "S", Pins: []string{"R1:1", "R2:1", "R3:1"}}, // on all three pages
			{Name: "T", Pins: []string{"R4:1", "R3:2"}},
			{Name: "U", Pins: []string{"R1:2", "R2:2", "R4:2"}},
		},
	}
	res, err := runSchBuild(spec, sbOptions{OutDir: filepath.Join(t.TempDir(), "o"), NoIntent: true, NoReview: true, Jobs: 2, PlannerBudget: 2000}, nil)
	if err != nil && (res == nil || sbGateStatus(res, "netlist") != "pass") {
		t.Fatalf("build: %v", err)
	}
	if s := sbGateStatus(res, "netlist"); s != "pass" {
		t.Fatalf("netlist gate %s", s)
	}
}

// The root sheet wraps its sheet symbols into rows inside A0; with two very
// tall symbols the reading-order rows overflow, and the height shelves fit.
func TestSbRootRowsFitA0(t *testing.T) {
	const n = 25
	h := make([]float64, n)
	for i := range h {
		h[i] = 30 + float64(i%6)*15
	}
	h[18], h[24] = 325, 350
	x := make([]float64, n)
	sy, rowOf, order := sbRootRows(n, func(i int) (float64, float64, float64, float64) { return 35, 70, 35, h[i] },
		func(i int, v float64) { x[i] = v })
	a0 := kicad.Papers[len(kicad.Papers)-1]
	for i := 0; i < n; i++ {
		if x[i]+70+35 > a0.W-10 || sy[i]+h[i] > a0.H-10-34 {
			t.Fatalf("sheet %d at (%.1f, %.1f) h=%.0f is outside A0 (order %v rows %v)", i, x[i], sy[i], h[i], order, rowOf)
		}
	}
	if rowOf[order[len(order)-1]] == 0 {
		t.Fatal("25 sheet symbols must wrap into several rows")
	}
}
