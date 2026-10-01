package schaes

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

func wireSnap(wires ...Wire) *Snapshot {
	return &Snapshot{Source: "test", HasWires: true, HasMarkers: true, Wires: wires}
}

// EasyEDA contact semantics: strict X does not connect, T does.
func TestTopologyContactSemantics(t *testing.T) {
	x := buildTopo(wireSnap(Wire{Net: "A", Pts: []Pt{{0, 50}, {100, 50}}}, Wire{Net: "B", Pts: []Pt{{50, 0}, {50, 100}}}))
	if len(x.islands) != 2 || len(x.crossings()) != 1 {
		t.Fatalf("strict X must stay two islands with one crossing: %d islands %d crossings", len(x.islands), len(x.crossings()))
	}
	tee := buildTopo(wireSnap(Wire{Net: "A", Pts: []Pt{{0, 50}, {100, 50}}}, Wire{Net: "A", Pts: []Pt{{50, 50}, {50, 100}}}))
	if len(tee.islands) != 1 || len(tee.crossings()) != 0 {
		t.Fatalf("T must merge: %d islands", len(tee.islands))
	}
	if n := tee.nodes[qkey(Pt{50, 50})]; n == nil || len(n.arms) != 3 {
		t.Fatalf("T node must have 3 arms")
	}
	plus := wireSnap(Wire{Net: "A", Pts: []Pt{{0, 50}, {50, 50}}}, Wire{Net: "A", Pts: []Pt{{50, 50}, {100, 50}}},
		Wire{Net: "A", Pts: []Pt{{50, 0}, {50, 50}}}, Wire{Net: "A", Pts: []Pt{{50, 50}, {50, 100}}})
	r := Analyze(plus, nil)
	if r.Metric("W3").Extra["fourWay"] != 1 {
		t.Fatalf("plus junction must count as 4-way: %+v", r.Metric("W3"))
	}
	// same-net X without contact is ambiguous, not a different-net crossing
	amb := Analyze(wireSnap(Wire{Net: "A", Pts: []Pt{{0, 50}, {100, 50}}}, Wire{Net: "A", Pts: []Pt{{50, 0}, {50, 100}}}), nil)
	if amb.Metric("W3").Extra["ambiguousX"] != 1 || amb.Metric("W2").Extra["crossings"] != 0 {
		t.Fatalf("same-net X: W3 %+v W2 %+v", amb.Metric("W3").Extra, amb.Metric("W2").Extra)
	}
}

func TestRMST(t *testing.T) {
	if got := rmstManhattan([]Pt{{0, 0}, {10, 0}, {10, 10}}); got != 20 {
		t.Fatalf("rmst %v", got)
	}
}

func TestParseFormats(t *testing.T) {
	// components.list wrapped in a daemon envelope, with a reversed stub
	raw, err := os.ReadFile("../../internal/schguard/testdata/d1-pin3-reversed.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Result json.RawMessage `json:"result"`
	}
	_ = json.Unmarshal(raw, &fx)
	env, _ := json.Marshal(map[string]any{"ok": true, "result": json.RawMessage(fx.Result)})
	s, err := Parse(env)
	if err != nil {
		t.Fatal(err)
	}
	if s.Source != "components-list" || len(s.Parts) != 1 || len(s.Markers) != 1 || len(s.Wires) != 1 || !s.HasWires {
		t.Fatalf("components-list parse: %+v", s)
	}
	if s.Parts[0].Pins[0].Dir != "left" || s.Markers[0].Dir != "right" {
		t.Fatalf("pin rotation 180 → left, port rotation 0 → right: %+v %+v", s.Parts[0].Pins[0], s.Markers[0])
	}
	r := Analyze(s, nil)
	if r.Metric("W7").Extra["segments"] != 1 {
		t.Fatalf("the reversed stub runs through D1's body: %+v", r.Metric("W7"))
	}
	// lib-layout: flags become implicit stubs + markers
	ams := loadSnap(t, "testdata/ams1117-lib-layout.json")
	if ams.Source != "lib-layout" || len(ams.Parts) != 4 || len(ams.Markers) != 6 {
		t.Fatalf("lib-layout parse: %d parts %d markers", len(ams.Parts), len(ams.Markers))
	}
	implicit := 0
	for _, w := range ams.Wires {
		if w.Implicit {
			implicit++
		}
	}
	if implicit != 6 {
		t.Fatalf("each flag with an offset carries an implicit stub, got %d", implicit)
	}
	// canonical: wiring is skipped (not measured), never scored perfect
	can := Analyze(loadSnap(t, esp32MCU), nil)
	if !can.Metric("W1").Skipped || can.Verdict != "incomplete" || can.WiredShare != 1 {
		t.Fatalf("canonical must skip wiring: %+v %s", can.Metric("W1"), can.Verdict)
	}
	if _, err := Parse([]byte(`{"zones":[]}`)); err == nil || !strings.Contains(err.Error(), "--zone") {
		t.Fatalf("zones packet must ask for --zone: %v", err)
	}
	if _, err := Parse([]byte(`{"foo":1}`)); err == nil {
		t.Fatal("unknown format accepted")
	}
	zs, err := ParseZones([]byte(`{"zones":[{"id":"Z1","title":"T","layout":{"placements":[],"wires":[],"flags":[]},"frame":{"title":"T","rect":{"minX":0,"minY":0,"maxX":100,"maxY":100}}}]}`))
	if err != nil || len(zs) != 1 || len(zs[0].Snapshot.Frames) != 1 {
		t.Fatalf("zones: %v %+v", err, zs)
	}
}

// Anti-gaming: deleting wires never looks tidier.
func TestUnwiringNeverHelps(t *testing.T) {
	base := drafted(loadSnap(t, esp32MCU))
	full := Analyze(base, nil)
	bare := cloneSnap(base)
	bare.Wires, bare.Markers = nil, nil
	none := Analyze(bare, nil)
	if !(none.Score < full.Score) || none.Groups[GroupWiring] != 0 {
		t.Fatalf("no wires must score below wired: %.1f vs %.1f (wiring %.1f)", none.Score, full.Score, none.Groups[GroupWiring])
	}
	half := cloneSnap(base)
	half.Wires = half.Wires[:len(half.Wires)/2]
	if h := Analyze(half, nil); !(h.WiredShare < full.WiredShare) {
		t.Fatalf("dropping wires must lower the wired share: %.3f vs %.3f", h.WiredShare, full.WiredShare)
	}
}

func TestBusCandidates(t *testing.T) {
	nets := []string{"D0", "D1", "D2", "D3", "D4", "D5", "D6", "D7", "IO0", "IO21", "IO45",
		"U0TXD", "U0RXD", "SPI_SCK", "SPI_MOSI", "SPI_MISO", "SPI_CS", "I2C_SDA", "I2C_SCL",
		"USB_DP", "USB_DM", "MIPI_CLK_P", "MIPI_CLK_N", "MIPI_D0_P", "MIPI_D0_N", "SD_CMD", "SD_CLK", "SD_D0", "SD_D1",
		"GND", "+3V3", "VCC1", "VCC2", "VCC3", "LED_A", "RTS", "DTR"}
	got := map[string][]string{}
	for _, c := range DetectBusCandidates(nets) {
		got[c.Kind+":"+c.Suggested] = c.Members
	}
	want := map[string][]string{
		"indexed:D[0:7]":  {"D0", "D1", "D2", "D3", "D4", "D5", "D6", "D7"},
		"uart:U0_UART":    {"U0RXD", "U0TXD"},
		"spi:SPI_SPI":     {"SPI_CS", "SPI_MISO", "SPI_MOSI", "SPI_SCK"},
		"i2c:I2C_I2C":     {"I2C_SCL", "I2C_SDA"},
		"usb:USB":         {"USB_DM", "USB_DP"},
		"mipi:MIPI_LANES": {"MIPI_CLK_N", "MIPI_CLK_P", "MIPI_D0_N", "MIPI_D0_P"},
		"sdio:SD_SDIO":    {"SD_CLK", "SD_CMD", "SD_D0", "SD_D1"},
	}
	if !reflect.DeepEqual(got, want) {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t.Fatalf("candidates:\n got %v\nwant %v", got, want)
	}
}

// A lane of indexed labels in one column at equal pitch scores 100; a
// scattered one scores lower; a native bus with the group prefix scores 100.
func TestBusLaneScoring(t *testing.T) {
	mk := func(pos func(i int) Pt, dir func(i int) string) *Snapshot {
		s := &Snapshot{Source: "test", HasWires: true, HasMarkers: true}
		for i := 0; i < 4; i++ {
			n := "D" + string(rune('0'+i))
			p := pos(i)
			s.Markers = append(s.Markers, Marker{Kind: KindNetPort, Net: n, Anchor: p, Dir: dir(i), Box: predictMarkerBox(KindNetPort, n, p, dir(i))})
		}
		return s
	}
	lane := Analyze(mk(func(i int) Pt { return Pt{500, 100 + 20*float64(i)} }, func(int) string { return "right" }), nil)
	messy := Analyze(mk(func(i int) Pt { return Pt{500 + 35*float64(i%2), 100 + float64(i*i)*15} }, func(i int) string {
		if i%2 == 0 {
			return "left"
		}
		return "right"
	}), nil)
	if lane.Metric("N3").Score != 100 || !(messy.Metric("N3").Score < 50) {
		t.Fatalf("lane %.1f messy %.1f", lane.Metric("N3").Score, messy.Metric("N3").Score)
	}
	bus := mk(func(i int) Pt { return Pt{500 + 35*float64(i%2), 100 + float64(i*i)*15} }, func(int) string { return "up" })
	bus.HasBuses, bus.Buses = true, []Bus{{Name: "D[0:3]", Pts: [][]Pt{{{400, 100}, {400, 300}}}}}
	if r := Analyze(bus, nil); r.Metric("N3").Score != 100 || r.Lanes[0].NativeBus != "D[0:3]" {
		t.Fatalf("native bus: %+v", r.Lanes)
	}
}

func TestDecodeBusLine(t *testing.T) {
	flat := DecodeBusLine(json.RawMessage(`[0,0,0,100,50,100]`))
	multi := DecodeBusLine(json.RawMessage(`[[0,0,0,100],[0,100,50,100]]`))
	if len(flat) != 1 || len(flat[0]) != 3 || len(multi) != 2 {
		t.Fatalf("flat %v multi %v", flat, multi)
	}
}

// The style vocabulary is shared with the PCB side.
func TestProfilesShareThePCBVocabulary(t *testing.T) {
	var sch, pcb []string
	for k := range Profiles {
		sch = append(sch, k)
	}
	for k := range pcbauto.AesProfiles {
		pcb = append(pcb, k)
	}
	sort.Strings(sch)
	sort.Strings(pcb)
	if !reflect.DeepEqual(sch, pcb) {
		t.Fatalf("schematic profiles %v must match pcb profiles %v", sch, pcb)
	}
	for _, n := range sch {
		p := Profiles[n].clone()
		if err := p.Validate(); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
		if p.Weight != pcbauto.AesProfiles[n].Weight {
			t.Fatalf("%s planned weight %.2f must equal the PCB preset %.2f", n, p.Weight, pcbauto.AesProfiles[n].Weight)
		}
	}
	// precision is stricter than functional on every soft tolerance
	f, pr := Profiles["functional"], Profiles["precision"]
	if !(pr.AlignTol < f.AlignTol && pr.LongWire < f.LongWire && pr.LongCrossings < f.LongCrossings && pr.GridBlend > f.GridBlend) {
		t.Fatal("precision must be stricter than functional")
	}
}

func TestStyleFile(t *testing.T) {
	p, err := ParseStyle([]byte(`{"base":"precision","longWireUnits":300,"metricWeights":{"N3":2}}`))
	if err != nil || p.Name != "custom" || p.LongWire != 300 || p.metricWeight("N3") != 2 || p.AlignTol != Profiles["precision"].AlignTol {
		t.Fatalf("custom: %+v %v", p, err)
	}
	p, err = ParseStyle([]byte(`{"profile":"functional","schematic":{"gridBlend":0.5}}`))
	if err != nil || p.GridBlend != 0.5 || p.LongWire != Profiles["functional"].LongWire {
		t.Fatalf("wrapped: %+v %v", p, err)
	}
	for _, bad := range []string{`{"connectivity":false}`, `{"nc":"ignore"}`, `{"minScore":0}`, `{"weight":0.9}`, `{"foo":1}`, `{"metricWeights":{"Z9":1}}`, `{"targetGridUnits":7}`} {
		if _, err := ParseStyle([]byte(bad)); err == nil {
			t.Fatalf("style %s accepted", bad)
		}
	}
}

func TestAutoProfile(t *testing.T) {
	small := &Snapshot{Parts: []Part{{Ref: "R1", Box: Box{0, 0, 10, 10}, HasBox: true}, {Ref: "R2", Box: Box{200, 200, 210, 210}, HasBox: true}}}
	if p := AutoProfile(small); p.Name != "precision" || p.Auto == nil {
		t.Fatalf("small sparse page → precision, got %+v", p)
	}
	dense := &Snapshot{}
	for i := 0; i < 80; i++ {
		x := float64(i%10) * 30
		y := float64(i/10) * 30
		pins := make([]Pin, 8)
		dense.Parts = append(dense.Parts, Part{Ref: "U", Box: Box{x, y, x + 25, y + 25}, HasBox: true, Pins: pins})
	}
	if p := AutoProfile(dense); p.Name != "functional" {
		t.Fatalf("dense page → functional, got %s (%s)", p.Name, p.Auto.Reason)
	}
	r := Analyze(small, &Profile{Name: "auto"})
	if r.Profile.Auto == nil || r.Weight != 0 {
		t.Fatal("auto must record its reason and the applied weight stays 0")
	}
}

// orientation.json is the single source of truth for flag directions.
func TestFlagTableMatchesOrientationJSON(t *testing.T) {
	raw, err := os.ReadFile("../../.agents/skills/pcbpilot/references/orientation.json")
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		FrozenTable map[string]map[string]float64 `json:"frozenTable"`
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(o.FrozenTable, flagBodyRotation) {
		t.Fatalf("flag table drifted from orientation.json:\n%v\n%v", o.FrozenTable, flagBodyRotation)
	}
}

func TestEveryMetricHasThresholdAndSource(t *testing.T) {
	for _, s := range []*Snapshot{drafted(loadSnap(t, esp32MCU)), loadSnap(t, "testdata/ams1117-lib-layout.json")} {
		r := Analyze(s, nil)
		if len(r.Metrics) != len(metricOrder) {
			t.Fatalf("metric count %d", len(r.Metrics))
		}
		for _, m := range r.Metrics {
			if m.Skipped {
				if m.Reason == "" {
					t.Fatalf("%s skipped without a reason", m.ID)
				}
				continue
			}
			if m.Thresholds == "" || m.Source == "" || m.Score < 0 || m.Score > 100 || math.IsNaN(m.Score) {
				t.Fatalf("%s: thresholds %q source %q score %v", m.ID, m.Thresholds, m.Source, m.Score)
			}
		}
		if r.Weight != 0 || len(r.Priority) != 3 || !strings.HasPrefix(r.Priority[0], "1 连接正确性") {
			t.Fatal("report-only contract: weight 0 and connectivity first")
		}
	}
}
