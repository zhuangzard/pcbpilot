package app

import (
	"math"
	"testing"
)

func exitEnv() *escapeEnv {
	return &escapeEnv{pads: escQFNRow("SIG1", "SIG2", "+3V3", "SIG3", "SIG4"), clr: 5.9055 + 0.2, margin: 0.2,
		court: map[string]layoutBBox{"U1": {MinX: -20, MinY: -300, MaxX: 85, MaxY: 29}}}
}

func rowOf(rows []exitRow, pin string) *exitRow {
	for i := range rows {
		if rows[i].Pin == pin {
			return &rows[i]
		}
	}
	return nil
}

// A 0.4 mm pitch row: the intent width (15.75 mil) does not exit, a neck to
// widthMil.min (10) does, and the report names the width the geometry allows.
func TestExitFeasibilityNeck(t *testing.T) {
	rows := exitFeasibility(exitEnv(), picoReqs(10))
	if len(rows) != 1 {
		t.Fatalf("rows %+v", rows)
	}
	r := rows[0]
	if r.Pin != "U1.3" || r.Class != "neck" || r.Failure != "" || r.Escape != "outward" {
		t.Fatalf("row %+v", r)
	}
	// 2·(15.75 − 3.937 − 6.1055) = 11.41 mil, same figure as the planner's.
	if math.Abs(r.WMaxOutwardMil-11.41) > 0.02 || r.WMaxOutwardMil < r.MinMil || r.WMaxOutwardMil >= r.OuterMil {
		t.Fatalf("wmax %v", r.WMaxOutwardMil)
	}
	if r.LimiterOutward == "" {
		t.Fatal("no limiter named")
	}
}

// widthMil.min above what the pitch allows: IMPOSSIBLE, geometry-blocked,
// with the shortfall; and the report never changes the env.
func TestExitFeasibilityImpossible(t *testing.T) {
	env := exitEnv()
	n := len(env.planned)
	rows := exitFeasibility(env, picoReqs(12))
	r := rowOf(rows, "U1.3")
	if r == nil || r.Class != "IMPOSSIBLE" || r.Failure != failGeometry || r.Why == "" {
		t.Fatalf("row %+v", r)
	}
	if want := 12 - r.WMaxOutwardMil; math.Abs(r.ShortfallMil-want) > 0.011 || r.ShortfallMil <= 0 {
		t.Fatalf("shortfall %v want %v", r.ShortfallMil, want)
	}
	if len(env.planned) != n || len(env.vias) != 0 {
		t.Fatal("report modified the env")
	}
}

// A wide pitch exits at full width.
func TestExitFeasibilityFull(t *testing.T) {
	pads := escQFNRow("SIG1", "+3V3", "SIG2")
	for i := range pads[:3] {
		pads[i].X = float64(i) * 50
	}
	rows := exitFeasibility(&escapeEnv{pads: pads, clr: 6.1055}, picoReqs(10))
	if r := rowOf(rows, "U1.2"); r == nil || r.Class != "full" || r.WMaxOutwardMil != 15.75 {
		t.Fatalf("rows %+v", rows)
	}
}

// Own-net via at an SMD pad: the default planner keeps the via a clearance
// away from same-net pads; with ownViaAtSMD (fastroute accepts it) only
// touching the pad is refused.
func TestViaAtSMDOwnNet(t *testing.T) {
	pad := pcbPadP{Designator: "U1", Number: "1", Net: "+3V3", Layer: 1, X: 0, Y: 0, W: 7.874, H: 34.45}
	env := &escapeEnv{pads: []pcbPadP{pad}, clr: 6.1055, viaDia: 12}
	// Via edge 2 mil from the pad edge: inside the 6.1 mil clearance.
	near := [2]float64{3.937 + 2 + 6, 0}
	if env.viaAtSMD("+3V3", near) == "" {
		t.Fatal("default rule accepted a via within clearance of an own-net pad")
	}
	env.ownViaAtSMD = true
	if got := env.viaAtSMD("+3V3", near); got != "" {
		t.Fatalf("relaxed rule refused a non-touching via: %s", got)
	}
	if env.viaAtSMD("+3V3", [2]float64{3.937 + 4, 0}) == "" {
		t.Fatal("relaxed rule accepted a via on the pad")
	}
	// Another net is never affected by the flag.
	if env.viaAtSMD("SIG", near) != "" {
		t.Fatal("other net flagged")
	}
}
