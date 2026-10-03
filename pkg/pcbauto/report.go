package pcbauto

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// Report bundles everything one run decided, for JSON and Markdown output.
type Report struct {
	Result    *Result      `json:"result"`
	Circuit   *Circuit     `json:"circuit"`
	Placement *PlaceResult `json:"placement,omitempty"`
	SI        *SIReport    `json:"si,omitempty"`
	Mechanics *Mechanics   `json:"mechanics,omitempty"`
	Joint     *JointScore  `json:"joint,omitempty"`
	// Stage0 is the physical-feasibility assessment made before placement.
	Stage0 *Feasibility `json:"stage0,omitempty"`
	Loop   []LoopPass   `json:"loop,omitempty"`
	// Frame is the autoSize frame search (mech board.autoSize with no size).
	Frame *FrameSearch `json:"frame,omitempty"`
	// Feedback are the schematic proposals routing evidence supports
	// (also written as feedback.json).
	Feedback *Feedback `json:"feedback,omitempty"`
}

// WriteMarkdown renders the report in Chinese for the designer to review.
// Every inferred value says where it came from; heuristic currents are
// flagged for confirmation.
func (r *Report) WriteMarkdown(w io.Writer) {
	p := func(format string, args ...any) { fmt.Fprintf(w, format, args...) }
	res := r.Result
	p("# PCB 自动设计报告\n\n")
	p("> 本报告由 pcbauto 离线引擎生成。所有写入以 `playbook.json` 经 `pcbpilot apply` 执行；")
	p("最终以 EasyEDA 原生 DRC 与保存重载后的回读为准。\n\n")

	if st := res.Stackup; st != nil {
		writeStage0(p, r)
		p("## 1. 层数与叠层决策\n\n**%d 层**（%s）\n\n| 层 | 名称 | 类型 | 网络 | 走线方向 |\n|---|---|---|---|---|\n", st.Layers, st.JLCStackup)
		for _, l := range st.Stack {
			nets := strings.Join(append(append([]string{}, l.Nets...), l.PourNets...), ", ")
			p("| %d | %s | %s | %s | %s |\n", l.ID, l.Name, l.Kind, nets, l.Dir)
		}
		p("\n决策依据：\n\n")
		for _, why := range st.Reasons {
			p("- %s\n", why)
		}
		m := st.Metrics
		p("\n板面积 %.2f in²，焊盘 %d 个（%.0f 个/in²），最细间距 %.1f mil，信号连接 %d 条，差分对 %d 对，电源轨 %d 条。\n\n",
			m.AreaIn2, m.PadCount, m.PinDensity, m.FinestPitchMil, m.Connections, m.DiffPairs, m.PowerRails)
		if len(res.Attempts) > 1 {
			p("尝试过的叠层方案：\n\n| 方案 | 布通率 | 过孔 | DRC 违规 |\n|---|---|---|---|\n")
			for _, a := range res.Attempts {
				p("| %s | %.1f%% | %d | %d |\n", a.Stack, a.Completion, a.Vias, a.Violations)
			}
			p("\n")
		}
	}

	if an := res.Analysis; an != nil {
		p("## 2. 电压、电流与线宽/间距\n\n温升 %.0f°C，IPC-2221 载流公式；间距按 IPC-2221B 电压表与工艺最小值取大。\n\n", an.TempRiseC)
		p("| 网络 | 角色 | 电压 V | 电流 A | 来源 | 外层线宽 mil | 内层线宽 mil | 间距 mil | 换层过孔 |\n|---|---|---|---|---|---|---|---|---|\n")
		var heuristic []string
		for _, np := range an.Nets {
			if np.Role == RoleSignal && np.ClearanceMil <= 0 {
				continue
			}
			if np.Role == RoleSignal || np.Role == RoleAnalog {
				continue
			}
			p("| %s | %s | %.2g | %.2f | %s | %.1f | %.1f | %.1f | %d |\n", np.Net, np.Role, np.Voltage, np.CurrentA, np.Source,
				np.WidthMil, np.InnerWidthMil, np.ClearanceMil, np.ViasPerTransition)
			if np.Source == "heuristic" {
				heuristic = append(heuristic, np.Net)
			}
		}
		p("\n普通信号网络使用板规则线宽；上表只列电源、地、差分、时钟、射频和开关节点。\n\n")
		if len(heuristic) > 0 {
			p("**需要确认**：以下电源轨的电流是按网名估算的，请用 `--power power.json` 给出真实预算：%s\n\n", strings.Join(heuristic, "、"))
		}
		writeSimCurrents(p, an)
	}

	if c := r.Circuit; c != nil {
		p("## 3. 电路理解\n\n### 功能块\n\n| 功能块 | 类型 | 核心 | 器件数 | 电压域 |\n|---|---|---|---|---|\n")
		for _, bl := range c.Blocks {
			p("| %s | %s | %s | %d | %s |\n", bl.ID, bl.Kind, bl.Core, len(bl.Parts), bl.Domain)
		}
		if len(c.Links) > 0 {
			p("\n### 功能块之间的信号\n\n| 从 | 到 | 类型 | 网络数 |\n|---|---|---|---|\n")
			for i, l := range c.Links {
				if i >= 20 {
					p("| … | 另有 %d 条 | | |\n", len(c.Links)-20)
					break
				}
				p("| %s | %s | %s | %d |\n", l.From, l.To, strings.Join(l.Kinds, ", "), len(l.Nets))
			}
		}
		writeLayoutBasis(p, c)
		if len(c.Notes) > 0 {
			p("\n电路理解备注（含原理图模块归属的采纳/保留）：\n\n")
			for _, n := range c.Notes {
				p("- %s\n", n)
			}
		}
		writeConverters(p, c)
		writeChains(p, c)
		p("\n### 电压域与隔离\n\n")
		for _, d := range c.Domains {
			tag := ""
			if d.Hazardous {
				tag = "（危险电压）"
			}
			p("- **%s**%s：最高 %.0f V，%d 个器件\n", d.ID, tag, d.MaxVoltage, len(d.Parts))
		}
		for _, br := range c.Barriers {
			p("- 隔离带 %s ↔ %s：%s 绝缘，爬电距离 %.0f mil（%.2f mm），电气间隙 %.0f mil（%.2f mm），跨接器件 %s\n",
				br.A, br.B, br.Insulation, br.CreepageMil, br.CreepageMil*0.0254, br.ClearanceMil, br.ClearanceMil*0.0254, strings.Join(br.Bridges, "、"))
			for _, why := range br.Why {
				p("  - %s\n", why)
			}
		}
		p("\n")
	}

	if pl := r.Placement; pl != nil {
		m := pl.Metrics
		p("## 4. 布局\n\n| 指标 | 数值 |\n|---|---|\n")
		p("| 加权线长 | %.1f in（起始 %.1f in） |\n| 重叠 | %d |\n| 出板 | %d |\n| 越出电压域分区 | %d |\n| 进入禁布区 | %d |\n| 去耦电容到电源脚平均距离 | %.0f mil |\n| 器件面积占比 | %.1f%% |\n| 耗时 | %d ms |\n\n",
			m.WirelengthIn, m.StartWireIn, m.Overlaps, m.OutOfBoard, m.OutOfZone, m.KeepoutHits, m.DecapMeanMil, m.Utilisation, m.Millis)
		for _, n := range pl.Notes {
			p("- %s\n", n)
		}
		if ar := pl.Aesthetics; ar != nil && ar.Guard != "" {
			p("- 布局美观阶段（%s 档）电气子项交换（每项容差 %.2g 分；安全/布通/DRC/发现数/过孔/超预算压降零容差）：%s\n", ar.Profile, ar.ElectricalTol, aesTradesCN(ar.Trades))
		}
		p("\n")
		if f := r.Frame; f != nil {
			p("板框自动尺寸：%.1f × %.1f %s（从密到疏逐一试放，取无重叠/出板/禁布/压孔、且加权线长 ≤ 宽松参考框 %.1f in 的 115%%、关键辅助件（去耦/热回路/功率级/晶振/保护/电源路径）超出牵引距离 ≤ 参考 %.0f mil + max(150, 50%%) 的最小矩形）\n\n| 宽 | 高 | 长宽比 | 填充率 | 线长 in | 关键牵引超出 mil | 结果 |\n|---|---|---|---|---|---|---|\n", f.Width, f.Height, f.Units, f.RefWireIn, f.RefTetherMil)
			for _, t := range f.Trials {
				res := "可行"
				if !t.Feasible {
					res = t.Reason
				}
				p("| %.1f | %.1f | %.2f | %.0f%% | %.1f | %.0f | %s |\n", t.Width, t.Height, t.Aspect, t.Fill*100, t.WireIn, t.TetherMil, res)
			}
			p("\n")
		}
	}

	if rr := res.Route; rr != nil {
		s := rr.Stats
		p("## 5. 布线\n\n| 指标 | 数值 |\n|---|---|\n")
		p("| 信号布通率 | %.1f%%（%d/%d） |\n", s.Completion, s.Routed, s.Connections)
		if j := r.Joint; j != nil && j.PlanePads > 0 {
			p("| 平面/地连接 | %d/%d 接通 |\n", j.PlanePads-j.PlaneOpen, j.PlanePads)
		}
		p("| 走线总长 | %.1f in |\n| 信号过孔 | %d |\n| 扇出过孔 | %d |\n| 协商迭代 | %d |\n| 等长调整 | %d 条 |\n| 栅格 | %.2f mil |\n| 耗时 | %.1f s |\n\n",
			s.WireLengthIn, s.Vias, s.FanoutVias, s.Iterations, s.Tuned, s.GridMil, float64(s.Millis)/1000)
		if d := res.DRC; d != nil {
			if len(d.Violations) == 0 {
				p("独立几何 DRC：**0 违规**。\n\n")
			} else {
				p("独立几何 DRC：%d 处违规 %v。\n\n", len(d.Violations), d.ByKind)
			}
		}
		if len(rr.Unrouted) > 0 {
			reasons := map[string]int{}
			for _, u := range rr.Unrouted {
				reasons[u.Reason]++
			}
			keys := make([]string, 0, len(reasons))
			for k := range reasons {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			p("未布通 %d 处：", len(rr.Unrouted))
			for _, k := range keys {
				p("%s×%d ", k, reasons[k])
			}
			p("\n\n")
		}
		for _, n := range rr.Notes {
			if strings.HasPrefix(n, "fan-out: no via site") {
				continue
			}
			p("- %s\n", n)
		}
		if bs := rr.Beautify; bs != nil && bs.Kept {
			p("- 布线美化电气子项交换（每项容差 %.2g 分；安全/布通/DRC/发现数/过孔/超预算压降零容差）：%s\n", bs.ElectricalTol, aesTradesCN(bs.Trades))
		}
		p("\n")
		writePowerIntegrity(p, rr.Power)
	}
	writeIsolation(p, res.Isolation)
	writeEdge(p, res.Edge)
	if r.Joint == nil && len(res.Blockers) > 0 {
		// No joint score (route-only paths): the safety / electrical
		// verdict still stands on its own.
		p("### 交付结论：不可交付\n\n")
		for _, b := range res.Blockers {
			p("- %s\n", b)
		}
		p("\n")
	}

	writeJoint(p, r)
	if si := r.SI; si != nil && (len(si.Pairs) > 0 || len(si.Groups) > 0 || len(si.Findings) > 0) {
		p("## 6. 高速信号\n\n")
		if len(si.Pairs) > 0 {
			p("| 差分对 | 长度差 mil | 允许 mil | 主体耦合 %% | 端部未耦合 / 预算 mil | 过孔 P/N | 层 P/N |\n|---|---|---|---|---|---|---|\n")
			for _, pr := range si.Pairs {
				if c := pr.Coupling; c != nil {
					body := sprintf("%.0f（主体 %.0f mil）", c.CoupledPct, c.BodyMil)
					if c.BodyMil < pairMinBodyMil {
						body = sprintf("—（全在出线区；整根耦合 %.0f/%.0f mil）", c.CoupledPMil, c.CoupledNMil)
					}
					p("| %s / %s | %.0f | %.0f | %s | %.0f / %.0f | %d / %d | %v / %v |\n", pr.P, pr.N, pr.SkewMil, pr.LimitMil,
						body, c.UncoupledMil, c.BreakoutMil, c.ViasP, c.ViasN, c.LayersP, c.LayersN)
					continue
				}
				p("| %s / %s | %.0f | %.0f | – | – | – | – |\n", pr.P, pr.N, pr.SkewMil, pr.LimitMil)
			}
			p("\n")
		}
		if len(si.Groups) > 0 {
			p("| 等长组 | 成员 | 未布通 | 最短 mil | 最长 mil | 差 mil | 容差 mil |\n|---|---|---|---|---|---|---|\n")
			for _, g := range si.Groups {
				p("| %s | %d | %d | %.0f | %.0f | %.0f | %.0f |\n", g.Name, len(g.Units), g.Unrouted, g.MinMil, g.MaxMil, g.SpreadMil, g.TolMil)
			}
			p("\n")
		}
		for _, f := range si.Findings {
			p("- %s `%s` %.0f（上限 %.0f）：%s\n", f.Kind, f.Net, f.Value, f.Limit, f.Fix)
		}
		p("\n")
	}
	WriteFeedback(p, r.Feedback)
	p("## 7. 执行\n\n```bash\npcbpilot apply playbook.json --project <工程> --dry-run\npcbpilot apply playbook.json --project <工程>\n```\n\n")
	p("执行后按仓库准则：`pcb save` → `doc reload` → `pcb drc` / `pcb check` 回读确认。\n")
}

var roleCN = map[string]string{
	"decap": "去耦", "clock": "晶振", "clock-load": "晶振负载电容", "power-stage": "功率级",
	"protection": "端口保护", "pull": "上下拉/偏置", "signal": "信号串联/滤波", "chain": "链上器件", "test": "测试点", "power-path": "电源路径", "pin-filter": "引脚滤波", "group": "模块成员", "hot-loop": "热回路", "bootstrap": "自举", "feedback": "反馈分压", "unassigned": "未归属",
}

// writeLayoutBasis lists, per core, which auxiliaries follow it, in what
// role and to which pin — the reasons the placer pulls each part where it does.
func writeLayoutBasis(p func(string, ...any), c *Circuit) {
	p("\n### 布局依据：核心件与辅助件\n\n核心件（IC、模块、连接器、隔离器件）先定位置和朝向；辅助件按角色贴到它服务的那只引脚，拉力从强到弱依次为去耦 → 晶振 → 功率级 → 端口保护 → 上下拉 → 信号串联。\n\n| 核心 | 类型 | 辅助件（角色 → 引脚） |\n|---|---|---|\n")
	for _, bl := range c.Blocks {
		if len(bl.Members) == 0 {
			continue
		}
		var parts []string
		for i, m := range bl.Members {
			if i >= 12 {
				parts = append(parts, fmt.Sprintf("…另 %d 个", len(bl.Members)-12))
				break
			}
			r := roleCN[m.Role]
			if r == "" {
				r = m.Role
			}
			if m.Pin != "" {
				parts = append(parts, fmt.Sprintf("%s（%s→%s）", m.Ref, r, m.Pin))
			} else {
				parts = append(parts, fmt.Sprintf("%s（%s）", m.Ref, r))
			}
		}
		core := bl.Core
		if core == "" {
			core = "—"
		}
		p("| %s | %s | %s |\n", core, bl.Kind, strings.Join(parts, "、"))
	}
}

// writeConverters lists each switching regulator's topology and the parts
// that make or break its layout.
func writeConverters(p func(string, ...any), c *Circuit) {
	if len(c.Converters) == 0 {
		return
	}
	p("\n### 开关电源\n\n布局第一优先是热回路面积（决定开关节点振铃和辐射）：Buck 的关键是输入电容，Boost 的关键是输出电容。反馈分压电阻贴 FB 脚、远离电感和开关节点。\n\n| 芯片 | 拓扑 | 置信度 | 电感 | 整流/续流管 | 输入 → 输出 | 热回路电容 | 自举 | 反馈 |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, cv := range c.Converters {
		d := cv.Diode
		if d == "" {
			d = "同步"
		}
		p("| %s | %s | %s | %s | %s | %s → %s | %s | %s | %s |\n", cv.Core, cv.Topology, cv.Confidence, cv.Inductor, d,
			dash(cv.InRail), dash(cv.OutRail), dash(cv.HotCap), dash(cv.Bootstrap), dash(strings.Join(cv.Feedback, "、")))
	}
}

func dash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// writeChains lists interface signal chains in their required order.
func writeChains(p func(string, ...any), c *Circuit) {
	if len(c.Chains) == 0 {
		return
	}
	p("\n### 接口信号链\n\n外部信号进板后必须先遇到 ESD/TVS（在走线上、不挂支线），再经共模电感、串阻/AC 耦合电容到芯片；差分对两侧同一级器件并排。\n\n| 顺序 | 网络 | 差分 |\n|---|---|---|\n")
	for i, ch := range c.Chains {
		if i >= 30 {
			p("| …另 %d 条 | | |\n", len(c.Chains)-30)
			break
		}
		pair := ""
		if ch.Pair != nil {
			pair = "是"
		}
		p("| %s | %s | %s |\n", strings.Join(ch.Seq(), " → "), strings.Join(ch.Nets, " → "), pair)
	}
}

var jointGroupCN = map[string]string{"electrical": "电气（布线后实测）", "efficiency": "布线效率", "placement": "布局装配"}

// writeJoint renders the joint place+route score and the loop history.
func writeJoint(p func(string, ...any), r *Report) {
	j := r.Joint
	if j == nil {
		return
	}
	verdict := "可交付"
	if !j.Deliverable {
		verdict = "不可交付"
	}
	p("### 综合评分：%.1f（%s）\n\n", j.Overall, verdict)
	p("综合分 = 门槛 × 布通系数 × 质量分。布通系数 = 布通率² × 0.97^DRC = **%.2f**（布通率 %.1f%%，DRC %d）；质量分 = 各组加权几何平均 = **%.0f**。\n\n",
		j.CompletionFactor, j.Completion, j.DRC, j.Quality)
	if j.PlanePads > 0 {
		p("平面/铺铜与地网络连接：%d 条，其中 **%d 条未接通**（计入布通系数）。\n\n", j.PlanePads, j.PlaneOpen)
	}
	for _, g := range j.Gates {
		p("- 门槛未过：%s（总分封顶 40）\n", g)
	}
	for _, g := range j.Blockers {
		p("- 电气未达标（不可交付）：%s\n", g)
	}
	if why := j.NotDeliverable(); len(why) > 0 {
		p("\n**不可交付**（安全 > 电气 > DRC > 布通 > 美观）：%s。\n\n", strings.Join(why, "；"))
	}
	p("| 组 | 项 | 得分 | 实测 |\n|---|---|---|---|\n")
	for _, it := range j.Items {
		g := jointGroupCN[it.Group]
		if g == "" {
			g = it.Group
		}
		p("| %s | %s | %.0f | %s |\n", g, it.ID, it.Score, it.Detail)
	}
	if a := j.Aesthetics; a != nil {
		p("\n美观度（只报告，权重 %.2f，不计入综合分；电气规则优先）：%.1f（布局 %.1f / 布线 %.1f，布通份额 %.2f）。明细见 `pcbpilot pcb aesthetics --board board.routed.json`。\n",
			a.Weight, a.Score, a.Placement, a.Routing, a.RoutedShare)
	}
	if len(r.Loop) > 0 {
		p("\n布局↔布线闭环（布不通和 DRC 违规附近的器件每轮加宽半条布线通道后重新布局，取综合分最高的一轮）：\n\n| 轮 | 布通率 | DRC | 综合分 | 加宽器件 | 备注 |\n|---|---|---|---|---|---|\n")
		for _, lp := range r.Loop {
			p("| %d | %.1f%% | %d | %.1f | %d | %s |\n", lp.Pass, lp.Completion, lp.DRC, lp.Joint, lp.Inflated, lp.Note)
		}
	}
	p("\n")
}

// writeStage0 renders the physical-feasibility stage: what the board must
// hold, what each stackup offers, BGA escape, and what to negotiate.
func writeStage0(p func(string, ...any), r *Report) {
	f := r.Stage0
	if f == nil && r.Result != nil && r.Result.Stackup != nil {
		f = r.Result.Stackup.Feasibility
	}
	if f == nil {
		return
	}
	p("## 0. 物理可行性与叠层决策\n\n先算物理约束，再定层数和过孔工艺，最后才布局布线。\n\n")
	p("| 项 | 数值 |\n|---|---|\n| 板面积 | %.2f in² |\n| 器件占地（顶 / 底） | %.0f%% / %.0f%% |\n| 信号连接 | %d 条 |\n| 估算走线总长 | %.1f m |\n| 布线需求（标准线宽折算） | %.0f in（电源走线另计 %.0f in） |\n| 布线效率 η | %.2f（按 7 块已量产真板标定） |\n\n",
		f.BoardAreaIn2, 100*f.TopOccupancy, 100*f.BotOccupancy, f.Connections, f.WireLengthM, f.SignalDemand, f.PowerDemand, f.Efficiency)
	p("| 方案 | 信号层 | 容量 in | 需求 in | 利用率 | 结论 | 说明 |\n|---|---|---|---|---|---|---|\n")
	for _, o := range f.Options {
		name := fmt.Sprintf("%d 层", o.Layers)
		if o.Mixed {
			name += "（混合）"
		}
		mark := ""
		if o.Layers == f.Choice && o.Mixed == f.ChoiceMixed {
			mark = " **←**"
		}
		p("| %s%s | %d | %.0f | %.0f | %.0f%% | %s | %s |\n", name, mark, o.SignalLayers, o.CapacityIn, o.DemandIn, 100*o.Utilisation, verdictCN[o.Verdict], strings.Join(o.Why, "；"))
	}
	if len(f.BGAs) > 0 {
		p("\n| BGA | 球距 | 信号球 | 表层逃逸圈 | 每内层圈 | 需信号层 | 过孔工艺 |\n|---|---|---|---|---|---|---|\n")
		for _, g := range f.sortedBGAs() {
			layers := fmt.Sprint(g.SignalLayers)
			if g.SignalLayers >= 99 {
				layers = "无法逃逸"
			}
			tech := g.ViaTech
			if g.RegionRule != "" {
				tech += "；" + g.RegionRule
			}
			p("| %s | %.2f mm | %d | %d | %d | %s | %s |\n", g.Ref, g.PitchMil*0.0254, g.SignalBalls, g.TopRings, g.InnerRings, layers, tech)
		}
	}
	if len(f.Negotiation) > 0 {
		p("\n**需要与机械 / 客户协商：**\n\n")
		for _, n := range f.Negotiation {
			p("- %s\n", n)
		}
	}
	p("\n")
}

var verdictCN = map[string]string{"ok": "宽裕", "tight": "偏紧", "insufficient": "不足", "excluded": "排除"}

// writeIsolation renders the intent insulation outcome.
func writeIsolation(p func(string, ...any), iso *IsolationReport) {
	if iso == nil || len(iso.Pairs) == 0 {
		return
	}
	p("### 安规隔离（%s）\n\n| 域 A | 域 B | 工作电压 | 绝缘 | 电气间隙 | 爬电距离 | 槽宽下限 | 来源 | 依据 |\n|---|---|---|---|---|---|---|---|---|\n", iso.Standard)
	for _, ip := range iso.Pairs {
		p("| %s | %s | %.0f Vrms / %.0f Vpk | %s | %.2f mm | %.2f mm | %.2f mm | %s | %s |\n", ip.A, ip.B, ip.WorkingVrms, ip.WorkingVpeak, ip.Insulation,
			ip.ClearanceMil*0.0254, ip.CreepageMil*0.0254, ip.SlotWidthMil*0.0254, ip.Source, ip.Ref)
	}
	p("\n> %s\n\n", safety.Caveat)
	if len(iso.Infeasible) > 0 {
		p("**INFEASIBLE — %d 个跨隔离器件自身焊盘无法满足要求（开槽/布线/布局都救不了，必须换器件）：**\n\n", len(iso.Infeasible))
		for _, f := range iso.Infeasible {
			p("- %s\n", f.Reason)
		}
		p("\n")
	}
	for _, s := range iso.Slots {
		p("- 开槽 %s（%s|%s）：%.0f × %.0f mil，焊盘间距 %.0f mil\n", s.Ref, s.A, s.B, s.WidthMil, s.LengthMil, s.GapMil)
		for _, w := range s.Why {
			p("  - %s\n", w)
		}
	}
	if len(iso.Moats) > 0 {
		p("- 隔离带禁铺铜区域 %d 个（live `pcb.region.create` no-pours）\n", len(iso.Moats))
	}
	for _, n := range iso.Notes {
		p("- %s\n", n)
	}
	if len(iso.Findings) == 0 {
		p("\n跨域铜皮电气间隙 / 爬电距离核查：**0 违规**（外层按沿面路径并计入开槽；覆铜未计入，由禁铺铜区域保证）。\n\n")
		return
	}
	p("\n跨域铜皮核查：**%d 处不足**\n\n| 类型 | 对象 A | 对象 B | 直线 mil | 沿面 mil | 要求 mil |\n|---|---|---|---|---|---|\n", len(iso.Findings))
	for i, f := range iso.Findings {
		if i == 30 {
			p("| … | 另 %d 处 | | | | |\n", len(iso.Findings)-30)
			break
		}
		p("| %s | %s (%s) | %s (%s) | %.1f | %.1f | %.1f |\n", f.Kind, f.ItemA, f.NetA, f.ItemB, f.NetB, f.GapMil, f.PathMil, f.RequiredMil)
	}
	p("\n")
}

// writeEdge reports the board-edge safety distance of the plan.
func writeEdge(p func(string, ...any), e *EdgeCheck) {
	if e == nil || e.Policy == nil {
		return
	}
	pol := e.Policy
	p("### 板边安全距离\n\n%s 板边（%s）：外层 ≥ %.1f mil，内层/平面 ≥ %.1f mil（工艺下限 %.1f mil）。\n\n",
		pol.Kind, pol.Source, pol.LayerReq(LayerTop), pol.LayerReq(LayerInner1), pol.FabMinMil)
	p("| 层 | 类 | 要求 mil | 实测最小 mil | 对象 |\n|---|---|---|---|---|\n")
	for _, l := range e.Layers {
		if !l.Measured {
			continue
		}
		p("| %d | %s | %.1f | %.2f | %s %s |\n", l.Layer, l.Class, l.RequiredMil, l.MinMil, l.Kind, l.Net)
	}
	for _, id := range sortedDomainIDs(pol.ByDomain) {
		d := pol.ByDomain[id]
		p("\n- 域 %s：到板边与金属安装孔 ≥ %.1f mil（%s 绝缘，间隙 %.2f / 爬电 %.2f mm）", id, d.Mil, d.Insulation, d.ClearanceMm, d.CreepageMm)
	}
	for _, f := range e.Findings {
		p("\n- **%s** %s", f.Level, f.Message)
	}
	for _, n := range e.Notes {
		p("\n- %s", n)
	}
	p("\n\n")
}

func sortedDomainIDs(m map[string]*EdgeDomain) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// aesTradesCN renders the electrical trades of an aesthetics stage for
// report.md ("无" when none: the line is printed either way, never silent).
func aesTradesCN(ts []AesTrade) string {
	if len(ts) == 0 {
		return "无"
	}
	return AesTradesText(ts)
}
