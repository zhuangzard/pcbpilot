package safety

import (
	"math"
	"strings"
	"testing"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

// Pinned table points. A change to any of these must be a deliberate table
// correction with its source, never a side effect.
func TestIPC2221BTable6_1(t *testing.T) {
	for _, c := range []struct {
		v    float64
		col  IPCColumn
		want float64
	}{
		{100, IPCB2, 0.6}, {300, IPCB2, 1.25}, {500, IPCB2, 2.5}, {600, IPCB2, 3.0},
		{100, IPCB1, 0.1}, {300, IPCB1, 0.2}, {500, IPCB1, 0.25},
		{12, IPCB2, 0.1}, {160, IPCB2, 1.25}, {300, IPCB4, 0.4}, {300, IPCA5, 0.4},
		{300, IPCB3, 12.5}, {40, IPCA5, 0.13},
	} {
		if got := IPC2221BSpacing(c.v, c.col); !near(got, c.want) {
			t.Errorf("IPC %s at %gV = %g, want %g", c.col, c.v, got, c.want)
		}
	}
}

func TestCreepageF4(t *testing.T) {
	for _, c := range []struct {
		v    float64
		pd   int
		mg   string
		want float64
	}{
		{250, 2, "IIIa", 2.5}, {250, 2, "IIIb", 2.5}, {250, 2, "II", 1.8}, {250, 2, "I", 1.25},
		{400, 2, "IIIa", 4.0}, {250, 3, "IIIa", 4.0}, {250, 1, "IIIa", 0.56},
		{300, 2, "IIIa", 3.0}, // interpolated 250→320 (2.5→3.2), 61010-1 Table 4 value
		{230, 2, "IIIa", 2.3},
		{5, 2, "IIIa", 0.4},
	} {
		if got := CreepageBasic(c.v, c.pd, c.mg, false); !near(got, c.want) {
			t.Errorf("creepage %gV PD%d MG %s = %g, want %g", c.v, c.pd, c.mg, got, c.want)
		}
	}
	if got := CreepageBasic(250, 2, "IIIa", true); !near(got, 1.0) {
		t.Errorf("PWB PD2 250V = %g, want 1.0", got)
	}
}

func TestClearanceAndAltitude(t *testing.T) {
	if got := MainsTransientV(230, 2); got != 2500 {
		t.Errorf("230V OVC II transient %g", got)
	}
	if got := MainsTransientV(120, 3); got != 2500 {
		t.Errorf("120V OVC III transient %g", got)
	}
	if got := ClearanceForWithstand(2500, 2); got != 1.5 {
		t.Errorf("2.5kV PD2 %g", got)
	}
	if got := ClearanceForWithstand(4000, 2); got != 3.0 {
		t.Errorf("4kV PD2 %g", got)
	}
	if got := ClearanceForWithstand(1000, 3); got != 0.8 {
		t.Errorf("1kV PD3 %g", got)
	}
	if got := NextImpulseStep(2500); got != 4000 {
		t.Errorf("step after 2500 %g", got)
	}
	for _, c := range []struct{ m, k float64 }{{0, 1}, {2000, 1}, {3000, 1.14}, {2500, 1.07}, {5000, 1.48}} {
		if got := AltitudeFactor(c.m); !near(got, c.k) {
			t.Errorf("altitude %g → %g, want %g", c.m, got, c.k)
		}
	}
}

func TestIEC62368Mains(t *testing.T) {
	s := Standard{Name: "IEC 62368-1", PollutionDegree: 2, MaterialGroup: "IIIa", OvervoltageCategory: "II"}
	basic := Distances(Pair{WorkingVrms: 250, WorkingVpeak: 354, Insulation: "basic", Transient: "mains"}, s)
	if basic.ClearanceMm != 1.5 || basic.CreepageMm != 2.5 {
		t.Fatalf("62368 basic 250V: %+v", basic)
	}
	re := Distances(Pair{WorkingVrms: 230, WorkingVpeak: 325, Insulation: "reinforced"}, s)
	if re.ClearanceMm != 3.0 || re.CreepageMm != 4.6 || re.RequiredWithstandV != 4000 {
		t.Fatalf("62368 reinforced 230V: %+v", re)
	}
	if re.ESClass != "ES3" || re.SlotWidthMm != 1.0 || !strings.Contains(strings.Join(re.Why, "\n"), Caveat) {
		t.Fatalf("62368 reinforced metadata: %+v", re)
	}
	// Altitude 4000 m scales the clearance only.
	hi := s
	hi.AltitudeM = 4000
	ra := Distances(Pair{WorkingVrms: 230, WorkingVpeak: 325, Insulation: "reinforced"}, hi)
	if !near(ra.ClearanceMm, 3.87) || ra.CreepageMm != 4.6 {
		t.Fatalf("62368 reinforced 230V @4000m: %+v", ra)
	}
	// Secondary circuit: one OVC lower (1500 V → basic 0.5 mm).
	sec := Distances(Pair{WorkingVrms: 230, WorkingVpeak: 325, Insulation: "basic", Transient: "secondary"}, s)
	if sec.ClearanceMm != 0.5 {
		t.Fatalf("62368 secondary basic: %+v", sec)
	}
	// ES1 → ES1 functional falls back to IPC-2221B.
	fn := Distances(Pair{WorkingVrms: 5, WorkingVpeak: 5, Insulation: "functional"}, s)
	if fn.ClearanceMm != 0.1 || !strings.HasPrefix(fn.Ref, "IPC-2221B") {
		t.Fatalf("functional: %+v", fn)
	}
}

func TestIEC60601MOPP(t *testing.T) {
	s := Standard{Name: "IEC60601-1", MOP: "MOPP"}
	one := Distances(Pair{WorkingVrms: 250, WorkingVpeak: 354, MOPCount: 1}, s)
	if one.CreepageMm != 4 || one.ClearanceMm != 2.5 || one.TestVoltageVrms != 1500 {
		t.Fatalf("1 MOPP 250V: %+v", one)
	}
	two := Distances(Pair{WorkingVrms: 250, WorkingVpeak: 354, MOPCount: 2}, s)
	if two.CreepageMm != 8 || two.ClearanceMm != 5 || two.TestVoltageVrms != 4000 || two.Insulation != "double" {
		t.Fatalf("2 MOPP 250V: %+v", two)
	}
	// Reinforced without an explicit count → 2 MOPP; 230 Vrms uses the 250 row.
	re := Distances(Pair{WorkingVrms: 230, WorkingVpeak: 325, Insulation: "reinforced"}, s)
	if re.MOPCount != 2 || re.CreepageMm != 8 {
		t.Fatalf("reinforced → 2 MOPP: %+v", re)
	}
	// MOOP goes through the 62368-1 tables: 2 MOOP at 230 V = reinforced 3.0 / 4.6.
	moop := Distances(Pair{WorkingVrms: 230, WorkingVpeak: 325, MOP: "MOOP", MOPCount: 2}, s)
	if moop.ClearanceMm != 3.0 || moop.CreepageMm != 4.6 || moop.TestVoltageVrms != 3000 {
		t.Fatalf("2 MOOP 230V: %+v", moop)
	}
}

func TestIEC61010(t *testing.T) {
	s := Standard{Name: "IEC61010-1", OvervoltageCategory: "II"}
	b := Distances(Pair{WorkingVrms: 300, WorkingVpeak: 424, Insulation: "basic", Transient: "mains"}, s)
	if b.ClearanceMm != 1.5 || b.CreepageMm != 3.0 {
		t.Fatalf("61010 CAT II 300V basic: %+v", b)
	}
	r := Distances(Pair{WorkingVrms: 300, WorkingVpeak: 424, Insulation: "reinforced", Transient: "mains"}, s)
	if r.ClearanceMm != 3.0 || r.CreepageMm != 6.0 {
		t.Fatalf("61010 CAT II 300V reinforced: %+v", r)
	}
	s.OvervoltageCategory = "III"
	r3 := Distances(Pair{WorkingVrms: 300, WorkingVpeak: 424, Insulation: "reinforced", Transient: "mains"}, s)
	if r3.ClearanceMm != 6.0 {
		t.Fatalf("61010 CAT III 300V reinforced: %+v", r3)
	}
}

func TestSlotDecision(t *testing.T) {
	s := Standard{Name: "IEC60601-1", MOP: "MOPP", MOPCount: 2}
	// 2 MOPP (8 mm creepage) across an opto whose rows are 6 mm apart.
	r := Distances(Pair{WorkingVrms: 250, WorkingVpeak: 354, AvailableMm: 6, BarrierSpanMm: 4}, s)
	if !r.SlotRequired || r.Infeasible || r.SlotWidthMm != 1.0 {
		t.Fatalf("slot: %+v", r)
	}
	e := SlotExtension(8, 6, 1)
	if !near(e, math.Sqrt(3.5*3.5-2.5*2.5)) || r.SlotLengthMm != ceilTo(4+2*e, 0.1) {
		t.Fatalf("slot length %g (e %g)", r.SlotLengthMm, e)
	}
	// Rows closer than the clearance: a slot cannot fix the air path.
	bad := Distances(Pair{WorkingVrms: 250, WorkingVpeak: 354, AvailableMm: 4}, s)
	if !bad.SlotRequired || !bad.Infeasible {
		t.Fatalf("infeasible expected: %+v", bad)
	}
	// Enough distance: no slot.
	ok := Distances(Pair{WorkingVrms: 250, WorkingVpeak: 354, AvailableMm: 9}, s)
	if ok.SlotRequired {
		t.Fatalf("no slot expected: %+v", ok)
	}
	if SlotMinWidth(1) != 0.25 || SlotMinWidth(2) != 1.0 || SlotMinWidth(3) != 1.5 {
		t.Fatal("slot widths")
	}
}

func TestESClass(t *testing.T) {
	for _, c := range []struct {
		rms, pk float64
		dc      bool
		want    string
	}{
		{5, 5, true, "ES1"}, {60, 60, true, "ES1"}, {100, 100, true, "ES2"}, {400, 400, true, "ES3"},
		{24, 34, false, "ES1"}, {48, 68, false, "ES2"}, {230, 325, false, "ES3"},
	} {
		if got := ESClass(c.rms, c.pk, c.dc); got != c.want {
			t.Errorf("ES(%g,%g,%v) = %s, want %s", c.rms, c.pk, c.dc, got, c.want)
		}
	}
}

func TestNormStandard(t *testing.T) {
	for in, want := range map[string]string{"IEC 62368-1": IEC62368, "iec60601-1": IEC60601, "IEC61010-1": IEC61010,
		"IEC 60950-1": IEC62368, "": IPC2221B, "IPC-2221B": IPC2221B} {
		if got := NormStandard(in); got != want {
			t.Errorf("%q → %s, want %s", in, got, want)
		}
	}
}
