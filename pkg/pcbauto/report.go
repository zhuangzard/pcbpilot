package pcbauto

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Report bundles everything one run decided, for JSON and Markdown output.
type Report struct {
	Result    *Result      `json:"result"`
	Circuit   *Circuit     `json:"circuit"`
	Placement *PlaceResult `json:"placement,omitempty"`
	SI        *SIReport    `json:"si,omitempty"`
	Mechanics *Mechanics   `json:"mechanics,omitempty"`
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
		writeConverters(p, c)
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
		p("\n")
	}

	if rr := res.Route; rr != nil {
		s := rr.Stats
		p("## 5. 布线\n\n| 指标 | 数值 |\n|---|---|\n")
		p("| 布通率 | %.1f%%（%d/%d） |\n| 走线总长 | %.1f in |\n| 信号过孔 | %d |\n| 扇出过孔 | %d |\n| 协商迭代 | %d |\n| 等长调整 | %d 条 |\n| 栅格 | %.2f mil |\n| 耗时 | %.1f s |\n\n",
			s.Completion, s.Routed, s.Connections, s.WireLengthIn, s.Vias, s.FanoutVias, s.Iterations, s.Tuned, s.GridMil, float64(s.Millis)/1000)
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
		p("\n")
	}

	if si := r.SI; si != nil && (len(si.Pairs) > 0 || len(si.Findings) > 0) {
		p("## 6. 高速信号\n\n")
		if len(si.Pairs) > 0 {
			p("| 差分对 | 长度差 mil | 允许 mil |\n|---|---|---|\n")
			for _, pr := range si.Pairs {
				p("| %s / %s | %.0f | %.0f |\n", pr.P, pr.N, pr.SkewMil, pr.LimitMil)
			}
			p("\n")
		}
		for _, f := range si.Findings {
			p("- %s `%s` %.0f（上限 %.0f）：%s\n", f.Kind, f.Net, f.Value, f.Limit, f.Fix)
		}
		p("\n")
	}
	p("## 7. 执行\n\n```bash\npcbpilot apply playbook.json --project <工程> --dry-run\npcbpilot apply playbook.json --project <工程>\n```\n\n")
	p("执行后按仓库准则：`pcb save` → `doc reload` → `pcb drc` / `pcb check` 回读确认。\n")
}

var roleCN = map[string]string{
	"decap": "去耦", "clock": "晶振", "clock-load": "晶振负载电容", "power-stage": "功率级",
	"protection": "端口保护", "pull": "上下拉/偏置", "signal": "信号串联/滤波", "chain": "链上器件", "test": "测试点", "hot-loop": "热回路", "bootstrap": "自举", "feedback": "反馈分压", "unassigned": "未归属",
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
