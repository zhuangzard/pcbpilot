package analogsim

import (
	"bytes"
	"math"
	"math/cmplx"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// ---------------------------------------------------------------------------
// Synthetic-design helpers.

type pn struct{ num, name, net string }

func part(ref, mpn, value, desc string, pins ...pn) *powersim.Part {
	p := &powersim.Part{Ref: ref, MPN: mpn, Value: value, Description: desc}
	for _, x := range pins {
		p.Pins = append(p.Pins, powersim.Pin{Number: x.num, Name: x.name, Net: x.net})
	}
	return p
}

func res(ref, val, a, b string) *powersim.Part {
	return part(ref, "", val, "Tolerance:±1%", pn{"1", "1", a}, pn{"2", "2", b})
}

func capa(ref, val, a, b string) *powersim.Part {
	return part(ref, "", val, "Tolerance:±5% C0G", pn{"1", "1", a}, pn{"2", "2", b})
}

// opamp is a single op-amp with named pins.
func opamp(ref, mpn, inp, inn, out, vp, vn string) *powersim.Part {
	return part(ref, mpn, mpn, "", pn{"1", "OUT", out}, pn{"2", "V-", vn}, pn{"3", "IN+", inp}, pn{"4", "IN-", inn}, pn{"5", "V+", vp})
}

func design(parts ...*powersim.Part) *powersim.Design {
	d := &powersim.Design{NetRole: map[string]string{"GND": "ground", "+5V": "power", "+3V3": "power", "+12V": "power", "-12V": "power"}}
	d.Parts = parts
	return d
}

func testLib(t *testing.T) *Library { return skillLib(t) }

func runNoSpice(t *testing.T, d *powersim.Design, spec *Spec) *Output {
	t.Helper()
	out, err := Run(d, testLib(t), Options{Ngspice: "none", Spec: spec, Optimise: true, MCRuns: 20})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func blockOf(t *testing.T, out *Output, class string) *Block {
	t.Helper()
	for _, b := range out.Blocks {
		if b.Class == class {
			return b
		}
	}
	var got []string
	for _, b := range out.Blocks {
		got = append(got, b.Core+"="+b.Class)
	}
	t.Fatalf("no %s block; got %v", class, got)
	return nil
}

func near(t *testing.T, what string, got, want, relTol float64) {
	t.Helper()
	if strings.HasPrefix(t.Name(), "TestNgspice") {
		t.Logf("%s: ngspice %.6g vs analytic %.6g (Δ %.3g %%)", what, got, want, (got-want)/want*100)
	}
	if math.Abs(got-want) > relTol*math.Abs(want) {
		t.Errorf("%s = %.6g, want %.6g ±%.2g%%", what, got, want, relTol*100)
	}
}

func needNgspice(t *testing.T) {
	t.Helper()
	if p, _ := FindNgspice(""); p == "" {
		t.Skip("ngspice not installed")
	}
}

// ---------------------------------------------------------------------------
// Offline unit tests.

func TestESeries(t *testing.T) {
	for _, c := range []struct {
		v      float64
		series string
		want   float64
	}{{11.2e3, "E96", 11.3e3}, {4.6e3, "E24", 4.7e3}, {9.9e-9, "E12", 10e-9}, {1.49e-12, "E6", 1.5e-12}, {5.08e3, "E96", 5.11e3}} {
		if got := Snap(c.v, c.series); math.Abs(got-c.want) > 1e-9*c.want {
			t.Errorf("Snap(%g,%s) = %g, want %g", c.v, c.series, got, c.want)
		}
	}
	if n := len(SeriesValues("E96", 1000, 9999)); n != 96 {
		t.Errorf("E96 per decade = %d", n)
	}
	if n := len(SeriesValues("E24", 1e-9, 9.99e-9)); n != 24 {
		t.Errorf("E24 per decade = %d", n)
	}
	if !InSeries(4.7e3, "E12") || InSeries(4.99e3, "E24") || SeriesOf(4.99e3) != "E96" || SeriesOf(10e3) != "E6" {
		t.Error("series membership")
	}
	for v, want := range map[float64]string{11300: "11.3kΩ", 4.7e-9: "4.7nF", 1e-6: "1µF", 100: "100Ω", 2.2e-6: "2.2µF"} {
		kind := "R"
		if strings.HasSuffix(want, "F") {
			kind = "C"
		}
		if got := FormatValue(v, kind); got != want {
			t.Errorf("FormatValue(%g) = %s, want %s", v, got, want)
		}
	}
}

func TestOpampPinRole(t *testing.T) {
	for name, want := range map[string]string{
		"IN+": "inp:", "-IN": "inn:", "INA-": "inn:A", "1IN+": "inp:A", "OUTB": "out:B", "2OUT": "out:B", "VOUT": "out:",
		"+INB": "inp:B", "V+": "vp:", "VSS": "vn:", "DOUT": ":", "INP": "inp:", "IN1-": "inn:A", "GPIO4": ":", "RXD": ":",
	} {
		r, ch := opampPinRole(name)
		if got := r + ":" + ch; got != want {
			t.Errorf("opampPinRole(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestToleranceCodes(t *testing.T) {
	for _, c := range []struct {
		mpn, desc, kind string
		want            float64
	}{
		{"0402WGF1002TCE", "", "R", 1}, {"0402WGJ0472TCE", "", "R", 5}, {"RC0402FR-0710KL", "", "R", 1},
		{"CL05B104KO5NNNC", "", "C", 10}, {"GRM155R71C104KA88D", "", "C", 10}, {"", "Tolerance:±2%", "R", 2}, {"", "C0G", "C", 5},
	} {
		got, _ := tolerance(&powersim.Part{MPN: c.mpn, Description: c.desc}, c.kind, nil)
		if got != c.want {
			t.Errorf("tolerance(%s %q) = %g, want %g", c.mpn, c.desc, got, c.want)
		}
	}
}

func TestGenericLibMatchesSkill(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(skillRefs, "spice-models", "pcbpilot-generic.lib"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, []byte(GenericLib)) {
		t.Error("pkg/analogsim/models/pcbpilot-generic.lib differs from the Skill copy — edit the Skill copy and cp it to pkg/analogsim/models/")
	}
}

func TestLibraryEntriesHaveSources(t *testing.T) {
	lib := testLib(t)
	if len(lib.Opamps) < 8 {
		t.Fatalf("only %d op-amps", len(lib.Opamps))
	}
	for _, o := range lib.Opamps {
		if o.GBWHz <= 0 || o.SlewVPerUs <= 0 || o.Source == "" || o.Confidence == "" {
			t.Errorf("%s: incomplete (%+v)", o.ID, o)
		}
	}
	for _, id := range []string{"LM358", "TL072", "OPA2333", "MCP6002", "LMV321", "TLV9061"} {
		if lib.opamp(&powersim.Part{MPN: id}) == nil {
			t.Errorf("%s not matched", id)
		}
	}
	if lib.opamp(&powersim.Part{MPN: "XYZ123"}) != nil {
		t.Error("unknown part matched an op-amp")
	}
}

// classes: one synthetic circuit per detector, analytic values checked.
func TestDetectClasses(t *testing.T) {
	cases := []struct {
		name  string
		parts []*powersim.Part
		class string
		check func(t *testing.T, b *Block)
	}{
		{"follower", []*powersim.Part{opamp("U1", "MCP6001", "IN", "OUT", "OUT", "+5V", "GND"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassFollower, func(t *testing.T, b *Block) { near(t, "gain", b.Analytic["gain"], 1, 1e-6) }},
		{"non-inverting", []*powersim.Part{opamp("U1", "TLV9061", "IN", "FB", "OUT", "+5V", "GND"), res("R1", "90k", "OUT", "FB"), res("R2", "10k", "FB", "GND"),
			part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassNonInverting, func(t *testing.T, b *Block) {
				near(t, "gain", b.Analytic["gain"], 10, 1e-6)
				near(t, "bw", b.Analytic["fcHz"], 1e6, 1e-6) // 10 MHz / 10
			}},
		{"inverting", []*powersim.Part{opamp("U1", "MCP6001", "VMID", "SUM", "OUT", "+5V", "GND"), res("R1", "10k", "IN", "SUM"), res("R2", "47k", "SUM", "OUT"),
			res("R3", "10k", "+5V", "VMID"), res("R4", "10k", "VMID", "GND"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassInverting, func(t *testing.T, b *Block) { near(t, "gain", b.Analytic["gain"], 4.7, 1e-6) }},
		{"difference", []*powersim.Part{opamp("U1", "MCP6001", "P", "N", "OUT", "+5V", "GND"), res("R1", "10k", "INA", "N"), res("R2", "100k", "N", "OUT"),
			res("R3", "10k", "INB", "P"), res("R4", "100k", "P", "GND"), part("J1", "", "", "", pn{"1", "1", "INA"}, pn{"2", "2", "INB"})},
			ClassDifference, func(t *testing.T, b *Block) { near(t, "gain", b.Analytic["gain"], 10, 1e-6) }},
		{"integrator", []*powersim.Part{opamp("U1", "MCP6001", "GND", "SUM", "OUT", "+5V", "-12V"), res("R1", "10k", "IN", "SUM"), capa("C1", "15.9nF", "SUM", "OUT"),
			part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassIntegrator, func(t *testing.T, b *Block) { near(t, "unity", b.Analytic["unityHz"], 1000.97, 1e-3) }},
		{"sallen-key-lp", []*powersim.Part{opamp("U1", "TLV9061", "P", "OUT", "OUT", "+5V", "GND"), res("R1", "10k", "IN", "X"), res("R2", "10k", "X", "P"),
			capa("C1", "22nF", "X", "OUT"), capa("C2", "10nF", "P", "GND"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassSKLowPass, func(t *testing.T, b *Block) {
				near(t, "f0", b.Analytic["f0Hz"], 1/(2*math.Pi*10e3*math.Sqrt(22e-9*10e-9)), 1e-4)
				near(t, "q", b.Analytic["q"], math.Sqrt(22e-9/10e-9)/2, 1e-4)
			}},
		{"sallen-key-hp", []*powersim.Part{opamp("U1", "TLV9061", "P", "OUT", "OUT", "+5V", "GND"), capa("C1", "10nF", "IN", "X"), capa("C2", "10nF", "X", "P"),
			res("R1", "11.3k", "X", "OUT"), res("R2", "22.6k", "P", "GND"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassSKHighPass, func(t *testing.T, b *Block) {
				near(t, "q", b.Analytic["q"], math.Sqrt(22.6e3/11.3e3)/2, 1e-4)
			}},
		{"mfb-lp", []*powersim.Part{opamp("U1", "TLV9061", "GND", "N", "OUT", "+5V", "-12V"), res("R1", "10k", "IN", "X"), res("R2", "10k", "X", "OUT"),
			res("R3", "10k", "X", "N"), capa("C1", "33nF", "X", "GND"), capa("C2", "3.3nF", "N", "OUT"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassMFBLowPass, func(t *testing.T, b *Block) {
				w0 := 1 / math.Sqrt(10e3*10e3*33e-9*3.3e-9)
				near(t, "f0", b.Analytic["f0Hz"], w0/(2*math.Pi), 1e-4)
				near(t, "q", b.Analytic["q"], w0*33e-9/(3/10e3), 1e-4)
				near(t, "gain", b.Analytic["gain"], 1, 1e-6)
			}},
		{"mfb-hp", []*powersim.Part{opamp("U1", "TLV9061", "GND", "N", "OUT", "+5V", "-12V"), capa("C1", "10nF", "IN", "X"), capa("C2", "10nF", "X", "OUT"),
			capa("C3", "10nF", "X", "N"), res("R1", "5.1k", "X", "GND"), res("R2", "47k", "N", "OUT"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"})},
			ClassMFBHighPass, func(t *testing.T, b *Block) { near(t, "gain", b.Analytic["gain"], 1, 1e-6) }},
		{"comparator", []*powersim.Part{part("U1", "LM393", "LM393", "", pn{"1", "OUTA", "OUT"}, pn{"2", "INA-", "REF"}, pn{"3", "INA+", "P"}, pn{"4", "GND", "GND"},
			pn{"5", "INB+", "GND"}, pn{"6", "INB-", "+5V"}, pn{"7", "OUTB", "NC1"}, pn{"8", "VCC", "+5V"}),
			res("R1", "10k", "SIG", "P"), res("R2", "100k", "P", "OUT"), res("R3", "10k", "OUT", "+5V"), res("R4", "10k", "+5V", "REF"), res("R5", "10k", "REF", "GND"),
			part("J1", "", "", "", pn{"1", "1", "SIG"}, pn{"2", "2", "GND"})},
			ClassComparator, func(t *testing.T, b *Block) {
				// Output low (0 V): Vp = Vsig·100/110 = 2.5 → 2.75 V; output high through 10k+100k pull-up path → 2.2727 V.
				near(t, "rise", b.Analytic["thresholdLowOutV"], 2.75, 2e-3)
				near(t, "fall", b.Analytic["thresholdHighOutV"], 2.2727, 2e-3)
			}},
		{"reference", []*powersim.Part{part("U1", "LM4040DIM3-4.1/NOPB", "LM4040DIM3-4.1", "", pn{"1", "K", "VREF"}, pn{"2", "A", "GND"}), res("R1", "1k", "+5V", "VREF")},
			ClassReference, func(t *testing.T, b *Block) { near(t, "ik", b.Analytic["ikA"], (5-4.096)/1e3, 1e-6) }},
		{"crystal", []*powersim.Part{part("Y1", "", "8MHz", "Frequency:8MHz Load Capacitance:12pF", pn{"1", "1", "XI"}, pn{"2", "2", "XO"}),
			capa("C1", "22pF", "XI", "GND"), capa("C2", "22pF", "XO", "GND"), part("U1", "MCU", "MCU", "", pn{"1", "OSC_IN", "XI"}, pn{"2", "OSC_OUT", "XO"})},
			ClassCrystal, func(t *testing.T, b *Block) { near(t, "cl", b.Analytic["clPF"], 11+3, 1e-6) }},
		{"reset", []*powersim.Part{part("U1", "ESP32-S3-WROOM-1", "", "", pn{"2", "3V3", "+3V3"}, pn{"3", "EN", "EN"}, pn{"1", "GND", "GND"}, pn{"4", "IO4", "X4"}, pn{"5", "IO5", "X5"},
			pn{"6", "IO6", "X6"}, pn{"7", "IO7", "X7"}, pn{"8", "IO15", "X8"}),
			res("R1", "10k", "+3V3", "EN"), capa("C1", "1uF", "EN", "GND")},
			ClassResetRC, func(t *testing.T, b *Block) { near(t, "delay", b.Analytic["delayS"], 10e-3*math.Log(4), 1e-5) }},
		{"bjt-switch", []*powersim.Part{part("Q1", "MMBT3904", "", "", pn{"1", "B", "QB"}, pn{"2", "E", "GND"}, pn{"3", "C", "LOAD"}),
			res("R1", "4.7k", "CTRL", "QB"), res("R2", "1k", "+5V", "LOAD"), part("U1", "MCU", "MCU", "", pn{"1", "PA1", "CTRL"}, pn{"2", "VDD", "+3V3"}, pn{"3", "VSS", "GND"})},
			ClassTransistorSw, func(t *testing.T, b *Block) {
				near(t, "ib", b.Analytic["ibA"], (3.3-0.7)/4.7e3, 1e-6)
				near(t, "ic", b.Analytic["icA"], 5e-3, 1e-6)
			}},
		{"lc-filter", []*powersim.Part{part("FB1", "", "600Ω@100MHz", "", pn{"1", "1", "+5V"}, pn{"2", "2", "+5V_A"}), capa("C1", "10uF", "+5V_A", "GND"), capa("C2", "100nF", "+5V_A", "GND")},
			ClassLCFilter, func(t *testing.T, b *Block) {
				if b.Analytic["f0Hz"] <= 0 {
					t.Error("no f0")
				}
			}},
		{"rc-lowpass", []*powersim.Part{res("R1", "10k", "DRV", "FILT"), capa("C1", "15.9nF", "FILT", "GND"),
			part("U1", "MCU", "MCU", "", pn{"1", "TX", "DRV"}, pn{"2", "RX", "FILT"}, pn{"3", "VDD", "+3V3"}, pn{"4", "VSS", "GND"})},
			ClassRCLowPass, func(t *testing.T, b *Block) { near(t, "fc", b.Analytic["fcHz"], 1/(2*math.Pi*10e3*15.9e-9), 1e-5) }},
		{"adc-divider", []*powersim.Part{part("U1", "MCP3201-CI/SN", "MCP3201", "", pn{"1", "VREF", "+3V3"}, pn{"2", "IN+", "VSENSE"}, pn{"3", "IN-", "GND"}, pn{"4", "VSS", "GND"}, pn{"8", "VDD", "+3V3"}),
			res("R1", "100k", "+12V", "VSENSE"), res("R2", "10k", "VSENSE", "GND")},
			ClassADCInput, func(t *testing.T, b *Block) {
				near(t, "rs", b.Analytic["sourceOhm"], 100e3*10e3/110e3, 1e-3)
				if b.Analytic["rsMaxOhm"] >= b.Analytic["sourceOhm"] {
					t.Errorf("9 kΩ source should exceed the MCP3201 limit (rsMax %g)", b.Analytic["rsMaxOhm"])
				}
			}},
		{"current-sense", []*powersim.Part{part("U1", "INA180A1IDBVR", "INA180A1", "", pn{"1", "OUT", "ISENSE"}, pn{"2", "GND", "GND"}, pn{"3", "IN+", "VBUS_IN"}, pn{"4", "IN-", "VBUS_LOAD"}, pn{"5", "VS", "+3V3"}),
			res("R1", "0.1", "VBUS_IN", "VBUS_LOAD")},
			ClassCurrentSense, func(t *testing.T, b *Block) { near(t, "tz", b.Analytic["transimpedanceVperA"], 2, 1e-6) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := runNoSpice(t, design(c.parts...), nil)
			b := blockOf(t, out, c.class)
			c.check(t, b)
			if b.Simulated || b.Skipped != MissingNgspiceNote {
				t.Errorf("ngspice none: simulated=%v skipped=%q", b.Simulated, b.Skipped)
			}
		})
	}
}

func TestRegulatorFeedbackDivider(t *testing.T) {
	plib, err := powersim.ParseLibrary([]byte(`{"schemaVersion":1,"models":[{"id":"buck","kind":"buck","match":{"mpn":["BUCK1"]},"pins":{"in":["IN"],"out":["LX"],"gnd":["GND"],"fb":["FB"]},"vref":0.6,"fswHz":1e6,"eta":0.9}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d := design(part("U1", "BUCK1", "", "", pn{"1", "IN", "+5V"}, pn{"2", "GND", "GND"}, pn{"3", "LX", "SW"}, pn{"4", "FB", "FB"}),
		res("R1", "45.3k", "+3V3", "FB"), res("R2", "10k", "FB", "GND"))
	out, err := Run(d, testLib(t), Options{Ngspice: "none", PowerLibs: powersim.Libraries{plib}, MCRuns: 200})
	if err != nil {
		t.Fatal(err)
	}
	b := blockOf(t, out, ClassRegulatorFB)
	near(t, "vout", b.Analytic["voutV"], 0.6*5.53, 1e-6)
	m, ok := b.metric("voutV")
	if !ok || m.Status != StatusPass || m.Target == nil || *m.Target.Value != 3.3 {
		t.Errorf("vout target: %+v", m)
	}
	if b.Tolerance == nil || b.Tolerance.Method != "analytic-mc" || b.Tolerance.Stats["voutV"].Std <= 0 {
		t.Errorf("tolerance: %+v", b.Tolerance)
	}
}

func TestOptimiserSallenKeyAnalytic(t *testing.T) {
	d := design(opamp("U1", "TLV9061", "P", "OUT", "OUT", "+5V", "GND"), res("R1", "10k", "IN", "X"), res("R2", "10k", "X", "P"),
		capa("C1", "10nF", "X", "OUT"), capa("C2", "10nF", "P", "GND"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"}))
	spec := &Spec{Blocks: []SpecBlock{{Core: "U1", Targets: map[string]float64{"fcHz": 1000, "q": 0.707}, TolPct: map[string]float64{"fcHz": 3, "q": 3}}}}
	out := runNoSpice(t, d, spec)
	b := blockOf(t, out, ClassSKLowPass)
	o := b.Optimise
	if o == nil || o.Status != "improved" || len(o.Changes) == 0 {
		t.Fatalf("optimisation: %+v", o)
	}
	near(t, "fc after", o.After["fcHz"], 1000, 0.03)
	near(t, "q after", o.After["q"], 0.707, 0.03)
	for _, ch := range o.Changes {
		series := "E96"
		if ch.Kind == "C" {
			series = "E12"
		}
		if !InSeries(ch.ToValue, series) {
			t.Errorf("%s → %g not in %s", ch.Ref, ch.ToValue, series)
		}
		if ch.Action == "" || ch.Reason == "" || len(ch.Before) == 0 {
			t.Errorf("incomplete change %+v", ch)
		}
	}
	if out.Plan == nil || !out.Plan.RequiresUserConfirmation || out.Plan.Kind != PlanKind {
		t.Fatalf("plan: %+v", out.Plan)
	}
}

func TestCompilePlaybook(t *testing.T) {
	plan := BuildPlan([]Change{
		{Ref: "R5", Block: "A3", Kind: "R", From: "10kΩ", To: "5.11kΩ", Action: "replace-lcsc", Part: &StockPart{LCSC: "C123", Value: "5.11k"}},
		{Ref: "C3", Block: "A3", Kind: "C", From: "10nF", To: "15nF", Action: "set-value", NeedsPartSelection: true, SearchHint: "0402 15nF"},
	})
	comps := []map[string]any{{"designator": "R5", "primitiveId": "p-r5"}, {"designator": "C3", "primitiveId": "p-c3"}}
	if _, _, err := CompilePlaybook(plan, comps, PlaybookOptions{}); err == nil || !strings.Contains(err.Error(), "C3") {
		t.Fatalf("value-only change must be refused without AllowValueOnly: %v", err)
	}
	pb, warns, err := CompilePlaybook(plan, comps, PlaybookOptions{AllowValueOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	steps := pb["steps"].([]map[string]any)
	byID := map[string]map[string]any{}
	for _, s := range steps {
		byID[s["id"].(string)] = s
	}
	if len(steps) != 3 || byID["value-R5"]["action"] != "schematic.component.replace" || byID["value-C3"]["action"] != "schematic.component.modify" || steps[2]["action"] != "schematic.save" {
		t.Fatalf("steps: %+v", steps)
	}
	if byID["value-R5"]["payload"].(map[string]any)["lcsc"] != "C123" || byID["value-R5"]["confirm"] != true || len(warns) != 1 {
		t.Errorf("replace step %+v warns %v", byID["value-R5"], warns)
	}
	if _, _, err := CompilePlaybook(plan, comps[:1], PlaybookOptions{AllowValueOnly: true}); err == nil {
		t.Error("missing ref must fail")
	}
}

func TestNoAnalogBlocks(t *testing.T) {
	out := runNoSpice(t, design(capa("C1", "100nF", "+5V", "GND"), part("U1", "MCU", "MCU", "", pn{"1", "VDD", "+5V"}, pn{"2", "VSS", "GND"})), nil)
	if len(out.Blocks) != 0 || out.Summary.Status != StatusInfo || out.Plan != nil {
		t.Errorf("decoupling only: %+v", out.Summary)
	}
	var buf bytes.Buffer
	if err := WriteReport(&buf, out, nil); err != nil || !strings.Contains(buf.String(), "未识别到模拟电路块") {
		t.Errorf("report: %v %s", err, buf.String())
	}
}

// ---------------------------------------------------------------------------
// ngspice-backed validation against closed-form results.

func simOne(t *testing.T, d *powersim.Design, spec *Spec, class string) *Block {
	t.Helper()
	out, err := Run(d, testLib(t), Options{Spec: spec, MCRuns: 20, Optimise: spec != nil})
	if err != nil {
		t.Fatal(err)
	}
	b := blockOf(t, out, class)
	if !b.Simulated {
		t.Fatalf("%s not simulated: %s", b.ID, b.Skipped)
	}
	return b
}

func metricOf(t *testing.T, b *Block, name string) float64 {
	t.Helper()
	m, ok := b.metric(name)
	if !ok {
		t.Fatalf("%s: no metric %s", b.ID, name)
	}
	return m.Value
}

func TestNgspiceRCLowPass(t *testing.T) {
	needNgspice(t)
	d := design(res("R1", "10k", "DRV", "FILT"), capa("C1", "15.9nF", "FILT", "GND"),
		part("U1", "MCU", "MCU", "", pn{"1", "TX", "DRV"}, pn{"2", "RX", "FILT"}, pn{"3", "VDD", "+3V3"}, pn{"4", "VSS", "GND"}))
	b := simOne(t, d, nil, ClassRCLowPass)
	near(t, "fc", metricOf(t, b, "fcHz"), 1/(2*math.Pi*10e3*15.9e-9), 0.005)
	near(t, "gain", metricOf(t, b, "gain"), 1, 1e-3)
	// τ = RC → 10–90 % rise = ln 9 · τ.
	near(t, "rise", metricOf(t, b, "riseTimeS"), math.Log(9)*10e3*15.9e-9, 0.02)
	if b.Tolerance == nil || b.Tolerance.Method != "ngspice-ac-mc" {
		t.Errorf("MC: %+v", b.Tolerance)
	}
}

func TestNgspiceSallenKey(t *testing.T) {
	needNgspice(t)
	// TLV9061 (10 MHz): the op-amp is ideal enough at 1 kHz for the textbook f0 / Q.
	d := design(opamp("U1", "TLV9061", "P", "OUT", "OUT", "+5V", "GND"), res("R1", "10k", "IN", "X"), res("R2", "10k", "X", "P"),
		capa("C1", "22nF", "X", "OUT"), capa("C2", "10nF", "P", "GND"), part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"}))
	b := simOne(t, d, nil, ClassSKLowPass)
	f0 := 1 / (2 * math.Pi * 10e3 * math.Sqrt(22e-9*10e-9))
	q := math.Sqrt(22e-9/10e-9) / 2
	near(t, "f0", metricOf(t, b, "f0Hz"), f0, 0.01)
	near(t, "q", metricOf(t, b, "q"), q, 0.02)
	near(t, "fc", metricOf(t, b, "fcHz"), fcLowPass2(f0, q), 0.01)
	near(t, "gain", metricOf(t, b, "gain"), 1, 1e-3)
}

func TestNgspiceNonInvertingGBW(t *testing.T) {
	needNgspice(t)
	// Generic op-amp (1 MHz, 100 dB, p2 = 3 MHz), gain 10 → β = 0.1.
	d := design(opamp("U1", "GENERIC-OA", "IN", "FB", "OUT", "+5V", "-12V"), res("R1", "90k", "OUT", "FB"), res("R2", "10k", "FB", "GND"),
		part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"}))
	b := simOne(t, d, nil, ClassNonInverting)
	// Closed loop of the same two-pole model, solved numerically.
	aol, gbw, p2 := 1e5, 1e6, 3e6
	h := func(f float64) complex128 {
		s := complex(0, 2*math.Pi*f)
		a := complex(aol, 0) / (1 + s*complex(aol/(2*math.Pi*gbw), 0)) / (1 + s/complex(2*math.Pi*p2, 0))
		return a / (1 + a*0.1)
	}
	lo, hi := 1e3, 1e7
	for i := 0; i < 80; i++ {
		m := math.Sqrt(lo * hi)
		if cmplx.Abs(h(m)) > cmplx.Abs(h(1))/math.Sqrt2 {
			lo = m
		} else {
			hi = m
		}
	}
	near(t, "gain", metricOf(t, b, "gain"), 10, 0.002)
	near(t, "bandwidth", metricOf(t, b, "fcHz"), lo, 0.02)
}

func TestNgspiceInvertingPhaseMarginCapLoad(t *testing.T) {
	needNgspice(t)
	// Inverting gain −1, 10 nF directly on the output of the generic model (Rout 50 Ω).
	d := design(opamp("U1", "GENERIC-OA", "VMID", "SUM", "OUT", "+5V", "GND"), res("R1", "10k", "IN", "SUM"), res("R2", "10k", "SUM", "OUT"),
		capa("C1", "10nF", "OUT", "GND"), part("U2", "REFBUF", "", "", pn{"1", "OUT", "VMID"}, pn{"2", "VDD", "+5V"}, pn{"3", "VSS", "GND"}),
		part("J1", "", "", "", pn{"1", "1", "IN"}, pn{"2", "2", "GND"}))
	b := simOne(t, d, nil, ClassInverting)
	pmSim := metricOf(t, b, "phaseMarginDeg")
	// Loop gain of the same model: T = A(s) · Zl/(Zl+Rout) · R1/(R1+R2), Zl = C ∥ (R1+R2).
	aol, gbw, p2, rout := 1e5, 1e6, 3e6, 50.0
	T := func(f float64) complex128 {
		s := complex(0, 2*math.Pi*f)
		a := complex(aol, 0) / (1 + s*complex(aol/(2*math.Pi*gbw), 0)) / (1 + s/complex(2*math.Pi*p2, 0))
		zc := 1 / (s * 10e-9)
		zl := zc * 20e3 / (zc + 20e3)
		return a * zl / (zl + complex(rout, 0)) * 0.5
	}
	lo, hi := 1e3, 1e8
	for i := 0; i < 80; i++ {
		m := math.Sqrt(lo * hi)
		if cmplx.Abs(T(m)) > 1 {
			lo = m
		} else {
			hi = m
		}
	}
	pm := 180 + cmplx.Phase(T(lo))*180/math.Pi
	t.Logf("phase margin: ngspice %.2f° vs analytic %.2f° (crossover %.4g Hz)", pmSim, pm, lo)
	if math.Abs(pmSim-pm) > 1.5 {
		t.Errorf("phase margin: ngspice %.2f°, analytic %.2f°", pmSim, pm)
	}
	if pm > 45 {
		t.Fatalf("test circuit should be marginal, analytic PM %.1f°", pm)
	}
	found := false
	for _, f := range b.Findings {
		found = found || f.Kind == "analog-phase-margin"
	}
	if !found {
		t.Errorf("no analog-phase-margin finding: %+v", b.Findings)
	}
}

func TestNgspiceFrontendLoop(t *testing.T) {
	needNgspice(t)
	d := loadFixture(t, "../../testdata/analog/frontend", "sch-frontend.json")
	sb, _ := os.ReadFile("../../testdata/analog/frontend/spec.json")
	spec, err := ParseSpec(sb)
	if err != nil {
		t.Fatal(err)
	}
	stock, _ := LoadStock(filepath.Join(skillRefs, "standard-parts.json"))
	out, err := Run(d, testLib(t), Options{Spec: spec, Stock: stock, Optimise: true, MCRuns: 40})
	if err != nil {
		t.Fatal(err)
	}
	sk := blockOf(t, out, ClassSKLowPass)
	if q := metricOf(t, sk, "q"); math.Abs(q-0.5) > 0.01 {
		t.Errorf("fixture Q = %.3f, want the deliberate 0.5", q)
	}
	o := sk.Optimise
	if o == nil || o.Status != "improved" || o.Verified != "ngspice" {
		t.Fatalf("optimisation: %+v", o)
	}
	near(t, "q after (ngspice)", o.After["q"], 0.707, 0.05)
	near(t, "fc after (ngspice)", o.After["fcHz"], 1000, 0.05)
	ref := blockOf(t, out, ClassReference)
	near(t, "ik", metricOf(t, ref, "ikA"), (5-4.096)/1e3-150e-6, 0.02)
	fol := blockOf(t, out, ClassFollower)
	near(t, "divider gain", metricOf(t, fol, "gain"), 20.0/120.0, 0.005)
	near(t, "input pole", metricOf(t, fol, "fcHz"), 1/(2*math.Pi*(1e3+100e3*20e3/120e3)*100e-9), 0.01)
}

func TestNgspiceESP32(t *testing.T) {
	needNgspice(t)
	d, sim, libs := esp32Design(t)
	out, err := Run(d, testLib(t), Options{MCRuns: 20, PowerSim: sim, PowerLibs: libs, Optimise: true})
	if err != nil {
		t.Fatal(err)
	}
	rst := blockOf(t, out, ClassResetRC)
	// EN: 10 kΩ · 1 µF, VIH = 0.75·VDD → 13.9 ms from a step; the 1 ms rail ramp pre-charges C6.
	if dl := metricOf(t, rst, "delayS"); dl < 12e-3 || dl > 14e-3 {
		t.Errorf("EN delay %.4g s", dl)
	}
	fb := blockOf(t, out, ClassRegulatorFB)
	near(t, "vout", metricOf(t, fb, "voutV"), 0.6*(1+45.3/10), 0.002)
	var q2 *Block
	for _, b := range out.Blocks {
		if b.Class == ClassTransistorSw && b.Core == "Q2" {
			q2 = b
		}
	}
	if q2 == nil {
		t.Fatal("Q2 auto-reset switch not found")
	}
	if ip := metricOf(t, q2, "icPeakA"); ip < 0.15 || ip > 0.3 {
		t.Errorf("Q2 C6 discharge peak %.3g A", ip)
	}
	if ov := metricOf(t, q2, "overdrive"); ov < 2 {
		t.Errorf("Q2 overdrive %.3g", ov)
	}
}

// A TL431 whose REF sits on a divider (the opto feedback of an isolated
// flyback) is a loop error amplifier, not a biased reference: it must not be
// simulated as one (the cathode was left unbiased and a 0 A Ik failed the
// HV stress E2E), but its Ik,min is still flagged for the loop to verify.
// A REF-to-cathode TL431 stays a reference block.
func TestShuntRegulatorInLoopNotAReference(t *testing.T) {
	tl := func(ref, refNet, k string) *powersim.Part {
		return part(ref, "TL431AIDBZR", "TL431", "", pn{"1", "REF", refNet}, pn{"2", "K", k}, pn{"3", "A", "GND"})
	}
	loop := design(tl("U3", "TL_REF", "OPTO_K"), res("R8", "1k", "+12V", "OPTO_A"),
		part("U2", "PC817C", "PC817C", "", pn{"1", "A", "OPTO_A"}, pn{"2", "K", "OPTO_K"}, pn{"3", "E", "PGND"}, pn{"4", "C", "COMP"}),
		res("R9", "38.3k", "+12V", "TL_REF"), res("R10", "10k", "TL_REF", "GND"))
	out := runNoSpice(t, loop, nil)
	for _, b := range out.Blocks {
		if b.Class == ClassReference {
			t.Fatalf("loop TL431 simulated as a reference: %+v", b)
		}
	}
	var warn bool
	for _, f := range out.Findings {
		if f.Kind == "shunt-regulator-loop" && f.Severity == "warn" && strings.Contains(f.Message, "U3") {
			warn = true
		}
		if f.Severity == "error" {
			t.Fatalf("error finding %+v", f)
		}
	}
	if !warn {
		t.Fatalf("no shunt-regulator-loop warning: %+v", out.Findings)
	}
	ref := design(tl("U1", "VREF", "VREF"), res("R1", "1k", "+5V", "VREF"))
	if b := blockOf(t, runNoSpice(t, ref, nil), ClassReference); b.Core != "U1" {
		t.Fatalf("reference block %+v", b)
	}
}
