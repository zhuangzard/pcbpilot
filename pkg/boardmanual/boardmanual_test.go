package boardmanual

import (
	"bytes"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

const skillTmpl = "../../.agents/skills/pcbpilot/templates/board-manual"

// testBoard is a 40 × 30 mm board: J1 power jack, J2 3-pin signal header,
// JP1 2-pin jumper, X1 oscillator, U1 IC, two LEDs on the bottom edge and
// a round multi-layer fill (mounting hole).
const testBoard = `{"result":{
 "components":[
  {"designator":"J1","device":"DC-005-5A-2.0","x":100,"y":600,"bbox":{"minX":20,"minY":500,"maxX":220,"maxY":700},
   "pads":[{"padNumber":"1","net":"VIN","x":60,"y":600,"width":60,"height":120,"shape":["OVAL",120,60]},
           {"padNumber":"2","net":"GND","x":160,"y":600,"width":60,"height":120,"shape":["OVAL",120,60]}]},
  {"designator":"J2","device":"B3B-XH-A","x":1400,"y":600,
   "pads":[{"padNumber":"1","net":"TX","x":1500,"y":500,"width":60,"height":60,"shape":["RECT",60,60,0]},
           {"padNumber":"2","net":"+3V3","x":1500,"y":598,"width":60,"height":60,"shape":["ELLIPSE",60,60]},
           {"padNumber":"3","net":"","x":1500,"y":696,"width":60,"height":60,"shape":["ELLIPSE",60,60]}]},
  {"designator":"JP1","device":"PZ254V-11-02P","x":800,"y":1000,
   "pads":[{"padNumber":"1","net":"CFG","x":750,"y":1000,"width":60,"height":60,"shape":["RECT",60,60,0]},
           {"padNumber":"2","net":"GND","x":850,"y":1000,"width":60,"height":60,"shape":["ELLIPSE",60,60]}]},
  {"designator":"X1","device":"HSO321S 16MHZ","x":700,"y":400,
   "pads":[{"padNumber":"1","net":"OE","x":680,"y":400,"width":20,"height":20},{"padNumber":"2","net":"CLK","x":720,"y":400,"width":20,"height":20}]},
  {"designator":"U1","device":"MCU","x":800,"y":600,
   "pads":[{"padNumber":"1","net":"TX","x":780,"y":600,"width":10,"height":30},{"padNumber":"2","net":"+3V3","x":820,"y":600,"width":10,"height":30}]},
  {"designator":"C1","device":"100nF","x":900,"y":700,
   "pads":[{"padNumber":"1","net":"+3V3","x":890,"y":700,"width":20,"height":20},{"padNumber":"2","net":"GND","x":910,"y":700,"width":20,"height":20}]},
  {"designator":"D1","device":"LED","x":600,"y":60,"pads":[{"padNumber":"1","net":"LED0_A","x":590,"y":60,"width":20,"height":20},{"padNumber":"2","net":"GND","x":610,"y":60,"width":20,"height":20}]},
  {"designator":"D2","device":"LED","x":700,"y":60,"pads":[{"padNumber":"1","net":"LED1_A","x":690,"y":60,"width":20,"height":20},{"padNumber":"2","net":"GND","x":710,"y":60,"width":20,"height":20}]}
 ],
 "outline":{"points":[[0,0],[1574.8,0],[1574.8,1181.1],[0,1181.1]]},
 "copperLayers":2,
 "copper":{"fills":[{"bbox":{"minX":100,"minY":1000,"maxX":226,"maxY":1126},"layer":12,"net":null}]},
 "capturedAt":"2026-10-06T00:00:00Z"
}}`

func testInputs(t *testing.T, notes string) Inputs {
	t.Helper()
	b, err := ParseBoard([]byte(testBoard))
	if err != nil {
		t.Fatal(err)
	}
	in := Inputs{Board: b, GeneratedAt: time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)}
	in.Sim = &powersim.Output{Scenarios: []string{"typical", "peak"}, Results: []powersim.Result{
		{Scenario: "typical", Nets: map[string]*powersim.NetResult{
			"VIN":  {Voltage: 12, CurrentA: 0.5, Role: "power", Pins: []powersim.PinResult{{Ref: "J1", Pin: "1", CurrentA: 0.5, Dir: "source"}}},
			"+3V3": {Voltage: 3.3, CurrentA: 0.1, Role: "power", Pins: []powersim.PinResult{{Ref: "J2", Pin: "2", CurrentA: 0.02, Dir: "sink"}}},
		}},
		{Scenario: "peak", Nets: map[string]*powersim.NetResult{
			"VIN":  {Voltage: 11.9, CurrentA: 2.4, Role: "power", Pins: []powersim.PinResult{{Ref: "J1", Pin: "1", CurrentA: 2.4, Dir: "source"}}},
			"+3V3": {Voltage: 3.29, CurrentA: 0.2, Role: "power", Pins: []powersim.PinResult{{Ref: "J2", Pin: "2", CurrentA: 0.05, Dir: "sink"}}},
		}},
	}}
	in.Intent = &intent.Intent{Nets: map[string]*intent.NetPlan{"TX": {Role: "signal"}}, Domains: []*intent.Domain{{ID: "HV", Kind: "hazardous", Nets: []string{"VIN"}, WorkingVpeak: 60}}}
	if notes != "" {
		n, err := ParseNotes([]byte(notes))
		if err != nil {
			t.Fatal(err)
		}
		in.Notes = n
	}
	return in
}

func TestConnectorDetection(t *testing.T) {
	b, _ := ParseBoard([]byte(testBoard))
	want := map[string]bool{"J1": true, "J2": true, "JP1": true, "X1": false, "U1": false, "C1": false, "D1": false}
	for ref, w := range want {
		if got := IsConnector(b.Part(ref)); got != w {
			t.Errorf("IsConnector(%s) = %v, want %v", ref, got, w)
		}
	}
	if k := ConnKind(b.Part("JP1"), false); k != "jumper" {
		t.Errorf("JP1 kind %q", k)
	}
	if r := ConnRole(b.Part("J1"), "connector"); r != "POWER" {
		t.Errorf("J1 role %q", r)
	}
	if r := ConnRole(b.Part("JP1"), "jumper"); r != "JUMPER" {
		t.Errorf("JP1 role %q", r)
	}
	jtag := &Part{Designator: "J9", Pads: []Pad{{PadNumber: "1", Net: "TCK"}, {PadNumber: "2", Net: "TMS"}, {PadNumber: "3", Net: "TDI"}, {PadNumber: "4", Net: "GND"}}}
	if r := ConnRole(jtag, "header"); r != "JTAG" {
		t.Errorf("jtag role %q", r)
	}
	can := &Part{Designator: "J8", Pads: []Pad{{PadNumber: "1", Net: "CANH"}, {PadNumber: "2", Net: "CANL"}, {PadNumber: "3", Net: "GND"}}}
	if r := ConnRole(can, "connector"); r != "CAN" {
		t.Errorf("can role %q", r)
	}
	if h := b.MountHoles(); len(h) != 1 || h[0].From != "fill" {
		t.Errorf("mount holes %+v", h)
	}
}

func TestPinTable(t *testing.T) {
	m := Build(testInputs(t, `{"connectors":{"J2":{"name":"串口","pinNotes":{"1":"发送"},"expectedPins":4,
	  "docPins":{"1":{"name":"TXD","net":"TX"},"2":{"net":"+5V"},"3":{"net":"NC"}}}},
	  "io":[{"signal":"TXD","connector":"J2","pin":"1","direction":"输出","level":"3.3 V"}]}`))
	var j2 *Conn
	for _, c := range m.Connectors {
		if c.Ref == "J2" {
			j2 = c
		}
	}
	if j2 == nil || len(m.Connectors) != 3 {
		t.Fatalf("connectors %+v", m.Connectors)
	}
	if j2.PadCount != 3 || len(j2.Pins) != 3 {
		t.Fatalf("J2 pins %+v", j2.Pins)
	}
	p1, p2, p3 := j2.Pins[0], j2.Pins[1], j2.Pins[2]
	if p1.Role != "signal" || p1.Note != "发送" || p1.Direction != "输出" || p1.Voltage != "3.3 V" || p1.Mismatch {
		t.Errorf("pin1 %+v", p1)
	}
	if p2.Role != "power" || p2.Voltage != "3.3 V" || p2.MaxCurrent != "50 mA" || p2.Direction != label("zh", "dirOut") || !p2.Mismatch {
		t.Errorf("pin2 %+v", p2)
	}
	if p3.Role != "nc" || p3.Mismatch {
		t.Errorf("pin3 %+v", p3)
	}
	// mismatches: pin 2 net and the pin count
	var where []string
	for _, c := range m.Checks {
		where = append(where, c.Where)
	}
	if !strings.Contains(strings.Join(where, " "), "J2.2") || !strings.Contains(strings.Join(where, " "), "J2 ") && !contains(where, "J2") {
		t.Errorf("checks %v", where)
	}
}

func contains(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

func TestPowerAndCautions(t *testing.T) {
	m := Build(testInputs(t, ""))
	if m.Power.InputRef != "J1" || m.Power.Typ != "500 mA" || m.Power.Peak != "2.40 A" {
		t.Errorf("power %+v", m.Power)
	}
	if !strings.Contains(m.Power.Recommend, "3.60 A") {
		t.Errorf("recommend %q", m.Power.Recommend)
	}
	if len(m.Power.Rails) != 2 || m.Power.Rails[0].Net != "VIN" {
		t.Errorf("rails %+v", m.Power.Rails)
	}
	all := ""
	for _, c := range m.Cautions {
		if c.Source != "auto" {
			t.Errorf("unexpected source %+v", c)
		}
		all += c.Text + "\n"
	}
	for _, want := range []string{"J1 电源脚最大电流", "2.40 A", "HV", "J2", "超过 2 A"} {
		if !strings.Contains(all, want) {
			t.Errorf("cautions miss %q:\n%s", want, all)
		}
	}
	// rail probes: VIN on the J1 connector pin, +3V3 on a connector pin too
	if len(m.RailProbes) != 2 || m.RailProbes[0].Where != "J1.1" || !m.RailProbes[0].Found {
		t.Errorf("probes %+v", m.RailProbes)
	}
}

func TestNotesMerge(t *testing.T) {
	if _, err := ParseNotes([]byte(`{"titel":"x"}`)); err == nil {
		t.Fatal("unknown field accepted")
	}
	m := Build(testInputs(t, `{"title":"Demo","revision":"A","cautions":["先断电"],
	  "connectors":{"J1":{"name":"电源","role":"power","cautions":["中心正"]},"J7":{"name":"不存在"}},
	  "power":{"input":{"voltageV":12,"recommendedSupply":"12 V / 3 A"},"rails":{"+3V3":{"probe":"C1.1","tolerance":"±2 %"}}},
	  "leds":[{"ref":"D1","name":"PWR","color":"green","net":"LED0_A"},{"ref":"D2","name":"ERR","color":"red","net":"LED9_A"}],
	  "firmwares":["fw"],"measurements":{"signals":[{"signal":"clk","probe":"X1.2","net":"CLK","expected":"16 MHz"}]}}`))
	if m.Title != "Demo" || m.Revision != "A" {
		t.Errorf("title %q rev %q", m.Title, m.Revision)
	}
	if m.Cautions[0].Text != "先断电" || m.Cautions[0].Source != "notes" {
		t.Errorf("first caution %+v", m.Cautions[0])
	}
	if m.Connectors[0].Name != "电源" || m.Connectors[0].Role != "POWER" || len(m.ConnCaut) != 1 {
		t.Errorf("J1 %+v / %+v", m.Connectors[0], m.ConnCaut)
	}
	if m.Power.Voltage != "12.00 V" {
		t.Errorf("voltage %q", m.Power.Voltage)
	}
	var p3 *ProbeRow
	for i := range m.RailProbes {
		if m.RailProbes[i].Name == "+3V3" {
			p3 = &m.RailProbes[i]
		}
	}
	if p3 == nil || p3.Where != "C1.1" || p3.Tolerance != "±2 %" {
		t.Errorf("+3V3 probe %+v", p3)
	}
	if len(m.SigProbes) != 1 || !m.SigProbes[0].Found || m.SigProbes[0].N != 3 {
		t.Errorf("signal probes %+v", m.SigProbes)
	}
	if len(m.LEDs) != 2 || m.LEDs[0].Mismatch || !m.LEDs[1].Mismatch || m.LEDs[1].Net != "LED1_A" {
		t.Errorf("leds %+v", m.LEDs)
	}
	got := map[string]bool{}
	for _, c := range m.Checks {
		got[c.Where] = true
	}
	for _, w := range []string{"J7", "D2", "JP1"} {
		if !got[w] {
			t.Errorf("missing check %s in %+v", w, m.Checks)
		}
	}
}

func TestPinMap(t *testing.T) {
	pm, err := ParsePinMap([]byte("# x\nset_location_assignment PIN_1 -to tx\nset_location_assignment PIN_2 -to led[0]\nset_property -dict {PACKAGE_PIN Y9 IOSTANDARD LVCMOS33} [get_ports clk]\n"))
	if err != nil || len(pm) != 3 || pm[1].Signal != "led[0]" || pm[2].Pin != "Y9" {
		t.Fatalf("pin map %+v %v", pm, err)
	}
	if _, err := ParsePinMap([]byte("nothing")); err == nil {
		t.Fatal("empty pin map accepted")
	}
	in := testInputs(t, "")
	in.PinMap = pm[:2]
	m := Build(in)
	if m.PinMapRef != "U1" || len(m.PinMap) != 2 || !m.PinMap[0].Match || m.PinMap[1].Match {
		t.Errorf("pin map rows %+v ref %s", m.PinMap, m.PinMapRef)
	}
}

// svgStats parses an SVG and counts elements by name and attribute.
func svgStats(t *testing.T, s string) (map[string]int, *xml.StartElement) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(s))
	n := map[string]int{}
	var root *xml.StartElement
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("svg does not parse: %v\n%s", err, s[:min(len(s), 400)])
		}
		if se, ok := tok.(xml.StartElement); ok {
			if root == nil {
				c := se.Copy()
				root = &c
			}
			n[se.Name.Local]++
			for _, a := range se.Attr {
				if a.Name.Local == "data-pin" {
					n["pad"]++
				}
				if a.Name.Local == "class" && a.Value == "pin1ring" {
					n["pin1ring"]++
				}
			}
		}
	}
	return n, root
}

func TestSVGStructure(t *testing.T) {
	m := Build(testInputs(t, `{"leds":[{"ref":"D1","name":"PWR"},{"ref":"D2","name":"ERR"}]}`))
	n, root := svgStats(t, m.BoardSVG)
	var vb string
	for _, a := range root.Attr {
		if a.Name.Local == "viewBox" {
			vb = a.Value
		}
	}
	f := strings.Fields(vb)
	if len(f) != 4 || f[2] != f[3] {
		t.Errorf("board map not square: %q", vb)
	}
	for _, want := range []string{"J1 POWER", "J2", "JP1 JUMPER", "D1 PWR", "10 mm"} {
		if !strings.Contains(m.BoardSVG, want) {
			t.Errorf("board svg misses %q", want)
		}
	}
	if n["circle"] < 3 {
		t.Errorf("board svg circles %d", n["circle"])
	}
	for _, c := range m.Connectors {
		n, _ := svgStats(t, c.SVG)
		if n["pad"] != c.PadCount || n["pin1ring"] != 1 {
			t.Errorf("%s: %d pads drawn (want %d), pin1 rings %d", c.Ref, n["pad"], c.PadCount, n["pin1ring"])
		}
	}
	j2 := m.Connectors[1].SVG
	if !regexp.MustCompile(`data-pin="1"[^>]*fill="#1f5fbf"`).MatchString(j2) || !regexp.MustCompile(`data-pin="2"[^>]*fill="#d62728"`).MatchString(j2) ||
		!regexp.MustCompile(`data-pin="3"[^>]*fill="#a8a8a8"`).MatchString(j2) || !strings.Contains(j2, "<rect data-pin=\"1\"") || !strings.Contains(j2, "<ellipse data-pin=\"2\"") {
		t.Errorf("J2 pad colours / shapes wrong:\n%s", j2)
	}
	if m.LEDSVG == "" {
		t.Error("no LED picture")
	}
	if _, _ = svgStats(t, m.LEDSVG); m.ProbeSVG == "" {
		t.Error("no probe picture")
	}
}

func TestRenderSelfContained(t *testing.T) {
	for _, lang := range []string{"zh", "en"} {
		in := testInputs(t, `{"title":"Demo","bringup":[{"title":"上电","checks":[{"item":"电流","expect":"TODO"}]}],
		  "software":{"commands":[{"key":"R","name":"run"}],"telemetry":{"example":"A=1","fields":[{"field":"A"}]}},
		  "troubleshooting":[{"symptom":"无灯","cause":"无电"}],"equipment":[{"item":"万用表"}]}`)
		in.Lang = lang
		html, err := RenderHTML(Build(in))
		if err != nil {
			t.Fatal(err)
		}
		s := string(html)
		if regexp.MustCompile(`(?i)<(script|link|img)[^>]+(src|href)="https?:`).MatchString(s) || strings.Contains(s, "ZgotmplZ") {
			t.Errorf("%s: external reference or unsafe value", lang)
		}
		if strings.Count(s, "<svg") < 4 {
			t.Errorf("%s: %d svgs", lang, strings.Count(s, "<svg"))
		}
		want := "接口详细说明"
		if lang == "en" {
			want = "Connector details"
		}
		if !strings.Contains(s, want) || !strings.Contains(s, "@media print") || !strings.Contains(s, `class="todo"`) {
			t.Errorf("%s: missing heading / print css / todo marking", lang)
		}
	}
}

func TestLabelsComplete(t *testing.T) {
	for k := range labels["zh"] {
		if _, ok := labels["en"][k]; !ok {
			t.Errorf("en misses %q", k)
		}
	}
	for k := range labels["en"] {
		if _, ok := labels["zh"][k]; !ok {
			t.Errorf("zh misses %q", k)
		}
	}
}

// TestTemplateMatchesSkill keeps the embedded template identical to the
// Skill's canonical copy (.agents/skills/pcbpilot/templates/board-manual/).
func TestTemplateMatchesSkill(t *testing.T) {
	emb, err := Templates.ReadFile("templates/manual.html.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	sk, err := os.ReadFile(filepath.Join(skillTmpl, "manual.html.tmpl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(emb, sk) {
		t.Errorf("manual.html.tmpl differs from the Skill copy — edit %s and run: cp %s pkg/boardmanual/templates/", skillTmpl, filepath.Join(".agents/skills/pcbpilot/templates/board-manual", "manual.html.tmpl"))
	}
}

func TestNatLess(t *testing.T) {
	if !natLess("J2", "J10") || natLess("10", "2") || !natLess("2", "10") {
		t.Error("natural sort")
	}
}

// completeNotes passes the board-manual gate on testBoard.
const completeNotes = `{"title":"Demo",
 "power":{"input":{"connector":"J1","polarity":"中心正","recommendedSupply":"12 V / 3 A"},
   "rails":{"VIN":{"tolerance":"±5 %"},"+3V3":{"tolerance":"±2 %"}}},
 "mechanical":{"thicknessMm":1.6,"heights":[{"ref":"J1","heightMm":11,"source":"datasheet"}]},
 "connectors":{
  "J1":{"name":"电源","purpose":"电源入口","connectsTo":"12 V 适配器","systemRole":"唯一电源","usage":["插上"],"cautions":["中心正"],"pinNotes":{"1":"+","2":"GND"}},
  "J2":{"name":"串口","purpose":"串口","connectsTo":"PC","systemRole":"命令","usage":["115200"],"cautions":["RS232"],"pinNotes":{"1":"TX","2":"3V3","3":"NC"},
        "docPins":{"1":{"net":"TX"}}},
  "JP1":{"name":"配置","purpose":"跳线","connectsTo":"跳线帽","systemRole":"配置","usage":["插帽"],"cautions":["断电"],"pinNotes":{"1":"CFG","2":"GND"}}},
 "io":[{"signal":"TX","connector":"J2","pin":"1","level":"3.3 V"}],
 "firmwares":["fw"],
 "leds":[{"ref":"D1","name":"PWR","net":"LED0_A","hardware":"上电亮"},{"ref":"D2","name":"RUN","net":"LED1_A","modes":{"fw":"运行闪烁"}}],
 "openItems":["TODO 允许出现在未决项里"]}`

func gateItems(t *testing.T, notes string) []string {
	t.Helper()
	in := testInputs(t, notes)
	in.Intent = nil // no hazardous domain needed here
	return GateCheck(Build(in), in.Notes)
}

func TestGateFailureModes(t *testing.T) {
	if items := gateItems(t, completeNotes); len(items) != 0 {
		t.Fatalf("complete notes fail the gate: %v", items)
	}
	b, _ := ParseBoard([]byte(testBoard))
	if items := GateCheck(Build(Inputs{Board: b}), nil); len(items) != 1 || !strings.Contains(items[0], "notes file missing") {
		t.Errorf("missing notes: %v", items)
	}
	cases := []struct{ name, from, to, want string }{
		{"purpose", `"purpose":"串口",`, ``, "connector J2: missing 是什么"},
		{"cautions", `"cautions":["断电"],`, ``, "connector JP1: missing 注意"},
		{"pin note", `"pinNotes":{"1":"TX","2":"3V3","3":"NC"}`, `"pinNotes":{"1":"TX","2":"3V3"}`, "pin(s) 3 have no"},
		{"led", `"modes":{"fw":"运行闪烁"}`, `"modes":{}`, "LED D2: no meaning"},
		{"power", `"polarity":"中心正",`, ``, "power input: missing"},
		{"todo", `"connectsTo":"PC"`, `"connectsTo":"TODO"`, "TODO left in Connectors"},
		{"doc net", `"docPins":{"1":{"net":"TX"}}`, `"docPins":{"1":{"net":"TXD"}}`, "notes vs board J2.1"},
		{"connector not on board", `"JP1":{`, `"J9":{"name":"x"},"JP1":{`, "notes vs board J9"},
		{"heights", `"heights":[{"ref":"J1","heightMm":11,"source":"datasheet"}]`, `"heights":[]`, "no part heights"},
		{"thickness", `"thicknessMm":1.6,`, ``, "board thickness unknown"},
	}
	for _, tc := range cases {
		if !strings.Contains(completeNotes, tc.from) {
			t.Fatalf("%s: fixture lacks %q", tc.name, tc.from)
		}
		items := gateItems(t, strings.Replace(completeNotes, tc.from, tc.to, 1))
		if !strings.Contains(strings.Join(items, "\n"), tc.want) {
			t.Errorf("%s: want %q in %v", tc.name, tc.want, items)
		}
	}
}

func TestMechanical(t *testing.T) {
	in := testInputs(t, completeNotes)
	m := Build(in)
	ms := m.Mech
	if ms.WidthMM != 40 || ms.HeightMM != 30 || len(ms.Holes) != 1 || ms.Holes[0].Drill != 3.2 || ms.Holes[0].Screw != "M3" {
		t.Errorf("mech %+v holes %+v", ms, ms.Holes)
	}
	if ms.Thickness != "1.6 mm" || len(ms.ConnPos) != 3 || ms.Scale != "1:1" {
		t.Errorf("mech %+v", ms)
	}
	if _, root := svgStats(t, ms.SVG); root == nil || !strings.Contains(ms.SVG, `width="`) || !strings.Contains(ms.SVG, "40 mm") {
		t.Error("mechanical drawing")
	}
	if !strings.HasPrefix(ms.SVGDataURI, "data:image/svg+xml;base64,") || !strings.HasPrefix(ms.CSVDataURI, "data:text/csv") {
		t.Error("downloads")
	}
}

func TestPublishVersions(t *testing.T) {
	dir := t.TempDir()
	render := func(m *Manual) ([]byte, error) { return RenderHTML(m) }
	build := func(sha string, mut func(*Inputs)) *Manual {
		in := testInputs(t, completeNotes)
		in.BoardSHA = sha
		if mut != nil {
			mut(&in)
		}
		return Build(in)
	}
	p1, err := Publish(dir, "Demo", build("aaa", nil), true, render)
	if err != nil || p1.Version != 1 || !p1.Bumped {
		t.Fatalf("v1 %+v %v", p1, err)
	}
	// same board sha256: no bump
	p1b, err := Publish(dir, "Demo", build("aaa", nil), true, render)
	if err != nil || p1b.Version != 1 || p1b.Bumped {
		t.Fatalf("rebuild %+v %v", p1b, err)
	}
	// new board: J2.3 gets a net, JP1 removed
	m2 := build("bbb", func(in *Inputs) {
		b := *in.Board
		b.Components = nil
		for _, p := range in.Board.Components {
			if p.Designator == "JP1" {
				continue
			}
			if p.Designator == "J2" {
				p.Pads = append([]Pad(nil), p.Pads...)
				p.Pads[2].Net = "RX"
			}
			b.Components = append(b.Components, p)
		}
		in.Board = &b
	})
	p2, err := Publish(dir, "Demo", m2, false, render)
	if err != nil || p2.Version != 2 || !p2.Bumped {
		t.Fatalf("v2 %+v %v", p2, err)
	}
	ch := strings.Join(m2.Changes, "\n")
	if m2.PrevVersion != 1 || !strings.Contains(ch, "J2.3 网络变更：NC → RX") || !strings.Contains(ch, "删除接口 JP1") {
		t.Errorf("changes %q", ch)
	}
	ix, err := LoadIndex(dir)
	if err != nil || len(ix.Versions) != 2 || ix.Versions[1].Pass || !ix.Versions[0].Pass {
		t.Fatalf("index %+v %v", ix, err)
	}
	for _, f := range []string{"v1/Demo_使用说明.html", "v2/Demo_使用说明.html", "Demo_使用说明.html"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Error(err)
		}
	}
	html, _ := os.ReadFile(filepath.Join(dir, "Demo_使用说明.html"))
	if !strings.Contains(string(html), "<b>v2</b>") || !strings.Contains(string(html), "bbb") || !strings.Contains(string(html), "变更记录") {
		t.Error("current copy lacks the v2 header / change log")
	}
	// rebuilding v2 (same sha) keeps the diff against v1
	m2b := build("bbb", func(in *Inputs) { in.Board = m2.Board })
	if p, err := Publish(dir, "Demo", m2b, true, render); err != nil || p.Version != 2 || m2b.PrevVersion != 1 || len(m2b.Changes) == 0 {
		t.Errorf("rebuild v2 %+v %v %v", p, err, m2b.Changes)
	}
}

func TestDiffConnectorAdded(t *testing.T) {
	prev := Snapshot{Conns: map[string]map[string]string{"J1": {"1": "A"}}}
	cur := Snapshot{Conns: map[string]map[string]string{"J1": {"1": "A"}, "J2": {"1": "B", "2": "C"}}, LEDs: map[string]string{"D1": "PWR | X"}}
	d := strings.Join(Diff(prev, cur, "zh"), "\n")
	if !strings.Contains(d, "新增接口 J2（2 脚）") || !strings.Contains(d, "LED D1") {
		t.Errorf("diff %q", d)
	}
}
