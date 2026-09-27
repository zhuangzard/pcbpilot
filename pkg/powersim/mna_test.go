package powersim

import (
	"math"
	"strings"
	"testing"
)

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.9g, want %.9g ± %.3g", what, got, want, tol)
	}
}

func solveOK(t *testing.T, c *Circuit) []float64 {
	t.Helper()
	sol, _, conv, err := c.Solve()
	if err != nil {
		t.Fatal(err)
	}
	if !conv {
		t.Fatal("did not converge")
	}
	return sol.X
}

// kcl sums element terminal currents per node; every non-ground node must
// balance (up to gmin leakage).
func kcl(t *testing.T, c *Circuit, x []float64) {
	t.Helper()
	sum := make([]float64, len(c.names))
	for _, el := range c.elems {
		cur := el.currents(x)
		for i, term := range el.terminals() {
			if term.Node >= 0 {
				sum[term.Node] += cur[i]
			}
		}
	}
	for i, s := range sum {
		if math.Abs(s+gminNode*x[i]) > 1e-9 {
			t.Fatalf("KCL violated at %s: %.3g A", c.names[i], s)
		}
	}
}

func T(n int) Terminal { return Terminal{Node: n} }

func TestDivider(t *testing.T) {
	c := NewCircuit()
	a, m := c.Node("a"), c.Node("m")
	c.add(&vsource{t: [2]Terminal{T(a), T(Ground)}, e: 10})
	r1 := &resistor{t: [2]Terminal{T(a), T(m)}, r: 1000}
	c.add(r1)
	c.add(&resistor{t: [2]Terminal{T(m), T(Ground)}, r: 3000})
	x := solveOK(t, c)
	near(t, "V(m)", x[m], 7.5, 1e-6)
	near(t, "I(R1)", r1.currents(x)[0], 2.5e-3, 1e-9)
	kcl(t, c, x)
}

func TestSourceSeriesResistance(t *testing.T) {
	c := NewCircuit()
	a := c.Node("a")
	c.add(&vsource{t: [2]Terminal{T(a), T(Ground)}, e: 5, rs: 0.5})
	c.add(&load{t: [2]Terminal{T(a), T(Ground)}, inom: 1, knee: 0.8})
	x := solveOK(t, c)
	near(t, "V(a)", x[a], 4.5, 1e-6)
}

func TestDiodeResistor(t *testing.T) {
	c := NewCircuit()
	a, k := c.Node("a"), c.Node("k")
	c.add(&vsource{t: [2]Terminal{T(a), T(Ground)}, e: 5})
	c.add(&resistor{t: [2]Terminal{T(a), T(k)}, r: 1000})
	d := &diode{t: [2]Terminal{T(k), T(Ground)}, is: 1e-14, n: 1}
	c.add(d)
	x := solveOK(t, c)
	i := d.currents(x)[0]
	near(t, "I", i, (5-x[k])/1000, 1e-12)
	near(t, "Vd from Shockley", x[k], thermalVoltage*math.Log(i/1e-14+1), 1e-6)
	if x[k] < 0.6 || x[k] > 0.75 {
		t.Fatalf("diode drop %.3f V out of range", x[k])
	}
	kcl(t, c, x)
}

func TestLEDCurrentFromResistor(t *testing.T) {
	is, n := diodeParams(Model{}, 2.0, 0.005, 2) // yellow knee
	c := NewCircuit()
	a, k := c.Node("a"), c.Node("k")
	c.add(&vsource{t: [2]Terminal{T(a), T(Ground)}, e: 3.3})
	c.add(&resistor{t: [2]Terminal{T(a), T(k)}, r: 330})
	led := &diode{t: [2]Terminal{T(k), T(Ground)}, is: is, n: n}
	c.add(led)
	x := solveOK(t, c)
	i := led.currents(x)[0]
	// At 5 mA the LED drops exactly 2.0 V, so (3.3-2.0)/330 = 3.94 mA and the
	// drop must be slightly below 2.0 V.
	if x[k] > 2.0 || x[k] < 1.95 {
		t.Fatalf("LED Vf %.4f", x[k])
	}
	near(t, "I(LED)", i, (3.3-x[k])/330, 1e-9)
	near(t, "I(LED) ≈ (3.3−2.0)/330", i, 1.3/330, 0.2e-3)
}

func TestORDiodesPickHigherSource(t *testing.T) {
	is, n := diodeParams(Model{VfPoints: [][2]float64{{0.1, 0.30}, {3, 0.45}}}, 0, 0, 1.5)
	c := NewCircuit()
	s1, s2, out := c.Node("s1"), c.Node("s2"), c.Node("out")
	c.add(&vsource{t: [2]Terminal{T(s1), T(Ground)}, e: 5.0, rs: 0.05})
	c.add(&vsource{t: [2]Terminal{T(s2), T(Ground)}, e: 4.6, rs: 0.05})
	d1 := &diode{t: [2]Terminal{T(s1), T(out)}, is: is, n: n}
	d2 := &diode{t: [2]Terminal{T(s2), T(out)}, is: is, n: n}
	c.add(d1)
	c.add(d2)
	c.add(&load{t: [2]Terminal{T(out), T(Ground)}, inom: 0.5, knee: 0.8})
	x := solveOK(t, c)
	i1, i2 := d1.currents(x)[0], d2.currents(x)[0]
	near(t, "I(D1)+I(D2)", i1+i2, 0.5, 1e-9)
	if i1 < 0.49 || math.Abs(i2) > 0.01 {
		t.Fatalf("higher source should carry the load: I1=%.4f I2=%.4f", i1, i2)
	}
	// Fit check: the two points are reproduced.
	for _, p := range [][2]float64{{0.1, 0.30}, {3, 0.45}} {
		near(t, "fit", n*thermalVoltage*math.Log(p[0]/is+1), p[1], 1e-4)
	}
	kcl(t, c, x)
}

func TestBJTSaturates(t *testing.T) {
	c := NewCircuit()
	vcc, b, col := c.Node("vcc"), c.Node("b"), c.Node("c")
	c.add(&vsource{t: [2]Terminal{T(vcc), T(Ground)}, e: 3.3})
	c.add(&resistor{t: [2]Terminal{T(vcc), T(b)}, r: 4700})
	c.add(&resistor{t: [2]Terminal{T(vcc), T(col)}, r: 10000})
	q := &bjt{t: [3]Terminal{T(col), T(b), T(Ground)}, is: 6.734e-15, bf: 416.4, br: 0.7371}
	c.add(q)
	x := solveOK(t, c)
	if x[col] > 0.2 {
		t.Fatalf("NPN with 0.55 mA base drive should saturate, Vce=%.3f", x[col])
	}
	cur := q.currents(x)
	near(t, "Ic", cur[0], (3.3-x[col])/10000, 1e-9)
	near(t, "Ie = -(Ic+Ib)", cur[2], -(cur[0] + cur[1]), 1e-12)
	kcl(t, c, x)
}

func TestLoadKneeUnpowered(t *testing.T) {
	c := NewCircuit()
	a := c.Node("a")
	l := &load{t: [2]Terminal{T(a), T(Ground)}, inom: 0.1, knee: 0.8}
	c.add(l)
	x := solveOK(t, c)
	near(t, "unpowered rail current", l.currents(x)[0], 0, 1e-12)
}

func TestParseValue(t *testing.T) {
	cases := map[string]float64{
		"10kΩ": 1e4, "4.7kΩ": 4700, "45.3kΩ": 45300, "4K7": 4700, "1R5": 1.5, "0R": 0, "0Ω": 0,
		"2.2uH": 2.2e-6, "2.2µH": 2.2e-6, "100nF": 100e-9, "22uF": 22e-6, "10 uF": 10e-6,
		"1M": 1e6, "5.1k": 5100, "330": 330, "2u2": 2.2e-6, "10k 1%": 1e4, "100nF/50V": 100e-9,
	}
	for in, want := range cases {
		got, ok := ParseValue(in)
		if !ok || math.Abs(got-want) > 1e-15+1e-9*math.Abs(want) {
			t.Errorf("ParseValue(%q) = %g, %v; want %g", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "0402WGF4532TCE", "YELLOW", "={Value}"} {
		if _, ok := ParseValue(bad); ok {
			t.Errorf("ParseValue(%q) should fail", bad)
		}
	}
	if v, ok := valueFromDescription("Inductance:2.2uH Tolerance:±20% DC Resistance(DCR):169mΩ", "dcr"); !ok || math.Abs(v-0.169) > 1e-12 {
		t.Errorf("dcr from description = %g %v", v, ok)
	}
}

// ---- design-level helpers ------------------------------------------------

// part builds a Part; pins are "number:name:net".
func part(ref, value, mpn string, pins ...string) *Part {
	p := &Part{Ref: ref, Value: value, MPN: mpn}
	for _, s := range pins {
		f := strings.SplitN(s, ":", 3)
		p.Pins = append(p.Pins, Pin{Number: f[0], Name: f[1], Net: f[2]})
	}
	return p
}

func design(parts ...*Part) *Design {
	return &Design{Parts: parts, NetRole: map[string]string{"GND": "ground"}}
}

func testLib(t *testing.T, js string) Libraries {
	t.Helper()
	lib, err := ParseLibrary([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	lib.Origin = "test"
	return Libraries{lib}
}

const buckLib = `{"schemaVersion":1,"models":[
 {"id":"buck-x","kind":"buck","match":{"mpn":["BUCKX"]},"pins":{"in":["IN"],"out":["LX"],"gnd":["GND"],"fb":["FB"],"en":["EN"]},
  "vref":0.6,"fswHz":1000000,"eta":0.9,"iqA":0.0001,"vinMinV":2.5,"maxA":2},
 {"id":"mcu-x","kind":"load","match":{"mpn":["MCUX"]},"supplyPins":["VDD"],"returnPins":["VSS"],"typA":0.2,"peakA":0.6},
 {"id":"src-x","kind":"connector-source","match":{"mpn":["JACK"]},"voltageV":5,"rsOhm":0.0001},
 {"id":"ldo-x","kind":"ldo","match":{"mpn":["LDOX"]},"pins":{"in":["VIN"],"out":["VOUT"],"gnd":["GND"]},"vout":3.3,"dropoutV":1.1,"iqA":0.005}
]}`

func buckDesign() *Design {
	return design(
		part("J1", "", "JACK", "1:1:VIN", "2:2:GND"),
		part("U1", "", "BUCKX", "1:EN:VIN", "2:GND:GND", "3:LX:SW", "4:IN:VIN", "5:FB:FB"),
		part("L1", "4.7uH", "", "1:1:SW", "2:2:+3V3"),
		part("R1", "45.3k", "", "1:1:+3V3", "2:2:FB"),
		part("R2", "10k", "", "1:1:FB", "2:2:GND"),
		part("C1", "10uF", "", "1:1:VIN", "2:2:GND"),
		part("C2", "22uF", "", "1:1:+3V3", "2:2:GND"),
		part("U2", "", "MCUX", "1:VDD:+3V3", "2:VSS:GND"),
	)
}

func findResult(t *testing.T, out *Output, name string) *Result {
	t.Helper()
	for i := range out.Results {
		if out.Results[i].Scenario == name {
			return &out.Results[i]
		}
	}
	t.Fatalf("no result %s in %v", name, out.Scenarios)
	return nil
}

func pinCurrent(t *testing.T, res *Result, net, ref, pin string) PinResult {
	t.Helper()
	nr, ok := res.Nets[net]
	if !ok {
		t.Fatalf("no net %s", net)
	}
	for _, p := range nr.Pins {
		if p.Ref == ref && p.Pin == pin {
			return p
		}
	}
	t.Fatalf("no pin %s.%s on %s", ref, pin, net)
	return PinResult{}
}

func assertKCL(t *testing.T, res *Result) {
	t.Helper()
	for net, nr := range res.Nets {
		var src, snk float64
		for _, p := range nr.Pins {
			switch p.Dir {
			case "source":
				src += p.CurrentA
			case "sink":
				snk += p.CurrentA
			}
		}
		if math.Abs(src-snk) > 1e-6 || nr.KCLErrorA > 1e-6 {
			t.Fatalf("%s %s: KCL source %.7f sink %.7f", res.Scenario, net, src, snk)
		}
		if math.Abs(nr.CurrentA-math.Max(src, snk)) > 1e-6 {
			t.Fatalf("%s %s: net currentA %.7f ≠ Σsource %.7f", res.Scenario, net, nr.CurrentA, src)
		}
	}
}

func TestBuckDividerAndInputCurrent(t *testing.T) {
	out, _, err := Simulate(buckDesign(), testLib(t, buckLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"typical", "peak"} {
		res := findResult(t, out, name)
		assertKCL(t, res)
		vout := 0.6 * (1 + 45.3/10)
		near(t, name+" V(+3V3)", res.Nets["+3V3"].Voltage, vout, 1e-3)
		u1 := res.Parts["U1"]
		iout := map[string]float64{"typical": 0.2, "peak": 0.6}[name] + vout/55300
		near(t, name+" Iout", u1.OutputA, iout, 1e-6)
		vlx := res.Nets["SW"].Voltage
		near(t, name+" Iin = Vlx·Iout/(η·Vin)+Iq", u1.InputA, vlx*u1.OutputA/(0.9*u1.VinV)+0.0001, 3e-5) // V rounded to 0.1 mV
		near(t, name+" IN pin current", pinCurrent(t, res, "VIN", "U1", "4").CurrentA, u1.InputA, 1e-6)
		if p := pinCurrent(t, res, "SW", "U1", "3"); p.Dir != "source" {
			t.Fatalf("LX pin should source, got %s", p.Dir)
		}
		if !strings.Contains(strings.Join(u1.Notes, " "), "Vout=0.6*(1+R1/R2)=3.318V") {
			t.Fatalf("divider note missing: %v", u1.Notes)
		}
		// Ripple formulas.
		vin, vo := u1.VinV, res.Nets["+3V3"].Voltage
		d := vo / (0.9 * vin)
		dI := (vin - vo) * d / (4.7e-6 * 1e6)
		sw := res.Ripple["SW"]
		near(t, name+" ΔI", sw.DeltaIA, dI, 1e-4)
		near(t, name+" Ipk", sw.IPeakA, iout+dI/2, 1e-4)
		near(t, name+" Irms", sw.IRmsA, math.Sqrt(iout*iout+dI*dI/12), 1e-4)
		near(t, name+" Cin Irms", res.Ripple["C1"].IRmsA, iout*math.Sqrt(d*(1-d)), 1e-4)
		near(t, name+" Cout Irms", res.Ripple["C2"].IRmsA, dI/(2*math.Sqrt(3)), 1e-4)
		if res.Ripple["L1"] == nil {
			t.Fatal("inductor ripple entry missing")
		}
		if res.Nets["SW"].Role != "switch" || res.Nets["VIN"].Role != "power" || res.Nets["GND"].Role != "ground" || res.Nets["FB"].Role != "signal" {
			t.Fatalf("roles: SW=%s VIN=%s GND=%s FB=%s", res.Nets["SW"].Role, res.Nets["VIN"].Role, res.Nets["GND"].Role, res.Nets["FB"].Role)
		}
	}
}

func TestLDODropoutAndUnknownIC(t *testing.T) {
	d := design(
		part("J1", "", "JACK", "1:1:VIN", "2:2:GND"),
		part("U1", "", "LDOX", "1:GND:GND", "2:VOUT:+3V3", "3:VIN:VIN", "4:VOUT:+3V3"),
		part("U2", "", "MYSTERY123", "1:VCC:+3V3", "2:VCC:+3V3", "3:GND:GND", "4:IO:SIG"),
	)
	out, _, err := Simulate(d, testLib(t, buckLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	res := findResult(t, out, "typical")
	assertKCL(t, res)
	// 5 V in, 3.3 V out regulating; unknown IC = 2 × 50 mA assumed, split.
	near(t, "V(+3V3)", res.Nets["+3V3"].Voltage, 3.3, 1e-6)
	near(t, "U2.1", pinCurrent(t, res, "+3V3", "U2", "1").CurrentA, 0.05, 1e-6)
	near(t, "U2.2", pinCurrent(t, res, "+3V3", "U2", "2").CurrentA, 0.05, 1e-6)
	// LDO: Iin = Iout + Iq, output split over VOUT and TAB.
	near(t, "LDO IN", pinCurrent(t, res, "VIN", "U1", "3").CurrentA, 0.1+0.005, 1e-6)
	near(t, "LDO VOUT pin", pinCurrent(t, res, "+3V3", "U1", "2").CurrentA, 0.05, 1e-6)
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "U2 (MYSTERY123): no power model — assumed 50 mA per supply pin") {
		t.Fatalf("unknown IC warning missing: %v", res.Warnings)
	}
	// Now starve the input: 3.5 V source → dropout, out = 3.5 − 1.1.
	lib := testLib(t, strings.Replace(buckLib, `"voltageV":5`, `"voltageV":3.5`, 1))
	out, _, err = Simulate(d, lib, Options{Scenarios: []string{"typical"}})
	if err != nil {
		t.Fatal(err)
	}
	res = findResult(t, out, "typical")
	if res.Parts["U1"].Mode != modeDropout {
		t.Fatalf("mode %s, want dropout", res.Parts["U1"].Mode)
	}
	near(t, "dropout V(+3V3)", res.Nets["+3V3"].Voltage, 3.5-1.1, 1e-3)
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "U1: regulator dropout") {
		t.Fatalf("dropout warning missing: %v", res.Warnings)
	}
}

func TestLibraryMatchPrecedence(t *testing.T) {
	base := testLib(t, `{"models":[
	 {"id":"by-regex","kind":"load","match":{"nameRegex":"(?i)^ABC"}},
	 {"id":"by-mpn","kind":"load","match":{"mpn":["ABC123"]}},
	 {"id":"by-lcsc","kind":"load","match":{"lcsc":["C999"]}}]}`)
	p := &Part{Ref: "U1", MPN: "abc123", LCSC: "C999"}
	if m := base.Match(p); m == nil || m.Model.ID != "by-lcsc" || m.By != "lcsc" {
		t.Fatalf("LCSC should win: %+v", m)
	}
	p.LCSC = ""
	if m := base.Match(p); m == nil || m.Model.ID != "by-mpn" {
		t.Fatalf("MPN (case-insensitive) should win over regex: %+v", m)
	}
	p.MPN = "ABC999"
	if m := base.Match(p); m == nil || m.Model.ID != "by-regex" {
		t.Fatalf("regex fallback: %+v", m)
	}
	over := testLib(t, `{"models":[{"id":"override","kind":"load","match":{"nameRegex":"^ABC9"}}]}`)
	if m := append(over, base...).Match(p); m == nil || m.Model.ID != "override" {
		t.Fatalf("--models file must override the base library: %+v", m)
	}
	if m := base.Match(&Part{Ref: "U9", MPN: "XYZ"}); m != nil {
		t.Fatalf("unexpected match %+v", m)
	}
}

func TestSkillLibraryParsesAndMatchesBoardParts(t *testing.T) {
	libs := skillLibrary(t)
	want := map[string]string{
		"C9900163599": "esp32-s3-wroom-1", "C84681": "ch340c", "C479074": "sy8089a", "C8678": "ss34",
		"C143135": "smaj5.0a", "C2687116": "usblc6-2sc6", "C20526": "mmbt3904", "C2296": "kt-0805y",
		"C6186": "ams1117-3.3", "C474881": "kf301-2p", "C9900012665": "usb-c-receptacle", "C720477": "ts-1088", "C250183": "nlcv32t-2r2m",
	}
	for lcsc, id := range want {
		m := libs.Match(&Part{Ref: "X1", LCSC: lcsc})
		if m == nil || m.Model.ID != id {
			t.Errorf("%s → %v, want %s", lcsc, m, id)
		}
	}
	for _, lib := range libs {
		for _, m := range lib.Models {
			if m.Source == "" || m.Confidence == "" {
				t.Errorf("model %s must cite a source and confidence", m.ID)
			}
		}
	}
	if m := libs.Match(&Part{Ref: "U5", MPN: "AMS1117-3.3"}); m == nil || m.Model.Kind != KindLDO {
		t.Fatalf("AMS1117 by MPN: %+v", m)
	}
}

func TestSwitchOverrideAndScenarios(t *testing.T) {
	d := design(
		part("J1", "", "JACK", "1:1:+3V3", "2:2:GND"),
		part("R1", "10k", "", "1:1:+3V3", "2:2:BTN"),
		part("SW1", "", "", "1:1:BTN", "2:2:GND"),
	)
	out, _, err := Simulate(d, testLib(t, buckLib), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(out.Scenarios, ","); got != "typical,peak,buttons-pressed,worst" {
		t.Fatalf("scenarios %s", got)
	}
	near(t, "typical R1", pinCurrent(t, findResult(t, out, "typical"), "BTN", "R1", "2").CurrentA, 0, 1e-9)
	near(t, "pressed R1", pinCurrent(t, findResult(t, out, "buttons-pressed"), "BTN", "R1", "2").CurrentA, 5/10000.05, 1e-8)
	w := findResult(t, out, "worst")
	if p := pinCurrent(t, w, "BTN", "SW1", "1"); p.Scenario != "buttons-pressed" || p.Dir != "sink" {
		t.Fatalf("worst pin %+v", p)
	}
	out, _, err = Simulate(d, testLib(t, buckLib), Options{Switches: map[string]bool{"SW1": true}, Scenarios: []string{"typical"}})
	if err != nil {
		t.Fatal(err)
	}
	near(t, "forced closed", pinCurrent(t, findResult(t, out, "typical"), "BTN", "R1", "2").CurrentA, 5/10000.05, 1e-8)
	if _, _, err := Simulate(d, testLib(t, buckLib), Options{Scenarios: []string{"nope"}}); err == nil {
		t.Fatal("unknown scenario must fail")
	}
}
