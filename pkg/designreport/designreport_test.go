package designreport

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

const (
	esp32Dir   = "../powersim/testdata/esp32mini"
	skillModel = "../../.agents/skills/pcbpilot/references/power-models.json"
	skillTmpl  = "../../.agents/skills/pcbpilot/templates/design-report"
)

var fixedTime = time.Date(2026, 9, 27, 22, 30, 0, 0, time.UTC)

// esp32Inputs replays the committed ESP32-mini schematic: DC simulation with
// the skill models, intent derive, values (MPNs + LCSC descriptions).
func esp32Inputs(t *testing.T) *Inputs {
	t.Helper()
	var docs []*powersim.ConnDoc
	for _, f := range []string{"sch-905bb85957eaf435.json", "sch-950ae6609e91d753.json"} {
		b, err := os.ReadFile(filepath.Join(esp32Dir, f))
		if err != nil {
			t.Fatal(err)
		}
		d, err := powersim.ParseConnectivity(b)
		if err != nil {
			t.Fatal(err)
		}
		docs = append(docs, d)
	}
	vb, err := os.ReadFile(filepath.Join(esp32Dir, "values.json"))
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
	lib, err := powersim.LoadLibrary(skillModel)
	if err != nil {
		t.Fatal(err)
	}
	sim, _, err := powersim.Simulate(d, powersim.Libraries{lib}, powersim.Options{})
	if err != nil {
		t.Fatal(err)
	}
	it, err := intent.Derive(intent.Input{Design: d, Libs: powersim.Libraries{lib}, Sim: sim})
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := os.ReadFile(skillModel)
	models, err := ParseModels(mb, skillModel)
	if err != nil {
		t.Fatal(err)
	}
	return &Inputs{Project: "ESP32 mini", Customer: "Test", GeneratedAt: fixedTime, Intent: it, Sim: sim, Models: models, Values: vals,
		Refs: []InputRef{{Kind: "intent", Label: "intent", Path: "intent.json", SHA256: "aa", Present: true}, {Kind: "sim", Label: "sim", Path: "sim.json", SHA256: "bb", Present: true}}}
}

func row(t *testing.T, r *Report, ref, checkPrefix string) FeasRow {
	t.Helper()
	for _, x := range r.Feasibility.Rows {
		if x.Ref == ref && strings.HasPrefix(x.Check, checkPrefix) {
			return x
		}
	}
	t.Fatalf("no feasibility row %s %s", ref, checkPrefix)
	return FeasRow{}
}

func TestMPNDecoders(t *testing.T) {
	cases := []struct {
		mpn          string
		pkg, diel    string
		farad, volts float64
	}{
		{"CL05B104KO5NNNC", "0402", "X7R", 100e-9, 16},
		{"CL21A226MAQNNNE", "0805", "X5R", 22e-6, 25},
		{"CL05A105KA5NQNC", "0402", "X5R", 1e-6, 25},
		{"GRM21BR61H106KE43L", "0805", "X5R", 10e-6, 50},
		{"GRM155R71C104KA88D", "0402", "X7R", 100e-9, 16},
		{"CC0402KRX7R9BB104", "0402", "X7R", 100e-9, 50},
	}
	for _, c := range cases {
		ci, ok := ParseCapacitor(c.mpn, "")
		if !ok || ci.Package != c.pkg || ci.Dielectric != c.diel || math.Abs(ci.Farad-c.farad)/c.farad > 1e-9 || ci.RatedV != c.volts {
			t.Errorf("%s → %+v", c.mpn, ci)
		}
	}
	if ci, ok := ParseCapacitor("", "4.7uF 10V 0603"); !ok || ci.RatedV != 10 || ci.Package != "0603" || math.Abs(ci.Farad-4.7e-6) > 1e-12 {
		t.Errorf("value string → %+v", ci)
	}
	if _, ok := ParseCapacitor("XYZ123", ""); ok {
		t.Error("unknown capacitor decoded")
	}
	res := []struct {
		mpn string
		pkg string
		ohm float64
		tol float64
	}{
		{"0402WGF4532TCE", "0402", 45300, 1},
		{"0402WGJ0472TCE", "0402", 4700, 5},
		{"0603WAF1002T5E", "0603", 10000, 1},
		{"RC0402FR-0710KL", "0402", 10000, 1},
		{"RC0805JR-074K7L", "0805", 4700, 5},
	}
	for _, c := range res {
		ri, ok := ParseResistor(c.mpn, "")
		if !ok || ri.Package != c.pkg || math.Abs(ri.Ohm-c.ohm) > 1e-6 || ri.TolPct != c.tol {
			t.Errorf("%s → %+v", c.mpn, ri)
		}
	}
	if ri, _ := ParseResistor("", "1M"); ri.Ohm != 1e6 {
		t.Errorf("1M → %g", ri.Ohm)
	}
	want := map[string]float64{"0402": 1.0 / 16, "0603": 0.1, "0805": 0.125, "1206": 0.25}
	for p, w := range want {
		if ResistorPackageW[p] != w {
			t.Errorf("%s rating %g want %g", p, ResistorPackageW[p], w)
		}
	}
	d, ok := ParseDescRatings("Pitch:5mm Current Rating:17A Voltage Rating (Max):250V Power(Watts):62.5mW")
	if !ok || d.CurrentA != 17 || d.VoltageV != 250 || math.Abs(d.PowerW-0.0625) > 1e-12 {
		t.Errorf("desc → %+v", d)
	}
	if d, _ := ParseDescRatings("Power(Watts):1/10W"); math.Abs(d.PowerW-0.1) > 1e-12 {
		t.Errorf("fraction power → %+v", d)
	}
}

func TestFeasibilityESP32(t *testing.T) {
	r := Build(esp32Inputs(t))
	if r.Feasibility == nil {
		t.Fatal("no feasibility")
	}
	// Inductor: Ipk vs the single current rating (0.77 A) — marginal.
	l := row(t, r, "L1", "峰值电流")
	if l.Status != FeasMarginal || l.Rating != 0.77 || l.MarginPct == nil || *l.MarginPct <= 0 || *l.MarginPct >= 20 {
		t.Errorf("L1 Ipk row %+v", l)
	}
	// Regulator (U4 SY8089A in this fixture) output current vs 2 A: ok.
	if u := row(t, r, "U4", "输出电流"); u.Status != FeasOK || u.Rating != 2 {
		t.Errorf("U4 Iout %+v", u)
	}
	// Junction temperature without θJA → needs datasheet, never guessed.
	if u := row(t, r, "U4", "结温"); u.Status != FeasUnknown || u.Rating != 0 || !strings.Contains(u.Note, "需数据手册") {
		t.Errorf("U4 Tj %+v", u)
	}
	// ESP32 module supply within 3.0–3.6 V (ratings), GPIO drive vs 40 mA.
	if u := row(t, r, "U1", "供电 +3V3 Vmax"); u.Rating != 3.6 || u.Status != FeasOK {
		t.Errorf("U1 supply %+v", u)
	}
	// Resistor on 0402 → 1/16 W from the package table, P from the sim.
	loaded := 0
	for _, x := range r.Feasibility.Rows {
		if x.Kind == powersim.KindResistor && x.Stress > 0 {
			loaded++
			if x.Status != FeasOK || math.Abs(x.Rating-1.0/16) > 1e-9 {
				t.Errorf("resistor %+v", x)
			}
		}
	}
	if loaded == 0 {
		t.Error("no resistor carries simulated power")
	}
	// MLCC DC voltage from the decoded MPN.
	for _, x := range r.Feasibility.Rows {
		if x.Kind == powersim.KindCapacitor && strings.HasPrefix(x.Check, "直流电压") && x.Status == FeasUnknown {
			t.Errorf("capacitor voltage rating not decoded: %+v", x)
		}
	}
	// J1 contact current from the LCSC description "Current Rating:17A".
	if j := row(t, r, "J1", "接触电流"); j.Rating != 17 || j.Status != FeasOK {
		t.Errorf("J1 %+v", j)
	}
	// Connector-source budget (USB 0.5 A) exists.
	if j := row(t, r, "J2", "供电预算"); j.Rating != 0.5 {
		t.Errorf("J2 budget %+v", j)
	}
	n := 0
	for _, x := range r.Feasibility.Rows {
		n += 0
		if x.Status == FeasUnknown && x.MarginPct != nil {
			t.Errorf("unknown row with margin %+v", x)
		}
		n++
	}
	if n != r.Feasibility.Counts[FeasOK]+r.Feasibility.Counts[FeasMarginal]+r.Feasibility.Counts[FeasOver]+r.Feasibility.Counts[FeasUnknown] {
		t.Error("counts do not add up")
	}
}

func TestFeasibilityOverAndUnknown(t *testing.T) {
	// Minimal synthetic sim: a 0402 resistor dissipating 0.1 W (> 1/16 W) and
	// an unmodelled IC.
	sim := &powersim.Output{SchemaVersion: 1, Scenarios: []string{"typical", "worst"}}
	nets := map[string]*powersim.NetResult{
		"VIN": {Voltage: 5, CurrentA: 0.02, Role: "power", Pins: []powersim.PinResult{{Ref: "R1", Pin: "1", CurrentA: 0.02, Dir: "sink"}, {Ref: "U9", Pin: "1", CurrentA: 0.001, Dir: "sink"}}},
		"GND": {Voltage: 0, CurrentA: 0.02, Role: "ground", Pins: []powersim.PinResult{{Ref: "R1", Pin: "2", CurrentA: 0.02, Dir: "source"}, {Ref: "U9", Pin: "2", CurrentA: 0.001, Dir: "source"}}},
	}
	parts := map[string]*powersim.PartResult{"R1": {Model: "resistor", PowerW: 0.1}}
	sim.Results = []powersim.Result{{Scenario: "typical", Converged: true, Nets: nets, Parts: parts}, {Scenario: "worst", Converged: true, Nets: nets, Parts: parts}}
	sim.Models = []powersim.ModelUse{{Ref: "R1", Kind: "resistor", ModelID: "generic-resistor", Confidence: "value"}}
	r := Build(&Inputs{GeneratedAt: fixedTime, Sim: sim, Values: map[string]powersim.PartValues{"R1": {MPN: "0402WGF2500TCE"}}})
	x := row(t, r, "R1", "功率")
	if x.Status != FeasOver || x.MarginPct == nil || *x.MarginPct >= 0 {
		t.Fatalf("R1 %+v", x)
	}
	if u := row(t, r, "U9", "无功率模型"); u.Status != FeasUnknown {
		t.Fatalf("U9 %+v", u)
	}
	if r.Verdict.Status != VerdictFail || !strings.Contains(strings.Join(r.Verdict.Reasons, ";"), "超过额定") {
		t.Fatalf("verdict %+v", r.Verdict)
	}
}

func TestTestPlan(t *testing.T) {
	in := esp32Inputs(t)
	// A board with the +3V3 load pad (U1 = ESP32 module in this fixture) and
	// a nearby cap pad but no TP part.
	in.Board = &Board{Components: []BoardPart{
		{Designator: "U1", Pads: []BoardPad{{PadNumber: "2", Net: "+3V3", X: 100, Y: 100}}},
		{Designator: "C3", Pads: []BoardPad{{PadNumber: "1", Net: "+3V3", X: 140, Y: 100}, {PadNumber: "2", Net: "GND", X: 180, Y: 100}}},
	}}
	in.Board.Outline.Points = [][]float64{{0, 0}, {1000, 0}, {1000, 1000}, {0, 1000}}
	r := Build(in)
	var rail *TestItem
	for i := range r.TestPlan.Items {
		if r.TestPlan.Items[i].Category == "电源轨" && r.TestPlan.Items[i].Net == "+3V3" {
			rail = &r.TestPlan.Items[i]
		}
	}
	if rail == nil {
		t.Fatalf("no +3V3 rail item: %+v", r.TestPlan.Items)
	}
	// Regulated rail: ± (Vref accuracy + divider tolerance), probe C3.1.
	if !strings.Contains(rail.Expected, "±") || !strings.Contains(rail.Where, "C3.1") || !strings.Contains(rail.Basis, "分压") {
		t.Errorf("rail item %+v", *rail)
	}
	var tp *TPRecommendation
	for i := range r.TestPlan.AddTP {
		if r.TestPlan.AddTP[i].Net == "+3V3" {
			tp = &r.TestPlan.AddTP[i]
		}
	}
	if tp == nil || tp.Near != "U1.2" || tp.XMm != 2.5 || tp.YMm != 2.5 {
		t.Errorf("TP recommendation %+v", tp)
	}
	cats := map[string]bool{}
	for _, it := range r.TestPlan.Items {
		cats[it.Category] = true
	}
	for _, c := range []string{"上电限流", "电源轨", "开关节点", "输出纹波", "复位/启动", "指示灯", "接口", "阻抗"} {
		if !cats[c] {
			t.Errorf("test plan lacks %s", c)
		}
	}
}

func TestChartsWellFormed(t *testing.T) {
	r := Build(esp32Inputs(t))
	ch := Charts(r)
	for _, k := range []string{"rail-current", "part-power", "rail-power", "power-tree", "margins"} {
		if ch[k] == "" {
			t.Errorf("chart %s missing", k)
		}
	}
	for name, svg := range ch {
		if strings.Contains(svg, "NaN") || strings.Contains(svg, "Inf") {
			t.Errorf("%s contains NaN/Inf", name)
		}
		dec := xml.NewDecoder(strings.NewReader(svg))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: not well-formed XML: %v", name, err)
			}
		}
		if !strings.Contains(svg, "<title>") {
			t.Errorf("%s has no title", name)
		}
	}
	// Edge cases: empty rows and a single series.
	for _, svg := range []string{HBars("empty", "x", "u", nil, 0, 0, nil, nil), GroupedBars("one", "y", "A", []string{"a"}, []string{"s"}, [][]float64{{0}})} {
		if err := xml.Unmarshal([]byte(svg), new(struct{})); err != nil {
			t.Errorf("edge chart: %v", err)
		}
	}
}

func TestDeterminismAndRender(t *testing.T) {
	a, b := Build(esp32Inputs(t)), Build(esp32Inputs(t))
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if !bytes.Equal(ja, jb) {
		t.Fatal("report.json differs for identical inputs")
	}
	ha, err := RenderHTML(a, Charts(a))
	if err != nil {
		t.Fatal(err)
	}
	hb, _ := RenderHTML(b, Charts(b))
	if !bytes.Equal(ha, hb) {
		t.Fatal("report.html differs for identical inputs")
	}
	ma, err := RenderMarkdown(a)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := RenderMarkdown(b)
	if !bytes.Equal(ma, mb) {
		t.Fatal("report.md differs for identical inputs")
	}
	for _, s := range []string{"1 执行摘要", "4 器件可行性", "8 测试点计划", "11 附录", "<svg"} {
		if !bytes.Contains(ha, []byte(s)) {
			t.Errorf("html lacks %q", s)
		}
	}
	if bytes.Contains(ha, []byte("ZgotmplZ")) || bytes.Contains(ma, []byte("<no value>")) {
		t.Error("template produced an escaped/unset value")
	}
	// Only the header carries time.
	c := Build(func() *Inputs { in := esp32Inputs(t); in.GeneratedAt = fixedTime.Add(time.Hour); return in }())
	c.GeneratedAt = a.GeneratedAt
	jc, _ := json.Marshal(c)
	if !bytes.Equal(ja, jc) {
		t.Fatal("generatedAt is not the only time-dependent field")
	}
}

func TestMissingInputs(t *testing.T) {
	r := Build(&Inputs{GeneratedAt: fixedTime, Project: "empty"})
	if r.Power != nil || r.Feasibility != nil || r.Calcs != nil || r.TestPlan != nil || r.Requirements != nil {
		t.Fatal("sections invented without inputs")
	}
	if len(r.Missing) < 5 || r.Verdict.Status != VerdictWarnings {
		t.Fatalf("missing %v verdict %+v", r.Missing, r.Verdict)
	}
	for _, ch := range r.Verification {
		if ch.Status != StatusNA {
			t.Errorf("check %s = %s without evidence", ch.Name, ch.Status)
		}
	}
	h, err := RenderHTML(r, Charts(r))
	if err != nil {
		t.Fatal(err)
	}
	if n := bytes.Count(h, []byte("本节不可用")); n < 6 {
		t.Errorf("html has %d not-available notes", n)
	}
	m, err := RenderMarkdown(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(m, []byte("本节不可用")) {
		t.Error("markdown lacks not-available notes")
	}
	// Parsers reject documents of the wrong kind instead of guessing.
	if _, err := ParseDRC([]byte(`{"foo":1}`)); err == nil {
		t.Error("ParseDRC accepted a non-DRC document")
	}
	if _, err := ParseBoard([]byte(`{"components":[]}`)); err == nil {
		t.Error("ParseBoard accepted an empty dump")
	}
	if _, err := ParseCheck([]byte("hello")); err == nil {
		t.Error("ParseCheck accepted free text")
	}
}

func TestParsers(t *testing.T) {
	d, err := ParseDRC([]byte(`{"ok":true,"createdAt":"t","result":{"passed":false,"violations":[{"count":2,"list":[{"count":2,"list":[{"errorObjType":"Track","errorType":"Physical Error"},{"errorObjType":"Via","errorType":"Clearance Error"}]}]}]}}`))
	if err != nil || d.Passed || d.Violations != 2 || len(d.Kinds) != 2 {
		t.Fatalf("drc %+v %v", d, err)
	}
	txt := "PCB check (DFM): 3 track(s)\n  LIMIT pours are not measured\n  ERROR=0 WARN=2  |  dangling=1 fiducialMissing=1\n  WARN  dangling-end      track end connects to nothing @ (1, 2)  [USB_DP]\n  INFO  fiducial-missing  no marks  []\n  WARN  non-orthogonal    trace runs at 10° @ (3, 4)  [GND]\n"
	c, err := ParseCheck([]byte(txt))
	if err != nil || c.Warns != 2 || c.Infos != 1 || c.Counts["dangling"] != 1 || len(c.Limits) != 1 || c.Items[0].Nets != "USB_DP" {
		t.Fatalf("check %+v %v", c, err)
	}
	cj, err := ParseCheck([]byte(`{"passed":false,"findings":[{"type":"acute-angle","level":"WARN","message":"m","net":"A"}]}`))
	if err != nil || cj.Warns != 1 || cj.Items[0].Type != "acute-angle" {
		t.Fatalf("check json %+v %v", cj, err)
	}
	nd, err := ParseNetDiff([]byte(`{"mismatches":[{"a":1}]}`))
	if err != nil || nd.Passed || nd.Diffs != 1 {
		t.Fatalf("netdiff %+v %v", nd, err)
	}
}

func TestVersioningChangelog(t *testing.T) {
	idx, _ := ParseIndex(nil)
	if v, _ := idx.ResolveVersion("auto"); v != 1 {
		t.Fatalf("first auto = %d", v)
	}
	if _, err := idx.ResolveVersion("vX"); err == nil {
		t.Fatal("bad version accepted")
	}
	in := esp32Inputs(t)
	r1 := Build(in)
	r1.Version, r1.VersionLabel = 1, "v1"
	e1 := EntryOf(r1)
	idx.Put(e1)
	if v, _ := idx.ResolveVersion("auto"); v != 2 {
		t.Fatalf("next auto = %d", v)
	}
	if v, _ := idx.ResolveVersion("v7"); v != 7 {
		t.Fatalf("v7 = %d", v)
	}
	// v2: declare a 1 A load on +3V3 through the intent's current and a
	// changed input digest; a finding disappears, another appears.
	in2 := esp32Inputs(t)
	in2.Refs[0].SHA256 = "cc"
	in2.Intent.Findings = append(in2.Intent.Findings[1:], &intent.Finding{Severity: "warn", Kind: "declared-load", Message: "+3V3 declared 1 A"})
	w := &in2.Sim.Results[len(in2.Sim.Results)-1]
	w.Nets["+3V3"].CurrentA = 1.0
	r2 := Build(in2)
	r2.Version, r2.VersionLabel = 2, "v2"
	e2 := EntryOf(r2)
	ch := Compare(idx.Latest(2), e2)
	if ch == nil || ch.Previous != "v1" || ch.Identical || len(ch.InputsChanged) != 1 {
		t.Fatalf("changes %+v", ch)
	}
	found := false
	for _, row := range ch.Rows {
		if row.Metric == "电源轨电流 +3V3" && strings.HasPrefix(row.After, "1 ") {
			found = true
		}
	}
	if !found {
		t.Errorf("rail current change missing: %+v", ch.Rows)
	}
	if len(ch.FindingsAdded) == 0 || len(ch.FindingsResolved) == 0 {
		t.Errorf("findings diff %+v / %+v", ch.FindingsAdded, ch.FindingsResolved)
	}
	e2.Changes = ch
	idx.Put(e2)
	log := RenderChangelog(idx)
	if !strings.Contains(log, "## v2") || !strings.Contains(log, "相对 v1") || strings.Index(log, "## v2") > strings.Index(log, "## v1") {
		t.Errorf("changelog:\n%s", log)
	}
	// Identical inputs are flagged.
	if c := Compare(&e1, EntryOf(r1)); c == nil || !c.Identical || len(c.Rows) != 0 {
		t.Errorf("identical compare %+v", c)
	}
}

// TestTemplatesMatchSkill keeps the embedded templates identical to the
// Skill's canonical copy (.agents/skills/pcbpilot/templates/design-report/).
func TestTemplatesMatchSkill(t *testing.T) {
	for _, name := range []string{"report.html.tmpl", "report.md.tmpl"} {
		emb, err := Templates.ReadFile("templates/" + name)
		if err != nil {
			t.Fatal(err)
		}
		sk, err := os.ReadFile(filepath.Join(skillTmpl, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(emb, sk) {
			t.Errorf("%s differs from the Skill copy — edit %s and run: cp %s pkg/designreport/templates/", name, filepath.Join(skillTmpl, name), filepath.Join(".agents/skills/pcbpilot/templates/design-report", name))
		}
	}
}

func TestImagePrepare(t *testing.T) {
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>`)
	img, err := PrepareImage("preview", "p", "p.svg", svg, 0)
	if err != nil || img.Mime != "image/svg+xml" || !regexp.MustCompile(`^[0-9a-f]{16}\.svg$`).MatchString(img.Asset) {
		t.Fatalf("svg %+v %v", img, err)
	}
	if _, err := PrepareImage("x", "x", "x.bin", []byte("plain text"), 0); err == nil {
		t.Fatal("text accepted as image")
	}
}
