package intent

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

const (
	esp32Dir   = "../powersim/testdata/esp32mini"
	mainsDir   = "testdata/mains-opto"
	skillModel = "../../.agents/skills/pcbpilot/references/power-models.json"
)

func loadDesign(t *testing.T, dir string, pages []string) *powersim.Design {
	t.Helper()
	var docs []*powersim.ConnDoc
	for _, f := range pages {
		b, err := os.ReadFile(filepath.Join(dir, f))
		if err != nil {
			t.Fatal(err)
		}
		d, err := powersim.ParseConnectivity(b)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, d)
	}
	vb, err := os.ReadFile(filepath.Join(dir, "values.json"))
	if err != nil {
		t.Fatal(err)
	}
	vals, err := powersim.ParseValues(vb)
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := powersim.BuildDesign(docs, vals)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func libs(t *testing.T, extra ...string) powersim.Libraries {
	t.Helper()
	var out powersim.Libraries
	for _, p := range append(extra, skillModel) {
		lib, err := powersim.LoadLibrary(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, lib)
	}
	return out
}

func esp32Intent(t *testing.T, spec *Spec) *Intent {
	t.Helper()
	d := loadDesign(t, esp32Dir, []string{"sch-905bb85957eaf435.json", "sch-950ae6609e91d753.json"})
	it, err := Derive(Input{Design: d, Libs: libs(t), Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func blockBy(t *testing.T, it *Intent, fn, core string) *Block {
	t.Helper()
	for _, b := range it.Blocks {
		if b.Function == fn && (core == "" || b.Core == core) {
			return b
		}
	}
	var got []string
	for _, b := range it.Blocks {
		got = append(got, b.ID+":"+b.Function+":"+b.Core)
	}
	t.Fatalf("no %s block with core %q in %v", fn, core, got)
	return nil
}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.6g, want %.6g ± %.3g", what, got, want, tol)
	}
}

func hasAll(t *testing.T, what string, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !has(got, w) {
			t.Fatalf("%s %v lacks %s", what, got, w)
		}
	}
}

// TestESP32MiniIntent replays the committed ESP32-mini E2E schematic (2 pages
// + values) with the skill power models: blocks, the +3V3 plan, the switch
// node, the USB pair, net classes and the intended findings.
func TestESP32MiniIntent(t *testing.T) {
	it := esp32Intent(t, nil)
	if it.SchemaVersion != 1 || it.Generator != Generator {
		t.Fatalf("header %d %s", it.SchemaVersion, it.Generator)
	}
	// Circuit functions.
	pin := blockBy(t, it, "power-input", "J1")
	hasAll(t, "POWER_IN parts", pin.Parts, "J1", "D1", "D2", "D3")
	hasAll(t, "POWER_IN nets", pin.Nets, "+5V", "5V_TERM", "VBUS")
	if pin.SubFunction != "or-ing" || !strings.Contains(pin.Summary, "OR-ed by D1, D2") {
		t.Fatalf("power input %+v", pin)
	}
	buck := blockBy(t, it, "buck", "U4")
	if buck.ID != "BUCK_3V3" {
		t.Fatalf("buck id %s", buck.ID)
	}
	hasAll(t, "buck parts", buck.Parts, "U4", "L1", "R3", "R4", "C1")
	hasAll(t, "buck nets", buck.Nets, "SW", "+3V3", "FB")
	for _, s := range []string{"3.318", "synchronous buck", "0.521 A peak", "η 90%"} {
		if !strings.Contains(buck.Summary, s) {
			t.Fatalf("buck summary %q lacks %q", buck.Summary, s)
		}
	}
	if len(buck.Notes) == 0 || !strings.Contains(buck.Notes[0], "R3") {
		t.Fatalf("buck divider note %v", buck.Notes)
	}
	blockBy(t, it, "rf-module", "U1")
	blockBy(t, it, "usb-uart", "U3")
	blockBy(t, it, "esd", "U2")
	blockBy(t, it, "connector", "J2")
	led := blockBy(t, it, "led", "LED1")
	hasAll(t, "led parts", led.Parts, "R9")
	if !strings.Contains(led.Summary, "U1.IO2") {
		t.Fatalf("led summary %q", led.Summary)
	}
	var keys, adl *Block
	for _, b := range it.Blocks {
		switch b.SubFunction {
		case "keys":
			keys = b
		case "auto-download":
			adl = b
		}
	}
	if keys == nil || adl == nil {
		t.Fatalf("keys/auto-download blocks missing")
	}
	hasAll(t, "keys", keys.Parts, "SW1", "SW2", "R5", "R6", "C6")
	hasAll(t, "auto-download", adl.Parts, "Q1", "Q2", "R7", "R8")
	seen := map[string]string{}
	for _, b := range it.Blocks {
		if b.Summary == "" || b.Domain != "SELV_5V" {
			t.Fatalf("block %s summary %q domain %q", b.ID, b.Summary, b.Domain)
		}
		for _, p := range b.Parts {
			if prev := seen[p]; prev != "" {
				t.Fatalf("%s in %s and %s", p, prev, b.ID)
			}
			seen[p] = b.ID
		}
	}
	if len(seen) != 31 {
		t.Fatalf("%d of 31 parts in blocks", len(seen))
	}

	// +3V3: simulated 3.318 V (0.6·(1+45.3k/10k)) / 0.52 A.
	v := it.Nets["+3V3"]
	near(t, "+3V3 nom", v.Voltage.Nom, 0.6*(1+45.3/10), 2e-3)
	near(t, "+3V3 current", v.CurrentA, 0.52, 0.01)
	if v.CurrentSource != "simulated" || v.NetClass != "POWER" || v.Block != "BUCK_3V3" || v.Domain != "SELV_5V" || v.Role != "power" {
		t.Fatalf("+3V3 plan %+v", v)
	}
	if v.WidthMil.Outer < 10 || v.WidthMil.Inner < v.WidthMil.Outer || v.WidthMil.Min > v.WidthMil.Outer || v.ViasPerTransition < 1 {
		t.Fatalf("+3V3 width %+v vias %d", v.WidthMil, v.ViasPerTransition)
	}
	u1 := false
	for _, p := range v.Pins {
		if p.Ref == "U1" && p.Pin == "2" {
			u1 = p.Dir == "sink" && p.CurrentA > 0.49
		}
	}
	if !u1 {
		t.Fatalf("+3V3 pins lack U1.2 sink ~0.5 A: %+v", v.Pins)
	}
	// Switch node.
	sw := it.Nets["SW"]
	if sw.NetClass != "SWITCH" || sw.Role != "switch" || sw.WidthMil.Outer < 20 || sw.Voltage.Peak < 4.5 || sw.PeakA < 0.6 {
		t.Fatalf("SW plan %+v", sw)
	}
	// USB pair: 90 Ω on the JLC 4-layer stackup.
	for _, pair := range [][2]string{{"USB_DP", "USB_DM"}, {"USB_DM", "USB_DP"}} {
		np := it.Nets[pair[0]]
		if np.DiffPair != pair[1] || np.ImpedanceOhm != 90 || np.NetClass != "HS_DIFF" || np.Role != "diff" || np.LengthGroup != "USB_D" {
			t.Fatalf("%s plan %+v", pair[0], np)
		}
		if np.WidthMil.Outer < 8 || np.WidthMil.Outer > 16 || np.PairGapMil < 5 {
			t.Fatalf("%s width %+v gap %.1f", pair[0], np.WidthMil, np.PairGapMil)
		}
	}
	// Ground is global (no block), grounds are GND class.
	if g := it.Nets["GND"]; g.NetClass != "GND" || g.Block != "" || g.Role != "ground" {
		t.Fatalf("GND plan %+v", g)
	}
	for name, np := range it.Nets {
		if len(np.Why) == 0 || np.NetClass == "" || np.Domain == "" {
			t.Fatalf("%s lacks why/class/domain", name)
		}
		if np.Role != "ground" && np.Block == "" {
			t.Fatalf("%s has no block", name)
		}
	}
	var classes []string
	for _, nc := range it.NetClasses {
		classes = append(classes, nc.Name)
		if nc.TrackMil <= 0 || nc.ClearanceMil < 6 || nc.ViaDrillMil <= 0 || nc.ViaDiaMil <= nc.ViaDrillMil {
			t.Fatalf("class %+v", nc)
		}
	}
	if got := strings.Join(classes, ","); got != "GND,POWER,SWITCH,HS_DIFF,SIGNAL" {
		t.Fatalf("classes %s", got)
	}
	// Domains: one SELV domain, no insulation pairs.
	if len(it.Domains) != 1 || it.Domains[0].ID != "SELV_5V" || it.Domains[0].Kind != "SELV" || it.Domains[0].Reference != "GND" || len(it.Pairs) != 0 {
		t.Fatalf("domains %+v pairs %+v", it.Domains, it.Pairs)
	}
	near(t, "domain Vrms", it.Domains[0].WorkingVrms, 5, 0.01)
	if it.Standard.Name != "IPC-2221B" || it.Standard.Insulation != "functional" {
		t.Fatalf("standard %+v", it.Standard)
	}
	// Findings: no errors; the intended warnings (L1 11 % peak margin, USB 86 %).
	kinds := map[string]string{}
	for _, f := range it.Findings {
		if f.Severity == "error" {
			t.Fatalf("unexpected error finding: %+v", f)
		}
		kinds[f.Kind] = f.Severity
	}
	for k, sev := range map[string]string{"inductor-rating": "warn", "usb-budget": "warn", "diode-loss": "info", "regulator-headroom": "info"} {
		if kinds[k] != sev {
			t.Fatalf("finding %s = %q, want %s (all: %v)", k, kinds[k], sev, kinds)
		}
	}
	// Contract keys survive JSON.
	b, _ := json.Marshal(it)
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"schemaVersion", "generator", "sources", "standard", "blocks", "domains", "nets", "pairs", "netClasses", "findings"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("intent.json lacks %s", k)
		}
	}
	n := raw["nets"].(map[string]any)["+3V3"].(map[string]any)
	for _, k := range []string{"role", "domain", "block", "voltage", "currentA", "currentSource", "pins", "widthMil", "viasPerTransition", "clearanceMil", "impedanceOhm", "diffPair", "lengthGroup", "netClass", "why"} {
		if _, ok := n[k]; !ok {
			t.Fatalf("nets[+3V3] lacks %s", k)
		}
	}
	for _, k := range []string{"nom", "min", "max", "peak"} {
		if _, ok := n["voltage"].(map[string]any)[k]; !ok {
			t.Fatalf("voltage lacks %s", k)
		}
	}
	st := raw["standard"].(map[string]any)
	for _, k := range []string{"name", "insulation", "mop", "mopCount", "pollutionDegree", "materialGroup", "altitudeM", "overvoltageCategory", "coated"} {
		if _, ok := st[k]; !ok {
			t.Fatalf("standard lacks %s", k)
		}
	}
	var md bytes.Buffer
	if err := WriteReport(&md, it); err != nil || !strings.Contains(md.String(), "BUCK_3V3") || !strings.Contains(md.String(), "HS_DIFF") {
		t.Fatalf("report: %v", err)
	}
}

// TestSimOnly derives from a sim document alone (netlist rebuilt from its pin
// lists): the buck and the +3V3 plan survive without part values.
func TestSimOnly(t *testing.T) {
	d := loadDesign(t, esp32Dir, []string{"sch-905bb85957eaf435.json", "sch-950ae6609e91d753.json"})
	out, _, err := powersim.Simulate(d, libs(t), powersim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	sim, err := ParseSimOutput(b)
	if err != nil {
		t.Fatal(err)
	}
	it, err := Derive(Input{Sim: sim, Libs: libs(t)})
	if err != nil {
		t.Fatal(err)
	}
	blockBy(t, it, "buck", "U4")
	blockBy(t, it, "rf-module", "U1")
	near(t, "+3V3", it.Nets["+3V3"].Voltage.Nom, 3.318, 2e-3)
	if it.Nets["USB_DP"].DiffPair != "USB_DM" || it.Nets["SW"].NetClass != "SWITCH" {
		t.Fatalf("sim-only nets %+v %+v", it.Nets["USB_DP"], it.Nets["SW"])
	}
	if _, err := ParseSimOutput([]byte(`{"schemaVersion":2,"results":[{}]}`)); err == nil {
		t.Fatal("schemaVersion 2 accepted")
	}
}

// TestSpecOverrides: a declared rail current wins (and changes the class), a
// 2-layer stackup cannot hold 90 Ω, coated boards use IPC-2221B B4.
func TestSpecOverrides(t *testing.T) {
	spec, err := ParseSpec([]byte(`{"layers":2,"rails":[{"net":"+3V3","currentA":2.0,"rippleMvpp":30}],"usbBudgetA":1.5,"standard":{"coated":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	it := esp32Intent(t, spec)
	v := it.Nets["+3V3"]
	if v.CurrentSource != "declared" || v.CurrentA != 2 || v.NetClass != "POWER_HI" || v.WidthMil.Outer <= 20 || v.RippleMvpp != 30 || v.ViasPerTransition < 2 {
		t.Fatalf("declared +3V3 %+v", v)
	}
	if dp := it.Nets["USB_DP"]; dp.WidthMil.Outer != 6 || dp.ImpedanceOhm != 90 {
		t.Fatalf("2-layer USB_DP %+v", dp)
	}
	kinds := map[string]string{}
	for _, f := range it.Findings {
		kinds[f.Kind] = f.Severity
	}
	if kinds["impedance-uncontrolled"] != "warn" || kinds["usb-budget"] != "info" {
		t.Fatalf("findings %v", kinds)
	}
	if !it.Standard.Coated || it.Copper.Layers != 2 {
		t.Fatalf("standard/copper %+v %+v", it.Standard, it.Copper)
	}
	for _, bad := range []string{`{"standrad":{}}`, `{"standard":{"name":"UL94"}}`, `{"standard":{"insulation":"strong"}}`, `{"domains":[{"kind":"alien","nets":["X"]}]}`} {
		if _, err := ParseSpec([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func mainsIntent(t *testing.T, mutate func(*Spec)) *Intent {
	t.Helper()
	d := loadDesign(t, mainsDir, []string{"connectivity.json"})
	sb, err := os.ReadFile(filepath.Join(mainsDir, "spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec, err := ParseSpec(sb)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(spec)
	}
	it, err := Derive(Input{Design: d, Libs: libs(t, filepath.Join(mainsDir, "models.json")), Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

// TestMainsOpto exercises domains and insulation pairs on a synthetic board:
// mains terminal → fuse → isolated AC-DC → LDO → MCU; relay switching the
// line; a PC817 from a 24 V field input on its own ground.
func TestMainsOpto(t *testing.T) {
	it := mainsIntent(t, nil)
	kinds := map[string]*Domain{}
	for _, d := range it.Domains {
		kinds[d.Kind] = d
	}
	mains, selv, iso := kinds["mains"], kinds["SELV"], kinds["isolated-secondary"]
	if mains == nil || selv == nil || iso == nil || len(it.Domains) != 3 {
		t.Fatalf("domains %+v", it.Domains)
	}
	if mains.ID != "MAINS_230VAC" || mains.WorkingVrms != 230 || math.Abs(mains.WorkingVpeak-325.27) > 0.01 || iso.Reference != "FIELD_GND" || iso.WorkingVrms != 24 {
		t.Fatalf("mains %+v iso %+v", mains, iso)
	}
	hasAll(t, "mains nets", mains.Nets, "L", "N", "L_F", "LOAD_L")
	if len(it.Pairs) != 2 {
		t.Fatalf("pairs %+v", it.Pairs)
	}
	var hv, fn *Pair
	for _, p := range it.Pairs {
		switch p.Insulation {
		case "reinforced":
			hv = p
		case "functional":
			fn = p
		}
	}
	if hv == nil || fn == nil {
		t.Fatalf("pairs %+v", it.Pairs)
	}
	if hv.A != "domain:SELV_5V" || hv.B != "domain:MAINS_230VAC" || hv.WorkingVrms != 230 || hv.ClearanceMm < 4 || hv.CreepageMm < hv.ClearanceMm || hv.SlotRequired || !strings.Contains(hv.StandardRef, "IEC62368-1") {
		t.Fatalf("hv pair %+v", hv)
	}
	hasAll(t, "hv bridges", hv.Bridges, "PS1", "K1")
	hasAll(t, "fn bridges", fn.Bridges, "U3")
	// Line nets: HV class, IPC-2221B B2 clearance at 325 V, fuse-sized copper.
	l := it.Nets["L_F"]
	if l.NetClass != "HV_MAINS_230VAC" || l.ClearanceMil < 90 || math.Abs(l.Voltage.Peak-325.27) > 0.01 || l.CurrentA != 1 || l.Role != "power" || l.WidthMil.Outer < 10 {
		t.Fatalf("L_F plan %+v", l)
	}
	if it.Nets["FIELD_24V"].Domain != iso.ID || it.Nets["+3V3"].Domain != selv.ID {
		t.Fatalf("net domains")
	}
	blockBy(t, it, "isolation", "U3")
	blockBy(t, it, "isolation", "PS1")
	blockBy(t, it, "mains", "J1")
	blockBy(t, it, "ldo", "U2")
	fk := map[string]string{}
	for _, f := range it.Findings {
		if f.Severity == "error" {
			t.Fatalf("unexpected error %+v", f)
		}
		fk[f.Kind] = f.Severity
	}
	if fk["mains-current"] != "warn" || fk["insulation"] != "info" {
		t.Fatalf("findings %v", fk)
	}
	// Pollution degree 3: creepage ×1.6 exceeds what the bridge footprint
	// gives → a slot is required and flagged.
	it3 := mainsIntent(t, func(s *Spec) { s.Standard.PollutionDegree = 3 })
	slot := false
	for _, p := range it3.Pairs {
		if p.Insulation == "reinforced" {
			slot = p.SlotRequired && p.SlotWidthMm >= 1 && p.CreepageMm > 7
		}
	}
	found := false
	for _, f := range it3.Findings {
		found = found || f.Kind == "insulation-slot"
	}
	if !slot || !found {
		t.Fatalf("PD3 slot not required/flagged: %+v", it3.Pairs)
	}
	// A default (undeclared) standard on a mains board is flagged.
	itd := mainsIntent(t, func(s *Spec) { s.Standard = nil })
	if itd.Standard.Name != "IEC62368-1" || itd.Standard.Insulation != "reinforced" {
		t.Fatalf("default standard %+v", itd.Standard)
	}
	ok := false
	for _, f := range itd.Findings {
		ok = ok || f.Kind == "standard-defaulted"
	}
	if !ok {
		t.Fatal("standard-defaulted finding missing")
	}
}

// TestSafetyProvider: the one integration point swaps the numbers.
func TestSafetyProvider(t *testing.T) {
	defer func() { SafetyProvider = nil }()
	SafetyProvider = func(p Pair, st Standard) (float64, float64, bool, float64, string, []string) {
		return 6.4, 8.0, true, 2.0, "stub " + st.Name, []string{"stub"}
	}
	it := mainsIntent(t, nil)
	for _, p := range it.Pairs {
		if p.ClearanceMm != 6.4 || p.CreepageMm != 8 || !p.SlotRequired || p.SlotWidthMm != 2 || p.StandardRef != "stub IEC62368-1" {
			t.Fatalf("provider not used: %+v", p)
		}
	}
}

func TestPlaceholderDistances(t *testing.T) {
	base := Standard{Name: "IEC62368-1", PollutionDegree: 2, MaterialGroup: "IIIa", AltitudeM: 2000}
	cl, cr, slot, _, _, _ := placeholderDistances(Pair{WorkingVrms: 230, Insulation: "reinforced"}, base)
	if cl != 4 || math.Abs(cr-4.6) > 1e-9 || slot {
		t.Fatalf("230 V reinforced: %.2f/%.2f slot=%v", cl, cr, slot)
	}
	hi := base
	hi.AltitudeM = 3000
	cl3, _, _, _, _, _ := placeholderDistances(Pair{WorkingVrms: 230, Insulation: "reinforced"}, hi)
	near(t, "altitude 3000 m", cl3, 4*1.14, 1e-9)
	med := base
	med.Name, med.MOP = "IEC60601-1", "MOPP"
	cl, cr, slot, _, _, _ = placeholderDistances(Pair{WorkingVrms: 230, Insulation: "reinforced", MOPCount: 2}, med)
	if cl < 5 || cr < 8 || !slot {
		t.Fatalf("2 MOPP: %.2f/%.2f slot=%v", cl, cr, slot)
	}
	cl, cr, _, _, _, _ = placeholderDistances(Pair{WorkingVrms: 5, Insulation: "functional"}, base)
	if cl > 0.2 || cr < cl {
		t.Fatalf("functional 5 V: %.2f/%.2f", cl, cr)
	}
}

func TestResolveStandard(t *testing.T) {
	st := resolveStandard(&Standard{Name: "iec60601-1", Insulation: "Reinforced"}, true)
	if st.Name != "IEC60601-1" || st.Insulation != "reinforced" || st.MOP != "MOPP" || st.MOPCount != 2 || st.PollutionDegree != 2 {
		t.Fatalf("%+v", st)
	}
	if has(st.Defaulted, "name") || !has(st.Defaulted, "mop") {
		t.Fatalf("defaulted %v", st.Defaulted)
	}
}
