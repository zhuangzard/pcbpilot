package postsim

import (
	"fmt"
	"io"
	"path"
	"strings"
)

// WriteMarkdown writes the human report. mapsRel is the heat-map directory
// relative to the report file ("" = no image links).
func (res *Result) WriteMarkdown(w io.Writer, mapsRel string) {
	p := func(f string, a ...any) { fmt.Fprintf(w, f, a...) }
	p("# 设计后仿真验证 Post-layout verification\n\n")
	p("在**完成布线的真实铜皮**（`pcb dump --include-copper` 回读，或 pcb auto 结果同构导出）上，用 `sim power` 的每引脚电流与器件功耗，求解直流压降与稳态温升。")
	p("边界：**板级**——裸板静止空气，上下表面自然对流，不含外壳、风扇或气流（非 CFD）。\n\n")
	p("| 项 | 值 |\n|---|---|\n")
	p("| 结论 | **%s** |\n", strings.ToUpper(res.Verdict.Status))
	if res.Inputs.Board != "" {
		p("| 板 | `%s` |\n", res.Inputs.Board)
	}
	if res.Inputs.BoardSemantic != "" {
		p("| semanticSha256 | `%s` |\n", res.Inputs.BoardSemantic)
	}
	if res.Inputs.Source != "" {
		p("| 来源 | %s |\n", res.Inputs.Source)
	}
	if res.Inputs.Sim != "" {
		p("| 仿真 | `%s` |\n", res.Inputs.Sim)
	}
	p("| 场景 | %s（%s） |\n", strings.Join(res.Scenarios, ", "), res.Mode)
	p("| 网格 | %.3g mm × %d×%d 单元，每单元 %d×%d 子采样，板内 %d 单元/层 |\n", res.Grid.CellMm, res.Grid.NX, res.Grid.NY, res.Grid.Sub, res.Grid.Sub, res.Grid.Cells)
	p("| 环境 | %.0f °C，h 顶 %.1f / 底 %.1f W/m²K", res.Settings.AmbientC, res.Settings.HTop, res.Settings.HBottom)
	if res.Settings.Emissivity > 0 {
		p("，辐射 ε %.2g", res.Settings.Emissivity)
	}
	p(" |\n")
	p("| 预算 | IR %s；铜温升 %.0f °C；余量 ×%.2g |\n\n", res.Settings.IRBudget, res.Limits.TempRiseC, res.Limits.Margin)
	if len(res.Verdict.Reasons) > 0 {
		for _, r := range res.Verdict.Reasons {
			p("- %s\n", r)
		}
		p("\n")
	}

	p("## 1. 真实铜皮直流压降 IR drop on the real copper\n\n")
	p("| 网络 | 角色 | 最坏场景 | 参考 | 电流 A | 预算 mV | 最坏压降 mV | 最坏焊盘 | 铜损 mW | 最大 J A/mm² | 最大过孔 A | 铜自热 °C | 状态 |\n")
	p("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, n := range res.Nets {
		budget := "—"
		if n.BudgetMV > 0 {
			budget = fmt.Sprintf("%.1f", n.BudgetMV)
		}
		p("| %s | %s | %s | %s | %.3f | %s | %.2f | %s | %.3f | %.1f | %.3f | %.2f | %s |\n", n.Net, n.Role, n.Scenario, n.Reference, n.CurrentA,
			budget, n.WorstMV, n.WorstPad, n.LossMW, n.MaxJAmm2, n.MaxViaA, n.MaxTraceRiseC, statusZh(n.Status))
	}
	p("\n")
	for _, n := range res.Nets {
		if len(n.Pads) == 0 {
			continue
		}
		p("**%s**（%s，%d 段走线，%d 个面铜单元，%d 个过孔）", n.Net, n.Role, n.TrackPieces, n.SheetCells, n.ViaCount)
		p("：各场景最坏 ")
		var ps []string
		for _, s := range n.PerScenario {
			ps = append(ps, fmt.Sprintf("%s %.2f mV", s.Scenario, s.WorstMV))
		}
		p("%s\n\n", strings.Join(ps, " · "))
		p("| 焊盘 | 方向 | 电流 A | 压降 mV | 电压 V | 场景 |\n|---|---|---|---|---|---|\n")
		for _, pd := range n.Pads {
			d := fmt.Sprintf("%.3f", pd.DropMV)
			if pd.DropMV < 0 {
				d = "开路"
			}
			p("| %s | %s | %.4f | %s | %.4f | %s |\n", pd.Pad, pd.Dir, pd.CurrentA, d, pd.VoltageV, pd.Scenario)
		}
		if len(n.Hotspots) > 0 {
			var hs []string
			for _, h := range n.Hotspots {
				s := fmt.Sprintf("%s %s (%.0f,%.0f) %.1f A/mm²", h.Layer, h.Kind, h.X, h.Y, h.Value)
				if h.WidthMil > 0 {
					s += fmt.Sprintf(" @ %.1f mil", h.WidthMil)
				}
				hs = append(hs, s)
			}
			p("\n电流密度热点：%s\n", strings.Join(hs, "；"))
		}
		for _, s := range n.Notes {
			p("\n- %s", s)
		}
		p("\n\n")
	}

	p("## 2. 过孔电流 Via currents\n\n")
	if len(res.Vias) == 0 {
		p("没有过孔承载仿真电流。\n\n")
	} else {
		p("载流量 = IPC-2221 外层曲线作用于孔壁截面 π(d+t)t，ΔT %.0f °C。\n\n", res.Settings.ViaDeltaTC)
		p("| 过孔 | 网络 | x, y (mil) | 钻孔 mil | 电流 A | 载流量 A | 占用 %% | 场景 |\n|---|---|---|---|---|---|---|---|\n")
		for i, v := range res.Vias {
			if i >= 12 || v.CurrentA <= 0 {
				break
			}
			p("| %s | %s | %.0f, %.0f | %.1f | %.3f | %.2f | %.1f | %s |\n", v.ID, v.Net, v.X, v.Y, v.DrillMil, v.CurrentA, v.AmpacityA, v.UsePct, v.Scenario)
		}
		p("\n")
	}

	if t := res.Thermal; t != nil {
		p("## 3. 稳态热仿真 Thermal\n\n")
		p("最热场景 **%s**：板最高 **%.1f °C**（%s，(%.0f, %.0f) mil", t.Scenario, t.MaxBoardC, t.MaxAt.Layer, t.MaxAt.X, t.MaxAt.Y)
		if t.MaxAt.What != "" {
			p("，%s 下方", t.MaxAt.What)
		}
		p("）。热源 %.3f W（器件 %.3f W + 铜损 %.4f W），对流散出 %.3f W，能量平衡误差 %.4f %%。", t.TotalW, t.PartsW, t.JouleW, t.LossW, t.BalanceErrPct)
		if t.JouleScenario != "" {
			p("铜损单独作用（%s）时铜自身最大温升 %.2f °C。", t.JouleScenario, t.MaxCopperRiseC)
		}
		p("\n\n| 场景 | 器件 W | 铜损 W | 最高 °C | 散出 W | 平衡误差 %% |\n|---|---|---|---|---|---|\n")
		for _, s := range t.PerScenario {
			p("| %s | %.3f | %.4f | %.1f | %.3f | %.4f |\n", s.Scenario, s.PartsW, s.JouleW, s.MaxC, s.LossW, s.ErrPct)
		}
		p("\n| 层 | 最高 °C | 平均 °C | 位置 (mil) |\n|---|---|---|---|\n")
		for _, l := range t.Layers {
			p("| %s | %.1f | %.1f | %.0f, %.0f |\n", l.Layer, l.MaxC, l.MeanC, l.X, l.Y)
		}
		p("\n| 器件 | 面 | 功耗 W | 场景 | 板温 max / 均 °C | θ °C/W | Tj °C | Tj,max | 状态 |\n|---|---|---|---|---|---|---|---|---|\n")
		for _, pt := range t.Parts {
			th, tj, tjm := "—", "—", "—"
			if pt.ThetaCW > 0 {
				th = fmt.Sprintf("%s %.0f", pt.ThetaKind, pt.ThetaCW)
				tj = fmt.Sprintf("%.1f", pt.TjC)
			}
			if pt.TjMaxC > 0 {
				tjm = fmt.Sprintf("%.0f", pt.TjMaxC)
			}
			st := statusZh(pt.Status)
			if pt.Note != "" && pt.Status != "ok" {
				st += "（" + pt.Note + "）"
			}
			p("| %s | %s | %.4f | %s | %.1f / %.1f | %s | %s | %s | %s |\n", pt.Ref, pt.Side, pt.PowerW, pt.Scenario, pt.BoardMaxC, pt.BoardMeanC, th, tj, tjm, st)
		}
		p("\n")
	}

	if len(res.Maps) > 0 {
		p("## 4. 热图与电流密度图 Maps\n\n")
		for _, m := range res.Maps {
			f := m.File
			if mapsRel != "" {
				f = path.Join(mapsRel, m.File)
			}
			kind := "温度"
			if m.Kind == "current-density" {
				kind = "电流密度"
			}
			p("**%s %s**（%.2f–%.2f %s）\n\n![%s %s](%s)\n\n", m.Layer, kind, m.Min, m.Max, m.Unit, m.Layer, m.Kind, f)
		}
	}

	p("## 5. 铜皮修改建议 Copper feedback\n\n")
	if len(res.Feedback) == 0 {
		p("没有需要加宽的走线、需要倒角的拐角或过孔瓶颈（判据：IPC-2152 载流 × 余量 %.2g、铜自热 ≤ %.0f °C、拐角拥挤系数 1 + (180° − 角度)/180°、过孔占用 ≤ 50 %%）。\n\n", res.Limits.Margin, res.Limits.TempRiseC)
	} else {
		p("| ID | 类型 | 级别 | 位置 | 建议 |\n|---|---|---|---|---|\n")
		for _, it := range res.Feedback {
			var loc []string
			for _, q := range it.Evidence.Points {
				loc = append(loc, fmt.Sprintf("%s (%.0f,%.0f)", q.Label, q.X, q.Y))
			}
			p("| %s | %s | %s | %s | %s |\n", it.ID, it.Kind, it.Severity, strings.Join(loc, " "), it.Proposal.Summary)
		}
		p("\n")
	}

	if len(res.Compare) > 0 {
		p("## 6. 与 pcb auto 布线期 IR 估算对比\n\n")
		p("| 网络 | pcb auto mV | 焊盘 | 设计后 mV | 焊盘 | 差 mV | 设计后场景 |\n|---|---|---|---|---|---|---|\n")
		for _, c := range res.Compare {
			p("| %s | %.2f | %s | %.2f | %s | %+.2f | %s |\n", c.Net, c.AutoMV, c.AutoPad, c.PostMV, c.PostPad, c.DeltaMV, c.Scenario)
		}
		p("\n差异来源（两者都是 DC 电阻网络，但输入与离散化不同）：\n\n")
		p("- pcb auto 从焊盘**中心**量走线长度；这里走线在焊盘内的部分被焊盘铜短接（焊盘比走线宽得多），每个焊盘端少约半个焊盘长的走线电阻。\n")
		p("- pcb auto 的平面/铺铜是它自己规划的多边形上 ≥ 25 mil 的粗网格，且不扣反焊盘（乐观）；这里用宿主**真实灌铜**（含净距挖空、热焊盘辐条），0.5 mm 单元。\n")
		p("- pcb auto 过孔长度取板厚/(层数−1) 等分；这里按叠层真实 z（外层半固化片 0.21 mm、芯板 1.07 mm）。\n")
		p("- pcb auto 对合并的 worst 文件逐个供电源求包络；这里逐场景（各自满足 KCL）求解再取最坏。\n\n")
	}

	if e := res.Elmer; e != nil {
		p("## 7. Elmer FEM 交叉校验\n\n")
		p("状态：**%s**。%s\n\n", e.Status, e.Note)
		if e.Deck != "" {
			p("输入包：`%s`（%d 节点、%d 六面体、%d 边界面、%d 体）。\n\n", e.Deck, e.Nodes, e.Elements, e.Boundary, e.Bodies)
		}
		if len(e.Probes) > 0 {
			p("| 探针 | 本模型 °C | Elmer °C | 差 °C |\n|---|---|---|---|\n")
			for _, q := range e.Probes {
				el := "—"
				d := "—"
				if q.HasElmer {
					el = fmt.Sprintf("%.2f", q.ElmerC)
					d = fmt.Sprintf("%+.2f", q.ElmerC-q.ModelC)
				}
				p("| %s | %.2f | %s | %s |\n", q.Name, q.ModelC, el, d)
			}
			p("\n")
		}
	}

	p("## 模型与假设 Model and assumptions\n\n")
	for _, m := range res.Model {
		p("- %s\n", m)
	}
	for _, a := range res.Assumptions {
		p("- %s\n", a)
	}
	if len(res.Findings) > 0 {
		p("\n## 发现 Findings\n\n")
		for _, f := range res.Findings {
			p("- [%s] %s: %s\n", f.Severity, f.Kind, f.Message)
		}
	}
}

func statusZh(s string) string {
	switch s {
	case "ok":
		return "✓ 通过"
	case "over-budget":
		return "✗ 超预算"
	case "open":
		return "✗ 开路"
	case "info":
		return "仅报告"
	case "no-copper":
		return "无铜"
	case "no-reference":
		return "无参考"
	case "warn":
		return "⚠ 警告"
	case "fail":
		return "✗ 失败"
	case "needs-datasheet":
		return "需数据手册"
	}
	return s
}
