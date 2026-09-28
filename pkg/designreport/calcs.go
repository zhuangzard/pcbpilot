package designreport

import (
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

const milPerOz = 1.378

func (c *ctx) buildCalcs() {
	it := c.in.Intent
	if it == nil {
		c.missing("5 工程计算", "intent.json 未提供（线宽/过孔/间距/阻抗/绝缘的计划值来自 intent）")
		return
	}
	cu := it.Copper
	if cu == nil {
		cu = &intent.Copper{Layers: 2, OuterOz: 1, InnerOz: 0.5, TempRiseC: 10, MinTrackMil: 6, ClearanceMil: 6, ViaDrillMil: 12, ViaDiaMil: 24}
	}
	cs := &CalcSection{}
	cs.Basis = []KV{
		{"外层/内层铜厚", sprintf("%g oz (%s mil) / %g oz (%s mil)", cu.OuterOz, f3(cu.OuterOz*milPerOz), cu.InnerOz, f3(cu.InnerOz*milPerOz)), "1 oz = 1.378 mil"},
		{"允许温升 ΔT", sprintf("%g °C", cu.TempRiseC), ""},
		{"叠层", cu.Stackup, ""},
		{"外层→参考平面 h", sprintf("%g mil", cu.RefHeightMil), ""},
		{"介电常数 εr", sprintf("%g", cu.Er), ""},
		{"工艺最小线宽/间距", sprintf("%g / %g mil", cu.MinTrackMil, cu.ClearanceMil), ""},
	}
	if it.Copper == nil {
		cs.Notes = append(cs.Notes, "intent.json 无 copper 段：按默认 1 oz/0.5 oz、ΔT 10 °C 计算")
	}
	cs.Formulas = []KV{
		{"IPC-2221/2152 载流", "I = 0.048 · ΔT^0.44 · A^0.725，A = w · t（mil²）；内层按 IPC-2152 用同一曲线、内层铜厚", "IPC-2221B §6.2 外层曲线；IPC-2152 内外层同截面载流相近（与 intent/pcbauto 一致）"},
		{"单过孔载流", "I = 0.024 · ΔT^0.44 · (π·(d+t)·t)^0.725，镀铜 t = 0.7 mil", "孔壁按内层导体计"},
		{"电压间距", "IPC-2221B 表 6-1（B1 内层 / B2 外层未涂覆 / B4 涂覆），与工艺最小间距取大", ""},
		{"微带线 Z0", "Hammerstad–Jensen（含铜厚修正）", "pkg/pcbauto MicrostripZ0"},
		{"差分 Zdiff", "Zdiff ≈ 2·Z0·(1 − 0.48·e^(−0.96·s/h))", "边耦合微带近似"},
		{"爬电/电气间隙", "IEC 62368-1 / 60601-1 / 61010-1 表格或 IPC-2221B（按 intent.standard）", "pkg/safety；工程参考，最终以认证机构为准"},
	}
	// Plan widths per net: widths / vias / clearance for power, ground, switch,
	// diff, hs, rf and any net with ≥ 10 mA.
	for _, net := range sortedKeys(it.Nets) {
		np := it.Nets[net]
		interesting := np.Role != "signal" || np.CurrentA >= 0.01
		if !interesting {
			continue
		}
		cur := np.CurrentA
		if cur > 0 {
			on := pcbauto.TraceWidthForCurrent(cur, cu.TempRiseC, cu.OuterOz, false)
			inn := pcbauto.TraceWidthForCurrent(cur, cu.TempRiseC, cu.InnerOz, false) // IPC-2152: inner ≈ outer per cross-section
			wc := WidthCalc{Net: net, Class: np.NetClass, CurrentA: cur, Source: np.CurrentSource, OuterNeedMil: round(on, 2), InnerNeedMil: round(inn, 2),
				OuterPlanMil: np.WidthMil.Outer, InnerPlanMil: np.WidthMil.Inner}
			wc.OuterCapA = round(pcbauto.CurrentForWidth(np.WidthMil.Outer, cu.TempRiseC, cu.OuterOz, false), 3)
			wc.Status = StatusPass
			if np.WidthMil.Outer+1e-6 < on || np.WidthMil.Inner+1e-6 < inn {
				wc.Status = StatusFail
			}
			cs.Widths = append(cs.Widths, wc)
			drill := cu.ViaDrillMil
			for _, ncl := range it.NetClasses {
				if ncl.Name == np.NetClass && ncl.ViaDrillMil > 0 {
					drill = ncl.ViaDrillMil
				}
			}
			plating := pcbauto.DefaultViaPlatingMil
			if cu.ViaPlatingMil > 0 {
				plating = cu.ViaPlatingMil
			}
			if np.Via != nil && np.Via.DrillMil > 0 {
				// The net's own current-sized via (intent via block).
				drill = np.Via.DrillMil
			}
			per := pcbauto.ViaAmpacity(drill, plating, cu.TempRiseC)
			need := int(math.Ceil(cur / per))
			if need < 1 {
				need = 1
			}
			vc := ViaCalc{Net: net, CurrentA: cur, DrillMil: drill, PerViaA: round(per, 3), Need: need, Plan: np.ViasPerTransition, Status: StatusPass}
			if np.ViasPerTransition < need {
				vc.Status = StatusFail
			}
			cs.Vias = append(cs.Vias, vc)
		}
		vpk := math.Max(np.Voltage.Peak, np.Voltage.Max)
		ipc := pcbauto.ClearanceForVoltage(vpk, true, it.Standard.Coated)
		cc := ClearCalc{Net: net, VPeak: round(vpk, 3), IPCMil: round(ipc, 2), FabMil: cu.ClearanceMil, PlanMil: np.ClearanceMil, Status: StatusPass}
		cc.Governs = "工艺最小值"
		if ipc > cu.ClearanceMil {
			cc.Governs = "IPC-2221B 电压"
		}
		if np.ClearanceMil+1e-6 < math.Max(ipc, cu.ClearanceMil) {
			cc.Status = StatusFail
		}
		cs.Clearance = append(cs.Clearance, cc)
	}
	// Impedance: recompute Z from the planned width/gap on the intent stackup.
	t := cu.OuterOz * milPerOz
	if cu.RefHeightMil > 0 && cu.Er > 0 {
		for _, ncl := range it.NetClasses {
			if ncl.ImpedanceOhm <= 0 {
				continue
			}
			ic := ImpCalc{Name: ncl.Name, Nets: strings.Join(ncl.Nets, ", "), WMil: ncl.TrackMil, HMil: cu.RefHeightMil, TMil: round(t, 3), Er: cu.Er, Target: ncl.ImpedanceOhm}
			z0 := pcbauto.MicrostripZ0(ncl.TrackMil, cu.RefHeightMil, t, cu.Er)
			ic.Z0 = round(z0, 1)
			got := z0
			if ncl.DiffGapMil > 0 {
				ic.Kind, ic.GapMil = "diff", ncl.DiffGapMil
				got = pcbauto.DiffZ(ncl.TrackMil, ncl.DiffGapMil, cu.RefHeightMil, t, cu.Er)
				ic.Zdiff = round(got, 1)
			} else {
				ic.Kind = "single"
			}
			ic.DevPct = round((got-ncl.ImpedanceOhm)/ncl.ImpedanceOhm*100, 1)
			ic.Status = StatusPass
			if math.Abs(ic.DevPct) > 10 {
				ic.Status = StatusFail
			} else if math.Abs(ic.DevPct) > 5 {
				ic.Status = StatusWarn
			}
			cs.Impedance = append(cs.Impedance, ic)
		}
	}
	if pl := c.in.Plan; pl != nil && pl.Result != nil && pl.Result.Stackup != nil {
		st := pl.Result.Stackup
		if (st.Er > 0 && math.Abs(st.Er-cu.Er) > 0.01) || (st.RefHeightMil > 0 && math.Abs(st.RefHeightMil-cu.RefHeightMil) > 0.05) {
			cs.Notes = append(cs.Notes, sprintf("pcb auto 叠层参数 εr %g / h %g mil 与 intent（εr %g / h %g mil）不同：阻抗线宽按 intent 求解，下单叠层须与 intent 一致", st.Er, st.RefHeightMil, cu.Er, cu.RefHeightMil))
		}
	}
	// Insulation pairs.
	std := safety.Standard{Name: it.Standard.Name, Insulation: it.Standard.Insulation, MOP: it.Standard.MOP, MOPCount: it.Standard.MOPCount,
		PollutionDegree: it.Standard.PollutionDegree, MaterialGroup: it.Standard.MaterialGroup, AltitudeM: it.Standard.AltitudeM,
		OvervoltageCategory: it.Standard.OvervoltageCategory, Coated: it.Standard.Coated}
	for _, p := range it.Pairs {
		res := safety.Distances(safety.Pair{A: p.A, B: p.B, WorkingVrms: p.WorkingVrms, WorkingVpeak: p.WorkingVpeak, Insulation: p.Insulation, MOP: p.MOP, MOPCount: p.MOPCount}, std)
		cs.Insulation = append(cs.Insulation, InsCalc{A: p.A, B: p.B, WorkingVrms: p.WorkingVrms, WorkingVpeak: p.WorkingVpeak, Insulation: p.Insulation,
			ClearanceMm: p.ClearanceMm, CreepageMm: p.CreepageMm, Slot: p.SlotRequired, SlotWidthMm: p.SlotWidthMm, StandardRef: p.StandardRef,
			TestVrms: res.TestVoltageVrms, TestRef: res.TestVoltageRef})
	}
	sort.SliceStable(cs.Widths, func(i, j int) bool { return cs.Widths[i].CurrentA > cs.Widths[j].CurrentA })
	sort.SliceStable(cs.Vias, func(i, j int) bool { return cs.Vias[i].CurrentA > cs.Vias[j].CurrentA })
	c.rep.Calcs = cs
}
