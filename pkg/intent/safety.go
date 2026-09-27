package intent

import (
	"fmt"
	"math"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// SafetyProvider, when set, supplies the creepage/clearance/slot numbers for
// an insulation pair. It is the single integration point for the full
// standards tables (pkg/safety): `intent.SafetyProvider = safety.Distances`
// (the signature matches SafetyDistances). nil = the built-in placeholder.
var SafetyProvider func(p Pair, st Standard) (clearanceMm, creepageMm float64, slotRequired bool, slotWidthMm float64, ref string, why []string)

// SafetyDistances returns the insulation distances for a pair under a
// standard. Every pair in intent.json gets its numbers from this one call.
func SafetyDistances(p Pair, st Standard) (clearanceMm, creepageMm float64, slotRequired bool, slotWidthMm float64, ref string, why []string) {
	if SafetyProvider != nil {
		return SafetyProvider(p, st)
	}
	return placeholderDistances(p, st)
}

// placeholderSlotGapMm is the surface distance a standard bridge footprint
// (SOP/DIP optocoupler, small transformer) gives between its pad rows. A
// creepage beyond it cannot be met on the surface: mill a slot.
const placeholderSlotGapMm = 5.0

// placeholderDistances: pcbauto.InsulationDistances (IEC 60664-1 / 62368-1
// PD2, MG III engineering defaults, reinforced = 2× basic) adjusted for
// pollution degree, altitude and IEC 60601-1 MOPP minimums.
func placeholderDistances(p Pair, st Standard) (float64, float64, bool, float64, string, []string) {
	var why []string
	v := p.WorkingVrms
	if v <= 0 {
		v = p.WorkingVpeak / math.Sqrt2
	}
	grade := p.Insulation
	switch grade {
	case "double":
		grade = "reinforced"
	case "supplementary":
		grade = "basic"
	}
	creep, clear := pcbauto.InsulationDistances(v, grade)
	why = append(why, fmt.Sprintf("pcbauto.InsulationDistances(%s Vrms, %s): creepage %.2f mm, clearance %.2f mm (PD2, MG III, ≤2000 m)", trimFloat(v, 1), grade, creep, clear))
	switch st.PollutionDegree {
	case 1:
		creep *= 0.5
		why = append(why, "pollution degree 1: creepage ×0.5")
	case 3:
		creep *= 1.6
		why = append(why, "pollution degree 3: creepage ×1.6")
	case 4:
		creep *= 2.0
		why = append(why, "pollution degree 4: creepage ×2")
	}
	if st.AltitudeM > 2000 {
		// IEC 60664-1 Table A.2 altitude correction factors.
		f := 1.0
		switch {
		case st.AltitudeM <= 3000:
			f = 1.14
		case st.AltitudeM <= 4000:
			f = 1.29
		default:
			f = 1.48
		}
		clear *= f
		why = append(why, fmt.Sprintf("altitude %.0f m: clearance ×%.2f (IEC 60664-1 Table A.2)", st.AltitudeM, f))
	}
	if st.MOP == "MOPP" && p.MOPCount > 0 && v <= 250 {
		mc, mr := 2.5, 4.0
		if p.MOPCount >= 2 {
			mc, mr = 5.0, 8.0
		}
		if clear < mc || creep < mr {
			why = append(why, fmt.Sprintf("IEC 60601-1 Table 12: %d MOPP ≤ 250 Vrms needs ≥ %.1f mm clearance / %.1f mm creepage", p.MOPCount, mc, mr))
		}
		clear, creep = math.Max(clear, mc), math.Max(creep, mr)
	}
	creep = math.Max(creep, clear) // creepage is never shorter than clearance
	slot := false
	slotW := 0.0
	if (grade == "reinforced" || grade == "basic") && creep > placeholderSlotGapMm {
		slot, slotW = true, 1.0
		why = append(why, fmt.Sprintf("creepage %.2f mm > %.1f mm a standard bridge footprint gives between its pad rows: mill a ≥ 1.0 mm slot under the bridge (pcb auto re-checks with the real pads)", creep, placeholderSlotGapMm))
	}
	ref := fmt.Sprintf("%s via IEC 60664-1 PD%d MG %s engineering default (placeholder until pkg/safety) — confirm", st.Name, st.PollutionDegree, st.MaterialGroup)
	return round(clear, 2), round(creep, 2), slot, slotW, ref, why
}
