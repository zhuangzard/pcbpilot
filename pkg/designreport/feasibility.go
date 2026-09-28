package designreport

import (
	"math"
	"regexp"
	"sort"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// Guideline margins (percent of the rating that must stay unused).
const (
	GuideDefault  = 20.0      // currents, general
	GuideResistor = 50.0      // resistor power ≤ 50 % of the 70 °C rating
	GuideCapVolt  = 100.0 / 3 // MLCC rated V ≥ 1.5 × working V
	GuideStandoff = 0.0       // TVS/ESD VRWM ≥ working V
	GuideSupply   = 3.0       // supply within the recommended range with ≥ 3 % headroom
	GuideJunction = 20.0      // Tj ≤ 80 % of Tj,max (in °C)
)

// FeasGuidelines documents the thresholds in the report.
func FeasGuidelines() []KV {
	return []KV{
		{"电流（电感/稳压器/二极管/LED/BJT/连接器）", "余量 ≥ 20 %", "margin = (额定 − 应力)/额定"},
		{"电感 Ipk", "Isat ≥ 1.3 × Ipk 更稳妥", "库中只有单一 Current Rating 时同时比较 Ipk 与 Irms"},
		{"电阻功率", "P ≤ 50 % 额定（70 °C 额定值）", "0402 1/16 W、0603 1/10 W、0805 1/8 W、1206 1/4 W（通用厚膜）"},
		{"MLCC 直流电压", "额定 ≥ 1.5 × 工作电压（余量 ≥ 33 %）", "X5R/X7R < 2× 时须查 DC 偏压容量曲线"},
		{"ESD/TVS", "VRWM ≥ 线上最高工作电压", "余量 ≥ 0 即通过"},
		{"供电范围", "工作电压距推荐范围边界 ≥ 3 %", "下限 margin = (V − Vmin)/V"},
		{"结温", "Tj ≤ 80 % Tj,max", "Tj = Ta + P·θJA，θJA 未知 → 需数据手册"},
		{"未知额定", "标记“需数据手册”", "绝不猜测额定值"},
	}
}

func marginUpper(stress, rating float64) float64 { return (rating - stress) / rating * 100 }
func marginLower(stress, limit float64) float64 {
	if stress == 0 {
		return -100
	}
	return (stress - limit) / stress * 100
}

func status(m, guide float64) string {
	switch {
	case m < 0:
		return FeasOver
	case m < guide:
		return FeasMarginal
	}
	return FeasOK
}

func (c *ctx) feasRow(p *partInfo, check string, stress float64, unit, stressNote string, rating float64, ratingSrc string, lower bool, guide float64, note string) FeasRow {
	r := FeasRow{Ref: p.Ref, Device: p.Device, Kind: p.Kind, Check: check, Stress: round(stress, 6), Unit: unit, StressNote: stressNote, GuidePct: round(guide, 1), Note: note}
	if rating <= 0 {
		r.Status = FeasUnknown
		if r.Note == "" {
			r.Note = "需数据手册（额定值未知，不做猜测）"
		}
		return r
	}
	m := marginUpper(stress, rating)
	if lower {
		m = marginLower(stress, rating)
	}
	m = round(m, 1)
	r.Rating, r.RatingSource, r.MarginPct, r.Status = round(rating, 6), ratingSrc, &m, status(m, guide)
	return r
}

func libSrc(p *partInfo) string {
	if p.Lib == nil {
		return ""
	}
	return "power-models.json " + p.Lib.ID + " (" + p.Lib.Confidence + ")"
}

// descCurrent / descVoltage / descPower fall back to the part description
// attributes (LCSC "Current Rating", "Voltage Rating", "Power(Watts)").
const descSrc = "器件描述属性（LCSC）"

func descCurrent(p *partInfo) (float64, string) {
	if p.Desc != nil && p.Desc.CurrentA > 0 {
		return p.Desc.CurrentA, descSrc + " Current Rating"
	}
	return 0, ""
}

func descVoltage(p *partInfo) (float64, string) {
	if p.Desc != nil && p.Desc.VoltageV > 0 {
		return p.Desc.VoltageV, descSrc + " Voltage Rating"
	}
	return 0, ""
}

func ratingsOf(p *partInfo) *Ratings {
	if p.Lib != nil && p.Lib.Ratings != nil {
		return p.Lib.Ratings
	}
	return &Ratings{}
}

var reClassII = regexp.MustCompile(`^(X5R|X7R|X6S|X7S|X7T|X5S|X7U|Y5V)$`)

func (c *ctx) buildFeasibility() {
	if c.src == "" {
		c.missing("4 器件可行性", "无电气数据（需要 sim.json 或 intent.json）")
		return
	}
	fs := &FeasibilitySection{Counts: map[string]int{}, Guidelines: FeasGuidelines()}
	w := c.worst
	partW := func(ref string) (float64, string) {
		if w == nil {
			return 0, ""
		}
		if pr := w.Parts[ref]; pr != nil {
			return pr.PowerW, pr.Scenario
		}
		return 0, ""
	}
	ripple := func(key string) *powersim.RippleEntry {
		if w == nil {
			return nil
		}
		return w.Ripple[key]
	}
	add := func(r FeasRow) { fs.Rows = append(fs.Rows, r) }
	for _, ref := range c.refs {
		p := c.parts[ref]
		rt := ratingsOf(p)
		i := c.partCurrent(ref)
		switch p.Kind {
		case powersim.KindResistor:
			pw, sc := partW(ref)
			note := ""
			if pw == 0 && p.Res != nil && p.Res.Ohm > 0 {
				pw = i * i * p.Res.Ohm
				note = "P = I²R（仿真未给功率）"
			}
			rating, src := 0.0, ""
			if p.Desc != nil && p.Desc.PowerW > 0 {
				rating, src = p.Desc.PowerW, descSrc+" Power(Watts)"
			} else if p.Res != nil && p.Res.Package != "" {
				rating = ResistorPackageW[p.Res.Package]
				src = p.Res.Package + " 通用厚膜额定（70 °C）；" + p.Res.Decoder
			}
			sn := ""
			if p.Res != nil && p.Res.Ohm > 0 {
				sn = fmtSI(p.Res.Ohm, "Ω")
			}
			if sc != "" {
				sn += " @" + sc
			}
			add(c.feasRow(p, "功率 P vs 封装额定", pw, "W", sn, rating, src, false, GuideResistor, note))
		case powersim.KindCapacitor:
			ns := p.nets()
			v := 0.0
			if len(ns) == 2 {
				v = c.vAcross(ns[0], ns[1])
			} else if len(ns) == 1 {
				v = c.vAcross(ns[0], "")
			}
			rating, src, note, sn := 0.0, "", "", ""
			if p.Cap == nil {
				rating, src = descVoltage(p)
			}
			if p.Cap != nil {
				rating = p.Cap.RatedV
				src = p.Cap.Decoder
				if p.Cap.Farad > 0 {
					sn = fmtSI(p.Cap.Farad, "F") + " " + p.Cap.Dielectric + " " + p.Cap.Package
				}
				if rating == 0 {
					rating, src = descVoltage(p)
				}
				if rating > 0 && reClassII.MatchString(p.Cap.Dielectric) && rating < 2*v {
					note = "额定 < 2× 工作电压：II 类介质 DC 偏压会显著降低有效容量，请查厂家曲线"
				}
			}
			add(c.feasRow(p, "直流电压 V vs 额定", v, "V", sn, rating, src, false, GuideCapVolt, note))
			if re := ripple(ref); re != nil && re.IRmsA > 0 {
				add(c.feasRow(p, "纹波电流 Irms", re.IRmsA, "A", "稳压器 "+re.Regulator+" @"+re.Scenario, 0, "", false, GuideDefault,
					"需数据手册（MLCC 纹波额定按自发热 ΔT≤20 °C 曲线查询）"))
			}
		case powersim.KindInductor:
			rated, src := rt.IsatA, "ratings.isatA"
			if rated == 0 && p.Lib != nil && p.Lib.MaxA > 0 {
				rated, src = p.Lib.MaxA, libSrc(p)+" maxA（单一 Current Rating）"
			}
			if rated == 0 {
				rated, src = descCurrent(p)
			}
			if re := ripple(ref); re != nil && re.IPeakA > 0 {
				add(c.feasRow(p, "峰值电流 Ipk vs Isat", re.IPeakA, "A", sprintf("ΔI %s, D %s @%s", fA(re.DeltaIA), f3(re.Duty), re.Scenario), rated, src, false, GuideDefault, ""))
				ir, isrc := rt.IratedA, "ratings.iratedA"
				if ir == 0 && p.Lib != nil && p.Lib.MaxA > 0 {
					ir, isrc = p.Lib.MaxA, libSrc(p)+" maxA"
				}
				if ir == 0 {
					ir, isrc = descCurrent(p)
				}
				add(c.feasRow(p, "有效值 Irms vs Irated", re.IRmsA, "A", "", ir, isrc, false, GuideDefault, ""))
			} else {
				add(c.feasRow(p, "直流电流 vs 额定", i, "A", "", rated, src, false, GuideDefault, ""))
			}
		case powersim.KindBuck, powersim.KindLDO:
			c.regulatorRows(p, rt, add)
		case powersim.KindDiode, powersim.KindESD:
			if p.Kind == powersim.KindESD || (p.Lib != nil && p.Lib.Kind == powersim.KindESD) {
				c.esdRows(p, rt, add)
				continue
			}
			rated, src := 0.0, ""
			if p.Lib != nil && p.Lib.MaxA > 0 {
				rated, src = p.Lib.MaxA, libSrc(p)
			} else {
				rated, src = descCurrent(p)
			}
			pw, sc := partW(ref)
			sn := ""
			if i > 0 && pw > 0 {
				sn = sprintf("Vf≈%s, P %s @%s", fV(pw/i), fW(pw), sc)
			}
			add(c.feasRow(p, "正向电流 If vs 额定", i, "A", sn, rated, src, false, GuideDefault, ""))
			a, k := p.pinNet("A", "ANODE"), p.pinNet("K", "CATHODE")
			if a != "" && k != "" {
				vr, how := c.reverseV(a, k)
				vrr, vsrc := rt.VrrmV, "ratings.vrrmV — "+rt.Source
				if vrr == 0 {
					vrr, vsrc = descVoltage(p)
				}
				add(c.feasRow(p, "反向电压 VR vs VRRM", vr, "V", how, vrr, vsrc, false, GuideDefault, ""))
			}
			add(c.feasRow(p, "耗散功率 P", pw, "W", "", 0, "", false, GuideDefault, "需数据手册（封装热阻/功率降额）"))
		case powersim.KindLED:
			rated, src := 0.0, ""
			if p.Lib != nil && p.Lib.MaxA > 0 {
				rated, src = p.Lib.MaxA, libSrc(p)
			}
			add(c.feasRow(p, "正向电流 If vs IF,max", i, "A", "", rated, src, false, GuideDefault, ""))
		case powersim.KindBJT:
			rated, src := 0.0, ""
			if p.Lib != nil && p.Lib.MaxA > 0 {
				rated, src = p.Lib.MaxA, libSrc(p)
			}
			add(c.feasRow(p, "集电极电流 Ic vs IC,max", i, "A", "", rated, src, false, GuideDefault, ""))
		case powersim.KindSource, powersim.KindConnector:
			ca, csrc := rt.ContactA, "ratings.contactA — "+rt.Source
			if ca == 0 {
				ca, csrc = descCurrent(p)
			}
			add(c.feasRow(p, "接触电流 vs 额定", i, "A", "", ca, csrc, false, GuideDefault, ""))
			if rt.SourceBudgetA > 0 {
				add(c.feasRow(p, "供电预算 I vs 端口额定", i, "A", "", rt.SourceBudgetA, "ratings.sourceBudgetA — "+rt.Source, false, GuideDefault, ""))
			}
		case powersim.KindSwitch:
			ca, csrc := rt.ContactA, "ratings.contactA — "+rt.Source
			if ca == 0 {
				ca, csrc = descCurrent(p)
			}
			add(c.feasRow(p, "触点电流 vs 额定", i, "A", "", ca, csrc, false, GuideDefault, ""))
		case powersim.KindLoad, powersim.KindICSmall:
			c.loadRows(p, rt, add)
		case powersim.KindFerrite, powersim.KindFuse:
			rated, src := 0.0, ""
			if p.Lib != nil && p.Lib.MaxA > 0 {
				rated, src = p.Lib.MaxA, libSrc(p)
			}
			add(c.feasRow(p, "电流 vs 额定", i, "A", "", rated, src, false, GuideDefault, ""))
		case "testpoint", powersim.KindIgnore:
		default:
			if len(p.Pins) > 0 {
				add(c.feasRow(p, "无功率模型", i, "A", "", 0, "", false, GuideDefault, "需数据手册（未匹配功率模型，无额定值）"))
			}
		}
	}
	for _, r := range fs.Rows {
		fs.Counts[r.Status]++
	}
	c.rep.Feasibility = fs
}

// reverseV is the largest V(k) − V(a) (≥ 0). When the anode rail comes from
// a pluggable input (connector-source), the input can be unplugged and the
// anode taken as 0 V: VR = V(K)max.
func (c *ctx) reverseV(a, k string) (float64, string) {
	ek := c.elec[k]
	for _, pr := range c.tree.producers[a] {
		if c.tree.kind[pr] == "source" && ek != nil {
			return ek.VMax, "输入 " + pr + " 拔出时阳极按 0 V：VR = V(" + k + ")max"
		}
	}
	m := 0.0
	if len(c.scen) > 0 {
		for _, r := range c.scen {
			na, nk := r.Nets[a], r.Nets[k]
			if na != nil && nk != nil {
				m = math.Max(m, nk.Voltage-na.Voltage)
			}
		}
		return m, "各场景 V(K)−V(A) 最大值"
	}
	if ea := c.elec[a]; ea != nil && ek != nil {
		m = math.Max(0, ek.VMax-ea.VMin)
	}
	return m, "V(K)max − V(A)min"
}

func (c *ctx) regulatorRows(p *partInfo, rt *Ratings, add func(FeasRow)) {
	var iout, vinMax, vinMin, pw, vout float64
	vinMin = math.Inf(1)
	sc := ""
	for _, r := range c.scen {
		pr := r.Parts[p.Ref]
		if pr == nil || pr.Mode == "off" {
			continue
		}
		if pr.OutputA > iout {
			iout, sc = pr.OutputA, r.Scenario
		}
		vinMax = math.Max(vinMax, pr.VinV)
		if pr.VinV > 0 {
			vinMin = math.Min(vinMin, pr.VinV)
		}
		pw = math.Max(pw, pr.PowerW)
		vout = math.Max(vout, pr.VoutV)
	}
	if math.IsInf(vinMin, 1) {
		vinMin = 0
	}
	maxA, src := 0.0, ""
	if p.Lib != nil && p.Lib.MaxA > 0 {
		maxA, src = p.Lib.MaxA, libSrc(p)
	}
	add(c.feasRow(p, "输出电流 Iout vs 额定", iout, "A", "@"+sc, maxA, src, false, GuideDefault, ""))
	add(c.feasRow(p, "输入电压 Vin,max vs 绝对/推荐上限", vinMax, "V", "", rt.VinMaxV, "ratings.vinMaxV — "+rt.Source, false, GuideDefault/2, ""))
	if p.Kind == powersim.KindLDO {
		drop := 0.0
		if p.Lib != nil {
			drop = p.Lib.DropoutV
		}
		avail := vinMin - vout
		r := c.feasRow(p, "压差余量 (Vin,min−Vout) vs dropout", avail, "V", "", drop, libSrc(p)+" dropoutV", true, GuideDefault, "")
		add(r)
	} else if p.Lib != nil && p.Lib.VinMinV > 0 {
		add(c.feasRow(p, "输入电压 Vin,min vs 最低工作电压", vinMin, "V", "", p.Lib.VinMinV, libSrc(p)+" vinMinV", true, GuideDefault, ""))
	}
	if rt.ThetaJACW > 0 && rt.TjMaxC > 0 {
		tj := 25 + pw*rt.ThetaJACW
		add(c.feasRow(p, "结温 Tj (Ta 25 °C) vs Tj,max", tj, "°C", sprintf("P %s × θJA %g °C/W", fW(pw), rt.ThetaJACW), rt.TjMaxC, "ratings.thetaJaCW/tjMaxC — "+rt.Source, false, GuideJunction, ""))
	} else {
		add(c.feasRow(p, "结温 Tj = Ta + P·θJA", pw, "W", "损耗 P（结温需 θJA）", 0, "", false, GuideJunction, "需数据手册（θJA / Tj,max）"))
	}
}

func (c *ctx) esdRows(p *partInfo, rt *Ratings, add func(FeasRow)) {
	vmax, srcMax, onNet := 0.0, 0.0, ""
	for _, n := range p.nets() {
		if c.isGround(n) {
			continue
		}
		if en := c.elec[n]; en != nil && en.VMax > vmax {
			vmax, onNet = en.VMax, n
		}
		for _, pr := range c.tree.producers[n] {
			if pp := c.parts[pr]; pp != nil && pp.Lib != nil && pp.Lib.Ratings != nil && pp.Lib.Ratings.SourceVMaxV > srcMax {
				srcMax = pp.Lib.Ratings.SourceVMaxV
			}
		}
	}
	r := c.feasRow(p, "VRWM vs 最高线电压", vmax, "V", "最高网络 "+onNet, rt.VrwmV, "ratings.vrwmV — "+rt.Source, false, GuideStandoff, "")
	if rt.VrwmV > 0 && srcMax > rt.VrwmV {
		r.Note = sprintf("源规格上限 %s > VRWM：高端漏电增加，请按数据手册核对 VBR/漏电", fV(srcMax))
		if r.Status == FeasOK {
			r.Status = FeasMarginal
		}
	}
	add(r)
}

func (c *ctx) loadRows(p *partInfo, rt *Ratings, add func(FeasRow)) {
	var supply []string
	for _, n := range c.pinsDir(p.Ref, "sink") {
		if en := c.elec[n]; en != nil && en.Role == "power" {
			supply = append(supply, n)
		}
	}
	sort.Strings(supply)
	for _, n := range supply {
		en := c.elec[n]
		if rt.VccMaxV > 0 {
			add(c.feasRow(p, "供电 "+n+" Vmax vs 推荐上限", en.VMax, "V", "", rt.VccMaxV, "ratings.vccMaxV — "+rt.Source, false, GuideSupply, ""))
		}
		if rt.VccMinV > 0 {
			add(c.feasRow(p, "供电 "+n+" Vmin vs 推荐下限", en.VMin, "V", "", rt.VccMinV, "ratings.vccMinV — "+rt.Source, true, GuideSupply, ""))
		}
		if rt.VccMaxV == 0 && rt.VccMinV == 0 {
			add(c.feasRow(p, "供电 "+n+" 电压范围", en.VMax, "V", "", 0, "", false, GuideSupply, "需数据手册（推荐工作电压范围）"))
		}
	}
	if p.Lib != nil && p.Lib.GpioMaxA > 0 && p.Lib.GpioPins != "" {
		re, err := regexp.Compile(p.Lib.GpioPins)
		if err != nil {
			return
		}
		for _, net := range sortedKeys(c.elec) {
			for _, pin := range c.elec[net].Pins {
				if pin.Ref == p.Ref && pin.Dir == "source" && pin.IA > 0 && re.MatchString(pin.Name) {
					add(c.feasRow(p, "GPIO "+pin.Name+" ("+net+") 输出电流", pin.IA, "A", "", p.Lib.GpioMaxA, libSrc(p)+" gpioMaxA", false, GuideDefault, ""))
				}
			}
		}
	}
}
