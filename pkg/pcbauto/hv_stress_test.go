package pcbauto

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// Regressions from the high-voltage stress suite (testdata/stress/hv,
// `make stress-hv`), on small synthetic boards.

func TestClassifyHVParts(t *testing.T) {
	for dev, want := range map[string]PartKind{
		"UCC21520DWR": KindIsolator, "UCC5350MCDR": KindIsolator, "SI8233BD-D-IS": KindIsolator, "1EDI60N12AF": KindIsolator,
		"STGAP2SICS": KindIsolator, "AMC1311DWVR": KindIsolator, "ISO224BDWVR": KindIsolator, "ISO7741DWR": KindIsolator,
		"MGJ2D051505SC": KindIsoPower, "UCC12050DVER": KindIsoPower, "NXE1S0505MC": KindIsoPower, "R05P05S": KindIsoPower,
		"UC3843BD1R2G": KindIC,
	} {
		if got := ClassifyPart(&Part{Ref: "U1", Device: dev, Pads: make([]*Pad, 8)}); got != want {
			t.Errorf("%s: %s, want %s", dev, got, want)
		}
	}
	for net, want := range map[string]NetRole{"DRAIN": RoleSwitch, "PHASE": RoleSwitch, "Q1_DRAIN": RoleSwitch, "AIN0": RoleAnalog,
		"ADC_AIN1": RoleAnalog, "MAIN_EN": RoleSignal, "GAIN_SEL": RoleSignal} {
		if got := classify(net, 2); got != want {
			t.Errorf("classify(%s) = %s, want %s", net, got, want)
		}
	}
}

// hvBoard builds a board from compact part specs (mil, y-up).
func hvBoard(parts ...*Part) *Board {
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 3000, 2000}.Corners(), CopperLayers: 2}
	b.Parts = parts
	if err := b.Index(); err != nil {
		panic(err)
	}
	return b
}

func hvPart(ref, dev string, at Point, pads ...*Pad) *Part {
	for _, pd := range pads {
		pd.Box.C = pd.Box.C.Add(at)
	}
	return &Part{Ref: ref, Device: dev, Pos: at, Pads: pads}
}

func smdPad(n, net string, x, y, w, h float64) *Pad {
	return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: w, H: h}}
}

func thtPad(n, net string, x, y, d float64) *Pad {
	return &Pad{Number: n, Net: net, Layer: LayerMulti, Box: OrientedBox{C: Point{x, y}, W: d, H: d, Round: true}}
}

// An off-line converter's primary ground is tied to the line through the
// bridge rectifier: one hazardous domain, and the only barrier is the
// transformer to the secondary (it used to be MAINS | PGND | GND_S with a
// "barrier" between the line and its own rectified bulk).
func TestMainsTiedPrimaryIsOneDomain(t *testing.T) {
	b := hvBoard(
		hvPart("J1", "TERM", Point{200, 1000}, thtPad("1", "L", 0, 100, 80), thtPad("2", "N", 0, -100, 80)),
		hvPart("BR1", "MB10S", Point{600, 1000}, smdPad("1", "HV_BULK", 100, 50, 60, 40), smdPad("2", "PGND", 100, -50, 60, 40),
			smdPad("3", "L", -100, 50, 60, 40), smdPad("4", "N", -100, -50, 60, 40)),
		hvPart("C2", "22uF", Point{900, 1000}, thtPad("1", "HV_BULK", 0, 100, 80), thtPad("2", "PGND", 0, -100, 80)),
		hvPart("T1", "EE25", Point{1500, 1000}, thtPad("1", "HV_BULK", -400, 200, 90), thtPad("2", "DRAIN", -400, 0, 90),
			thtPad("6", "SEC", 400, 200, 90), thtPad("8", "GND_S", 400, 0, 90)),
		hvPart("D3", "SS310", Point{2200, 1100}, smdPad("1", "VOUT", 85, 0, 85, 95), smdPad("2", "SEC", -85, 0, 85, 95)),
		hvPart("C9", "680uF", Point{2500, 1000}, thtPad("1", "VOUT", 0, 70, 60), thtPad("2", "GND_S", 0, -70, 60)),
	)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	if c.DomainOf["BR1"] != c.DomainOf["C2"] || c.DomainOf["J1"] != c.DomainOf["C2"] || c.DomainOf["C2"] == c.DomainOf["C9"] {
		t.Fatalf("domains %v", c.DomainOf)
	}
	if len(c.Barriers) != 1 || !containsStr(c.Barriers[0].Bridges, "T1") || c.Barriers[0].Insulation != "reinforced" {
		t.Fatalf("barriers %+v", c.Barriers)
	}
	for _, d := range c.Domains {
		if d.ID == "MAINS" {
			t.Fatalf("separate MAINS domain left: %+v", d)
		}
	}
}

// A 1206 between two high-voltage nets has its pads 1.8 mm apart, less than
// either net's IPC clearance: without the footprint relief both pads were
// "pad-inaccessible"; with it the copper routes and never comes closer to the
// neighbour pad than the footprint does. A pad of ANOTHER part keeps the full
// clearance.
func TestHVFootprintRelief(t *testing.T) {
	b := hvBoard(
		hvPart("J1", "TERM", Point{300, 1000}, thtPad("1", "HVA", 0, 300, 100), thtPad("2", "HVB", 0, -300, 100)),
		hvPart("R1", "1206", Point{1500, 1000}, smdPad("1", "HVA", -58, 0, 45, 71), smdPad("2", "HVB", 58, 0, 45, 71)),
		hvPart("R2", "1206", Point{2400, 1000}, smdPad("1", "HVB", -58, 0, 45, 71), smdPad("2", "HVA", 58, 0, 45, 71)),
	)
	power := PowerSpec{Rails: []PowerRail{{Net: "HVA", Voltage: 400, CurrentA: 0.01}, {Net: "HVB", Voltage: 400, CurrentA: 0.01}}}
	an := Analyze(b, power, nil)
	if an.ByNet["HVA"].ClearanceMil < 98 {
		t.Fatalf("HVA clearance %.1f", an.ByNet["HVA"].ClearanceMil)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Power: power, Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Route.Stats.Completion < 100 {
		t.Fatalf("completion %.1f%%, unrouted %+v", res.Route.Stats.Completion, res.Route.Unrouted)
	}
	if len(res.DRC.Violations) != 0 {
		t.Fatalf("DRC %+v", res.DRC.Violations)
	}
	// No copper closer to a pad of another net than the footprint gap (71 mil).
	for _, tr := range res.Route.Tracks {
		for _, p := range b.Parts {
			for _, pd := range p.Pads {
				if pd.Net == tr.Net || !pd.OnLayer(tr.Layer) {
					continue
				}
				if d := pd.Box.SegDist(tr.A, tr.B) - tr.Width/2; d < 71-0.5 {
					t.Fatalf("%s track %.1f mil from %s.%s", tr.Net, d, p.Ref, pd.Number)
				}
			}
		}
	}
	// A foreign part's pad does not get the relief.
	bad := []Track{{Net: "HVA", Layer: LayerTop, Width: 10, A: Point{1650, 1000}, B: Point{1650, 1300}}}
	drc := CheckDRC(b, an, &Stackup{Layers: 2}, bad, nil)
	found := false
	for _, v := range drc.Violations {
		found = found || v.NetB == "HVB" || v.NetA == "HVB"
	}
	if !found {
		t.Fatalf("track 57 mil from R1's HVB pad but 92 mil from the part it leaves not flagged: %+v", drc.Violations)
	}
}

// Divider chain: two nodes of one domain need IPC-2221B at their
// DIFFERENCE, not at the higher node's absolute voltage.
func TestPairClearanceDeltaV(t *testing.T) {
	b := hvBoard(
		hvPart("R1", "1206", Point{1000, 1000}, smdPad("1", "VIN", -58, 0, 45, 71), smdPad("2", "N1", 58, 0, 45, 71)),
		hvPart("R2", "1206", Point{1000, 800}, smdPad("1", "N1", 58, 0, 45, 71), smdPad("2", "N2", -58, 0, 45, 71)),
		hvPart("R3", "1206", Point{1000, 600}, smdPad("1", "N2", -58, 0, 45, 71), smdPad("2", "GND_M", 58, 0, 45, 71)),
	)
	in := &Intent{Domains: []IntentDomain{{ID: "M", Kind: "mains", Nets: []string{"VIN", "N1", "N2", "GND_M"}, WorkingVrms: 600, WorkingVpeak: 848}},
		Nets: map[string]*IntentNet{
			"VIN":   {Domain: "M", Voltage: IntentVoltage{Nom: 848, Min: 848, Max: 848, Peak: 848}, ClearanceMil: 167.1},
			"N1":    {Domain: "M", Voltage: IntentVoltage{Nom: 707, Min: 0, Max: 707, Peak: 707}, ClearanceMil: 139.2},
			"N2":    {Domain: "M", Voltage: IntentVoltage{Nom: 566, Min: 0, Max: 566, Peak: 566}, ClearanceMil: 111.4},
			"GND_M": {Domain: "M", Role: "ground"},
		}}
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	for _, c := range []struct {
		a, b string
		want float64
	}{{"VIN", "N1", 0.6 / 0.0254}, {"VIN", "N2", 1.25 / 0.0254}, {"N1", "N2", 0.6 / 0.0254}, {"VIN", "GND_M", 167.1}} {
		if got := an.PairClearanceMil(c.a, c.b, b.Rules); math.Abs(got-c.want) > 0.2 {
			t.Errorf("%s|%s = %.1f mil, want %.1f", c.a, c.b, got, c.want)
		}
	}
	// A swinging net (switch node 0…650 V) is judged over its whole span.
	in.Nets["N2"].Voltage = IntentVoltage{Nom: 325, Min: 0, Max: 325, Peak: 650}
	an = Analyze(b, PowerSpec{Intent: in}, nil)
	if got := an.PairClearanceMil("VIN", "N2", b.Rules); got < 166.5 {
		t.Errorf("VIN|switching N2 = %.1f, want ≈ IPC-2221B at 848 V (166.9)", got)
	}
}

// intentMainsSelv builds a reinforced mains ↔ SELV intent for a board.
func intentMainsSelv(mains, selv []string) *Intent {
	return &Intent{
		Standard: intentStandard(),
		Domains: []IntentDomain{{ID: "MAINS", Kind: "mains", Nets: mains, WorkingVrms: 325, WorkingVpeak: 325},
			{ID: "SELV", Kind: "SELV", Nets: selv, WorkingVrms: 5, WorkingVpeak: 5}},
		Pairs: []IntentPair{{A: "domain:MAINS", B: "domain:SELV", WorkingVrms: 325, WorkingVpeak: 325, Insulation: "reinforced", Transient: "mains", MainsVrms: 230}},
	}
}

// A 1206 "Y capacitor" across a reinforced barrier (pads 1.8 mm < 3.0 mm
// clearance) is infeasible — reported by the planner, the run and the offline
// check — while an EE10 whose rows are only short of the creepage gets an
// axis-aligned slot even with its pins used asymmetrically.
func TestInfeasibleBridgeAndSlotAxis(t *testing.T) {
	b := hvBoard(
		hvPart("C2", "1206Y", Point{1000, 800}, smdPad("1", "PGND", -58, 0, 45, 71), smdPad("2", "GND_S", 58, 0, 45, 71)),
		hvPart("T1", "EE10", Point{1000, 1300}, thtPad("1", "HV_BULK", -148, 98, 55), thtPad("2", "PGND", -148, 0, 55), thtPad("3", "", -148, -98, 55),
			thtPad("4", "VOUT", 148, -98, 55), thtPad("5", "", 148, 0, 55), thtPad("6", "GND_S", 148, 98, 55)),
	)
	in := intentMainsSelv([]string{"PGND", "HV_BULK"}, []string{"GND_S", "VOUT"})
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	slots, _, bad := PlanIsoSlotsDetail(b, an.Iso)
	if len(bad) != 1 || bad[0].Ref != "C2" || !strings.Contains(bad[0].Reason, "air path") {
		t.Fatalf("infeasible %+v", bad)
	}
	if len(slots) != 1 || slots[0].Ref != "T1" {
		t.Fatalf("slots %+v", slots)
	}
	s := slots[0]
	bb := PolyBounds(s.Poly)
	if math.Abs(bb.W()-s.WidthMil) > 0.5 || math.Abs(bb.H()-s.LengthMil) > 0.5 {
		t.Fatalf("slot not axis-aligned: bounds %.1f × %.1f for %.1f × %.1f", bb.W(), bb.H(), s.WidthMil, s.LengthMil)
	}
	if s.LengthMil < 330 || s.LengthMil > 355 {
		t.Fatalf("slot length %.1f, want ≈ 346 (rows 252 mil + 2 × 46)", s.LengthMil)
	}
	// The planned slot is a record of THIS placement: moving the bridge
	// afterwards (the next place/route loop pass) must not drag it along —
	// the loop restores the best pass's poses and emits its slots (flyback
	// E2E: the playbook's slot landed 500 mil from the opto).
	before := PolyBounds(s.Poly)
	b.Part("T1").MoveTo(Point{2000, 400}, 90)
	if after := PolyBounds(slots[0].Poly); after != before {
		t.Fatalf("planned slot moved with its part: %+v → %+v", before, after)
	}
	b.Part("T1").MoveTo(Point{1000, 1300}, 0)
	// Run + routed snapshot + offline check agree.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Power: PowerSpec{Intent: in}, Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Isolation.Infeasible) != 1 {
		t.Fatalf("run infeasible %+v", res.Isolation.Infeasible)
	}
	raw, _ := json.Marshal(boardDump(b))
	routed, err := ExportRoutedSnapshot(raw, b, res)
	if err != nil {
		t.Fatal(err)
	}
	chk, err := CheckIsolationSnapshot(routed, in)
	if err != nil {
		t.Fatal(err)
	}
	if len(chk.Infeasible) != 1 || chk.Cutouts != 1 || chk.Tracks != len(res.Route.Tracks) {
		t.Fatalf("offline check: infeasible %d cutouts %d tracks %d/%d", len(chk.Infeasible), chk.Cutouts, chk.Tracks, len(res.Route.Tracks))
	}
	clr := false
	for _, f := range chk.Findings {
		clr = clr || f.Kind == "iso-clearance" && strings.HasPrefix(f.ItemA, "C2.") && strings.HasPrefix(f.ItemB, "C2.")
	}
	if !clr {
		t.Fatalf("C2 pad-to-pad clearance not flagged: %+v", chk.Findings)
	}
}

// boardDump writes a minimal `pcb dump` of a synthetic board.
func boardDump(b *Board) map[string]any {
	var comps []any
	for _, p := range b.Parts {
		var pads []any
		for _, pd := range p.Pads {
			shape := "RECT"
			if pd.Box.Round {
				shape = "ELLIPSE"
			}
			pads = append(pads, map[string]any{"padNumber": pd.Number, "net": pd.Net, "layer": pd.Layer, "x": pd.Box.C.X, "y": pd.Box.C.Y,
				"width": pd.Box.W, "height": pd.Box.H, "shape": []any{shape, pd.Box.W, pd.Box.H}})
		}
		comps = append(comps, map[string]any{"primitiveId": "c-" + p.Ref, "designator": p.Ref, "device": p.Device, "layer": 1, "x": p.Pos.X, "y": p.Pos.Y, "pads": pads})
	}
	bb := PolyBounds(b.Outline)
	return map[string]any{"components": comps, "copperLayers": b.CopperLayers,
		"outline": map[string]any{"bbox": map[string]any{"minX": bb.MinX, "minY": bb.MinY, "maxX": bb.MaxX, "maxY": bb.MaxY}},
		"rules":   map[string]any{"clearanceMil": b.Rules.Clearance, "trackWidthMil": b.Rules.TrackWidth, "viaDrillMil": b.Rules.ViaDrill, "viaDiameterMil": b.Rules.ViaDia}}
}

// Declaring only a rail's voltage (or ripple) keeps its simulated / heuristic
// current: "VOUT 12 V" once zeroed the 2 A output to a 10 mil track.
func TestVoltageOnlyRailKeepsCurrent(t *testing.T) {
	b := hvBoard(hvPart("J2", "TERM", Point{500, 500}, thtPad("1", "VOUT", 0, 100, 80), thtPad("2", "GND", 0, -100, 80)),
		hvPart("C1", "100uF", Point{900, 500}, thtPad("1", "VOUT", 0, 50, 60), thtPad("2", "GND", 0, -50, 60)))
	sim := &SimPower{Scenario: "worst", Nets: map[string]*SimNet{"VOUT": {CurrentA: 2, Role: "power"}}}
	an := Analyze(b, PowerSpec{Sim: sim, Rails: []PowerRail{{Net: "VOUT", Voltage: 12}}}, nil)
	if p := an.ByNet["VOUT"]; p.CurrentA < 1.99 || p.WidthMil < 30 || p.Voltage != 12 {
		t.Fatalf("VOUT plan %+v", p)
	}
}

// intentStandard is IEC 62368-1 reinforced, PD2, MG IIIa, ≤ 2000 m, OVC II.
func intentStandard() safety.Standard {
	return safety.Standard{Name: "IEC62368-1", Insulation: "reinforced", PollutionDegree: 2, MaterialGroup: "IIIa", AltitudeM: 2000, OvervoltageCategory: "II"}
}

// The optional re-route passes only start when the context leaves room for
// a full pass (they ran HV boards past the command deadline).
func TestTimeLeft(t *testing.T) {
	if !timeLeft(context.Background(), time.Minute) {
		t.Fatal("no deadline must leave time")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if timeLeft(ctx, time.Minute) || !timeLeft(ctx, 10*time.Second) {
		t.Fatal("deadline arithmetic")
	}
}

// On a 4-layer board the SELV ground plane used to cover the whole board —
// under the mains section, 0.2 mm of prepreg from line copper. Planes and
// pours of an insulated domain are clipped to it and the isolation check now
// measures them.
func TestPlanesClippedToDomain(t *testing.T) {
	b := mainsSelvBoard()
	in := loadIntent(t, "iso-mains-selv.intent.json")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	res, err := Run(ctx, b, Options{Power: PowerSpec{Intent: in}, Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	clipped := false
	for _, n := range res.Isolation.Notes {
		clipped = clipped || strings.Contains(n, "GND on layer") && strings.Contains(n, "clipped")
	}
	if !clipped {
		t.Fatalf("GND plane not clipped: %v", res.Isolation.Notes)
	}
	for _, f := range res.Isolation.Findings {
		if strings.HasPrefix(f.ItemA, "pour#") {
			t.Fatalf("pour finding: %s", f.Message)
		}
	}
	// No GND plane cell over the mains terminal.
	j1 := b.Part("J1").Pads[0].Box.C
	for _, pr := range res.Route.Planes {
		if pr.Net != "GND" {
			continue
		}
		for _, poly := range pr.Polys {
			if PolyContains(poly, j1) {
				t.Fatalf("GND plane on layer %d covers the mains terminal", pr.Layer)
			}
		}
	}
}

// Gate-drive nets riding on the switch node are compared relative to it:
// VDDA–KS_H is 15 V, KS_H–HV_GND is the full swing.
func TestPairClearanceFloatingIsland(t *testing.T) {
	b := hvBoard(
		hvPart("C3", "1206", Point{1000, 1000}, smdPad("1", "VDDA", -58, 0, 45, 71), smdPad("2", "KS_H", 58, 0, 45, 71)),
		hvPart("Q2", "TO247", Point{1500, 1000}, thtPad("1", "PHASE", 0, 0, 72), thtPad("2", "HV_GND", 100, 0, 72)),
	)
	in := &Intent{Domains: []IntentDomain{{ID: "H", Kind: "hazardous", Nets: []string{"VDDA", "KS_H", "PHASE", "HV_GND"}}},
		Nets: map[string]*IntentNet{
			"PHASE":  {Domain: "H", Voltage: IntentVoltage{Nom: 0, Min: 0, Max: 450, Peak: 520}, ClearanceMil: 102.4},
			"KS_H":   {Domain: "H", Voltage: IntentVoltage{Min: 0, Max: 450, Peak: 520}, ClearanceMil: 102.4, FloatsOn: "PHASE", RelVoltage: &IntentVoltage{}},
			"VDDA":   {Domain: "H", Voltage: IntentVoltage{Nom: 15, Min: 15, Max: 465, Peak: 535}, ClearanceMil: 105.4, FloatsOn: "PHASE", RelVoltage: &IntentVoltage{Nom: 15, Min: 15, Max: 15, Peak: 15}},
			"HV_GND": {Domain: "H", Role: "ground"},
		}}
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	if got := an.PairClearanceMil("VDDA", "KS_H", b.Rules); got > 6.1 {
		t.Errorf("VDDA|KS_H = %.1f mil, want the board rule (15 V apart)", got)
	}
	if got := an.PairClearanceMil("VDDA", "PHASE", b.Rules); got > 6.1 {
		t.Errorf("VDDA|PHASE = %.1f mil, want the board rule", got)
	}
	if got := an.PairClearanceMil("KS_H", "HV_GND", b.Rules); got < 102 {
		t.Errorf("KS_H|HV_GND = %.1f mil, want the full 520 V swing", got)
	}
}

// A 6 mil intent class over a 5.98 mil live rule is rounding, not high
// voltage: the HV machinery stays off (it re-routed the ESP32 mini).
func TestHVMachineryNeedsRealExcess(t *testing.T) {
	b := hvBoard(hvPart("R1", "1206", Point{1000, 1000}, smdPad("1", "A", -58, 0, 45, 71), smdPad("2", "B", 58, 0, 45, 71)))
	b.Rules.Clearance = 5.98
	in := &Intent{Domains: []IntentDomain{{ID: "S", Kind: "SELV", Nets: []string{"A", "B"}}},
		Nets: map[string]*IntentNet{"A": {Domain: "S", ClearanceMil: 6, Voltage: IntentVoltage{Max: 5, Nom: 5, Peak: 5}}, "B": {Domain: "S", ClearanceMil: 6}}}
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	if got := an.PairClearanceMil("A", "B", b.Rules); got != 6 {
		t.Fatalf("pair clearance %.2f, want the plain 6", got)
	}
	pl := &placer{b: b, an: an}
	if pl.hvPadCost(b.Parts[0], b.Parts[0].Body()) != 0 {
		t.Fatal("HV pad cost on a SELV board")
	}
}
