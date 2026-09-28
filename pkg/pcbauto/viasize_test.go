package pcbauto

import (
	"context"
	"math"
	"testing"
	"time"
)

func nearV(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.4f, want %.4f ± %.4f", what, got, want, tol)
	}
}

// Hand-computed IPC-2221 points: barrel annulus π(d+t)t, internal k=0.024,
// I = k·ΔT^0.44·A^0.725.
func TestViaAmpacityIPCPoints(t *testing.T) {
	nearV(t, "12 mil / 0.7 / ΔT10", ViaAmpacity(12, 0.7, 10), 0.739, 0.003)    // A = 27.93 mil²
	nearV(t, "12 mil / 0.7 / ΔT20", ViaAmpacity(12, 0.7, 20), 1.002, 0.004)    // ×20^0.44/10^0.44
	nearV(t, "0.3 mm / 0.7 / ΔT10", ViaAmpacity(11.81, 0.7, 10), 0.731, 0.003) // A = 27.51 mil²
	nearV(t, "0.6 mm / 0.7 / ΔT10", ViaAmpacity(23.62, 0.7, 10), 1.184, 0.004) // A = 53.48 mil²
	if ViaCurrent(12, 10) != ViaAmpacity(12, DefaultViaPlatingMil, 10) {
		t.Fatal("ViaCurrent must be ViaAmpacity at the default plating")
	}
	if !(ViaAmpacity(12, 1.0, 10) > ViaAmpacity(12, 0.7, 10) && ViaAmpacity(15.75, 0.7, 10) > ViaAmpacity(12, 0.7, 10)) {
		t.Fatal("ampacity must grow with plating and drill")
	}
	if ViaAmpacity(0, 0.7, 10) != 0 || ViaAmpacity(12, 0, 10) != 0 {
		t.Fatal("degenerate via carries nothing")
	}
}

// R = ρL/(π(d+t)t): 12 mil drill, 0.7 mil plating, 1.6 mm board ≈ 1.53 mΩ.
func TestViaResistance(t *testing.T) {
	nearV(t, "R 12/0.7/62.99", ViaResistance(12, 0.7, 62.99)*1000, 1.527, 0.005)
	nearV(t, "R scales with L", ViaResistance(12, 0.7, 31.5)/ViaResistance(12, 0.7, 63), 0.5, 1e-3)
	p := EvalVias(ViaSizing{CurrentA: 3}, ViaSize{12, 24}, 5)
	nearV(t, "drop 3 A through 5 vias", p.DropMV, 3*1.527/5, 0.01)
}

func TestJLCViaLadder(t *testing.T) {
	l := JLCViaLadder()
	if len(l) != 6 {
		t.Fatalf("ladder %v", l)
	}
	nearV(t, "0.2 mm pad", l[0].DiaMil*0.0254, 0.4, 0.001)
	nearV(t, "0.3 mm pad", l[2].DiaMil*0.0254, 0.6, 0.001)
	nearV(t, "0.6 mm pad", l[5].DiaMil*0.0254, 0.9, 0.001)
}

func TestViaSpaceFits(t *testing.T) {
	v := ViaSize{12, 24} // pitch = 24 + 6 = 30
	if got := TrackViaSpace(10).Fits(v, 6, 0); got != 3 {
		t.Fatalf("10 mil track, 100 mil span: %d vias, want 3 in a row", got)
	}
	if got := TrackViaSpace(55).Fits(v, 6, 0); got != 6 {
		t.Fatalf("55 mil track: %d vias, want 2 rows × 3", got)
	}
	if got := (ViaSpace{}).Fits(v, 6, 0); got != viaMaxCount {
		t.Fatalf("unconstrained: %d", got)
	}
	// Hole gap governs a tight drill: pitch = max(24+2, 12+20) = 32.
	if got := (ViaSpace{WMil: 24, LMil: 88}).Fits(v, 2, 20); got != 3 {
		t.Fatalf("hole-gap pitch: %d", got)
	}
}

// Class size first, then more vias while they fit, then a larger drill.
func TestSizeViasPreference(t *testing.T) {
	cls := ViaSize{12, 24}
	p := SizeVias(ViaSizing{CurrentA: 0.5, Class: cls, Space: TrackViaSpace(10), Clearance: 6})
	if p.Count != 1 || p.DrillMil != 12 || !p.OK {
		t.Fatalf("0.5 A: %+v", p)
	}
	// 3 A on its 55 mil track: 3.6 / 0.739 → 5 class vias, 6 fit.
	p = SizeVias(ViaSizing{CurrentA: 3, Class: cls, Space: TrackViaSpace(55), Clearance: 6})
	if p.Count != 5 || p.DrillMil != 12 || !p.OK || p.MarginPct < 20 {
		t.Fatalf("3 A: %+v", p)
	}
	// 2 A on a 31.5 mil track: 4 class vias needed, 3 fit → 0.4 mm × 3.
	p = SizeVias(ViaSizing{CurrentA: 2, Class: cls, Space: TrackViaSpace(31.5), Clearance: 6})
	if p.Count != 3 || math.Abs(p.DrillMil-15.75) > 0.01 || !p.OK {
		t.Fatalf("2 A narrow: %+v", p)
	}
	// Margin: 0.65 A fits one via without margin, two with 20 %.
	if SizeVias(ViaSizing{CurrentA: 0.65, Class: cls, MarginPct: -1}).Count != 1 || SizeVias(ViaSizing{CurrentA: 0.65, Class: cls}).Count != 2 {
		t.Fatal("margin not applied")
	}
	// Plating is a parameter: 1.0 mil carries 0.65 A × 1.2 in one via.
	if p := SizeVias(ViaSizing{CurrentA: 0.65, Class: cls, PlatingMil: 1.0}); p.Count != 1 || p.PlatingMil != 1.0 {
		t.Fatalf("plating 1.0: %+v", p)
	}
	// Nothing fits: best effort, flagged.
	p = SizeVias(ViaSizing{CurrentA: 30, Class: cls, Space: TrackViaSpace(10), Clearance: 6})
	if p.OK || p.AmpacityA >= 30 {
		t.Fatalf("30 A through a 10 mil track: %+v", p)
	}
}

// A declared 3 A (2 A) net that has to change layer gets its current-sized via
// array at the transition: N vias of size S, not one.
func TestRouterPlacesViaArrayOnHighCurrentNet(t *testing.T) {
	for _, amps := range []float64{3, 2} {
		t.Run(sprintf("%.0fA", amps), func(t *testing.T) { routeViaArray(t, amps) })
	}
}

func routeViaArray(t *testing.T, amps float64) {
	b := &Board{Rules: DefaultRules(), CopperLayers: 2, Outline: Rect{0, 0, 1600, 1000}.Corners()}
	u1 := &Part{Ref: "U1", Device: "REG", Pos: Point{300, 500}, Side: LayerTop,
		Pads: []*Pad{{Part: "U1", Number: "1", Net: "VOUT", Layer: LayerTop, Box: OrientedBox{C: Point{300, 500}, W: 80, H: 80}}}}
	u2 := &Part{Ref: "U2", Device: "LOAD", Pos: Point{1300, 500}, Side: LayerBottom,
		Pads: []*Pad{{Part: "U2", Number: "1", Net: "VOUT", Layer: LayerBottom, Box: OrientedBox{C: Point{1300, 500}, W: 80, H: 80}}}}
	b.Parts = []*Part{u1, u2}
	if err := b.Index(); err != nil {
		t.Fatal(err)
	}
	no := false
	spec := PowerSpec{Rails: []PowerRail{{Net: "VOUT", Voltage: 5, CurrentA: amps, Plane: &no}}}
	st := DecideStackup(b, Analyze(b, spec, nil), StackOptions{Force: 2})
	an := Analyze(b, spec, st)
	np := an.ByNet["VOUT"]
	// 3 A: 5 × 12/24 on its 55 mil track; 2 A: its 31.5 mil track holds 3
	// class vias of the 4 needed → 3 × 0.4 mm.
	if np.Via == nil || np.Via.Count < 3 || np.ViasPerTransition != np.Via.Count || amps == 2 && np.Via.DrillMil < 15 {
		t.Fatalf("3 A plan: %+v", np.Via)
	}
	rr, err := Route(context.Background(), b, st, an, RouteOptions{Timeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if rr.Stats.Completion < 100 {
		t.Fatalf("completion %.1f: %+v %v", rr.Stats.Completion, rr.Unrouted, rr.Notes)
	}
	var vs []Via
	for _, v := range rr.Vias {
		if v.Net == "VOUT" {
			vs = append(vs, v)
			if v.Drill < np.Via.DrillMil-0.01 || v.Dia < np.Via.DiaMil-0.01 {
				t.Fatalf("via %+v smaller than the sized %.1f/%.1f", v, np.Via.DrillMil, np.Via.DiaMil)
			}
		}
	}
	t.Logf("plan %d × %.1f/%.1f mil (%.2f A); placed %d VOUT vias: %+v", np.Via.Count, np.Via.DrillMil, np.Via.DiaMil, np.Via.AmpacityA, len(vs), vs)
	if len(vs) < np.Via.Count {
		t.Fatalf("%d VOUT vias, want ≥ %d per transition (notes %v)", len(vs), np.Via.Count, rr.Notes)
	}
	drc := CheckDRC(b, an, st, rr.Tracks, rr.Vias)
	if len(drc.Violations) > 0 {
		t.Fatalf("DRC %+v", drc.Violations)
	}
	chk := CheckViaCurrent(b, rr.Tracks, rr.Vias, &Intent{Nets: map[string]*IntentNet{"VOUT": {Role: "power", CurrentA: amps, WidthMil: IntentWidth{Outer: np.WidthMil}}}})
	if chk.Groups == 0 || len(chk.Findings) > 0 {
		t.Fatalf("via-current on the routed array: %+v", chk)
	}
}

// pcb check via-current: one 0.3 mm via carrying 3 A is an ERROR; the sized
// array passes.
func TestViaCurrentCheckSingleVia3A(t *testing.T) {
	b := &Board{Rules: DefaultRules()}
	in := &Intent{Nets: map[string]*IntentNet{"VOUT": {Role: "power", CurrentA: 3}}}
	tracks := []Track{
		{Net: "VOUT", Layer: LayerTop, A: Point{0, 0}, B: Point{500, 0}, Width: 55},
		{Net: "VOUT", Layer: LayerBottom, A: Point{500, 0}, B: Point{1000, 0}, Width: 55},
	}
	one := []Via{{Net: "VOUT", C: Point{500, 0}, Drill: 11.81, Dia: 23.62}}
	chk := CheckViaCurrent(b, tracks, one, in)
	if len(chk.Findings) != 1 || chk.Findings[0].Level != "ERROR" {
		t.Fatalf("single via: %+v", chk)
	}
	f := chk.Findings[0]
	nearV(t, "Σ ampacity", f.AmpacityA, 0.731, 0.005)
	nearV(t, "required", f.RequiredA, 3, 1e-9)
	var arr []Via
	for i := 0; i < 5; i++ {
		arr = append(arr, Via{Net: "VOUT", C: Point{440 + float64(i)*30, 0}, Drill: 12, Dia: 24})
	}
	if chk := CheckViaCurrent(b, tracks, arr, in); len(chk.Findings) != 0 || chk.Groups != 1 {
		t.Fatalf("5 × 12/24 array: %+v", chk)
	}
	// Four vias carry 2.96 A < 3 A: still an ERROR; margin shortfalls WARN.
	if chk := CheckViaCurrent(b, tracks, arr[:4], in); len(chk.Findings) != 1 || chk.Findings[0].Level != "ERROR" {
		t.Fatalf("4 vias: %+v", chk)
	}
	in.Nets["VOUT"].CurrentA = 2.6
	if chk := CheckViaCurrent(b, tracks, arr[:4], in); len(chk.Findings) != 1 || chk.Findings[0].Level != "WARN" {
		t.Fatalf("4 vias at 2.6 A: %+v", chk)
	}
	// A narrow track bounds what the transition has to pass.
	in.Nets["VOUT"].CurrentA = 3
	thin := []Track{{Net: "VOUT", Layer: LayerTop, A: Point{0, 0}, B: Point{500, 0}, Width: 5}}
	if chk := CheckViaCurrent(b, thin, one, in); len(chk.Findings) != 0 {
		t.Fatalf("5 mil track (≈0.53 A) through a 0.73 A via: %+v", chk)
	}
}
