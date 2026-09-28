package app

import (
	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// `intent derive` takes its creepage/clearance/slot numbers from the full
// standards tables in pkg/safety (IPC-2221B, IEC 62368-1, IEC 60601-1
// MOOP/MOPP, IEC 61010-1) instead of pkg/intent's placeholder. The hook lives
// here so pkg/intent and pkg/safety stay independent of each other.
func init() {
	intent.SafetyProvider = func(p intent.Pair, st intent.Standard) (float64, float64, bool, float64, string, []string) {
		r := safety.Distances(safety.Pair{
			A: p.A, B: p.B, WorkingVrms: p.WorkingVrms, WorkingVpeak: p.WorkingVpeak,
			Insulation: p.Insulation, MOP: p.MOP, MOPCount: p.MOPCount, RequiredWithstandV: p.RequiredWithstandV,
			Transient: p.Transient, MainsVrms: p.MainsVrms,
		}, safety.Standard{
			Name: st.Name, Insulation: st.Insulation, MOP: st.MOP, MOPCount: st.MOPCount,
			PollutionDegree: st.PollutionDegree, MaterialGroup: st.MaterialGroup,
			AltitudeM: st.AltitudeM, OvervoltageCategory: st.OvervoltageCategory, Coated: st.Coated,
		})
		return r.ClearanceMm, r.CreepageMm, r.SlotRequired, r.SlotWidthMm, r.Ref, r.Why
	}
}
