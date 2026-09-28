package designreport

import (
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

var (
	reThicknessMM = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*mm`)
	rePolarCap    = regexp.MustCompile(`(?i)(tantal|electrolyt|polymer|^TAJ|^T491|^CA45|^EEE|^UWT|^UCL|^PCJ|^RVT)`)
)

// partPitchMil is the smallest pad-centre distance of a part.
func partPitchMil(bp *BoardPart) float64 {
	m := math.Inf(1)
	for i := range bp.Pads {
		for j := i + 1; j < len(bp.Pads); j++ {
			a, b := bp.Pads[i], bp.Pads[j]
			if d := math.Hypot(a.X-b.X, a.Y-b.Y); d > 0.5 && d < m {
				m = d
			}
		}
	}
	return m
}

// hasExposedPad: a pad ≥ 4× the median pad area on a part with ≥ 5 pads.
func hasExposedPad(bp *BoardPart) (string, bool) {
	if len(bp.Pads) < 5 {
		return "", false
	}
	areas := make([]float64, len(bp.Pads))
	for i, p := range bp.Pads {
		areas[i] = p.Width * p.Height
	}
	s := append([]float64(nil), areas...)
	sort.Float64s(s)
	med := s[len(s)/2]
	for i, a := range areas {
		if med > 0 && a >= 4*med {
			return bp.Pads[i].PadNumber, true
		}
	}
	return "", false
}

func (c *ctx) buildManufacturing() {
	it, pl, b := c.in.Intent, c.in.Plan, c.in.Board
	if it == nil && pl == nil && b == nil {
		c.missing("9 制造与装配", "无 intent.json / plan.json / 板级 dump")
		return
	}
	ms := &MfgSection{}
	layers, stack := 0, ""
	if pl != nil && pl.Result != nil && pl.Result.Stackup != nil {
		layers, stack = pl.Result.Stackup.Layers, pl.Result.Stackup.JLCStackup
	}
	if it != nil && it.Copper != nil {
		if layers == 0 {
			layers = it.Copper.Layers
		}
		if it.Copper.Stackup != "" {
			stack = it.Copper.Stackup
		}
	}
	if layers == 0 && b != nil {
		layers = b.CopperLayers
	}
	fab := []KV{{"层数", sprintf("%d", layers), ""}, {"叠层", orNA(stack), "下单时选择同名叠层"}}
	thick := "未声明"
	if m := reThicknessMM.FindStringSubmatch(stack); m != nil {
		thick = m[1] + " mm"
	}
	fab = append(fab, KV{"板厚", thick, ""})
	if it != nil && it.Copper != nil {
		fab = append(fab, KV{"铜厚 外/内", sprintf("%g oz / %g oz", it.Copper.OuterOz, it.Copper.InnerOz), ""})
	}
	if b != nil {
		r := b.Rules
		minLine := math.Inf(1)
		for _, l := range b.Copper.Lines {
			if l.LineWidth > 0 {
				minLine = math.Min(minLine, l.LineWidth)
			}
		}
		ml := "—"
		if !math.IsInf(minLine, 1) {
			ml = f2(minLine) + " mil"
		}
		fab = append(fab,
			KV{"最小线宽（规则 / 实际）", sprintf("%s mil / %s", f2(r.TrackWidthMinMil), ml), "规则来自板级回读 rules"},
			KV{"最小间距（通用 / 线-线）", sprintf("%s / %s mil", f2(r.ClearanceMil), f2(r.TrackTrackMil)), ""},
			KV{"过孔 钻孔/外径", sprintf("%s / %s mil（%s / %s mm）", f2(r.ViaDrillMil), f2(r.ViaDiameterMil), f2(r.ViaDrillMil*0.0254), f2(r.ViaDiameterMil*0.0254)), ""},
			KV{"铜到板边", sprintf("%s mil", f2(r.CopperToEdgeMil)), ""},
			KV{"孔-孔 / 槽间距", sprintf("%s / %s mil", f2(r.HoleToHoleMil), f2(r.SlotClearanceMil)), ""},
			KV{"走线 / 过孔数量", sprintf("%d / %d", len(b.Copper.Lines), b.ViaCount()), ""},
		)
	}
	var imp []string
	if it != nil {
		for _, ncl := range it.NetClasses {
			if ncl.ImpedanceOhm > 0 {
				k := "单端"
				if ncl.DiffGapMil > 0 {
					k = sprintf("差分，线宽 %g / 间距 %g mil", ncl.TrackMil, ncl.DiffGapMil)
				}
				imp = append(imp, sprintf("%s %g Ω（%s；%s）", ncl.Name, ncl.ImpedanceOhm, k, strings.Join(ncl.Nets, ",")))
			}
		}
	}
	if len(imp) > 0 {
		fab = append(fab, KV{"阻抗控制", "需要：" + strings.Join(imp, "；"), "下单勾选阻抗控制，叠层须与计算一致"})
	} else {
		fab = append(fab, KV{"阻抗控制", "不需要", ""})
	}
	// Surface finish from the finest pitch / EP parts.
	finest, finestRef := math.Inf(1), ""
	var epRefs []string
	if b != nil {
		for i := range b.Components {
			bp := &b.Components[i]
			if p := partPitchMil(bp); p < finest {
				finest, finestRef = p, bp.Designator
			}
			if _, ok := hasExposedPad(bp); ok {
				epRefs = append(epRefs, bp.Designator)
			}
		}
	}
	finish := "无铅喷锡（HASL-LF）即可"
	why := ""
	if !math.IsInf(finest, 1) {
		why = sprintf("最细焊盘中心距 %s mm（%s）", f2(finest*0.0254), finestRef)
		if finest*0.0254 <= 0.65 || len(epRefs) > 0 {
			finish = "建议沉金（ENIG）：细间距/底部焊盘需要平整焊盘面"
		}
	}
	fab = append(fab, KV{"表面处理（建议）", finish, why}, KV{"阻焊/丝印（建议）", "绿油白字（标准）；细间距器件间保留阻焊桥", "建议项，非计算结果"})
	ms.Fab = fab
	// DFM from pcb check.
	if k := c.in.Check; k != nil {
		type agg struct {
			level, ex string
			n         int
		}
		groups := map[string]*agg{}
		for _, item := range k.Items {
			g := groups[item.Type]
			if g == nil {
				g = &agg{level: item.Level, ex: item.Message}
				if item.Nets != "" {
					g.ex += " [" + item.Nets + "]"
				}
				groups[item.Type] = g
			}
			g.n++
		}
		for _, t := range sortedKeys(groups) {
			g := groups[t]
			ms.DFM = append(ms.DFM, DFMRow{Level: g.level, Type: t, Count: g.n, Example: g.ex})
		}
		rank := map[string]int{"ERROR": 0, "WARN": 1, "INFO": 2}
		sort.SliceStable(ms.DFM, func(i, j int) bool {
			if rank[ms.DFM[i].Level] != rank[ms.DFM[j].Level] {
				return rank[ms.DFM[i].Level] < rank[ms.DFM[j].Level]
			}
			return ms.DFM[i].Count > ms.DFM[j].Count
		})
		for _, l := range k.Limits {
			ms.DFM = append(ms.DFM, DFMRow{Level: "LIMIT", Type: "limitation", Count: 1, Example: l})
		}
	} else {
		c.missing("9.2 DFM 警告", "未提供 pcb check 输出（--check）")
	}
	// Assembly attention.
	for _, ref := range c.refs {
		p := c.parts[ref]
		var bp *BoardPart
		if b != nil {
			bp = b.Part(ref)
		}
		switch {
		case p.Kind == powersim.KindLED:
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "极性", Note: "LED 阴极标记与丝印一致"})
		case p.Kind == powersim.KindDiode:
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "极性", Note: "阴极色环/标记朝向丝印“K”端"})
		case p.Kind == powersim.KindESD:
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "方向（1 脚）", Note: "阵列型 ESD 器件按 1 脚标记贴装"})
		case p.Kind == powersim.KindBJT:
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "方向", Note: "SOT-23 B/E/C 与封装一致"})
		case p.Kind == powersim.KindCapacitor && rePolarCap.MatchString(p.Device+" "+p.Value):
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "极性", Note: "有极性电容：正极对应丝印“+”"})
		case isRegulator(p.Kind) || p.Kind == powersim.KindLoad || p.Kind == powersim.KindICSmall || p.Kind == "ic":
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "1 脚方向 / 湿敏", Note: "按 1 脚标记贴装；湿敏等级（MSL）以数据手册为准，超时需烘烤"})
		}
		if bp != nil {
			if pad, ok := hasExposedPad(bp); ok {
				ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "底部散热焊盘 EP（焊盘 " + pad + "）", Note: "钢网开窗分割（约 50–70 % 覆盖）防浮高/空洞；焊盘内过孔须塞孔或盖油"})
			}
			if pt := partPitchMil(bp); !math.IsInf(pt, 1) && pt*0.0254 <= 0.65 {
				ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: sprintf("细间距 %s mm", f2(pt*0.0254)), Note: "钢网厚度 0.10–0.12 mm，回流后检查连锡（AOI/放大镜）"})
			}
		}
		if p.Kind == powersim.KindSource || p.Kind == powersim.KindConnector {
			ms.Assembly = append(ms.Assembly, AsmRow{Ref: ref, Device: p.Device, Concern: "机械件", Note: "插拔受力件：检查定位柱/固定脚焊接强度与板边对齐"})
		}
	}
	// HV / isolation.
	if it != nil && len(it.Pairs) > 0 {
		for _, p := range it.Pairs {
			s := sprintf("%s ↔ %s：间隙 %g mm，爬电 %g mm（%s）", p.A, p.B, p.ClearanceMm, p.CreepageMm, p.StandardRef)
			if p.SlotRequired {
				s += sprintf("；需铣槽 ≥ %g mm 宽，Gerber 外形层标注", p.SlotWidthMm)
			}
			ms.HV = append(ms.HV, s)
		}
		if it.Standard.Coated {
			ms.HV = append(ms.HV, "规格声明三防涂覆：涂覆前清洗助焊剂残留，涂覆区避开连接器触点与测试点")
		}
	} else {
		ms.HV = append(ms.HV, "无绝缘对（单一 SELV 域）：无铣槽/爬电特别要求")
	}
	if pl != nil && pl.Result != nil && pl.Result.Isolation != nil {
		for _, s := range pl.Result.Isolation.Slots {
			ms.HV = append(ms.HV, sprintf("铣槽 %s：宽 %s mil × 长 %s mil", s.Ref, f1(s.WidthMil), f1(s.LengthMil)))
		}
	}
	ms.Handling = []string{
		"静电防护：全程佩戴腕带、防静电台面与包装（IC/模块/ESD 器件均为静电敏感）",
		"回流：无铅（SAC305）曲线峰值约 240–250 °C；模块类器件以其手册的回流次数/温度为准",
		"清洗：免洗助焊剂残留在高阻/射频区域需清洗",
	}
	if k := c.in.Check; k != nil && k.Counts["fiducialMissing"] > 0 {
		ms.Handling = append(ms.Handling, "无本地 Mark 点：拼板时由工厂工艺边加 Mark；细间距器件建议增加局部 Mark")
	}
	c.rep.Manufacturing = ms
}

func orNA(s string) string {
	if s == "" {
		return "未声明"
	}
	return s
}
