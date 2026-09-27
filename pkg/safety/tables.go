package safety

import "math"

// This file holds the pinned table values. Every table names its source; the
// numbers are the published values as commonly reproduced in the standards
// (IPC-2221B:2012, IEC 60664-1 annex F — which IEC 62368-1:2018, IEC 60601-1
// ed.3.x and IEC 61010-1:2010 build on — and IEC 60601-1 Table 12). They are
// an engineering reference: confirm against the edition your product
// standard / certification lab applies.

// ---- IPC-2221B Table 6-1 — Electrical conductor spacing (mm) ---------------

// ipcRow is one voltage band of IPC-2221B Table 6-1 (peak V between
// conductors, DC or AC peak).
type ipcRow struct {
	maxV                       float64
	b1, b2, b3, b4, a5, a6, a7 float64
}

// ipc2221bTable6_1 — IPC-2221B Table 6-1. Columns:
//
//	B1 internal conductors
//	B2 external conductors, uncoated, sea level to 3050 m
//	B3 external conductors, uncoated, above 3050 m
//	B4 external conductors, with permanent polymer coating (any elevation)
//	A5 external conductors, with conformal coating over assembly (any elevation)
//	A6 external component lead/termination, uncoated, sea level to 3050 m
//	A7 external component lead/termination, with conformal coating (any elevation)
var ipc2221bTable6_1 = []ipcRow{
	{15, 0.05, 0.1, 0.1, 0.05, 0.13, 0.13, 0.13},
	{30, 0.05, 0.1, 0.1, 0.05, 0.13, 0.25, 0.13},
	{50, 0.1, 0.6, 0.6, 0.13, 0.13, 0.4, 0.13},
	{100, 0.1, 0.6, 1.5, 0.13, 0.13, 0.5, 0.13},
	{150, 0.2, 0.6, 3.2, 0.4, 0.4, 0.8, 0.4},
	{170, 0.2, 1.25, 3.2, 0.4, 0.4, 0.8, 0.4},
	{250, 0.2, 1.25, 6.4, 0.4, 0.4, 0.8, 0.4},
	{300, 0.2, 1.25, 12.5, 0.4, 0.4, 0.8, 0.8},
	{500, 0.25, 2.5, 12.5, 0.8, 0.8, 1.5, 0.8},
}

// ipcPerVoltAbove500 — the ">500 V" row of Table 6-1 (mm per volt above
// 500 V, added to the 500 V value).
var ipcPerVoltAbove500 = ipcRow{0, 0.0025, 0.005, 0.025, 0.00305, 0.00305, 0.00305, 0.00305}

// IPCColumn names a Table 6-1 column.
type IPCColumn string

const (
	IPCB1 IPCColumn = "B1"
	IPCB2 IPCColumn = "B2"
	IPCB3 IPCColumn = "B3"
	IPCB4 IPCColumn = "B4"
	IPCA5 IPCColumn = "A5"
	IPCA6 IPCColumn = "A6"
	IPCA7 IPCColumn = "A7"
)

func (r ipcRow) col(c IPCColumn) float64 {
	switch c {
	case IPCB1:
		return r.b1
	case IPCB3:
		return r.b3
	case IPCB4:
		return r.b4
	case IPCA5:
		return r.a5
	case IPCA6:
		return r.a6
	case IPCA7:
		return r.a7
	}
	return r.b2
}

// IPC2221BSpacing returns the IPC-2221B Table 6-1 minimum spacing (mm) for a
// peak voltage between conductors. The table is a step table (no
// interpolation): a voltage takes the row whose band contains it.
func IPC2221BSpacing(vpeak float64, c IPCColumn) float64 {
	v := math.Abs(vpeak)
	for _, r := range ipc2221bTable6_1 {
		if v <= r.maxV {
			return r.col(c)
		}
	}
	last := ipc2221bTable6_1[len(ipc2221bTable6_1)-1]
	return last.col(c) + (v-500)*ipcPerVoltAbove500.col(c)
}

// ---- IEC 60664-1 Table F.1 — rated impulse voltage (mains transient) -------

// mainsTransient — IEC 60664-1 Table F.1 (= IEC 62368-1 Table 12 "mains
// transient voltages"; IEC 61010-1 Table 4/K.x use the same series): rated
// impulse withstand (V peak) by nominal line-to-neutral voltage and
// overvoltage category I..IV.
var mainsTransient = []struct {
	maxVrms float64
	ovc     [4]float64 // I, II, III, IV
}{
	{50, [4]float64{330, 500, 800, 1500}},
	{100, [4]float64{500, 800, 1500, 2500}},
	{150, [4]float64{800, 1500, 2500, 4000}},
	{300, [4]float64{1500, 2500, 4000, 6000}},
	{600, [4]float64{2500, 4000, 6000, 8000}},
	{1000, [4]float64{4000, 6000, 8000, 12000}},
}

// MainsTransientV returns the impulse withstand voltage (V peak) for a nominal
// mains line-to-neutral voltage (Vrms) and overvoltage category 1..4.
func MainsTransientV(mainsVrms float64, ovc int) float64 {
	ovc = clampInt(ovc, 1, 4)
	for _, r := range mainsTransient {
		if mainsVrms <= r.maxVrms {
			return r.ovc[ovc-1]
		}
	}
	return mainsTransient[len(mainsTransient)-1].ovc[ovc-1]
}

// preferredImpulse is the IEC 60664-1 preferred series of rated impulse
// voltages (V). §5.1.6: reinforced insulation is dimensioned for the value
// one step higher than basic insulation.
var preferredImpulse = []float64{330, 500, 800, 1500, 2500, 4000, 6000, 8000, 12000, 15000, 20000, 25000, 30000}

// NextImpulseStep returns the preferred impulse value one step above v.
func NextImpulseStep(v float64) float64 {
	for _, p := range preferredImpulse {
		if p > v+1e-9 {
			return p
		}
	}
	return v * 1.25
}

// ---- IEC 60664-1 Table F.2 — clearance to withstand transient overvoltages -

// clearanceF2 — IEC 60664-1 Table F.2, case A (inhomogeneous field), clearance
// in mm by required impulse withstand voltage (kV peak) for pollution degree
// 1 / 2 / 3. PD2 has a 0.2 mm floor and PD3 a 0.8 mm floor. IEC 62368-1
// Table 14 ("minimum clearances using required withstand voltage") is built
// from these values.
var clearanceF2 = []struct {
	kv float64
	pd [3]float64 // PD1, PD2, PD3
}{
	{0.33, [3]float64{0.01, 0.2, 0.8}},
	{0.40, [3]float64{0.02, 0.2, 0.8}},
	{0.50, [3]float64{0.04, 0.2, 0.8}},
	{0.60, [3]float64{0.06, 0.2, 0.8}},
	{0.80, [3]float64{0.10, 0.2, 0.8}},
	{1.0, [3]float64{0.15, 0.2, 0.8}},
	{1.2, [3]float64{0.25, 0.25, 0.8}},
	{1.5, [3]float64{0.5, 0.5, 0.8}},
	{2.0, [3]float64{1.0, 1.0, 1.0}},
	{2.5, [3]float64{1.5, 1.5, 1.5}},
	{3.0, [3]float64{2.0, 2.0, 2.0}},
	{4.0, [3]float64{3.0, 3.0, 3.0}},
	{5.0, [3]float64{4.0, 4.0, 4.0}},
	{6.0, [3]float64{5.5, 5.5, 5.5}},
	{8.0, [3]float64{8.0, 8.0, 8.0}},
	{10, [3]float64{11, 11, 11}},
	{12, [3]float64{14, 14, 14}},
	{15, [3]float64{18, 18, 18}},
	{20, [3]float64{25, 25, 25}},
	{25, [3]float64{33, 33, 33}},
	{30, [3]float64{40, 40, 40}},
}

// ClearanceForWithstand returns the Table F.2 clearance (mm) for a required
// withstand voltage (V peak) at a pollution degree. Step table: a voltage
// between two rows takes the higher row (conservative; the standards permit
// interpolation only where they say so).
func ClearanceForWithstand(vpeak float64, pd int) float64 {
	pd = clampInt(pd, 1, 3)
	kv := math.Abs(vpeak) / 1000
	for _, r := range clearanceF2 {
		if kv <= r.kv+1e-9 {
			return r.pd[pd-1]
		}
	}
	// Beyond 30 kV: F.2 continues roughly linearly (≈1.33 mm/kV); extrapolate
	// and let the caller flag it.
	last := clearanceF2[len(clearanceF2)-1]
	return last.pd[pd-1] + (kv-last.kv)*1.33
}

// ---- IEC 60664-1 Table A.2 — altitude correction for clearances ------------

// altitudeFactors — IEC 60664-1 Table A.2 (= IEC 62368-1 Table 16, IEC
// 60601-1 Table 8 up to 5000 m): multiplication factor for clearances above
// 2000 m. Linear interpolation between rows.
var altitudeFactors = []struct{ m, k float64 }{
	{2000, 1.00}, {3000, 1.14}, {4000, 1.29}, {5000, 1.48}, {6000, 1.70},
	{7000, 1.95}, {8000, 2.25}, {9000, 2.62}, {10000, 3.02}, {15000, 6.67}, {20000, 14.5},
}

// AltitudeFactor returns the clearance multiplication factor for an
// altitude in metres (1 at or below 2000 m).
func AltitudeFactor(altM float64) float64 {
	if altM <= 2000 {
		return 1
	}
	for i := 1; i < len(altitudeFactors); i++ {
		a, b := altitudeFactors[i-1], altitudeFactors[i]
		if altM <= b.m {
			return a.k + (altM-a.m)/(b.m-a.m)*(b.k-a.k)
		}
	}
	return altitudeFactors[len(altitudeFactors)-1].k
}

// ---- IEC 60664-1 Table F.4 — creepage distances for basic insulation -------

// creepRow — IEC 60664-1 Table F.4 (= IEC 62368-1 Table 17 for basic /
// supplementary insulation, IEC 61010-1 creepage by working voltage): minimum
// creepage (mm) by working voltage (Vrms). Columns: printed wiring material
// PD1 / PD2 (all material groups except IIIb), then general PD1, PD2 MG I /
// II / III, PD3 MG I / II / III. A zero PWB entry means the column ends
// (no printed-wiring value above 1000 V).
type creepRow struct {
	v                   float64
	pwb1, pwb2          float64
	pd1                 float64
	pd2I, pd2II, pd2III float64
	pd3I, pd3II, pd3III float64
}

var creepageF4 = []creepRow{
	{10, 0.025, 0.04, 0.08, 0.4, 0.4, 0.4, 1.0, 1.0, 1.0},
	{12.5, 0.025, 0.04, 0.09, 0.42, 0.42, 0.42, 1.05, 1.05, 1.05},
	{16, 0.025, 0.04, 0.1, 0.45, 0.45, 0.45, 1.1, 1.1, 1.1},
	{20, 0.025, 0.04, 0.11, 0.48, 0.48, 0.48, 1.2, 1.2, 1.2},
	{25, 0.025, 0.04, 0.125, 0.5, 0.5, 0.5, 1.25, 1.25, 1.25},
	{32, 0.025, 0.04, 0.14, 0.53, 0.53, 0.53, 1.3, 1.3, 1.3},
	{40, 0.025, 0.04, 0.16, 0.56, 0.8, 1.1, 1.4, 1.6, 1.8},
	{50, 0.025, 0.04, 0.18, 0.6, 0.85, 1.2, 1.5, 1.7, 1.9},
	{63, 0.04, 0.063, 0.2, 0.63, 0.9, 1.25, 1.6, 1.8, 2.0},
	{80, 0.063, 0.1, 0.22, 0.67, 0.95, 1.3, 1.7, 1.9, 2.1},
	{100, 0.1, 0.16, 0.25, 0.71, 1.0, 1.4, 1.8, 2.0, 2.2},
	{125, 0.16, 0.25, 0.28, 0.75, 1.05, 1.5, 1.9, 2.1, 2.4},
	{160, 0.25, 0.4, 0.32, 0.8, 1.1, 1.6, 2.0, 2.2, 2.5},
	{200, 0.4, 0.63, 0.42, 1.0, 1.4, 2.0, 2.5, 2.8, 3.2},
	{250, 0.56, 1.0, 0.56, 1.25, 1.8, 2.5, 3.2, 3.6, 4.0},
	{320, 0.75, 1.6, 0.75, 1.6, 2.2, 3.2, 4.0, 4.5, 5.0},
	{400, 1.0, 2.0, 1.0, 2.0, 2.8, 4.0, 5.0, 5.6, 6.3},
	{500, 1.3, 2.5, 1.3, 2.5, 3.6, 5.0, 6.3, 7.1, 8.0},
	{630, 1.8, 3.2, 1.8, 3.2, 4.5, 6.3, 8.0, 9.0, 10.0},
	{800, 2.4, 4.0, 2.4, 4.0, 5.6, 8.0, 10.0, 11, 12.5},
	{1000, 3.2, 5.0, 3.2, 5.0, 7.1, 10.0, 12.5, 14, 16},
	{1250, 0, 0, 4.2, 6.3, 9.0, 12.5, 16, 18, 20},
	{1600, 0, 0, 5.6, 8.0, 11, 16, 20, 22, 25},
	{2000, 0, 0, 7.5, 10, 14, 20, 25, 28, 32},
	{2500, 0, 0, 10, 12.5, 18, 25, 32, 36, 40},
	{3200, 0, 0, 12.5, 16, 22, 32, 40, 45, 50},
	{4000, 0, 0, 16, 20, 28, 40, 50, 56, 63},
	{5000, 0, 0, 20, 25, 36, 50, 63, 71, 80},
	{6300, 0, 0, 25, 32, 45, 63, 80, 90, 100},
	{8000, 0, 0, 32, 40, 56, 80, 100, 110, 125},
	{10000, 0, 0, 40, 50, 71, 100, 125, 140, 160},
}

// materialGroupIndex maps "I", "II", "IIIa", "IIIb", "III" to 0/1/2.
func materialGroupIndex(mg string) int {
	switch normMG(mg) {
	case "I":
		return 0
	case "II":
		return 1
	}
	return 2
}

func (r creepRow) value(pd, mg int, pwb bool) float64 {
	if pwb && pd <= 2 && r.pwb1 > 0 {
		if pd == 1 {
			return r.pwb1
		}
		return r.pwb2
	}
	switch pd {
	case 1:
		return r.pd1
	case 3:
		return [3]float64{r.pd3I, r.pd3II, r.pd3III}[mg]
	}
	return [3]float64{r.pd2I, r.pd2II, r.pd2III}[mg]
}

// CreepageBasic returns the IEC 60664-1 Table F.4 basic-insulation creepage
// (mm) for a working voltage (Vrms), pollution degree and material group.
// Linear interpolation between the nearest two rows is permitted (IEC 62368-1
// Table 17 note; IEC 60664-1 §5.2.2.2); the result is rounded up to the next
// 0.1 mm as those notes require for an interpolated value. pwb selects the
// printed-wiring-material columns (PD1/PD2 only).
func CreepageBasic(vrms float64, pd int, mg string, pwb bool) float64 {
	pd = clampInt(pd, 1, 3)
	m := materialGroupIndex(mg)
	v := math.Abs(vrms)
	if v <= creepageF4[0].v {
		return creepageF4[0].value(pd, m, pwb)
	}
	for i := 1; i < len(creepageF4); i++ {
		a, b := creepageF4[i-1], creepageF4[i]
		if v <= b.v {
			if v == b.v {
				return b.value(pd, m, pwb)
			}
			va, vb := a.value(pd, m, pwb), b.value(pd, m, pwb)
			if pwb && b.pwb1 == 0 {
				va, vb = a.value(pd, m, false), b.value(pd, m, false)
			}
			t := (v - a.v) / (b.v - a.v)
			return ceilTo(va+t*(vb-va), 0.1)
		}
	}
	return creepageF4[len(creepageF4)-1].value(pd, m, false)
}

// ---- IEC 60601-1 Table 12 — MOPP creepage and clearance --------------------

// moppTable — IEC 60601-1 ed.3.1 Table 12, "Minimum creepage distances and air
// clearances providing means of patient protection": working voltage bound
// (V DC and V rms), then 1 MOPP creepage/clearance and 2 MOPP
// creepage/clearance (mm). Pollution degree 2, material group III assumed by
// the standard.
var moppTable = []struct {
	vdc, vrms      float64
	creep1, clear1 float64
	creep2, clear2 float64
}{
	{17, 12, 1.7, 0.8, 3.4, 1.6},
	{43, 30, 2, 1, 4, 2},
	{85, 60, 2.3, 1.2, 4.6, 2.4},
	{177, 125, 3, 1.6, 6, 3.2},
	{354, 250, 4, 2.5, 8, 5},
	{566, 400, 6, 3.5, 12, 7},
	{707, 500, 8, 4.5, 16, 9},
	{934, 660, 10.5, 6, 21, 12},
	{1061, 750, 12, 6.5, 24, 13},
	{1414, 1000, 16, 9, 32, 18},
}

// MOPPDistances returns the Table 12 creepage and clearance (mm) for a
// working voltage (Vrms and V peak/DC) and 1 or 2 MOPP. Step table: the row
// whose DC bound covers the peak and whose rms bound covers the rms value
// (conservative; above 1000 Vrms the last row is scaled and flagged).
func MOPPDistances(vrms, vpeak float64, count int) (creepMM, clearMM float64, inRange bool) {
	for _, r := range moppTable {
		if vrms <= r.vrms+1e-9 && vpeak <= r.vdc+1e-9 {
			if count >= 2 {
				return r.creep2, r.clear2, true
			}
			return r.creep1, r.clear1, true
		}
	}
	last := moppTable[len(moppTable)-1]
	k := math.Max(vrms/last.vrms, vpeak/last.vdc)
	if count >= 2 {
		return last.creep2 * k, last.clear2 * k, false
	}
	return last.creep1 * k, last.clear1 * k, false
}

// ---- IEC 60601-1 Table 6 — dielectric strength test voltages ---------------

// MedicalTestVoltage returns the IEC 60601-1 ed.3.1 Table 6 test voltage
// (Vrms) for the pinned range peak working voltage ≤ 354 V (250 Vrms mains):
// MOPP 1500 / 4000 Vrms, MOOP (protection from the mains part) 1500 / 3000
// Vrms. Outside that range it returns 0: consult Table 6 (formula rows).
func MedicalTestVoltage(mop string, count int, vpeak float64) float64 {
	if vpeak > 354 {
		return 0
	}
	if mop == "MOOP" {
		if count >= 2 {
			return 3000
		}
		return 1500
	}
	if count >= 2 {
		return 4000
	}
	return 1500
}

// ---- IEC 62368-1 Table 4 — electrical energy source classes ----------------

// ES class limits (IEC 62368-1:2018 Table 4, steady state, ≤ 1 kHz):
// ES1 ≤ 30 Vrms / 42.4 V peak / 60 V DC; ES2 ≤ 50 Vrms / 70.7 V peak / 120 V DC.
const (
	es1Vrms, es1Vpk, es1Vdc = 30.0, 42.4, 60.0
	es2Vrms, es2Vpk, es2Vdc = 50.0, 70.7, 120.0
)

// ESClass classifies a voltage source by IEC 62368-1 Table 4. dc marks a DC
// (or DC-dominated) source; otherwise both the rms and the peak limits apply.
func ESClass(vrms, vpeak float64, dc bool) string {
	switch {
	case dc && vpeak <= es1Vdc, !dc && vrms <= es1Vrms && vpeak <= es1Vpk:
		return "ES1"
	case dc && vpeak <= es2Vdc, !dc && vrms <= es2Vrms && vpeak <= es2Vpk:
		return "ES2"
	}
	return "ES3"
}

// ---- helpers ---------------------------------------------------------------

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func ceilTo(v, step float64) float64 {
	return math.Round(math.Ceil(v/step-1e-9)*step*1e6) / 1e6
}

func normMG(mg string) string {
	switch mg {
	case "I", "i", "1":
		return "I"
	case "II", "ii", "2":
		return "II"
	case "IIIb", "IIIB", "iiib", "3b":
		return "IIIb"
	}
	return "IIIa"
}
