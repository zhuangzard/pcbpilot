package pcbauto

import (
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// railBoard builds a 2-layer board whose parts each have one pad on net
// "+3V3" (and one on GND), at the given x positions along y = 500.
func railBoard(xs []float64, refs []string) *Board {
	b := &Board{Rules: DefaultRules(), Outline: Rect{-200, 0, 6000, 1000}.Corners(), CopperLayers: 2}
	b.Rules.TrackWidth = 10
	for i, x := range xs {
		b.Parts = append(b.Parts, &Part{Ref: refs[i], Device: "X", Pos: Point{x, 500}, Pads: []*Pad{
			{Number: "1", Net: "+3V3", Layer: LayerTop, Box: OrientedBox{C: Point{x, 500}, W: 40, H: 60}},
			{Number: "2", Net: "GND", Layer: LayerTop, Box: OrientedBox{C: Point{x, 700}, W: 40, H: 60}},
		}})
	}
	_ = b.Index()
	return b
}

func simFor(pins []SimPin) *SimPower {
	f := &SimFile{SchemaVersion: 1, Results: []SimResult{{Scenario: "worst", Nets: map[string]SimNet{
		"+3V3": {Voltage: 3.3, Role: "power", Pins: pins},
	}}}}
	return f.Resolve()
}

func twoLayer() *Stackup {
	return &Stackup{Layers: 2, Stack: []StackLayer{{ID: LayerTop, Name: "TOP", Kind: KindSignal, Outer: true}, {ID: LayerBottom, Name: "BOTTOM", Kind: KindSignal, Outer: true}}}
}

func track(a, b Point, w float64) Track {
	return Track{Net: "+3V3", Layer: LayerTop, A: a, B: b, Width: w, Kind: "route"}
}

func netRes(t *testing.T, rp *IRReport, net string) *IRNet {
	t.Helper()
	for _, n := range rp.Nets {
		if n.Net == net {
			return n
		}
	}
	t.Fatalf("no IR result for %s", net)
	return nil
}

func padDrop(t *testing.T, n *IRNet, pad string) float64 {
	t.Helper()
	for _, p := range n.Pads {
		if p.Pad == pad {
			return p.DropMV
		}
	}
	t.Fatalf("no pad %s", pad)
	return 0
}

func near(a, b, rel float64) bool { return math.Abs(a-b) <= rel*math.Max(math.Abs(a), math.Abs(b)) }

func TestIRSingleTrackMatchesOhm(t *testing.T) {
	b := railBoard([]float64{0, 1000}, []string{"S", "L"})
	an := Analyze(b, PowerSpec{Sim: simFor([]SimPin{{Ref: "S", Pin: "1", CurrentA: 0.5, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 0.5, Dir: "sink"}})}, twoLayer())
	rr := &RouteResult{Tracks: []Track{track(Point{0, 500}, Point{1000, 500}, 10)}}
	rp := powerIntegrity(b, an, twoLayer(), rr)
	n := netRes(t, rp, "+3V3")
	want := 0.5 * trackOhm(1000, 10, 1) * 1000 // mV
	if got := padDrop(t, n, "L.1"); !near(got, want, 1e-6) {
		t.Fatalf("drop %.4f mV, want %.4f", got, want)
	}
	// 1000 mil of 10 mil 1 oz copper is ≈ 49 mΩ: sanity of the units.
	if r := trackOhm(1000, 10, 1); r < 0.045 || r > 0.052 {
		t.Fatalf("10 mil × 1 in 1 oz = %.4f Ω", r)
	}
	if n.Reference != "S" || n.Status != "ok" {
		t.Fatalf("ref %s status %s", n.Reference, n.Status)
	}
}

func TestIRParallelTracksHalve(t *testing.T) {
	b := railBoard([]float64{0, 1000}, []string{"S", "L"})
	an := Analyze(b, PowerSpec{Sim: simFor([]SimPin{{Ref: "S", Pin: "1", CurrentA: 0.4, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 0.4, Dir: "sink"}})}, twoLayer())
	rr := &RouteResult{Tracks: []Track{
		track(Point{0, 490}, Point{1000, 490}, 10),
		track(Point{0, 510}, Point{1000, 510}, 10),
	}}
	n := netRes(t, powerIntegrity(b, an, twoLayer(), rr), "+3V3")
	want := 0.4 * trackOhm(1000, 10, 1) / 2 * 1000
	if got := padDrop(t, n, "L.1"); !near(got, want, 1e-6) {
		t.Fatalf("drop %.4f mV, want %.4f", got, want)
	}
}

// A daisy chain (bus ladder) of n equal segments with a load I at every
// node: segment k carries (n−k+1)·I and the far end drops R·I·n(n+1)/2.
// The routed 60 mil bus tapers segment by segment.
func TestIRDaisyChainTapers(t *testing.T) {
	const n, I, seg = 4, 0.5, 800.0
	xs := []float64{0}
	refs := []string{"S"}
	pins := []SimPin{{Ref: "S", Pin: "1", CurrentA: n * I, Dir: "source"}}
	var ts []Track
	for k := 1; k <= n; k++ {
		ref := "L" + string(rune('0'+k))
		xs = append(xs, float64(k)*seg)
		refs = append(refs, ref)
		pins = append(pins, SimPin{Ref: ref, Pin: "1", CurrentA: I, Dir: "sink"})
		ts = append(ts, track(Point{float64(k-1) * seg, 500}, Point{float64(k) * seg, 500}, 60))
	}
	b := railBoard(xs, refs)
	an := Analyze(b, PowerSpec{Sim: simFor(pins), IRBudget: &IRBudget{MV: 1000}}, twoLayer())
	rr := &RouteResult{Tracks: ts}
	rp := powerIntegrity(b, an, twoLayer(), rr)
	res := netRes(t, rp, "+3V3")
	if res.Narrowed != n {
		t.Fatalf("narrowed %d of %d segments", res.Narrowed, n)
	}
	// Segment currents and widths, trunk first.
	var want float64
	for k, s := range res.Segments {
		ia := float64(n-k) * I
		if !near(s.CurrentA, ia, 1e-6) {
			t.Fatalf("segment %d carries %.4f A, want %.4f", k, s.CurrentA, ia)
		}
		w := branchWidth(ia, 10, 1, 10)
		if !near(s.WidthMil, w, 1e-9) {
			t.Fatalf("segment %d width %.2f, want %.2f", k, s.WidthMil, w)
		}
		if k > 0 && s.WidthMil > res.Segments[k-1].WidthMil {
			t.Fatalf("width grows downstream: %v", res.Segments)
		}
		want += ia * trackOhm(seg, w, 1)
	}
	if res.Segments[n-1].WidthMil != 10 {
		t.Fatalf("0.5 A tail should sit at the 10 mil class floor, got %.2f", res.Segments[n-1].WidthMil)
	}
	if got := padDrop(t, res, "L4.1"); !near(got, want*1000, 1e-6) {
		t.Fatalf("far-end drop %.4f mV, want %.4f", got, want*1000)
	}
	// The emitted tracks carry the tapered widths.
	for k, tr := range rr.Tracks {
		if tr.Width != res.Segments[k].WidthMil {
			t.Fatalf("route track %d width %.2f not written back (%.2f)", k, tr.Width, res.Segments[k].WidthMil)
		}
	}
	// Untapered analytic ladder: R·I·n(n+1)/2.
	b2 := railBoard(xs, refs)
	an2 := Analyze(b2, PowerSpec{Sim: simFor(pins)}, twoLayer())
	rr2 := &RouteResult{}
	for _, tr := range ts {
		tr.Width = 10
		rr2.Tracks = append(rr2.Tracks, tr)
	}
	r2 := netRes(t, powerIntegrity(b2, an2, twoLayer(), rr2), "+3V3")
	lad := trackOhm(seg, 10, 1) * I * n * (n + 1) / 2 * 1000
	if got := padDrop(t, r2, "L4.1"); !near(got, lad, 1e-6) {
		t.Fatalf("ladder drop %.4f mV, want %.4f", got, lad)
	}
}

// A star: every branch carries only its own load; a T-junction in the
// middle of the trunk splits it.
func TestIRStarAndTeeSplit(t *testing.T) {
	b := railBoard([]float64{0, 2000, 3000}, []string{"S", "A", "B"})
	// A sits off the bus: move its pad to (1000, 900).
	b.Part("A").Pads[0].Box.C = Point{1000, 900}
	pins := []SimPin{{Ref: "S", Pin: "1", CurrentA: 1.505, Dir: "source"}, {Ref: "A", Pin: "1", CurrentA: 0.005, Dir: "sink"}, {Ref: "B", Pin: "1", CurrentA: 1.5, Dir: "sink"}}
	an := Analyze(b, PowerSpec{Sim: simFor(pins)}, twoLayer())
	rr := &RouteResult{Tracks: []Track{
		track(Point{0, 500}, Point{3000, 500}, 40),    // trunk S → B
		track(Point{1000, 500}, Point{1000, 900}, 40), // tee to the pull-up at x=1000
	}}
	rp := powerIntegrity(b, an, twoLayer(), rr)
	res := netRes(t, rp, "+3V3")
	var trunk, branch []IRSegment
	for _, s := range res.Segments {
		if s.A.X == s.B.X {
			branch = append(branch, s)
		} else {
			trunk = append(trunk, s)
		}
	}
	if len(trunk) != 2 || len(branch) != 1 {
		t.Fatalf("trunk not split at the tee: %d trunk, %d branch", len(trunk), len(branch))
	}
	if !near(trunk[0].CurrentA, 1.505, 1e-6) || !near(trunk[1].CurrentA, 1.5, 1e-6) || !near(branch[0].CurrentA, 0.005, 1e-6) {
		t.Fatalf("currents %v %v", trunk, branch)
	}
	if branch[0].WidthMil != classMinWidth(RolePower, b.Rules) {
		t.Fatalf("5 mA pull-up branch width %.2f, want the class minimum", branch[0].WidthMil)
	}
	if trunk[0].WidthMil < 20 {
		t.Fatalf("1.5 A trunk tapered to %.2f", trunk[0].WidthMil)
	}
	// Written back: the trunk became two tracks of one width each.
	if len(rr.Tracks) != 2 && len(rr.Tracks) != 3 {
		t.Fatalf("tracks %d", len(rr.Tracks))
	}
}

// Two supplies feed one load from both ends in one scenario (KCL holds):
// the larger is the reference, the other injects its own current, each half
// carries its source's share.
func TestIRTwoSources(t *testing.T) {
	b := railBoard([]float64{0, 1000, 2000}, []string{"S1", "L", "S2"})
	pins := []SimPin{{Ref: "S1", Pin: "1", CurrentA: 0.6, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 1.0, Dir: "sink"}, {Ref: "S2", Pin: "1", CurrentA: 0.4, Dir: "source"}}
	an := Analyze(b, PowerSpec{Sim: simFor(pins)}, twoLayer())
	rr := &RouteResult{Tracks: []Track{track(Point{0, 500}, Point{1000, 500}, 10), track(Point{1000, 500}, Point{2000, 500}, 10)}}
	res := netRes(t, powerIntegrity(b, an, twoLayer(), rr), "+3V3")
	if res.Reference != "S1" {
		t.Fatalf("reference %s", res.Reference)
	}
	if !near(res.Segments[0].CurrentA, 0.6, 1e-6) || !near(res.Segments[1].CurrentA, 0.4, 1e-6) {
		t.Fatalf("segment currents %.3f / %.3f", res.Segments[0].CurrentA, res.Segments[1].CurrentA)
	}
}

// A merged worst-case file lists both OR-ing supplies at full current (no
// KCL): each is solved as the sole source and the maxima are kept, so both
// feeds are sized for the whole load and the drop is the worse of the two.
func TestIRDiodeOrEnvelope(t *testing.T) {
	b := railBoard([]float64{0, 1000, 3000}, []string{"D1", "U", "D2"})
	pins := []SimPin{{Ref: "D1", Pin: "1", CurrentA: 0.43, Dir: "source", Scenario: "terminal-only"}, {Ref: "U", Pin: "1", CurrentA: 0.43, Dir: "sink"},
		{Ref: "D2", Pin: "1", CurrentA: 0.43, Dir: "source", Scenario: "usb-only"}, {Ref: "C", Pin: "1", Dir: "pass"}}
	an := Analyze(b, PowerSpec{Sim: simFor(pins)}, twoLayer())
	rr := &RouteResult{Tracks: []Track{track(Point{0, 500}, Point{1000, 500}, 10), track(Point{1000, 500}, Point{3000, 500}, 10)}}
	res := netRes(t, powerIntegrity(b, an, twoLayer(), rr), "+3V3")
	if res.Reference != "D1|D2" && res.Reference != "D2|D1" {
		t.Fatalf("reference %s", res.Reference)
	}
	for k, s := range res.Segments {
		if !near(s.CurrentA, 0.43, 1e-6) {
			t.Fatalf("segment %d carries %.3f, want the full 0.43 A", k, s.CurrentA)
		}
	}
	far := 0.43 * trackOhm(2000, 10, 1) * 1000
	if got := padDrop(t, res, "U.1"); !near(got, far, 1e-6) || res.WorstRef != "D2" {
		t.Fatalf("drop %.3f mV from %s, want %.3f from D2", got, res.WorstRef, far)
	}
}

// A rectangular sheet of nx×ny cells between two bus bars is Rs·(nx−1)/ny:
// exercises the conjugate-gradient path (> 400 unknowns).
func TestIRSheetGridCG(t *testing.T) {
	const nx, ny = 40, 25
	rn := &resNet{}
	id := make([]int, nx*ny)
	for i := range id {
		id[i] = rn.node("c")
	}
	for y := 0; y < ny; y++ {
		for x := 0; x < nx; x++ {
			if x+1 < nx {
				rn.resistor(id[y*nx+x], id[y*nx+x+1], 0.001, -1, "plane")
			}
			if y+1 < ny {
				rn.resistor(id[y*nx+x], id[(y+1)*nx+x], 0.001, -1, "plane")
			}
		}
	}
	var refs []int
	for y := 0; y < ny; y++ {
		refs = append(refs, id[y*nx])
		rn.short(id[(ny-1)*nx+nx-1], id[y*nx+nx-1])
	}
	sol := rn.solve(refs, map[int]float64{id[nx-1]: 1.0})
	want := 0.001 * float64(nx-1) / float64(ny)
	if got := sol.drop[id[nx-1]]; !near(got, want, 1e-6) {
		t.Fatalf("sheet R %.6g, want %.6g", got, want)
	}
}

func TestIRBudgetParseAndDefault(t *testing.T) {
	d := DefaultIRBudget()
	if v := d.Volts(3.3); !near(v, 0.066, 1e-9) {
		t.Fatalf("3V3 budget %.4f", v)
	}
	if v := d.Volts(1.2); !near(v, 0.030, 1e-9) {
		t.Fatalf("1V2 budget %.4f", v)
	}
	if v := d.Volts(12); !near(v, 0.24, 1e-9) {
		t.Fatalf("12V budget %.4f", v)
	}
	b, err := ParseIRBudget("1%, 20mV")
	if err != nil || b.Pct != 1 || b.MV != 20 {
		t.Fatalf("%+v %v", b, err)
	}
	if b, err = ParseIRBudget("0.05V"); err != nil || b.MV != 50 {
		t.Fatalf("%+v %v", b, err)
	}
	if _, err = ParseIRBudget("fast"); err == nil {
		t.Fatal("garbage accepted")
	}
}

// A budget no routed width can meet: the tapered segments on the worst path
// widen back step by step to their routed width, then the net asks for a
// wider re-route — and the loop stops.
func TestIRWidenLoopTerminates(t *testing.T) {
	const n, I, seg = 4, 0.5, 800.0
	xs := []float64{0}
	refs := []string{"S"}
	pins := []SimPin{{Ref: "S", Pin: "1", CurrentA: n * I, Dir: "source"}}
	var ts []Track
	for k := 1; k <= n; k++ {
		ref := "L" + string(rune('0'+k))
		xs = append(xs, float64(k)*seg)
		refs = append(refs, ref)
		pins = append(pins, SimPin{Ref: ref, Pin: "1", CurrentA: I, Dir: "sink"})
		ts = append(ts, track(Point{float64(k-1) * seg, 500}, Point{float64(k) * seg, 500}, 60))
	}
	b := railBoard(xs, refs)
	an := Analyze(b, PowerSpec{Sim: simFor(pins), IRBudget: &IRBudget{MV: 1}}, twoLayer())
	rr := &RouteResult{Tracks: ts}
	rp := powerIntegrity(b, an, twoLayer(), rr)
	res := netRes(t, rp, "+3V3")
	if res.Status != "over-budget" || res.Reroute == nil {
		t.Fatalf("status %s reroute %v", res.Status, res.Reroute)
	}
	if res.Widened != n {
		t.Fatalf("widened %d, want all %d tapered segments back", res.Widened, n)
	}
	for _, s := range res.Segments {
		if s.WidthMil != s.RoutedMil {
			t.Fatalf("segment not back at routed width: %+v", s)
		}
	}
	if rp.Passes > 41 {
		t.Fatalf("passes %d", rp.Passes)
	}
	if res.Reroute.WidthMil <= 10 {
		t.Fatalf("reroute width %.1f", res.Reroute.WidthMil)
	}
}

// Fan-out vias are counted from the pad's own current; without sim data the
// old rail-based count is unchanged.
func TestFanoutNeedPerPad(t *testing.T) {
	ru := DefaultRules()
	small := &Pad{Part: "U", Number: "1", Box: OrientedBox{W: 20, H: 30}}
	mid := &Pad{Part: "U", Number: "2", Box: OrientedBox{W: 70, H: 70}}
	big := &Pad{Part: "U", Number: "3", Box: OrientedBox{W: 160, H: 160}}
	rail := &NetPlan{ViasPerTransition: 5}
	if fanoutNeed(rail, small, ru, 10) != 1 || fanoutNeed(rail, mid, ru, 10) != 3 || fanoutNeed(rail, big, ru, 10) != 5 {
		t.Fatalf("rail-based counts changed: %d %d %d", fanoutNeed(rail, small, ru, 10), fanoutNeed(rail, mid, ru, 10), fanoutNeed(rail, big, ru, 10))
	}
	per := ViaCurrent(ru.ViaDrill, 10)
	sim := &NetPlan{ViasPerTransition: 5, PadCurrents: []PadCurrent{
		{Pad: "U.1", CurrentA: 0.005, pad: small},
		{Pad: "U.2", CurrentA: 0.1, pad: mid},
		{Pad: "U.3", CurrentA: 3.5 * per, pad: big},
	}}
	if got := fanoutNeed(sim, small, ru, 10); got != 1 {
		t.Fatalf("5 mA pad wants %d vias", got)
	}
	if got := fanoutNeed(sim, mid, ru, 10); got != 1 {
		t.Fatalf("0.1 A mid pad wants %d vias (rail would give 3)", got)
	}
	if got := fanoutNeed(sim, big, ru, 10); got != 4 {
		t.Fatalf("3.5 via-currents on a thermal pad want %d vias", got)
	}
	sim.ExtraVias = 1
	if got := fanoutNeed(sim, small, ru, 10); got != 2 {
		t.Fatalf("extra via feedback ignored: %d", got)
	}
}

func TestSimResolveAndDeclared(t *testing.T) {
	f := &SimFile{SchemaVersion: 1, Results: []SimResult{
		{Scenario: "typical", Nets: map[string]SimNet{"+3V3": {CurrentA: 0.1, Role: "power", Pins: []SimPin{{Ref: "S", Pin: "1", CurrentA: 0.1, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 0.1, Dir: "sink"}}}}},
		{Scenario: "peak", Nets: map[string]SimNet{"+3V3": {CurrentA: 0.9, Role: "power", Pins: []SimPin{{Ref: "S", Pin: "1", CurrentA: 0.9, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 0.9, Dir: "sink"}}}}},
	}}
	sp := f.Resolve()
	if !strings.HasPrefix(sp.Scenario, "envelope") || sp.Nets["+3V3"].CurrentA != 0.9 || sp.Nets["+3V3"].Pins[1].CurrentA != 0.9 {
		t.Fatalf("envelope %+v %+v", sp.Scenario, sp.Nets["+3V3"])
	}
	f.Results = append(f.Results, SimResult{Scenario: "worst", Nets: map[string]SimNet{"+3V3": {CurrentA: 0.7, Role: "power", Pins: []SimPin{{Ref: "S", Pin: "1", CurrentA: 0.7, Dir: "source"}, {Ref: "L", Pin: "1", CurrentA: 0.7, Dir: "sink"}}}}})
	sp = f.Resolve()
	if sp.Scenario != "worst" || sp.Nets["+3V3"].CurrentA != 0.7 {
		t.Fatalf("worst not used: %s %.2f", sp.Scenario, sp.Nets["+3V3"].CurrentA)
	}
	b := railBoard([]float64{0, 1000}, []string{"S", "L"})
	an := Analyze(b, PowerSpec{Sim: sp}, nil)
	np := an.ByNet["+3V3"]
	if np.Source != "simulated" || np.CurrentA != 0.7 || len(np.PadCurrents) != 2 {
		t.Fatalf("simulated plan %+v", np)
	}
	an = Analyze(b, PowerSpec{Sim: sp, Rails: []PowerRail{{Net: "+3V3", Voltage: 3.3, CurrentA: 0.5}}}, nil)
	np = an.ByNet["+3V3"]
	if np.Source != "declared" || np.CurrentA != 0.5 || len(np.Warnings) == 0 {
		t.Fatalf("declared below simulated must win and warn: %+v", np)
	}
	// Nets absent from the sim keep the old logic.
	if g := an.ByNet["GND"]; g.Source != "largest-rail-return" || g.hasPadCurrents() {
		t.Fatalf("GND changed without sim data: %+v", g)
	}
}

// The ESP32 fixtures parse: the hand-built one (single scenario, KCL holds
// per net with pass = no current) and the real `sim power` output (merged
// worst case). With the board dump available both map every pin.
func TestESP32SimFixtureMaps(t *testing.T) {
	var sims []*SimPower
	for _, f := range []string{"testdata/esp32-mini-sim.json", "testdata/esp32-mini-sim.feat-sim.json"} {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sp, err := ParseSim(raw)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if sp.Scenario != "worst" || sp.Ripple["SW"].IRmsA <= 0 {
			t.Fatalf("%s: resolve %s %+v", f, sp.Scenario, sp.Ripple)
		}
		sims = append(sims, sp)
	}
	for name, n := range sims[0].Nets {
		src, snk := 0.0, 0.0
		for _, p := range n.Pins {
			switch p.Dir {
			case "source":
				src += p.CurrentA
			case "sink":
				snk += p.CurrentA
			default:
				if p.CurrentA != 0 {
					t.Fatalf("%s: pass pin %s.%s carries %.4f A", name, p.Ref, p.Pin, p.CurrentA)
				}
			}
		}
		if math.Abs(src-snk) > 1e-3 {
			t.Fatalf("%s unbalanced: source %.4f sink %.4f", name, src, snk)
		}
	}
	dump := os.Getenv("PCBAUTO_ESP32_DUMP")
	if dump == "" {
		t.Skip("PCBAUTO_ESP32_DUMP not set")
	}
	braw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(braw)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range sims {
		an := Analyze(b, PowerSpec{Sim: sp}, nil)
		for _, np := range an.Nets {
			if len(np.Warnings) > 0 {
				t.Fatalf("%s: %v", np.Net, np.Warnings)
			}
		}
	}
}

// The re-route feedback loop runs at most two passes on a real (tiny) route.
func TestIRRerouteBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("routes a board")
	}
	b := railBoard([]float64{0, 1500, 3000}, []string{"S", "A", "B"})
	pins := []SimPin{{Ref: "S", Pin: "1", CurrentA: 2, Dir: "source"}, {Ref: "A", Pin: "1", CurrentA: 1, Dir: "sink"}, {Ref: "B", Pin: "1", CurrentA: 1, Dir: "sink"}}
	out, err := Run(t.Context(), b, Options{Power: PowerSpec{Sim: simFor(pins), IRBudget: &IRBudget{MV: 0.5}},
		Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 20 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	re := 0
	for _, a := range out.Attempts {
		if strings.Contains(a.Stack, "IR re-route") {
			re++
		}
	}
	if re > 2 {
		t.Fatalf("%d IR re-routes", re)
	}
	if out.Route.Power == nil {
		t.Fatal("no IR report")
	}
	t.Logf("attempts %+v; notes %v", out.Attempts, out.Route.Notes)
}
