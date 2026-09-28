package postsim

import (
	"encoding/json"
	"math"
	"testing"
)

// Validation against analytic results. Every board here is a synthetic
// `pcb dump --include-copper` document built in code (mil, y-up).

type tb struct {
	comps   []map[string]any
	lines   []map[string]any
	vias    []map[string]any
	poured  []map[string]any
	outline [][2]float64
	layers  int
}

func newTB(w, h float64, layers int) *tb {
	return &tb{outline: [][2]float64{{0, 0}, {w, 0}, {w, h}, {0, h}}, layers: layers}
}

func (t *tb) part(ref string, side int, pads ...map[string]any) {
	t.comps = append(t.comps, map[string]any{"designator": ref, "device": ref, "layer": side, "pads": pads})
}

func pad(num, net string, layer int, x, y, w, h float64) map[string]any {
	return map[string]any{"padNumber": num, "net": net, "layer": layer, "x": x, "y": y, "width": w, "height": h,
		"rotation": 0, "shape": []any{"RECT", w, h, 0}}
}

func (t *tb) line(net string, layer int, x0, y0, x1, y1, w float64) {
	t.lines = append(t.lines, map[string]any{"net": net, "layer": layer, "startX": x0, "startY": y0, "endX": x1, "endY": y1, "lineWidth": w, "primitiveId": "t"})
}

func (t *tb) via(net string, x, y, dia, drill float64) {
	t.vias = append(t.vias, map[string]any{"net": net, "x": x, "y": y, "diameter": dia, "holeDiameter": drill, "primitiveId": "v"})
}

func (t *tb) rectFill(net string, layer int, x0, y0, x1, y1 float64) {
	src := []any{[]any{x0, y0, "L", x1, y0, x1, y1, x0, y1}}
	t.poured = append(t.poured, map[string]any{"layer": layer, "net": net, "pourPrimitiveId": "p", "primitiveId": "p",
		"fills": []any{map[string]any{"fill": true, "geometryKind": "filled-complex-polygon", "lineWidth": 0, "source": src}}})
}

func (t *tb) json() []byte {
	var pts []any
	for _, p := range t.outline {
		pts = append(pts, []any{p[0], p[1]})
	}
	d := map[string]any{"components": t.comps, "outline": map[string]any{"points": pts}, "copperLayers": t.layers,
		"rules": map[string]any{"clearanceMil": 6, "copperToEdgeMil": 0},
		"copper": map[string]any{"lines": t.lines, "arcs": []any{}, "vias": t.vias, "pours": []any{}, "poured": t.poured, "fills": []any{}, "regions": []any{}}}
	b, _ := json.Marshal(d)
	return b
}

// simDoc builds a one-scenario sim.json: net → pins (ref, pin, dir, A), part powers.
func simDoc(nets map[string][][4]any, roles map[string]string, powers map[string]float64) []byte {
	type pin struct {
		Ref, Pin, Dir string
		CurrentA      float64
	}
	res := map[string]any{"scenario": "typical", "converged": true}
	nm := map[string]any{}
	for n, ps := range nets {
		var pins []map[string]any
		tot := 0.0
		for _, p := range ps {
			pins = append(pins, map[string]any{"ref": p[0], "pin": p[1], "dir": p[2], "currentA": p[3]})
			if p[2] == "source" {
				tot += p[3].(float64)
			}
		}
		role := roles[n]
		if role == "" {
			role = "power"
		}
		nm[n] = map[string]any{"voltage": 5.0, "currentA": tot, "role": role, "pins": pins}
	}
	res["nets"] = nm
	pm := map[string]any{}
	for r, w := range powers {
		pm[r] = map[string]any{"model": "load", "powerW": w}
	}
	res["parts"] = pm
	b, _ := json.Marshal(map[string]any{"schemaVersion": 1, "scenarios": []string{"typical"}, "results": []any{res}})
	return b
}

func netOf(t *testing.T, r *Result, net string) *NetResult {
	for _, n := range r.Nets {
		if n.Net == net {
			return n
		}
	}
	t.Fatalf("net %s missing", net)
	return nil
}

func near(t *testing.T, what string, got, want, rel float64) {
	t.Helper()
	if math.Abs(got-want) > rel*math.Abs(want) {
		t.Errorf("%s = %.6g, want %.6g ± %.1f %%", what, got, want, rel*100)
	} else {
		t.Logf("%s = %.6g (analytic %.6g, %+.2f %%)", what, got, want, 100*(got-want)/want)
	}
}

// 1. A uniform trace: R = ρL/(w·t) between the pad edges, J = I/(w·t).
func TestValidateTraceResistance(t *testing.T) {
	b := newTB(1400, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1200, 150, 10, 10))
	b.line("VIN", 1, 200, 150, 1200, 150, 10)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.0}, {"L", "1", "sink", 1.0}}}, nil, nil)
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	n := netOf(t, r, "VIN")
	tM, wM := OzMm*1e-3, 10*MilMm*1e-3
	R := RhoCu * (990 * MilMm * 1e-3) / (wM * tM) // 1000 mil minus half of each 10 mil pad
	near(t, "trace drop (mV)", n.WorstMV, R*1000, 0.005)
	near(t, "current density (A/mm²)", n.MaxJAmm2, 1/(10*MilMm*OzMm), 0.005)
	near(t, "copper loss (mW)", n.LossMW, R*1000, 0.005)
}

// 2. A rectangular plate between two full-width contacts: R = Rs·L/W.
func TestValidatePlateResistance(t *testing.T) {
	b := newTB(1200, 700, 2)
	b.rectFill("VIN", 1, 100, 100, 1100, 600)
	b.part("S", 1, pad("1", "VIN", 1, 110, 350, 20, 500))
	b.part("L", 1, pad("1", "VIN", 1, 1090, 350, 20, 500))
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 10.0}, {"L", "1", "sink", 10.0}}}, nil, nil)
	o := DefaultOptions()
	o.CellMm = 0.25
	r, err := Run(b.json(), sim, o)
	if err != nil {
		t.Fatal(err)
	}
	n := netOf(t, r, "VIN")
	Rs := RhoCu / (OzMm * 1e-3)
	R := Rs * 960 / 500 // between the pad edges
	near(t, "plate drop (mV)", n.WorstMV, 10*R*1000, 0.03)
}

// 3. A two-layer via chain: track, via, bottom track, via, track.
func TestValidateViaChain(t *testing.T) {
	b := newTB(2000, 300, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 150, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1800, 150, 10, 10))
	b.line("VIN", 1, 200, 150, 700, 150, 10)
	b.via("VIN", 700, 150, 24, 12)
	b.line("VIN", 2, 700, 150, 1300, 150, 10)
	b.via("VIN", 1300, 150, 24, 12)
	b.line("VIN", 1, 1300, 150, 1800, 150, 10)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.0}, {"L", "1", "sink", 1.0}}}, nil, nil)
	o := DefaultOptions()
	r, err := Run(b.json(), sim, o)
	if err != nil {
		t.Fatal(err)
	}
	n := netOf(t, r, "VIN")
	tM, wM := OzMm*1e-3, 10*MilMm*1e-3
	track := func(l float64) float64 { return RhoCu * l * MilMm * 1e-3 / (wM * tM) }
	// top legs: pad edge (5) to via ring (12) → 500−5−12; bottom: ring to ring
	st := BuildStackup(StackLayers(2), StackOptions{})
	d, pl := 12*MilMm*1e-3, o.PlatingMil*MilMm*1e-3
	via := RhoCu * st.spanM(0, 1) / (math.Pi * (d + pl) * pl)
	R := track(500-5-12)*2 + track(600-24) + 2*via
	near(t, "via chain drop (mV)", n.WorstMV, R*1000, 0.005)
	if len(r.Vias) != 2 || math.Abs(r.Vias[0].CurrentA-1) > 1e-6 {
		t.Errorf("via currents %+v, want 1 A each", r.Vias)
	}
}

// thermalPlate is a board fully covered by top copper with one heated pad.
func thermalPlate(w, h float64, heatX, heatY, padMil, q float64) ([]byte, []byte) {
	b := newTB(w, h, 2)
	b.rectFill("HS", 1, 0, 0, w, h)
	b.part("H", 1, pad("1", "HS", 1, heatX, heatY, padMil, padMil))
	sim := simDoc(map[string][][4]any{}, nil, map[string]float64{"H": q})
	return b.json(), sim
}

// kt is the lumped in-plane conductance of the 2-layer thermal model:
// copper k·t plus the full dielectric (half on each layer).
func ktPlate(st *Stackup, o Options) float64 {
	return KCu*st.cuM(0) + o.KFR4XY*st.DielMm[0]*1e-3
}

// 4. A 1-D fin: heat at one end of a long strip, convection on both faces,
// adiabatic tip: θ(x) = θ0·cosh(m(L−x))/cosh(mL), θ0 = Q/(kt·W·m·tanh mL).
func TestValidateThermalFin(t *testing.T) {
	L, W := 4000.0, 200.0 // mil
	bj, sim := thermalPlate(L, W, 0, 0, 0, 0)
	// heat over the first 20 mil across the full width
	var d map[string]any
	_ = json.Unmarshal(bj, &d)
	d["components"] = []any{map[string]any{"designator": "H", "layer": 1, "pads": []any{pad("1", "HS", 1, 10, 100, 20, 200)}}}
	bj, _ = json.Marshal(d)
	sim = simDoc(map[string][][4]any{}, nil, map[string]float64{"H": 0.2})
	o := DefaultOptions()
	o.CellMm = 0.254 // 10 mil: the heated strip is two cells
	r, err := Run(bj, sim, o)
	if err != nil {
		t.Fatal(err)
	}
	st := r.Stackup
	kt := ktPlate(st, o)
	hsum := o.HTop + o.HBottom
	m := math.Sqrt(hsum / kt)
	Lm, Wm := L*MilMm*1e-3, W*MilMm*1e-3
	theta := func(xm float64) float64 {
		th0 := 0.2 / (kt * Wm * m * math.Tanh(m*Lm))
		return th0 * math.Cosh(m*(Lm-xm)) / math.Cosh(m*Lm)
	}
	g := r.grid
	at := func(xmil float64) float64 {
		c := g.cellAt(Point{xmil, W / 2})
		return (r.TempMap[0][c] + r.TempMap[1][c]) / 2
	}
	for _, x := range []float64{10, 500, 1000, 2000} {
		near(t, "fin θ at x="+ftoa(x)+" mil (°C)", at(x)-o.AmbientC, theta(x*MilMm*1e-3), 0.03)
	}
	if math.Abs(r.Thermal.BalanceErrPct) > 0.01 {
		t.Errorf("energy balance %.4f %%", r.Thermal.BalanceErrPct)
	}
}

// 5. A point source on a large plate: θ(r) = Q/(2π·kt)·K0(m·r).
func TestValidateThermalPointSource(t *testing.T) {
	S := 7874.0 // 200 mm
	bj, sim := thermalPlate(S, S, S/2, S/2, 39.37, 1.0)
	o := DefaultOptions()
	o.CellMm = 1
	o.MaxCells = 50000
	r, err := Run(bj, sim, o)
	if err != nil {
		t.Fatal(err)
	}
	kt := ktPlate(r.Stackup, o)
	m := math.Sqrt((o.HTop + o.HBottom) / kt)
	g := r.grid
	for _, rmm := range []float64{5, 10, 20, 40} {
		c := g.cellAt(Point{S/2 + rmm/MilMm, S / 2})
		cc := g.cellCentre(c)
		rr := math.Hypot(cc.X-S/2, cc.Y-S/2) * MilMm * 1e-3
		want := 1.0 / (2 * math.Pi * kt) * besselK0(m*rr)
		got := (r.TempMap[0][c]+r.TempMap[1][c])/2 - o.AmbientC
		near(t, "point source θ at r="+ftoa(rmm)+" mm (°C)", got, want, 0.06)
	}
}

// 6. Energy balance on a mixed board with Joule heat.
func TestValidateEnergyBalance(t *testing.T) {
	b := newTB(1400, 600, 2)
	b.part("S", 1, pad("1", "VIN", 1, 200, 300, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 1200, 300, 10, 10), pad("2", "GND", 1, 1200, 450, 40, 40))
	b.line("VIN", 1, 200, 300, 1200, 300, 8)
	b.rectFill("GND", 2, 0, 0, 1400, 600)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 2.0}, {"L", "1", "sink", 2.0}}}, nil, map[string]float64{"L": 0.5})
	r, err := Run(b.json(), sim, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	th := r.Thermal
	if th.JouleW <= 0 {
		t.Fatalf("no Joule heat")
	}
	near(t, "Joule heat = I²R (W)", th.JouleW, netOf(t, r, "VIN").LossMW/1000, 0.001)
	if math.Abs(th.LossW-th.TotalW) > 0.01*th.TotalW {
		t.Errorf("Σ convective loss %.6f W vs Σ power %.6f W", th.LossW, th.TotalW)
	}
	t.Logf("energy balance: in %.6f W out %.6f W (%.5f %%)", th.TotalW, th.LossW, th.BalanceErrPct)
}

// 7. IPC-2152-like: a 10 mil 1 oz trace at 1 A on a bare 3 × 5 in FR-4
// board. IPC-2221 external: ΔT = (I/(0.048·A^0.725))^(1/0.44) ≈ 13 °C for
// A = 13.8 mil²; IPC-2152 charts give the same order for a board without
// planes. The model must land in that order (5–40 °C), not match a chart.
func TestValidateIPCTraceRise(t *testing.T) {
	b := newTB(5000, 3000, 2)
	b.part("S", 1, pad("1", "VIN", 1, 500, 1500, 10, 10))
	b.part("L", 1, pad("1", "VIN", 1, 4500, 1500, 10, 10))
	b.line("VIN", 1, 500, 1500, 4500, 1500, 10)
	sim := simDoc(map[string][][4]any{"VIN": {{"S", "1", "source", 1.0}, {"L", "1", "sink", 1.0}}}, nil, nil)
	o := DefaultOptions()
	o.MaxCells = 150000
	r, err := Run(b.json(), sim, o)
	if err != nil {
		t.Fatal(err)
	}
	rise := r.Thermal.MaxBoardC - o.AmbientC
	ipc2221 := math.Pow(1/(0.048*math.Pow(10*1.378, 0.725)), 1/0.44)
	t.Logf("10 mil / 1 oz / 1 A over 4 in: model rise %.1f °C (Joule %.3f W); IPC-2221 external %.1f °C", rise, r.Thermal.JouleW, ipc2221)
	if rise < 5 || rise > 40 {
		t.Errorf("trace rise %.1f °C is not of the IPC order (5–40 °C)", rise)
	}
}

func ftoa(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// besselK0 is the modified Bessel function K0 (Abramowitz & Stegun 9.8.5–6).
func besselK0(x float64) float64 {
	if x <= 2 {
		t := x / 3.75
		t2 := t * t
		i0 := 1 + t2*(3.5156229+t2*(3.0899424+t2*(1.2067492+t2*(0.2659732+t2*(0.0360768+t2*0.0045813)))))
		y := x * x / 4
		return -math.Log(x/2)*i0 + (-0.57721566 + y*(0.42278420+y*(0.23069756+y*(0.03488590+y*(0.00262698+y*(0.00010750+y*0.0000074))))))
	}
	y := 2 / x
	return math.Exp(-x) / math.Sqrt(x) * (1.25331414 + y*(-0.07832358+y*(0.02189568+y*(-0.01062446+y*(0.00587872+y*(-0.00251540+y*0.00053208))))))
}
