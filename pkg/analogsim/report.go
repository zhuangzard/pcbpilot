package analogsim

import (
	"fmt"
	"io"
	"strings"
)

var statusZh = map[string]string{StatusPass: "通过", StatusWarn: "警告", StatusFail: "失败", StatusInfo: "信息"}

var classZh = map[string]string{
	ClassFollower: "电压跟随器", ClassNonInverting: "同相放大", ClassInverting: "反相放大", ClassDifference: "差分放大",
	ClassIntegrator: "积分器", ClassSKLowPass: "Sallen-Key 低通", ClassSKHighPass: "Sallen-Key 高通", ClassMFBLowPass: "MFB 低通",
	ClassMFBHighPass: "MFB 高通", ClassComparator: "比较器（回差）", ClassInstrument: "仪表放大器", ClassCurrentSense: "电流检测放大器",
	ClassRCLowPass: "RC 低通", ClassLCFilter: "LC 滤波", ClassADCInput: "ADC 输入采样",
	ClassReference: "电压基准", ClassRegulatorFB: "稳压器反馈分压", ClassCrystal: "晶振负载电容", ClassTransistorSw: "晶体管开关",
	ClassResetRC: "复位 RC 延时", ClassOpampOther: "运放（未识别拓扑）", ClassLevelShifter: "MOSFET 电平转换",
}

// ClassLabel is the Chinese label of a block class.
func ClassLabel(c string) string {
	if s := classZh[c]; s != "" {
		return s
	}
	return c
}

func mdEsc(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

// WriteReport writes analog.md. plots maps "<block>/<curve>" to a relative
// image path (may be nil).
func WriteReport(w io.Writer, out *Output, plots map[string]string) error {
	var b strings.Builder
	s := out.Summary
	fmt.Fprintf(&b, "# 模拟电路仿真 Analog SPICE（%s）\n\n", Generator)
	ng := "未安装（仅解析计算）— " + MissingNgspiceNote
	if out.Ngspice.Available {
		ng = fmt.Sprintf("%s（%s，%d 次运行）", out.Ngspice.Version, out.Ngspice.Path, out.Ngspice.Runs)
	}
	fmt.Fprintf(&b, "- ngspice：%s\n", ng)
	fmt.Fprintf(&b, "- 模拟块：%d 个，已仿真 %d；目标 %d 项，满足 %d，未满足 %d；结论 **%s**\n", s.Blocks, s.Simulated, s.Targets, s.Met, s.Failing, statusZh[s.Status])
	if len(s.ByClass) > 0 {
		var cls []string
		for _, k := range sortedKeys(s.ByClass) {
			cls = append(cls, fmt.Sprintf("%s ×%d", ClassLabel(k), s.ByClass[k]))
		}
		fmt.Fprintf(&b, "- 类别：%s\n", strings.Join(cls, "、"))
	}
	if out.Options != nil {
		fmt.Fprintf(&b, "- Monte-Carlo：%d 次/块，seed %d；优化：%v\n", out.Options.MCRuns, out.Options.Seed, out.Options.Optimise)
	}
	if s.Changes > 0 {
		fmt.Fprintf(&b, "- **建议修改 %d 个元件值**（原理图修改，须用户确认后执行，见文末“修改计划”）\n", s.Changes)
	}
	if len(out.Blocks) == 0 {
		b.WriteString("\n未识别到模拟电路块（运放/比较器/有源或无源滤波/ADC 输入/基准/晶振/复位 RC/晶体管开关/稳压反馈）。\n")
	}
	if len(out.Findings) > 0 {
		b.WriteString("\n## 发现\n\n| 级别 | 块 | 类型 | 说明 | 建议 |\n|---|---|---|---|---|\n")
		for _, f := range out.Findings {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", f.Severity, f.Block, f.Kind, mdEsc(f.Message), mdEsc(f.Suggestion))
		}
	}
	for _, blk := range out.Blocks {
		fmt.Fprintf(&b, "\n## %s · %s — %s\n\n", blk.ID, blk.Title, statusZh[blk.Status])
		fmt.Fprintf(&b, "- 类别：%s（%s）；拓扑：%s\n", ClassLabel(blk.Class), blk.Class, mdEsc(blk.Topology))
		fmt.Fprintf(&b, "- 器件：%s\n", strings.Join(blk.Parts, ", "))
		if len(blk.Rails) > 0 {
			var rs []string
			for _, r := range blk.Rails {
				rs = append(rs, fmt.Sprintf("%s = %s V（%s）", r.Net, trim(r.Voltage), r.Source))
			}
			fmt.Fprintf(&b, "- 电源轨：%s\n", strings.Join(rs, "，"))
		}
		if m := blk.Model; m != nil {
			fmt.Fprintf(&b, "- 模型：%s `%s`（%s）— %s\n", m.Kind, m.ID, m.Confidence, mdEsc(m.Source))
		}
		if blk.Skipped != "" {
			fmt.Fprintf(&b, "- 未仿真：%s\n", blk.Skipped)
		}
		if blk.Netlist != "" {
			fmt.Fprintf(&b, "- 网表：`%s`\n", blk.Netlist)
		}
		if len(blk.Components) > 0 {
			b.WriteString("\n| 位号 | 作用 | 值 | 容差 | 容差来源 |\n|---|---|---|---|---|\n")
			for _, c := range blk.Components {
				fmt.Fprintf(&b, "| %s | %s | %s | ±%g %% | %s |\n", c.Ref, c.Role, c.Text, c.TolPct, c.TolSource)
			}
		}
		if len(blk.Metrics) > 0 {
			b.WriteString("\n| 指标 | 仿真/计算 | 解析 | 方法 | 目标 | 状态 |\n|---|---|---|---|---|---|\n")
			for _, m := range blk.Metrics {
				an, tg := "—", "—"
				if m.Analytic != nil {
					an = fmtMetric(*m.Analytic, m.Unit)
				}
				if m.Target != nil {
					tg = fmtTarget(m.Target, m.Unit) + "（" + m.Target.Source + "）"
				}
				st := m.Status
				if st == "" {
					st = "—"
				}
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", m.Label, fmtMetric(m.Value, m.Unit), an, m.Method, tg, st)
			}
		}
		writeMC(&b, "容差 Monte-Carlo", blk.Tolerance)
		if o := blk.Optimise; o != nil {
			fmt.Fprintf(&b, "\n**优化**：%s", o.Status)
			if o.Reason != "" {
				fmt.Fprintf(&b, " — %s", o.Reason)
			}
			b.WriteString("\n")
			if len(o.Changes) > 0 {
				fmt.Fprintf(&b, "\n验证：%s；代价 %s → %s\n\n| 位号 | 原值 | 新值 | 系列 | 动作 | 器件 |\n|---|---|---|---|---|---|\n", o.Verified, trim(o.CostBefore), trim(o.CostAfter))
				for _, c := range o.Changes {
					part := "需选型：" + c.SearchHint
					if c.Part != nil {
						part = fmt.Sprintf("%s %s", c.Part.LCSC, c.Part.Key)
					}
					fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", c.Ref, c.From, c.To, c.Series, c.Action, mdEsc(part))
				}
				b.WriteString("\n| 指标 | 修改前 | 修改后 |\n|---|---|---|\n")
				for _, k := range sortedKeys(o.Changes[0].Before) {
					l := metricLabels[k]
					fmt.Fprintf(&b, "| %s | %s | %s |\n", nz(l[0], k), fmtMetric(o.Changes[0].Before[k], l[1]), fmtMetric(o.Changes[0].After[k], l[1]))
				}
				writeMC(&b, "修改后容差 Monte-Carlo", o.ToleranceAfter)
			}
		}
		for _, cv := range blk.Curves {
			n := cv.Name
			if p := plots[blk.ID+"/"+n]; p != "" {
				fmt.Fprintf(&b, "\n![%s %s](%s)\n", blk.ID, n, p)
			}
		}
		for _, n := range blk.Notes {
			fmt.Fprintf(&b, "\n> %s\n", n)
		}
	}
	if p := out.Plan; p != nil {
		b.WriteString("\n## 修改计划（原理图值修改 — 须用户确认）\n\n| 块 | 位号 | 原值 → 新值 | 动作 | 理由 |\n|---|---|---|---|---|\n")
		for _, c := range p.Changes {
			fmt.Fprintf(&b, "| %s | %s | %s → %s | %s | %s |\n", c.Block, c.Ref, c.From, c.To, c.Action, mdEsc(c.Reason))
		}
		b.WriteString("\n执行步骤：\n\n")
		for _, a := range p.Apply {
			fmt.Fprintf(&b, "- %s\n", a)
		}
		b.WriteString("\n执行之后：\n\n")
		for _, a := range p.AfterApply {
			fmt.Fprintf(&b, "- %s\n", a)
		}
		for _, n := range p.Notes {
			fmt.Fprintf(&b, "\n> %s\n", n)
		}
	}
	if len(out.Assumptions) > 0 || len(out.Warnings) > 0 {
		b.WriteString("\n## 假设与警告\n\n")
		for _, a := range out.Assumptions {
			fmt.Fprintf(&b, "- 假设：%s\n", a)
		}
		for _, a := range out.Warnings {
			fmt.Fprintf(&b, "- 警告：%s\n", a)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func writeMC(b *strings.Builder, title string, t *MCResult) {
	if t == nil || len(t.Stats) == 0 {
		return
	}
	y := "—"
	if t.YieldPc != nil {
		y = fmt.Sprintf("%.1f %%", *t.YieldPc)
	}
	fmt.Fprintf(b, "\n%s（%s，%d 次，%s，良率 %s；变动 %s）\n\n| 指标 | 标称 | 最小 | 最大 | 均值 | σ | 达标比例 |\n|---|---|---|---|---|---|---|\n",
		title, t.Method, t.Runs, t.Dist, y, strings.Join(t.Varied, ", "))
	for _, k := range sortedKeys(t.Stats) {
		st := t.Stats[k]
		l := metricLabels[k]
		in := "—"
		if st.InSpec != nil {
			in = fmt.Sprintf("%.1f %%", *st.InSpec)
		}
		fmt.Fprintf(b, "| %s | %s | %s | %s | %s | %s | %s |\n", nz(l[0], k), fmtMetric(st.Nominal, l[1]), fmtMetric(st.Min, l[1]), fmtMetric(st.Max, l[1]), fmtMetric(st.Mean, l[1]), fmtMetric(st.Std, l[1]), in)
	}
}

func trim(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", v), "0"), ".")
}

// MetricLabel is the Chinese label and unit of a metric name.
func MetricLabel(name string) (string, string) {
	l := metricLabels[name]
	return nz(l[0], name), l[1]
}
