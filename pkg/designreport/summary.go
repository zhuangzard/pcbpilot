package designreport

import (
	"math"
	"sort"
	"strings"
)

// MaxTopRisks bounds the executive-summary risk list.
const MaxTopRisks = 12

func (c *ctx) buildAppendix() {
	ap := AppendixSection{}
	if it := c.in.Intent; it != nil {
		for _, net := range sortedKeys(it.Nets) {
			np := it.Nets[net]
			ap.Nets = append(ap.Nets, NetRow{Net: net, Role: np.Role, Class: np.NetClass, Block: np.Block, VNom: np.Voltage.Nom, VMax: np.Voltage.Max,
				CurrentA: np.CurrentA, Source: np.CurrentSource, OuterMil: np.WidthMil.Outer, InnerMil: np.WidthMil.Inner, ClearMil: np.ClearanceMil,
				Impedance: np.ImpedanceOhm, Pads: np.PadCount})
		}
	} else {
		for _, net := range sortedKeys(c.elec) {
			en := c.elec[net]
			ap.Nets = append(ap.Nets, NetRow{Net: net, Role: en.Role, VNom: en.VNom, VMax: en.VMax, CurrentA: en.IA, Source: c.src, Pads: len(en.Pins)})
		}
	}
	for _, ref := range c.refs {
		p := c.parts[ref]
		row := PartRow{Ref: ref, Device: p.Device, Kind: p.Kind, ModelID: p.ModelID, Confidence: p.Confidence, Block: p.Block}
		switch {
		case p.Cap != nil:
			row.Parsed = strings.TrimSpace(sprintf("%s %s %s %s", fmtSI(p.Cap.Farad, "F"), p.Cap.Dielectric, p.Cap.Package, fmtSI(p.Cap.RatedV, "V")))
		case p.Res != nil:
			row.Parsed = strings.TrimSpace(sprintf("%s ±%g%% %s", fmtSI(p.Res.Ohm, "Ω"), p.Res.TolPct, p.Res.Package))
		}
		ap.Parts = append(ap.Parts, row)
	}
	ap.Glossary = []KV{
		{"余量 Margin", "(额定 − 应力)/额定 × 100 %；下限类检查为 (工作值 − 下限)/工作值", ""},
		{"IR 压降", "直流电流在走线/过孔/平面电阻上的电压降", ""},
		{"Ipk / Irms", "开关电源电感电流峰值 / 有效值", ""},
		{"Isat", "电感饱和电流：电感量下降到规定比例时的电流", ""},
		{"VRWM", "TVS/ESD 反向工作电压（不导通的最高电压）", ""},
		{"VRRM", "二极管最大重复反向电压", ""},
		{"θJA", "结到环境热阻（°C/W）", ""},
		{"DC 偏压", "II 类 MLCC 在直流电压下有效容量下降的现象", ""},
		{"爬电距离 / 电气间隙", "沿绝缘表面 / 空气中两导体的最短距离", ""},
		{"TDR", "时域反射计：测量传输线阻抗", ""},
		{"SELV", "安全特低电压", ""},
		{"DFM", "可制造性设计", ""},
		{"EP", "器件底部裸露散热/接地焊盘", ""},
	}
	c.rep.Appendix = ap
}

func (c *ctx) buildSummary() {
	s := SummarySection{}
	add := func(l, v, n string) { s.KeyNumbers = append(s.KeyNumbers, KV{l, v, n}) }
	if b := c.in.Board; b != nil {
		bb := b.Outline.BBox
		minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
		for _, p := range b.Outline.Points {
			if len(p) >= 2 {
				minX, minY, maxX, maxY = math.Min(minX, p[0]), math.Min(minY, p[1]), math.Max(maxX, p[0]), math.Max(maxY, p[1])
			}
		}
		if math.IsInf(minX, 1) {
			minX, minY, maxX, maxY = bb.MinX, bb.MinY, bb.MaxX, bb.MaxY
		}
		add("板尺寸", sprintf("%s × %s mm", f1((maxX-minX)*0.0254), f1((maxY-minY)*0.0254)), sprintf("%d 层，%d 个器件", b.CopperLayers, len(b.Components)))
	}
	if p := c.rep.Power; p != nil {
		for _, r := range p.Rails {
			add("电源轨 "+r.Net, sprintf("%s，最大 %s", fV(r.VNom), fA(r.IMaxA)), "最坏场景 "+r.Worst)
		}
		for _, sp := range p.ScenarioPwr {
			if sp.Scenario == "typical" || sp.Scenario == "peak" {
				add("输入功率（"+sp.Scenario+"）", fW(sp.SuppliedW), sprintf("负载 %s，损耗 %s", fW(sp.LoadW), fW(sp.LossW)))
			}
		}
	}
	if f := c.rep.Feasibility; f != nil {
		if r := minMargin(f.Rows); r != nil {
			add("最紧器件余量（相对准则）", sprintf("%s（准则 ≥ %s %%）", fPct(*r.MarginPct), f1(r.GuidePct)), r.Ref+" "+r.Check)
		}
		add("器件检查", sprintf("%d ok / %d 临界 / %d 超限 / %d 需数据手册", f.Counts[FeasOK], f.Counts[FeasMarginal], f.Counts[FeasOver], f.Counts[FeasUnknown]), "")
	}
	if l := c.rep.Layout; l != nil {
		worst := IRNetRow{}
		for _, n := range l.IRNets {
			if n.BudgetMV > 0 && n.RatioPct >= worst.RatioPct {
				worst = n
			}
		}
		if worst.Net != "" {
			add("最坏 IR 压降", sprintf("%s mV / 预算 %s mV（%s %%）", f2(worst.WorstMV), f1(worst.BudgetMV), f1(worst.RatioPct)), worst.Net+" @ "+worst.WorstPad)
		}
		for _, kv := range l.Routing {
			if strings.HasPrefix(kv.Label, "信号连接完成率") {
				add("布线完成率", kv.Value, "")
			}
		}
	}
	for _, ch := range c.rep.Verification {
		if strings.HasPrefix(ch.Name, "原生 DRC") || strings.HasPrefix(ch.Name, "pcb check") {
			add(ch.Name, ch.Status, ch.Detail)
		}
	}
	if it := c.in.Intent; it != nil {
		n := map[string]int{}
		for _, f := range it.Findings {
			n[f.Severity]++
		}
		add("设计发现（intent）", sprintf("%d error / %d warn / %d info", n["error"], n["warn"], n["info"]), "")
	}
	// Risks.
	var risks []Risk
	if r := c.rep.Requirements; r != nil {
		for _, f := range r.Findings {
			if f.Severity == "error" || f.Severity == "warn" {
				risks = append(risks, f)
			}
		}
	}
	if f := c.rep.Feasibility; f != nil {
		rows := append([]FeasRow(nil), f.Rows...)
		sort.SliceStable(rows, func(i, j int) bool { return *marginOr(rows[i]) < *marginOr(rows[j]) })
		unknown := 0
		for _, r := range rows {
			switch r.Status {
			case FeasOver:
				risks = append(risks, Risk{Severity: "error", Source: "feasibility", Message: sprintf("%s %s：应力 %s %s 超过额定 %s %s", r.Ref, r.Check, trimF(r.Stress), r.Unit, trimF(r.Rating), r.Unit), Suggestion: "更换更高额定器件或降低应力"})
			case FeasMarginal:
				risks = append(risks, Risk{Severity: "warn", Source: "feasibility", Message: sprintf("%s %s：余量 %s（准则 ≥ %s %%）", r.Ref, r.Check, fPct(*r.MarginPct), f1(r.GuidePct)), Suggestion: r.Note})
			case FeasUnknown:
				unknown++
			}
		}
		if unknown > 0 {
			risks = append(risks, Risk{Severity: "info", Source: "feasibility", Message: sprintf("%d 项器件额定未知（需数据手册），未计入余量判定", unknown), Suggestion: "在 power-models.json 的 ratings 中补充并注明来源"})
		}
	}
	for _, ch := range c.rep.Verification {
		if ch.Status == StatusFail {
			risks = append(risks, Risk{Severity: "error", Source: "verification", Message: ch.Name + "：" + ch.Detail})
		}
	}
	sev := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(risks, func(i, j int) bool { return sev[risks[i].Severity] < sev[risks[j].Severity] })
	if len(risks) > MaxTopRisks {
		risks = risks[:MaxTopRisks]
	}
	s.TopRisks = risks
	c.rep.Summary = s
	c.rep.Verdict = c.verdict()
}

func marginOr(r FeasRow) *float64 {
	if r.MarginPct != nil {
		return r.MarginPct
	}
	v := math.Inf(1)
	return &v
}

func minMargin(rows []FeasRow) *FeasRow {
	var best *FeasRow
	for i := range rows {
		r := &rows[i]
		if r.MarginPct == nil {
			continue
		}
		if best == nil || *r.MarginPct-r.GuidePct < *best.MarginPct-best.GuidePct {
			best = r
		}
	}
	return best
}

func trimF(v float64) string {
	if math.Abs(v) < 0.01 && v != 0 {
		return sprintf("%.3g", v)
	}
	return f3(v)
}

func (c *ctx) verdict() Verdict {
	v := Verdict{}
	fail := func(s string) { v.Reasons = append(v.Reasons, s) }
	warn := func(s string) { v.Warnings = append(v.Warnings, s) }
	if it := c.in.Intent; it != nil {
		n := map[string]int{}
		for _, f := range it.Findings {
			n[f.Severity]++
		}
		if n["error"] > 0 {
			fail(sprintf("intent 有 %d 条 error 级发现", n["error"]))
		}
		if n["warn"] > 0 {
			warn(sprintf("intent 有 %d 条 warn 级发现", n["warn"]))
		}
	}
	if p := c.rep.Power; p != nil {
		if !p.Converged {
			fail("电源仿真未收敛")
		}
		if len(p.Warnings) > 0 {
			warn(sprintf("电源仿真 %d 条警告", len(p.Warnings)))
		}
		if n := p.Confidence["assumed"]; n > 0 {
			warn(sprintf("%d 个器件功率模型为假定值（assumed）", n))
		}
	}
	if f := c.rep.Feasibility; f != nil {
		if n := f.Counts[FeasOver]; n > 0 {
			fail(sprintf("%d 项器件应力超过额定", n))
		}
		if n := f.Counts[FeasMarginal]; n > 0 {
			warn(sprintf("%d 项器件余量低于准则", n))
		}
		if n := f.Counts[FeasUnknown]; n > 0 {
			warn(sprintf("%d 项器件额定需数据手册确认", n))
		}
	}
	if cs := c.rep.Calcs; cs != nil {
		bad := 0
		for _, w := range cs.Widths {
			if w.Status == StatusFail {
				bad++
			}
		}
		for _, w := range cs.Vias {
			if w.Status == StatusFail {
				bad++
			}
		}
		for _, w := range cs.Clearance {
			if w.Status == StatusFail {
				bad++
			}
		}
		for _, w := range cs.Impedance {
			if w.Status == StatusFail {
				bad++
			} else if w.Status == StatusWarn {
				warn(sprintf("%s 阻抗偏差 %s %%", w.Name, f1(w.DevPct)))
			}
		}
		if bad > 0 {
			fail(sprintf("%d 项工程计算（线宽/过孔/间距/阻抗）不满足", bad))
		}
	}
	na := 0
	for _, ch := range c.rep.Verification {
		switch ch.Status {
		case StatusFail:
			fail(ch.Name + " 未通过")
		case StatusWarn:
			warn(ch.Name + "：" + ch.Detail)
		case StatusNA:
			na++
		}
	}
	if na > 0 {
		warn(sprintf("%d 项验证缺少证据（N/A）", na))
	}
	if len(c.rep.Missing) > 0 {
		warn(sprintf("%d 个章节/子项因输入缺失未生成", len(c.rep.Missing)))
	}
	switch {
	case len(v.Reasons) > 0:
		v.Status = VerdictFail
	case len(v.Warnings) > 0:
		v.Status = VerdictWarnings
	default:
		v.Status = VerdictPass
	}
	return v
}
