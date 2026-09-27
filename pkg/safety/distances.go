// Package safety computes insulation distances (clearance, creepage, milled
// slots, dielectric test voltages) between two voltage domains from their
// real working voltages and the product safety standard.
//
// Supported standards:
//
//   - IPC-2221B Table 6-1 — functional conductor spacing (also used for
//     "functional" insulation under every IEC standard below).
//   - IEC 62368-1:2018 — ICT/AV equipment: clearance by peak working voltage
//     (procedure 1) and by required withstand / mains transient with the
//     overvoltage category (procedure 2), altitude correction, creepage by
//     rms working voltage, pollution degree and material group; reinforced /
//     double = one impulse step higher clearance and 2× creepage.
//   - IEC 60601-1 ed.3.x — medical: MOPP from Table 12 (1× / 2×) with the
//     Table 6 test voltages; MOOP via the IEC 62368-1 (IEC 60950-1 style)
//     insulation-coordination tables with pollution degree and material group.
//   - IEC 61010-1:2010 — laboratory / measurement equipment: basic and
//     reinforced by overvoltage category and working voltage.
//
// Every Result carries the table references and the caveat that the values
// are an engineering reference to be confirmed against the product standard
// edition and the certification lab.
package safety

import (
	"fmt"
	"math"
	"strings"
)

// Caveat is appended to every Result.Why.
const Caveat = "engineering reference — confirm against the product standard/certification lab"

// Standard mirrors intent.json "standard".
type Standard struct {
	Name                string  `json:"name"`       // IPC-2221B | IEC62368-1 | IEC60601-1 | IEC61010-1
	Insulation          string  `json:"insulation"` // functional | basic | supplementary | double | reinforced
	MOP                 string  `json:"mop"`        // MOOP | MOPP | ""
	MOPCount            int     `json:"mopCount"`
	PollutionDegree     int     `json:"pollutionDegree"`
	MaterialGroup       string  `json:"materialGroup"` // I | II | IIIa | IIIb
	AltitudeM           float64 `json:"altitudeM"`
	OvervoltageCategory string  `json:"overvoltageCategory"` // I | II | III | IV
	Coated              bool    `json:"coated"`
}

// Pair is one insulation requirement between two domains (intent.json
// "pairs[]" plus optional geometry the caller may know).
type Pair struct {
	A            string  `json:"a,omitempty"`
	B            string  `json:"b,omitempty"`
	WorkingVrms  float64 `json:"workingVrms"`
	WorkingVpeak float64 `json:"workingVpeak"`
	Insulation   string  `json:"insulation,omitempty"`
	MOP          string  `json:"mop,omitempty"`
	MOPCount     int     `json:"mopCount,omitempty"`
	// Transient selects the transient overvoltage the insulation must
	// withstand: "mains" (a side is directly mains-connected: the
	// overvoltage category applies), "secondary" (both sides downstream of
	// an isolating transformer: one category lower), "none" (no mains
	// transients: battery / isolated DC). "" infers it: ES1-level voltages
	// → none, anything higher → mains (conservative).
	Transient string `json:"transient,omitempty"`
	// MainsVrms is the nominal mains line-to-neutral voltage the transient
	// is taken from (0 = the AC working voltage, else 230 V assumed).
	MainsVrms float64 `json:"mainsVrms,omitempty"`
	// RequiredWithstandV overrides the transient (V peak).
	RequiredWithstandV float64 `json:"requiredWithstandV,omitempty"`
	// Internal marks conductors on an inner layer (IPC B1 for functional
	// spacing; IEC creepage does not apply inside the laminate).
	Internal bool `json:"internal,omitempty"`
	// AvailableMm is the straight surface distance available between the
	// two domains' conductors across a bridge footprint (its pad rows);
	// 0 = unknown (the slot decision is then left to the board geometry).
	AvailableMm float64 `json:"availableMm,omitempty"`
	// BarrierSpanMm is the extent of the bridge's pad rows along the
	// barrier; with AvailableMm it sizes the slot length.
	BarrierSpanMm float64 `json:"barrierSpanMm,omitempty"`
}

// Result is the insulation requirement.
type Result struct {
	ClearanceMm  float64 `json:"clearanceMm"`
	CreepageMm   float64 `json:"creepageMm"`
	SlotRequired bool    `json:"slotRequired"`
	// SlotWidthMm is the minimum width X a groove/slot must have to count
	// as a break in the creepage path (IEC 60664-1 §6.2 / IEC 62368-1
	// §5.4.3: PD1 0.25 mm, PD2 1.0 mm, PD3 1.5 mm).
	SlotWidthMm float64 `json:"slotWidthMm"`
	// SlotLengthMm is the slot length along the barrier that makes the path
	// around its ends ≥ the creepage (needs AvailableMm and BarrierSpanMm).
	SlotLengthMm float64  `json:"slotLengthMm,omitempty"`
	Ref          string   `json:"ref"`
	Why          []string `json:"why"`

	Standard   string `json:"standard"`
	Insulation string `json:"insulation"` // effective grade
	MOP        string `json:"mop,omitempty"`
	MOPCount   int    `json:"mopCount,omitempty"`
	// ESClass is the IEC 62368-1 Table 4 class of the working voltage.
	ESClass            string  `json:"esClass,omitempty"`
	RequiredWithstandV float64 `json:"requiredWithstandV,omitempty"`
	AltitudeFactor     float64 `json:"altitudeFactor"`
	// TestVoltageVrms is the dielectric-strength test voltage (0 = not
	// tabulated here; TestVoltageRef says where to look).
	TestVoltageVrms float64 `json:"testVoltageVrms,omitempty"`
	TestVoltageRef  string  `json:"testVoltageRef,omitempty"`
	// Infeasible is set when the available geometry cannot meet the
	// requirement even with a slot (clearance is an air path; a slot that
	// does not fit between the pad rows cannot be milled).
	Infeasible bool     `json:"infeasible,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// Canonical standard names.
const (
	IPC2221B  = "IPC-2221B"
	IEC62368  = "IEC62368-1"
	IEC60601  = "IEC60601-1"
	IEC61010  = "IEC61010-1"
	slotCopMm = 0.3 // copper → milled slot edge (JLC routing tolerance, native "Slot Region" 0.3 mm)
)

// NormStandard maps spellings ("IEC 62368-1", "62368", "iec60601") to the
// canonical names; "" → IPC-2221B.
func NormStandard(name string) string {
	n := strings.ToUpper(strings.NewReplacer(" ", "", "_", "", ".", "").Replace(name))
	switch {
	case strings.Contains(n, "62368"):
		return IEC62368
	case strings.Contains(n, "60601"):
		return IEC60601
	case strings.Contains(n, "61010"):
		return IEC61010
	case strings.Contains(n, "60950"):
		return IEC62368 // successor; same insulation coordination basis
	}
	return IPC2221B
}

// NormInsulation canonicalises an insulation grade ("" stays "").
func NormInsulation(g string) string {
	switch strings.ToLower(strings.TrimSpace(g)) {
	case "functional", "operational":
		return "functional"
	case "basic":
		return "basic"
	case "supplementary":
		return "supplementary"
	case "double":
		return "double"
	case "reinforced":
		return "reinforced"
	}
	return ""
}

func strong(ins string) bool { return ins == "reinforced" || ins == "double" }

// parseOVC reads "I".."IV" / "1".."4" (default II).
func parseOVC(s string) int {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "I", "1":
		return 1
	case "III", "3":
		return 3
	case "IV", "4":
		return 4
	}
	return 2
}

// SlotMinWidth is the groove width X that counts as a creepage break for a
// pollution degree (IEC 60664-1 §6.2 dimension X; IEC 62368-1 §5.4.3).
func SlotMinWidth(pd int) float64 {
	switch clampInt(pd, 1, 3) {
	case 1:
		return 0.25
	case 3:
		return 1.5
	}
	return 1.0
}

// SlotExtension returns how far (mm) a slot of width w, centred between two
// pad rows gap apart (row face to row face), must extend past the end of
// the rows so that the surface path around its end — row corner → slot end
// → across the slot's end face → other row corner — is at least creep:
// 2·√(e² + ((gap−w)/2)²) + w ≥ creep.
func SlotExtension(creep, gap, w float64) float64 {
	half := (creep - w) / 2
	off := (gap - w) / 2
	if half <= off {
		return 0
	}
	return math.Sqrt(half*half - off*off)
}

// Distances computes the insulation distances for a pair of domains under a
// standard. Missing standard parameters take documented defaults (PD2, MG
// IIIa — standard FR-4 CTI ≥ 175 —, ≤ 2000 m, OVC II).
func Distances(p Pair, s Standard) Result {
	std := NormStandard(s.Name)
	pd := s.PollutionDegree
	if pd <= 0 {
		pd = 2
	}
	pd = clampInt(pd, 1, 3)
	mg := normMG(s.MaterialGroup)
	ovc := parseOVC(s.OvervoltageCategory)
	ins := NormInsulation(p.Insulation)
	if ins == "" {
		ins = NormInsulation(s.Insulation)
	}
	if ins == "" {
		if std == IPC2221B {
			ins = "functional"
		} else {
			ins = "basic"
		}
	}
	vrms, vpk := math.Abs(p.WorkingVrms), math.Abs(p.WorkingVpeak)
	dc := false
	switch {
	case vpk == 0 && vrms == 0:
	case vpk == 0:
		vpk = vrms * math.Sqrt2
	case vrms == 0:
		vrms, dc = vpk, true // a peak-only declaration is treated as DC (rms = peak, conservative for creepage)
	}
	if !dc && vrms > 0 && vpk/vrms < 1.05 {
		dc = true
	}
	r := Result{Standard: std, Insulation: ins, AltitudeFactor: AltitudeFactor(s.AltitudeM), ESClass: ESClass(vrms, vpk, dc)}
	why := func(f string, a ...any) { r.Why = append(r.Why, fmt.Sprintf(f, a...)) }
	why("working voltage %.0f Vrms / %.0f V peak (%s)", vrms, vpk, r.ESClass)

	switch {
	case std == IPC2221B || ins == "functional":
		ipcSpacing(&r, std, vpk, s, p.Internal)
	case std == IEC60601:
		medical(&r, p, s, vrms, vpk, pd, mg, ovc)
	case std == IEC61010:
		lab(&r, p, vrms, vpk, pd, mg, ovc, s.AltitudeM)
	default:
		ict(&r, p, vrms, vpk, pd, mg, ovc, s.AltitudeM)
	}
	r.ClearanceMm = round2(r.ClearanceMm)
	r.CreepageMm = round2(math.Max(r.CreepageMm, r.ClearanceMm))
	if r.Insulation != "functional" {
		r.SlotWidthMm = SlotMinWidth(pd)
	}
	slotDecision(&r, p)
	r.Why = append(r.Why, Caveat)
	return r
}

// ipcSpacing is IPC-2221B Table 6-1 (functional spacing).
func ipcSpacing(r *Result, std string, vpk float64, s Standard, internal bool) {
	col := IPCB2
	switch {
	case internal:
		col = IPCB1
	case s.Coated:
		col = IPCA5 // conformal-coated assembly (≥ B4 at every voltage)
	case s.AltitudeM > 3050:
		col = IPCB3
	}
	d := IPC2221BSpacing(vpk, col)
	r.ClearanceMm, r.CreepageMm = d, d
	r.Ref = fmt.Sprintf("IPC-2221B Table 6-1 column %s", col)
	if std != IPC2221B {
		r.Why = append(r.Why, fmt.Sprintf("functional insulation: %s sets no minimum distance for it (fault-tested instead) — IPC-2221B spacing used", std))
	}
	r.Why = append(r.Why, fmt.Sprintf("IPC-2221B Table 6-1 %s at %.0f V peak → %.2f mm", col, vpk, d))
}

// transient resolves the required withstand voltage (V peak) for a pair.
func transient(r *Result, p Pair, vrms, vpk float64, dc bool, ovc int) (float64, string) {
	if p.RequiredWithstandV > 0 {
		return p.RequiredWithstandV, "declared required withstand"
	}
	mode := strings.ToLower(p.Transient)
	inferred := false
	if mode == "" {
		inferred = true
		if vpk <= es1Vpk && vrms <= es1Vrms || dc && vpk <= es1Vdc {
			mode = "none"
		} else {
			mode = "mains"
		}
	}
	mains := p.MainsVrms
	if mains <= 0 {
		if !dc && vrms > 0 {
			mains = vrms
		} else {
			mains = 230
			if mode != "none" {
				r.Warnings = append(r.Warnings, "mains nominal voltage unknown: 230 Vrms assumed for the transient (set pair.mainsVrms)")
			}
		}
	}
	src := ""
	var v float64
	switch mode {
	case "none":
		return 0, "no mains transient (procedure 1 only)"
	case "secondary":
		o := max(ovc-1, 1)
		v = MainsTransientV(mains, o)
		src = fmt.Sprintf("secondary circuit: OVC %s (one below %s) at %.0f Vrms mains → %.0f V", roman(o), roman(ovc), mains, v)
	default:
		v = MainsTransientV(mains, ovc)
		src = fmt.Sprintf("mains transient OVC %s at %.0f Vrms → %.0f V (IEC 60664-1 Table F.1 / IEC 62368-1 Table 12)", roman(ovc), mains, v)
	}
	if inferred {
		src += " — transient inferred as mains-connected (conservative); set pair.transient=secondary|none if the circuit is isolated"
	}
	return v, src
}

// ict is IEC 62368-1:2018 §5.4.2 (clearance) and §5.4.3 (creepage).
func ict(r *Result, p Pair, vrms, vpk float64, pd int, mg string, ovc int, alt float64) {
	dc := r.ESClass != "" && vrms == vpk
	c1, c2, _ := insulationClearance(r, p, vrms, vpk, dc, pd, ovc, alt)
	cr := CreepageBasic(vrms, pd, mg, false)
	r.Why = append(r.Why, fmt.Sprintf("creepage basic %.2f mm at %.0f Vrms, PD%d, MG %s (IEC 62368-1 Table 17 / IEC 60664-1 Table F.4, interpolated, ↑0.1 mm)", cr, vrms, pd, mg))
	if strong(r.Insulation) {
		cr *= 2
		r.Why = append(r.Why, fmt.Sprintf("%s insulation: creepage = 2 × basic = %.2f mm (IEC 62368-1 §5.4.3)", r.Insulation, cr))
	}
	r.ClearanceMm = math.Max(c1, c2)
	r.CreepageMm = cr
	if r.CreepageMm < r.ClearanceMm {
		r.Why = append(r.Why, "creepage raised to the clearance (a creepage distance is never less than the clearance)")
	}
	r.Ref = "IEC 62368-1:2018 §5.4.2 (Tables 10/14, via IEC 60664-1 F.1/F.2), §5.4.3 (Table 17 via IEC 60664-1 F.4), Table 16 altitude"
	r.TestVoltageRef = "IEC 62368-1 §5.4.9 electric strength (Tables 25–27) — not computed here"
	switch r.ESClass {
	case "ES3":
		r.Why = append(r.Why, "ES3 source: an ordinary person needs basic + supplementary (double) or reinforced insulation to ES1 parts (IEC 62368-1 Table 4 / §4.3)")
	}
}

// insulationClearance computes procedure 1 (peak working voltage) and
// procedure 2 (required withstand) clearances with the altitude factor.
func insulationClearance(r *Result, p Pair, vrms, vpk float64, dc bool, pd, ovc int, alt float64) (c1, c2, basic float64) {
	k := AltitudeFactor(alt)
	v1 := vpk
	if strong(r.Insulation) {
		v1 = 1.6 * vpk // IEC 60664-1 §5.1.6: reinforced for steady-state = 160 %
	}
	c1 = ClearanceForWithstand(v1, pd) * k
	r.Why = append(r.Why, fmt.Sprintf("procedure 1 (peak working %.0f V%s): %.2f mm (IEC 60664-1 Table F.2, PD%d)", vpk, map[bool]string{true: " ×1.6 reinforced", false: ""}[strong(r.Insulation)], c1/k, pd))
	w, src := transient(r, p, vrms, vpk, dc, ovc)
	if w > 0 {
		wr := w
		if strong(r.Insulation) {
			wr = NextImpulseStep(w)
		}
		r.RequiredWithstandV = wr
		c2 = ClearanceForWithstand(wr, pd) * k
		basic = ClearanceForWithstand(w, pd) * k
		step := ""
		if wr != w {
			step = fmt.Sprintf(", %s → one impulse step higher %.0f V (IEC 60664-1 §5.1.6)", r.Insulation, wr)
		}
		r.Why = append(r.Why, fmt.Sprintf("procedure 2: %s%s → %.2f mm (IEC 60664-1 Table F.2, PD%d)", src, step, c2/k, pd))
	} else {
		r.Why = append(r.Why, "procedure 2: "+src)
		basic = ClearanceForWithstand(vpk, pd) * k
	}
	if k != 1 {
		r.Why = append(r.Why, fmt.Sprintf("altitude %.0f m: clearance × %.2f (IEC 60664-1 Table A.2 / IEC 62368-1 Table 16)", alt, k))
	}
	return
}

// medical is IEC 60601-1 ed.3.x §8.8–8.9.
func medical(r *Result, p Pair, s Standard, vrms, vpk float64, pd int, mg string, ovc int) {
	mop := strings.ToUpper(p.MOP)
	if mop == "" {
		mop = strings.ToUpper(s.MOP)
	}
	if mop != "MOOP" && mop != "MOPP" {
		mop = "MOPP"
		r.Why = append(r.Why, "means of protection not declared: MOPP assumed (the stricter; set pair.mop=MOOP for operator-only protection)")
	}
	n := p.MOPCount
	if n <= 0 {
		n = s.MOPCount
	}
	if n <= 0 {
		n = 1
		if strong(r.Insulation) {
			n = 2
		}
	}
	n = clampInt(n, 1, 2)
	r.MOP, r.MOPCount = mop, n
	if n == 2 {
		r.Insulation = "double"
	} else if r.Insulation != "supplementary" {
		r.Insulation = "basic"
	}
	if mop == "MOPP" {
		cr, cl, ok := MOPPDistances(vrms, vpk, n)
		k := AltitudeFactor(s.AltitudeM)
		r.CreepageMm, r.ClearanceMm = cr, cl*k
		r.Ref = "IEC 60601-1 ed.3.1 §8.9, Table 12 (MOPP)"
		r.Why = append(r.Why, fmt.Sprintf("%d × MOPP at %.0f Vrms / %.0f V peak: creepage %.1f mm, clearance %.1f mm (Table 12, next higher row)", n, vrms, vpk, cr, cl))
		if !ok {
			r.Warnings = append(r.Warnings, "working voltage beyond IEC 60601-1 Table 12 (1000 Vrms): values scaled from the last row — consult the standard")
		}
		if k != 1 {
			r.Why = append(r.Why, fmt.Sprintf("altitude %.0f m: clearance × %.2f (IEC 60601-1 Table 8 / IEC 60664-1 Table A.2)", s.AltitudeM, k))
		}
		if pd > 2 {
			r.Warnings = append(r.Warnings, "IEC 60601-1 Table 12 assumes pollution degree 2: PD3 needs sealing/coating or larger distances")
		}
	} else {
		// IEC 60601-1 §8.9.1.x: MOOP may be met by IEC 60950-1 / IEC 62368-1
		// insulation coordination.
		q := *r
		q.Insulation = map[bool]string{true: "reinforced", false: "basic"}[n == 2]
		ict(&q, p, vrms, vpk, pd, mg, ovc, s.AltitudeM)
		r.ClearanceMm, r.CreepageMm, r.RequiredWithstandV = q.ClearanceMm, q.CreepageMm, q.RequiredWithstandV
		r.Why = append(q.Why, fmt.Sprintf("%d × MOOP: operator protection per IEC 62368-1 insulation coordination (%s)", n, q.Insulation))
		r.Warnings = q.Warnings
		r.Ref = "IEC 60601-1 ed.3.1 §8.9 (MOOP via IEC 62368-1/60950-1 tables) + " + q.Ref
	}
	r.TestVoltageVrms = MedicalTestVoltage(mop, n, vpk)
	if r.TestVoltageVrms > 0 {
		r.TestVoltageRef = fmt.Sprintf("IEC 60601-1 ed.3.1 Table 6: %d × %s at peak working ≤ 354 V → %.0f Vrms", n, mop, r.TestVoltageVrms)
	} else {
		r.TestVoltageRef = "IEC 60601-1 Table 6 formula rows (peak working > 354 V) — not computed here"
	}
}

// lab is IEC 61010-1:2010 §6.7.
func lab(r *Result, p Pair, vrms, vpk float64, pd int, mg string, ovc int, alt float64) {
	dc := vrms == vpk
	strongIns := strong(r.Insulation)
	// Basic clearance first, then reinforced = max(2 × basic, one impulse step).
	r.Insulation = "basic"
	c1, c2, _ := insulationClearance(r, p, vrms, vpk, dc, pd, ovc, alt)
	basic := math.Max(c1, c2)
	cr := CreepageBasic(vrms, pd, mg, false)
	r.Why = append(r.Why, fmt.Sprintf("creepage basic %.2f mm at %.0f Vrms, PD%d, MG %s (IEC 61010-1 §6.7.3 via IEC 60664-1 Table F.4)", cr, vrms, pd, mg))
	r.ClearanceMm, r.CreepageMm = basic, cr
	if strongIns {
		r.Insulation = "reinforced"
		step := basic
		if r.RequiredWithstandV > 0 {
			step = ClearanceForWithstand(NextImpulseStep(r.RequiredWithstandV), pd) * AltitudeFactor(alt)
		}
		r.ClearanceMm = math.Max(2*basic, step)
		r.CreepageMm = 2 * cr
		r.Why = append(r.Why, fmt.Sprintf("reinforced: clearance max(2 × basic, one impulse step) = %.2f mm, creepage 2 × basic = %.2f mm (IEC 61010-1 §6.7.1)", r.ClearanceMm, r.CreepageMm))
	}
	r.Ref = "IEC 61010-1:2010 §6.7 (Table 4 / Annex K) via IEC 60664-1 F.1/F.2/F.4, Table A.2 altitude"
	r.TestVoltageRef = "IEC 61010-1 §6.8 / Table 5 — not computed here"
}

// slotDecision compares the available straight surface distance with the
// requirement and sizes the slot.
func slotDecision(r *Result, p Pair) {
	if p.AvailableMm <= 0 {
		if r.Insulation != "functional" {
			r.Why = append(r.Why, "slot need decided on the board: a milled slot ≥ the listed width is required under a bridge part whose pad rows are closer than the creepage")
		}
		return
	}
	g := p.AvailableMm
	if g >= r.CreepageMm {
		r.Why = append(r.Why, fmt.Sprintf("available surface distance %.2f mm ≥ creepage %.2f mm: no slot needed", g, r.CreepageMm))
		return
	}
	r.SlotRequired = true
	if g < r.ClearanceMm {
		r.Infeasible = true
		r.Warnings = append(r.Warnings, fmt.Sprintf("available %.2f mm < clearance %.2f mm: a slot does not lengthen the air path — choose a wider-body part or more spacing", g, r.ClearanceMm))
	}
	w := r.SlotWidthMm
	if w <= 0 {
		w = 1.0
	}
	if g-2*slotCopMm < w {
		r.Infeasible = true
		r.Warnings = append(r.Warnings, fmt.Sprintf("a %.2f mm slot plus %.1f mm copper keep-back on each side does not fit in the %.2f mm gap — choose a wider-body part", w, slotCopMm, g))
		return
	}
	e := SlotExtension(r.CreepageMm, g, w)
	r.Why = append(r.Why, fmt.Sprintf("available %.2f mm < creepage %.2f mm: mill a ≥ %.2f mm slot between the pad rows, extending %.2f mm past each row end", g, r.CreepageMm, w, e))
	if p.BarrierSpanMm > 0 {
		r.SlotLengthMm = ceilTo(p.BarrierSpanMm+2*e, 0.1)
		r.Why = append(r.Why, fmt.Sprintf("slot length along the barrier %.1f mm (row span %.2f + 2 × %.2f)", r.SlotLengthMm, p.BarrierSpanMm, e))
	}
}

func roman(n int) string {
	return [...]string{"", "I", "II", "III", "IV"}[clampInt(n, 1, 4)]
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
