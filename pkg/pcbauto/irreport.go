package pcbauto

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// writeSimCurrents renders the simulated-current inputs (section 2.1).
func writeSimCurrents(p func(string, ...any), an *Analysis) {
	if an == nil || an.Sim == nil {
		return
	}
	s := an.Sim
	p("### 2.1 仿真电流（--sim）\n\n场景 **%s**（%s，%d 个网络）。仿真列出的网络按仿真电流定线宽；power.json 显式声明的电流仍优先（设计意图），声明值低于仿真值时告警。开关节点按纹波 RMS 定线宽、按峰值定过孔数。\n\n", s.Scenario, s.Generator, s.Nets)
	p("| 网络 | 角色 | 仿真 A | 场景 | 采用 A | 来源 | 峰值 A | 焊盘电流数 | 外层线宽 mil |\n|---|---|---|---|---|---|---|---|---|\n")
	for _, np := range an.Nets {
		if np.SimScenario == "" {
			continue
		}
		pk := ""
		if np.PeakA > 0 {
			pk = fmt.Sprintf("%.2f", np.PeakA)
		}
		p("| %s | %s | %.3f | %s | %.3f | %s | %s | %d | %.1f |\n", np.Net, np.Role, np.SimCurrentA, np.SimScenario, np.CurrentA, np.Source, pk, len(np.PadCurrents), np.WidthMil)
	}
	p("\n")
	var warn []string
	for _, np := range an.Nets {
		for _, w := range np.Warnings {
			warn = append(warn, np.Net+"："+w)
		}
	}
	for _, w := range warn {
		p("- **告警** %s\n", w)
	}
	for _, a := range s.Assumptions {
		p("- 仿真假设：%s\n", a)
	}
	for _, w := range s.Warnings {
		p("- 仿真告警：%s\n", w)
	}
	p("\n")
}

// writePowerIntegrity renders segment widths and IR drop (section 5.1).
func writePowerIntegrity(p func(string, ...any), rp *IRReport) {
	if rp == nil {
		return
	}
	p("### 5.1 分段电流、分支线宽与 IR drop\n\n")
	p("布线后把每个电源网络的实际铜皮建成电阻网络，汇点焊盘按仿真电流取电、供电器件作为电压参考（多个供电源时逐个单独供电求解、取最大值——合并的 worst 结果不满足 KCL），解出每段电流与每个焊盘的直流压降。每段线宽 = 该段电流的 IPC 线宽，夹在 [类别最小值, 布线宽度] 内（只收窄不加宽，几何保持 DRC 已放行的状态）；超预算时沿最坏路径逐级（0.05 mm）回宽，布线宽度仍不够则带更宽线宽/加过孔重新布线（最多 2 轮）。预算：%s（场景 %s）。\n\n", rp.Budget.String()+"，取较大者", rp.Scenario)
	p("| 网络 | 角色 | 供电源 | 电流 A | 预算 mV | 最坏压降 mV | 最坏焊盘（源） | 状态 | 收窄段 | 回宽段 |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, n := range rp.Nets {
		bud := "—"
		if n.BudgetMV > 0 {
			bud = fmt.Sprintf("%.1f", n.BudgetMV)
		}
		wp := n.WorstPad
		if wp != "" && n.WorstRef != "" {
			wp += "（" + n.WorstRef + "）"
		}
		p("| %s | %s | %s | %.3f | %s | %.2f | %s | %s | %d | %d |\n", n.Net, n.Role, n.Reference, n.CurrentA, bud, n.WorstMV, wp, irStatusCN(n.Status), n.Narrowed, n.Widened)
	}
	p("\n")
	for _, n := range rp.Nets {
		if n.Role != RolePower || n.Status == "no-reference" {
			continue
		}
		p("**%s**", n.Net)
		// Width transitions routed → final.
		type tr struct{ a, b float64 }
		cnt := map[tr]int{}
		for _, s := range n.Segments {
			cnt[tr{math.Round(s.RoutedMil*100) / 100, math.Round(s.WidthMil*100) / 100}]++
		}
		keys := make([]tr, 0, len(cnt))
		for k := range cnt {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].a != keys[j].a {
				return keys[i].a > keys[j].a
			}
			return keys[i].b > keys[j].b
		})
		var parts []string
		for _, k := range keys {
			if k.a == k.b {
				parts = append(parts, fmt.Sprintf("%.2f mil ×%d", k.a, cnt[k]))
			} else {
				parts = append(parts, fmt.Sprintf("%.2f→%.2f mil ×%d", k.a, k.b, cnt[k]))
			}
		}
		if len(parts) > 0 {
			p("：段宽（布线→分段）%s", strings.Join(parts, "，"))
		}
		p("\n\n")
		var sinks []IRPad
		for _, pd := range n.Pads {
			if pd.CurrentA > 0 && pd.Dir != "source" {
				sinks = append(sinks, pd)
			}
		}
		if len(sinks) > 0 {
			p("| 汇点焊盘 | 电流 A | 压降 mV | 预算内 |\n|---|---|---|---|\n")
			for _, pd := range sinks {
				d := fmt.Sprintf("%.2f", pd.DropMV)
				if pd.DropMV < 0 {
					d = "开路"
				}
				ok := "✓"
				if !pd.OK {
					ok = "✗"
				}
				p("| %s | %.4f | %s | %s |\n", pd.Pad, pd.CurrentA, d, ok)
			}
			p("\n")
		}
		if len(n.WorstPath) > 0 {
			var steps []string
			for _, s := range n.WorstPath {
				what := s.What
				if s.Kind == "track" {
					what = fmt.Sprintf("L%d %s", s.Layer, s.What)
				}
				steps = append(steps, fmt.Sprintf("%s %s %.2f mV", irKindCN(s.Kind), what, s.DropMV))
			}
			p("最坏路径 %s → %s：%s\n\n", n.WorstRef, n.WorstPad, strings.Join(steps, " → "))
		}
		for _, s := range n.Notes {
			p("- %s\n", s)
		}
	}
	for _, n := range rp.Nets {
		if n.Role == RoleGround && n.Status != "no-reference" {
			p("**%s**（地，仅报告）：回流入口 %s，最大地电位抬升 %.2f mV @ %s（%s）。", n.Net, n.Reference, n.WorstMV, n.WorstPad, n.WorstRef)
			for _, s := range n.Notes {
				p(" %s。", s)
			}
			p("\n\n")
		}
	}
	p("模型：\n\n")
	for _, m := range rp.Model {
		p("- %s\n", m)
	}
	p("\n")
}

func irStatusCN(s string) string {
	switch s {
	case "ok":
		return "✓ 预算内"
	case "over-budget":
		return "✗ 超预算"
	case "open":
		return "✗ 开路"
	case "no-reference":
		return "无供电源"
	case "info":
		return "仅报告"
	}
	return s
}

func irKindCN(k string) string {
	switch k {
	case "track":
		return "走线"
	case "via":
		return "过孔"
	case "plane":
		return "平面"
	case "spread":
		return "扩散"
	}
	return k
}
